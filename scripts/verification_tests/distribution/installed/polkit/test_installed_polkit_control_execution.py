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


    def test_stale_session_borrows_one_prepared_lifetime_and_cleanup_survives_observer_loss(self):
        lifetime = Mock()
        lifetime.polkit_claim.return_value = bytearray(b'A1' * 20)
        native = target()['session']
        self.observer.observe.return_value = {'native_session': native}
        self.session = control.Session(self.actor, self.target, EXECUTION, stale_credential=lifetime)
        self.session.handle(request(control.START_STALE, native_session=native, runtime={}), self.deliver)
        lifetime.bind.assert_called_once_with(ACCOUNT, native)
        lifetime.prepare.assert_not_called()
        self.bind()
        with patch.object(control, 'Stage', return_value=Mock()):
            self.next(control.PURPOSE)
        lifetime.polkit_claim.assert_called_once_with({'native_session': native})
        lifetime.take.assert_not_called()
        self.observer.close.side_effect = OSError('observer lost')
        with self.assertRaises(OSError): self.session.close()
        lifetime.close.assert_called_once()

    def test_stale_start_without_lifetime_never_prepares_another_password(self):
        with self.assertRaises(ValueError):
            self.session.handle(request(control.START_STALE, native_session=target()['session'], runtime={}), self.deliver)
        self.credential.prepare.assert_not_called()
        self.observer.start.assert_not_called()


    def test_bridge_owns_prepare_and_transport_failure_cleanup_without_guest_secret_frame(self):
        from scripts.linux_release.installed_input import controller, polkit_account, unlock_wire
        from scripts.linux_release.installed_input import polkit_capture
        from verification_tests.distribution.installed.test_installed_native_unlock_execution import target as unlock_target
        selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/tmp/lease', 'vm_id': 'owned'}
        bridge = controller.Bridge(selected, EXECUTION, [], Mock(), target=self.target)
        bridge.actor = self.actor
        bridge._record_log = Mock()
        bridge.exchange = Mock(return_value={'state': 'acknowledged'})
        native = unlock_target()['session']
        native['native_session'].update(locked_hint=False, locker_active=False)
        pending = {'format_version': 1, 'binding': EXECUTION, 'transaction_id': 'a' * 32,
                   'operation': unlock_wire.STALE_PREPARE, 'native_session': native, 'account': ACCOUNT}
        lifetime = Mock()
        with patch.object(polkit_account, 'StaleCredential', return_value=lifetime), \
                patch.object(polkit_capture, 'prepare'):
            acknowledgment = bridge.prepare_stale(pending)
        lifetime.prepare.assert_called_once_with()
        self.assertNotIn('secret', str(acknowledgment))
        self.assertNotIn('sha256', str(acknowledgment['result']))
        self.assertEqual(bridge.exchange.call_args.args[0]['operation'], 'ack')
        self.assertIsNone(bridge.unlock_actor)
        with self.assertRaises(ValueError): bridge.prepare_stale(pending)
        bridge.exchange.side_effect = OSError('private transport ended')
        bridge._run()
        lifetime.close.assert_called_once_with()
        self.assertIsInstance(bridge.error, OSError)

    def test_bridge_uncertain_preparation_disables_without_publishing_success(self):
        from scripts.linux_release.installed_input import controller, polkit_account, polkit_capture, unlock_wire
        from verification_tests.distribution.installed.test_installed_native_unlock_execution import target as unlock_target
        bridge = controller.Bridge({'format_version': 1, 'kind': 'qemu-fixed-input',
            'lease': '/tmp/lease', 'vm_id': 'owned'}, EXECUTION, [], Mock(), target=self.target)
        bridge.actor = self.actor
        bridge.exchange = Mock()
        bridge._record_log = Mock()
        native = unlock_target()['session']
        native['native_session'].update(locked_hint=False, locker_active=False)
        pending = {'format_version': 1, 'binding': EXECUTION, 'transaction_id': 'a' * 32,
                   'operation': unlock_wire.STALE_PREPARE, 'native_session': native, 'account': ACCOUNT}
        lifetime = Mock()
        lifetime.prepare.side_effect = OSError('preparation uncertain')
        with patch.object(polkit_account, 'StaleCredential', return_value=lifetime), \
                patch.object(polkit_capture, 'prepare'), self.assertRaises(OSError):
            bridge.prepare_stale(pending)
        lifetime.close.assert_called_once_with()
        bridge.exchange.assert_not_called()
        bridge._record_log.assert_not_called()


if __name__ == '__main__':
    unittest.main()
