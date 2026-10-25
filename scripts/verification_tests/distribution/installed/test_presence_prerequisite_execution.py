"""The reveal prerequisite preserves system authentication, cancellation and expiry obligations."""
from contextlib import ExitStack
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import presence
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable


class PresencePrerequisiteTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.storage = SimpleNamespace(home=Path('/fixture'))
        self.app = Mock(observations={})
        self.app.streams.return_value = {'app-stderr': b'bounded'}
        self.reservation = Mock()
        self.reservation.__enter__ = Mock(return_value=self.reservation)
        self.reservation.__exit__ = Mock(return_value=False)
        self.reservation.facts.return_value = {'retained': True}
        self.policy = Mock(observations={'installed': True, 'restored': True})
        self.policy.__enter__ = Mock(return_value=self.policy)
        self.policy.__exit__ = Mock(return_value=False)
        self.fixture = Mock(discovery_observations=[])
        self.fixture.__enter__ = Mock(return_value=self.fixture)
        self.fixture.__exit__ = Mock(return_value=False)
        self.ui = Mock()
        self.ui.__enter__ = Mock(return_value=self.ui)
        self.ui.__exit__ = Mock(return_value=False)
        values = {'protected_launcher': Mock(return_value={'path': '/bootstrap'}),
            'resolve': Mock(return_value=self.storage), 'graphical_session': Mock(return_value={}),
            'session_environment': Mock(return_value={}), 'Reservation': Mock(return_value=self.reservation),
            'InstalledApp': Mock(return_value=self.app), 'PresencePolicy': Mock(return_value=self.policy),
            'DesktopUI': Mock(return_value=self.ui), 'activate_visible_frame': Mock(return_value={'same_frame_active': True}),
            'configure_provider': Mock(return_value={'provider_id': 'fixture'}),
            'await_session': Mock(return_value={'id': 'session', 'status': 'idle'}),
            'prepare_secret': Mock(return_value={'phase': 'ready', 'reveal_ready': True}),
            'confirmation_rows': Mock(return_value=[]),
            'await_confirmation': Mock(return_value={'status': 'refused', 'code': 'presence_unavailable'}),
            'await_alert': Mock(return_value={'attributes': ['xml-roles:alert']}),
            'run': Mock(return_value={'exit_code': 0, 'timed_out': False, 'output_limit': False})}
        self.values = values
        for name, value in values.items():
            self.stack.enter_context(patch.object(presence, name, value))
        declared_gui = presence.ProviderFixture.gui
        def fixture(calls):
            declared_gui(calls)  # Validate the actual fixture declaration without starting its server.
            return self.fixture
        self.stack.enter_context(patch.object(presence.ProviderFixture, 'gui', side_effect=fixture))
        self.stack.enter_context(patch.object(presence.tempfile, 'mkdtemp', return_value='/fixture/project'))

    def test_normal_reveal_restores_policy_and_retains_unverified_partial_facts(self):
        status, facts, streams = presence.unchallenged_presence({})
        self.assertEqual(status, 'unverified')
        self.assertTrue(facts['typed_refusal_verified'])
        self.assertFalse(facts['system_authentication_verified'])
        self.assertFalse(facts['cancel_verified'])
        self.assertFalse(facts['expiry_verified'])
        self.assertEqual(facts['presence_policy'], self.policy.observations)
        self.assertEqual(facts['opened_session'], {'id': 'session', 'status': 'idle'})
        self.values['await_session'].assert_called_once_with(
            self.app, Path('/fixture/project'), 'fixture', presence.MODEL, status='idle',
            reason='presence-project-session-not-opened')
        self.values['prepare_secret'].assert_called_once_with(self.app, self.ui, self.reservation)
        self.assertTrue(facts['secret_setup']['reveal_ready'])
        self.app.start.assert_not_called()
        self.app.launch.assert_called_once()
        self.app.wait_ready.assert_called_once()
        self.app.stop.assert_called_once()
        self.policy.__exit__.assert_called_once()
        self.ui.click.assert_any_call('Reveal value')
        self.reservation.check.assert_called_once_with(empty=True)
        self.assertEqual(streams, {'app-stderr': b'bounded'})

    def test_service_readiness_refusal_never_invokes_onboarding_or_creates_a_secret(self):
        self.app.wait_ready.side_effect = CaseUnavailable('service unavailable')
        with self.assertRaises(CaseUnavailable) as failed:
            presence.unchallenged_presence({})
        self.app.start.assert_not_called()
        self.values['prepare_secret'].assert_not_called()
        self.values['PresencePolicy'].assert_not_called()
        self.app.stop.assert_called_once()
        self.assertEqual(failed.exception.observations['fresh_storage'], {'retained': True})

    def test_native_activation_refusal_never_uses_a_password_or_entry_fallback(self):
        self.values['activate_visible_frame'].side_effect = CaseUnavailable('same frame inactive')
        with self.assertRaises(CaseUnavailable) as refused:
            presence.unchallenged_presence({})
        self.values['configure_provider'].assert_not_called()
        self.values['prepare_secret'].assert_not_called()
        self.values['PresencePolicy'].assert_not_called()
        self.app.start.assert_not_called()
        self.app.stop.assert_called_once()
        self.assertFalse(refused.exception.observations['system_authentication_verified'])

    def test_normal_project_open_refusal_is_unverified_and_preserves_fresh_state(self):
        self.values['run'].return_value = {'exit_code': 1, 'timed_out': False, 'output_limit': False}
        with self.assertRaises(CaseUnavailable) as refused:
            presence.unchallenged_presence({})
        self.values['await_session'].assert_not_called()
        self.values['prepare_secret'].assert_not_called()
        self.values['PresencePolicy'].assert_not_called()
        self.assertEqual(refused.exception.observations['fresh_storage'], {'retained': True})

    def test_policy_restoration_failure_is_retained_after_partial_launch(self):
        failure = CaseFailed('restoration unverified')
        failure.observations = {'presence_policy': {'recovery_required': True}}
        self.policy.observations = failure.observations['presence_policy']
        self.policy.__exit__.side_effect = failure
        with self.assertRaises(CaseFailed) as failed:
            presence.unchallenged_presence({})
        self.assertTrue(failed.exception.observations['presence_policy']['recovery_required'])
        self.app.stop.assert_called_once()
        self.assertEqual(failed.exception.streams, {'app-stderr': b'bounded'})

    def test_project_open_output_bytes_move_to_streams_and_keep_observations_json_safe(self):
        import json
        self.values['run'].return_value = {
            'launch_identity': {'pid': 42, 'start_time_ticks': 99},
            'exit_code': 0, 'timed_out': False, 'output_limit': False,
            'stdout': b'opened\n', 'stderr': b'',
        }
        status, facts, streams = presence.unchallenged_presence({})
        self.assertEqual(status, 'unverified')
        self.assertEqual(facts['project_open'], {
            'launch_identity': {'pid': 42, 'start_time_ticks': 99},
            'exit_code': 0, 'timed_out': False, 'output_limit': False,
        })
        self.assertEqual(streams['project-open-stdout'], b'opened\n')
        self.assertEqual(streams['project-open-stderr'], b'')
        json.dumps(facts)

    def test_polkit_authorized_without_confirmation_fails_the_refusal_contract(self):
        self.values['await_confirmation'].return_value = {
            'status': 'confirmed', 'authenticator': 'linux_user_presence', 'code': None}
        with self.assertRaisesRegex(CaseFailed, 'did-not-refuse') as failed:
            presence.unchallenged_presence({})
        self.assertFalse(failed.exception.observations['typed_refusal_verified'])
        self.policy.__exit__.assert_called_once()
        self.assertTrue(failed.exception.observations['presence_policy']['restored'])

    def test_missing_typed_record_keeps_alert_only_result_unverified(self):
        self.values['await_confirmation'].side_effect = CaseUnavailable('native outcome unavailable')
        with self.assertRaises(CaseUnavailable) as refused:
            presence.unchallenged_presence({})
        self.assertFalse(refused.exception.observations['typed_refusal_verified'])
        self.assertFalse(refused.exception.observations['system_authentication_verified'])
        self.policy.__exit__.assert_called_once()
