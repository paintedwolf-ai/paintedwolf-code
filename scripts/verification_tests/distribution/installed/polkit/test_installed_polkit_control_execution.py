"""Lifecycle owns cleanup; mailbox data never becomes independent input authority."""
from copy import deepcopy
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_input import polkit_control as control
from verification_tests.distribution.installed.test_installed_native_unlock_execution import ACCOUNT, EXECUTION
from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target


def request(operation, **fields):
    return {'format_version': 1, 'binding': EXECUTION, 'transaction_id': 'd' * 32,
        'session_id': 'e' * 32, 'operation': operation, 'account': ACCOUNT, **fields}


class PolkitLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.actor = Mock(nonce=EXECUTION['execution_nonce'], failed=False, closed=False)
        self.target = {'host': '127.0.0.1', 'port': 2222, 'user': 'qa'}
        self.session = control.Session(self.actor, self.target, EXECUTION)
        self.credential = Mock()
        self.credential.take.return_value = bytearray(b'A1' * 20)
        self.observer = Mock()
        self.capture = patch.object(control.polkit_capture, 'prepare', return_value={'prepared': True})
        self.make_credential = patch.object(control, 'Credential', return_value=self.credential)
        self.make_observer = patch.object(control, 'Proxy', return_value=self.observer)
        for patcher in (self.capture, self.make_credential, self.make_observer):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.deliver = Mock()

    def start(self):
        native = target()['session']
        return self.session.handle(request(control.START, native_session=native, runtime={}), self.deliver)

    def next(self, operation, **fields):
        pending = request(operation, **fields)
        pending['transaction_id'] = format(len(self.session.transactions) + 1, '032x')
        return self.session.handle(pending, self.deliver)

    def bind(self):
        self.next(control.BIND, attempt=target()['pending']['attempt'])

    def test_observer_starts_before_selection_and_one_authentication_consumes_credential(self):
        self.start()
        self.observer.start.assert_called_once()
        self.bind()
        stage = Mock()
        self.deliver.return_value = {'delivered': True}
        with patch.object(control, 'Stage', return_value=stage) as make:
            self.assertEqual(self.next(control.PURPOSE), {'delivered': True})
        self.assertEqual(make.call_args.args[3], bytearray(40))
        self.deliver.assert_called_once()
        self.credential.take.assert_called_once()
        with self.assertRaises(ValueError):
            self.next(control.CANCEL)
        stage.close.assert_called_once()
        self.observer.close.assert_called_once()
        self.credential.close.assert_called_once()

    def test_cancel_never_claims_a_credential_and_cleanup_survives_observer_failure(self):
        self.start()
        self.bind()
        with patch.object(control, 'Stage', return_value=Mock()) as make:
            self.next(control.CANCEL)
        self.assertIsNone(make.call_args.args[3])
        self.credential.take.assert_not_called()
        self.observer.close.side_effect = OSError('observer lost')
        with self.assertRaises(OSError):
            self.session.close()
        self.credential.close.assert_called_once()

    def test_uncertain_prepare_is_closed_without_starting_observer_or_replay(self):
        self.credential.prepare.side_effect = OSError('unknown prepare delivery')
        with self.assertRaises(OSError):
            self.start()
        self.observer.start.assert_not_called()
        self.credential.close.assert_called_once()
        with self.assertRaises(ValueError):
            self.start()
        self.deliver.assert_not_called()

    def test_execution_account_and_replayed_transaction_refuse_before_delivery(self):
        self.start()
        with self.assertRaises(ValueError):
            self.start()
        bad = request(control.OBSERVE)
        bad['binding'] = {**EXECUTION, 'execution_nonce': 'f' * 32}
        with self.assertRaises(ValueError):
            self.session.handle(bad, self.deliver)
        self.deliver.assert_not_called()

    def test_delivery_acknowledgment_cannot_assert_system_authentication_or_release(self):
        pending = request(control.PURPOSE)
        result = {'purpose': control.PURPOSE, 'transaction_id': pending['transaction_id'],
            'delivered': True, 'text_readback': False, 'host_release_verified': False}
        acknowledgment = {'format_version': 1, 'request_sha256': control.protocol.request_sha256(pending),
            'vm_id': 'owned', 'result': result}
        control.acknowledgment(acknowledgment, pending, 'owned')
        for changed in ({**result, 'host_release_verified': True}, {**result, 'system_authenticated': True}):
            with self.assertRaises(ValueError):
                control.acknowledgment({**acknowledgment, 'result': changed}, pending, 'owned')


if __name__ == '__main__':
    unittest.main()
