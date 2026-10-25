"""Source fixtures for the exact1433 diagnostic, never startup error prose."""
import json
from contextlib import ExitStack
from copy import deepcopy
from pathlib import Path
from types import SimpleNamespace
import tempfile
import time
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import startup_outcome as owner

ROW = {'format_version': 1, 'protocol': 1, 'pid': 4242, 'sequence': 2,
       'kind': 'failed', 'phase': 'credentials', 'elapsed_ms': 17,
       'code': 'credential_vault_custodian_absent'}


def encode(row):
    return owner.PREFIX + json.dumps(row).encode() + b'\n'


class StartupOutcomeTests(unittest.TestCase):
    def test_exact_native_terminal_projection_and_incremental_chunks(self):
        raw = encode(ROW)
        self.assertEqual(owner.failed_rows(raw), [ROW])
        self.assertEqual(owner.failed_rows(raw[:-1]), [])
        self.assertEqual(owner.failed_rows(b'ordinary native diagnostic\n' + raw), [ROW])

    def test_prose_unknown_shape_and_wrong_types_cannot_supply_failure(self):
        self.assertEqual(owner.failed_rows(b'credential_vault_custodian_absent prose\n'), [])
        for changed in ({**ROW, 'message': 'SECRET PROSE'}, {**ROW, 'code': 'credential_vault_corrupt'},
                        {**ROW, 'phase': 'ready'}, {**ROW, 'pid': True},
                        {**ROW, 'protocol': 2}, {**ROW, 'elapsed_ms': -1},
                        {key: value for key, value in ROW.items() if key != 'sequence'}):
            with self.subTest(changed=changed), self.assertRaises(ValueError): owner.failed_rows(encode(changed))
        with self.assertRaises(ValueError): owner.failed_rows(encode(ROW) * 2)

    def test_actual_ready_engine_refuses_before_absence_claim(self):
        app, reservation = Mock(timeout=60), Mock(deadline=time.monotonic() + 60)
        app.try_ready.return_value = True
        with self.assertRaises(owner.CaseFailed): owner.await_absence(app, reservation)
        app.streams.assert_not_called()

    def test_typed_selected_native_record_not_health_absence_is_the_witness(self):
        app, reservation = Mock(timeout=60), Mock(deadline=time.monotonic() + 60)
        app.try_ready.return_value = False
        app.observations = {'launch_identity': {'pid': 123, 'start_time_ticks': 456}}
        app.streams.return_value = {'app-stderr': encode(ROW)}
        with patch.object(owner, 'process_identity', return_value=app.observations['launch_identity']):
            result = owner.await_absence(app, reservation)
        self.assertEqual(result['native_terminal'], ROW)
        self.assertFalse(result['pid_is_kernel_birth'])
        self.assertFalse(result['health_absence_is_failure_proof'])
        reservation.check_launch.assert_called_once_with(app)


    def test_native_capture_identity_change_and_shorter_reservation_do_not_overstate_witness(self):
        app = Mock(timeout=60)
        app.try_ready.return_value = False
        app.observations = {'launch_identity': {'pid': 123, 'start_time_ticks': 456}}
        app.streams.return_value = {'app-stderr': encode(ROW)}
        reservation = Mock(deadline=110)
        with patch.object(owner.time, 'monotonic', side_effect=[100, 101, 103]), patch.object(
                owner, 'process_identity', return_value=app.observations['launch_identity']):
            result = owner.await_absence(app, reservation)
        self.assertEqual(result['elapsed_observation_seconds'], 3)
        self.assertTrue(result['child_pid_validated_by_native_startup_owner'])
        self.assertNotEqual(result['native_terminal']['pid'], result['launch_identity']['pid'])
        with patch.object(owner, 'process_identity', return_value={'pid': 123, 'start_time_ticks': 999}), \
                patch.object(owner.time, 'monotonic', return_value=100), self.assertRaises(owner.CaseFailed):
            owner.await_absence(app, reservation)


class VaultServiceRecoveryTests(unittest.TestCase):
    def execute(self, directory, *, changed=None):
        from linux_release.installed_cases import vault_service_recovery as case
        from linux_release.installed_cases import vault_quiet
        from verification_tests.distribution.installed.test_installed_vault_quiet_execution import turn
        before, after, events = turn(0), turn(1), []
        if changed == 'old_turn': after = deepcopy(before)
        if changed == 'old_invocation':
            after['reply']['tool_invocation'] = before['reply']['tool_invocation']
        apps = [MagicMock() for _ in range(3)]
        for index, app in enumerate(apps):
            app.__enter__.return_value = app
            app.observations = {'launch_identity': {'pid': 100 + index}}
            if index != 1:
                app.observations['engine_identity'] = {'pid': 200 + index, 'start_time_ticks': 100 + index}
            app.streams.return_value = {'app-stderr': b'bounded native fixture'}
            def stop(app=app, index=index):
                app.observations.update(exit_code=0, capture_complete=True, output_limit=False)
                events.append(('stopped', index))
            app.stop.side_effect = stop
            app.__exit__.side_effect = lambda *args, app=app: app.stop()
        if changed == 'same_engine': apps[2].observations['engine_identity'] = apps[0].observations['engine_identity']
        if changed == 'credential_reentry': apps[2].observations['fixed_input'] = [{'operation': 'unexpected'}]
        if changed == 'cleanup': apps[1].stop.side_effect = OSError('failed stop')
        reservation, fixture, phases = MagicMock(), MagicMock(), Mock(records=[])
        reservation.__enter__.return_value = reservation
        reservation.deadline = 9999
        reservation.facts.return_value = {'retained': True}
        fixture.__enter__.return_value = fixture
        fixture.transport_observations, fixture.transport_clipped = [], False
        def phase(operation):
            if operation == case.ABSENT:
                self.assertIn(('stopped', 0), events)
            if operation == case.RESTORE:
                self.assertIn(('stopped', 1), events)
            events.append(('root', operation))
            if changed == 'restore' and operation == case.RESTORE:
                raise case.CaseUnavailable('restore unverified')
        phases.phase.side_effect = phase
        with ExitStack() as stack:
            values = {'protected_launcher': Mock(return_value={}), 'graphical_session': Mock(return_value={}),
                'session_environment': Mock(return_value={}), 'resolve': Mock(return_value=SimpleNamespace(home=directory)),
                'Reservation': Mock(return_value=reservation), 'InstalledApp': Mock(side_effect=apps),
                'interactive_window': Mock(return_value={}), 'GuestPhases': Mock(return_value=phases),
                'configure_provider': Mock(return_value={'provider_id': 'fixture'}),
                'setup_state': Mock(return_value={'onboarding_completed': True,
                    'coordinator': {'provider_id': 'fixture', 'model': case.MODEL}}),
                'authenticated_turn': Mock(side_effect=[before, after]),
                'await_fixture_reply': Mock(return_value={} if changed == 'old_reply' else before['reply']),
                'await_absence': Mock(side_effect=case.CaseUnavailable('missing typed outcome') if changed == 'startup' else None,
                                      return_value={'native_terminal': ROW}),
                'retain': Mock(side_effect=lambda *args: args)}
            for key, value in values.items(): stack.enter_context(patch.object(case, key, value))
            stack.enter_context(patch.object(vault_quiet, 'process_identity', side_effect=ProcessLookupError))
            self.apps, self.phases, self.retained, self.events = apps, phases, values['retain'], events
            return case.execute({'native_input': {'binding': {}, 'vm_id': 'owned'}},
                                'vault', 'service-unavailable-retry', directory)

    def test_actual_phase_order_preserved_reply_and_fresh_authenticated_use(self):
        from linux_release.installed_cases import vault_service_recovery as case
        with tempfile.TemporaryDirectory() as directory:
            result = self.execute(Path(directory))
        self.assertEqual(result[4], 'passed')
        observed = result[5]
        self.assertTrue(observed['temporary_absence_repaired_use_verified'])
        self.assertFalse(observed['live_exchange_failure_verified'])
        self.assertFalse(observed['content_equality_claimed'])
        self.assertEqual([call.args[0] for call in self.phases.phase.call_args_list], [case.ABSENT, case.RESTORE])
        self.assertNotEqual(observed['turn_before']['reply']['attempt_id'], observed['turn_after']['reply']['attempt_id'])
        for app in self.apps: app.stop.assert_called()

    def test_unverified_startup_repair_cleanup_or_reused_evidence_never_pass(self):
        from linux_release.installed_cases import vault_service_recovery as case
        for changed in ('startup', 'restore', 'cleanup', 'old_turn', 'old_invocation', 'old_reply', 'same_engine', 'credential_reentry'):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises((case.CaseFailed, case.CaseUnavailable)):
                    self.execute(Path(directory), changed=changed)
                self.retained.assert_not_called()
                if changed == 'startup':
                    self.assertEqual(self.phases.phase.call_count, 1)
                    # No guest replay/repair request: the root seat owns independent cleanup.
                    self.apps[2].__enter__.assert_not_called()

    def test_unknown_case_or_inputs_never_reach_profile_or_root_phases(self):
        from linux_release.installed_cases import vault_service_recovery as case
        from linux_release import installed_execution
        self.assertEqual(installed_execution.case_family('vault', 'service-unavailable-retry', 'deb'),
                         'vault_service_recovery')
        with patch.object(case, 'protected_launcher') as launch:
            with self.assertRaises(ValueError): case.execute({}, 'vault', 'other', None)
            for security in (None, [], {'format_version': True}, {'unit': 'foreign.service'}):
                with self.subTest(security=security), self.assertRaises(ValueError):
                    case.execute({'case_inputs': {'security': security}}, *case.CASE, None)
            launch.assert_not_called()
