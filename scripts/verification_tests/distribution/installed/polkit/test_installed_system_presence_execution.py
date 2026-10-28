"""Real reveal qualification needs the bound system transaction and host audit together."""
from scripts.linux_release.installed_environment import GraphicalSessionObservation
from contextlib import ExitStack
from copy import deepcopy
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_cases import presence_system as system
from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target


class InstalledSystemPresenceTests(unittest.TestCase):
    def execute(self, *, cancel=False, audit=True, authenticated=True):
        app, ui, observations = Mock(), Mock(), {}
        attempt = target()['pending']['attempt']
        control = Mock(records=[])
        control.bind.return_value = {'attempt': attempt}
        witness = {'system_authenticated': authenticated if not cancel else False,
            'agent_cancelled': cancel, 'host_release_verified': False}
        before = {'secret_id': 'selected', 'person_id': 'person', 'reveals': []}
        after = deepcopy(before)
        if not cancel and audit:
            after['reveals'] = [{'challenge_id': attempt['challenge']['challenge_id']}]
        outcome = {'status': 'refused' if cancel else 'confirmed', 'authenticator': 'linux_user_presence'}
        with ExitStack() as stack:
            values = {'native_caller': Mock(return_value={'pid': 401, 'uid': 30008}),
                'reveal_audit': Mock(side_effect=[before, after]),
                'attempt_rows': Mock(return_value=[]), 'confirmation_rows': Mock(return_value=[]),
                'graphical_session': Mock(return_value=GraphicalSessionObservation({}, {})),
                'NativeSessionObserver': Mock(return_value=Mock(__enter__=Mock(return_value=Mock()),
                    __exit__=Mock(return_value=False))),
                'NativePolkitControl': Mock(return_value=control),
                'await_attempt': Mock(return_value=attempt),
                'await_observation': Mock(side_effect=[{'attempt': attempt}, witness]),
                'await_confirmation': Mock(return_value=outcome)}
            for key, value in values.items():
                stack.enter_context(patch.object(system, key, value))
            try:
                result = system.run_reveal(app, ui, 'project', {'binding': {}, 'vm_id': 'owned'},
                    observations, cancel=cancel)
            except BaseException as error:
                error.record, error.control, error.ui = observations, control, ui
                raise
        return result, control, ui

    def test_success_requires_native_authentication_and_exact_host_audit_without_value_readback(self):
        record, control, ui = self.execute()
        self.assertTrue(record['system_authentication_verified'])
        self.assertTrue(record['host_release_verified'])
        control.deliver.assert_called_once_with(cancel=False)
        control.close.assert_called_once()
        self.assertEqual([call.args[0] for call in ui.click.call_args_list], ['Reveal value', 'Hide now'])
        ui.find.assert_called_once()

    def test_native_success_without_host_audit_never_completes_the_case(self):
        with self.assertRaises(system.CaseFailed) as failed:
            self.execute(audit=False)
        self.assertFalse(failed.exception.record['system_authentication']['host_release_verified'])
        failed.exception.control.close.assert_called_once()
        failed.exception.ui.find.assert_not_called()

    def test_cancel_requires_system_agent_cancellation_and_unchanged_host_audit(self):
        record, control, ui = self.execute(cancel=True)
        self.assertTrue(record['cancel_verified'])
        self.assertFalse(record['host_release_verified'])
        control.deliver.assert_called_once_with(cancel=True)
        ui.find.assert_not_called()
        control.close.assert_called_once()

    def test_native_confirmation_without_actual_system_authentication_is_rejected(self):
        with self.assertRaises(system.CaseFailed) as failed:
            self.execute(authenticated=False)
        self.assertFalse(failed.exception.record['system_authentication']['system_authentication_verified'])


if __name__ == '__main__':
    unittest.main()
