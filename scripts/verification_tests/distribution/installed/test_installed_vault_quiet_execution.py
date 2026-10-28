"""Quiet custody requires new authenticated work, not an old persisted reply."""
from contextlib import ExitStack
import copy
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.campaign import execution as installed_execution
from linux_release.installed_cases import vault_quiet as owner
from linux_release.installed_input import controller


def turn(number):
    return {'request_start': number * 3, 'request_end': number * 3 + 3,
        'reply': {**{key: f'{key}-{number}' for key in ('project_id', 'session_id', 'turn_id', 'attempt_id')},
                  'schema_acquisition': {'invocation_id': f'acquire-{number}'},
                  'tool_invocation': {'invocation_id': f'command-{number}'}}}


class FreshAuthenticatedTurnTests(unittest.TestCase):
    def exercise(self, *, extra=True, credential=True, clipped=False, completed=True, opened=0, changed=False):
        app = Mock(binding={})
        fixture = SimpleNamespace(transport_observations=[{'path': '/v1/chat/completions',
            'status': 200, 'fixture_credential_observed': True}], transport_clipped=clipped,
            acquisition=SimpleNamespace(completed=completed))
        barrier = Mock()
        barrier.await_held.return_value = {'request_id': 'first'}
        barrier.await_response_written.return_value = {'request_id': 'other' if changed else 'first'}
        def submit(*args):
            if extra:
                fixture.transport_observations.append({'path': '/v1/chat/completions',
                    'status': 200, 'fixture_credential_observed': credential})
        with patch.object(owner, 'run', return_value={'exit_code': opened, 'timed_out': False,
                'output_limit': False}), patch.object(owner, 'submit_project_turn', side_effect=submit), patch.object(
                owner, 'await_fixture_reply', return_value=turn(1)['reply']):
            return owner.authenticated_turn(app, fixture, barrier, Path('/fixture'), {'provider_id': 'fixture'})

    def test_old_authenticated_requests_cannot_qualify_new_work(self):
        with self.assertRaises(ValueError): self.exercise(extra=False)
        row = self.exercise()
        self.assertEqual((row['request_start'], row['request_end']), (1, 2))
        self.assertEqual(len(row['authenticated_completions']), 1)

    def test_missing_credential_clipping_unsettled_acquisition_and_wrong_response_refuse(self):
        for changes in ({'credential': False}, {'clipped': True}, {'completed': False},
                        {'changed': True}, {'opened': 1}):
            with self.subTest(changes=changes), self.assertRaises(ValueError): self.exercise(**changes)


class QuietCustodyCaseTests(unittest.TestCase):
    def execute(self, root, *, duplicate=False, stale_provider=False, changed_reply=False, stop_failure=False,
                after_error=False, same_engine=False, startup_input=False):
        stack = ExitStack()
        self.addCleanup(stack.close)
        apps = [MagicMock(), MagicMock()]
        provider = {'provider_id': 'fixture'}
        for number, app in enumerate(apps):
            app.__enter__.return_value = app
            app.observations = {'engine_identity': {'pid': 123 if same_engine else 123 + number,
                'start_time_ticks': 7 if same_engine else 7 + number}, 'health': {'status': 200}}
            app.streams.return_value = {'app-stderr': b'bounded fixture output'}
            def stop(app=app):
                app.observations.update(exit_code=0, capture_complete=True, output_limit=False)
            app.stop.side_effect = stop
            app.__exit__.side_effect = lambda *args, app=app: app.stop()
        if startup_input:
            apps[0].observations['onboarding'] = {'normal_action': True}
        if stop_failure:
            apps[1].stop.side_effect = RuntimeError('failed settlement')
        reservation, fixture = MagicMock(), MagicMock()
        reservation.__enter__.return_value = reservation
        reservation.facts.return_value = {'retained': True}
        fixture.__enter__.return_value = fixture
        fixture.transport_observations = []
        fixture.transport_clipped = False
        before, after = turn(0), turn(1)
        if duplicate: after['reply']['tool_invocation'] = before['reply']['tool_invocation']
        for name, value in [('protected_launcher', {}), ('graphical_session', {}),
                ('session_environment', {}), ('resolve', SimpleNamespace(home=root)),
                ('Reservation', reservation), ('InstalledApp', None), ('interactive_window', {}),
                ('configure_provider', provider), ('setup_state', {'onboarding_completed': True,
                    'coordinator': {'provider_id': 'other' if stale_provider else 'fixture', 'model': owner.MODEL}}),
                ('await_fixture_reply', {} if changed_reply else before['reply'])]:
            if name != 'InstalledApp': stack.enter_context(patch.object(owner, name, return_value=value))
        stack.enter_context(patch.object(owner, 'process_identity', side_effect=ProcessLookupError))
        stack.enter_context(patch.object(owner, 'InstalledApp', side_effect=apps))
        stack.enter_context(patch.object(owner.ProviderFixture, 'gui', return_value=fixture))
        stack.enter_context(patch.object(owner, 'authenticated_turn', side_effect=[before,
            ValueError('new turn failed') if after_error else after]))
        retain = stack.enter_context(patch.object(owner, 'retain', side_effect=lambda *args: args))
        self.apps, self.retain = apps, retain
        return owner.execute({}, 'vault', 'service-onboarding', root)

    def test_complete_case_retains_both_new_turn_and_reopened_reply_and_settled_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.execute(Path(directory))
        self.assertEqual(result[4], 'passed')
        observed = result[5]
        self.assertEqual(observed['reply_reopened'], turn(0)['reply'])
        self.assertNotEqual(observed['turn_before']['reply']['turn_id'], observed['turn_after']['reply']['turn_id'])
        self.assertTrue(observed['fresh_authenticated_use_after_restart_verified'])
        self.assertTrue(observed['quiet_startup_without_input_verified'])
        for app in self.apps: app.stop.assert_called()
        self.assertEqual(observed['fresh_storage'], {'retained': True})

    def test_reused_engine_invocation_or_reply_and_new_turn_failure_never_pass(self):
        for changes in ({'same_engine': True}, {'duplicate': True}, {'changed_reply': True},
                        {'stale_provider': True}, {'after_error': True}, {'stop_failure': True}, {'startup_input': True}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(owner.CaseFailed) as failure:
                    self.execute(Path(directory), **changes)
                self.retain.assert_not_called()
                self.assertIn('fresh_storage', failure.exception.observations)
                for app in self.apps:
                    if app.__enter__.called: app.stop.assert_called()

    def test_settled_shell_does_not_hide_a_surviving_captured_engine(self):
        engine = {'pid': 123, 'parent_pid': 100, 'start_time_ticks': 77}
        app = SimpleNamespace(observations={'exit_code': 0, 'capture_complete': True,
            'output_limit': False, 'engine_identity': engine})
        for actual in (engine, {**engine, 'parent_pid': 1}):
            with self.subTest(actual=actual), patch.object(owner, 'process_identity', return_value=actual):
                with self.assertRaises(ValueError): owner.stopped(app)
        with patch.object(owner, 'process_identity', return_value={**engine, 'start_time_ticks': 78}):
            owner.stopped(app)

    def test_closed_case_selection_and_internal_catalog_successor(self):
        self.assertEqual(installed_execution.case_family('vault', 'service-onboarding', 'deb'), 'vault_quiet')
        with self.assertRaises(ValueError): owner.execute({}, 'vault', 'reinstall-with-healthy-credentials', None)
        for selected in (None, [], 'invalid', {'format_version': True}, {'format_version': 1.0}):
            with self.subTest(selected=selected), self.assertRaises(ValueError):
                owner.execute({'case_inputs': {'security': selected}}, 'vault', 'service-onboarding', None)
        root = Path(__file__).resolve().parents[4]
        catalog = json.loads((root / 'packaging/linux/support-matrix.json').read_text())
        cases = next(row['cases'] for row in catalog['capabilities'] if row['id'] == 'vault')
        self.assertIn('service-onboarding', cases)
        self.assertNotIn('service-and-password-onboarding', cases)
        self.assertIn('service-unavailable-retry', cases)
        self.assertIn('reinstall-with-healthy-credentials', cases)


class QuietPhysicalDeliveryTests(unittest.TestCase):
    def read(self, root, *, before=4, after=1, status='passed', phase='complete', partial=False, case='service-onboarding'):
        operations = ['provider-name-v1', 'provider-endpoint-v1-12345',
                      'provider-fixture-credential-v1', 'reply-fixture-prompt-v1']
        observed = {'launch_before': {'fixed_input': [{'operation': value, 'completed': True} for value in operations[:before]]},
            'launch_after': {'fixed_input': [{'operation': 'reply-fixture-prompt-v1', 'completed': True}] * after}, 'phase': phase}
        if partial: observed['native_input'] = {'operation': 'reply-fixture-prompt-v1'}
        leaf = root / f'leaf/evidence/vault/{case}/execution.json'
        leaf.parent.mkdir(parents=True)
        leaf.write_text(json.dumps({'status': status, 'observations': observed}))
        with patch.object(controller, 'expand_physical_transactions', side_effect=lambda leaf, rows: rows):
            return controller.delivery_records(root, {}, 'owned')

    def test_exact_two_turn_order_retained_and_missing_or_extra_input_refuses(self):
        with tempfile.TemporaryDirectory() as directory:
            records, operations = self.read(Path(directory))
        self.assertEqual(len(records), 5)
        self.assertEqual(operations, ['provider-name-v1', 'provider-endpoint-v1-12345',
            'provider-fixture-credential-v1', 'reply-fixture-prompt-v1', 'reply-fixture-prompt-v1'])
        for changes in ({'before': 3}, {'after': 0}, {'after': 2}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(ValueError): self.read(Path(directory), **changes)

    def test_failed_second_input_stays_after_restart_and_preserves_prior_transactions(self):
        with tempfile.TemporaryDirectory() as directory:
            records, operations = self.read(Path(directory), after=0, status='failed',
                                           phase='after-turn', partial=True)
        self.assertEqual(len(records), 5)
        self.assertEqual(operations[-1], 'reply-fixture-prompt-v1')


    def test_recovery_case_reuses_only_the_same_two_turn_delivery_boundary(self):
        self.assertEqual(installed_execution.case_family('vault', 'service-unavailable-retry', 'deb'),
                         'vault_service_recovery')
        with tempfile.TemporaryDirectory() as directory:
            records, operations = self.read(Path(directory), case='service-unavailable-retry')
        self.assertEqual(len(records), 5)
        self.assertEqual(operations[-2:], ['reply-fixture-prompt-v1', 'reply-fixture-prompt-v1'])
        with tempfile.TemporaryDirectory() as directory, self.assertRaises(ValueError):
            self.read(Path(directory), case='service-unavailable-retry', after=0)
