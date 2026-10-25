"""The guest endpoint reads only the current modal and never receives a credential."""
from copy import deepcopy
import unittest
from unittest.mock import Mock

from scripts.linux_release.installed_input import polkit_control, polkit_guest
from verification_tests.distribution.installed.polkit.test_installed_polkit_control_execution import request
from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target


class PolkitGuestTests(unittest.TestCase):
    def test_fixed_cancel_reads_one_actual_target_without_any_password_payload(self):
        pending = request(polkit_control.CANCEL)
        envelope = {'purpose': pending['operation'], 'binding': pending['binding'],
                    'transaction_id': pending['transaction_id']}
        pipe = Mock()
        observed = target()
        pipe.receive_metadata.side_effect = [
            {**envelope, 'ordinal': 0, 'phase': 'observe', 'pending': observed['pending']},
            {**envelope, 'phase': 'complete'}]
        observer = Mock()
        observer.observe.return_value = observed
        polkit_guest.exchange(pipe, observer, pending)
        observer.observe.assert_called_once_with(observed['pending'])
        self.assertEqual(pipe.send_metadata.call_args.args[0], {**envelope, 'phase': 'delivered'})
        self.assertNotIn('credential', repr(pipe.send_metadata.call_args_list))

    def test_replayed_or_foreign_ordinal_cannot_read_target(self):
        pending = request(polkit_control.CANCEL)
        pipe, observer = Mock(), Mock()
        pipe.receive_metadata.return_value = {'purpose': pending['operation'], 'binding': pending['binding'],
            'transaction_id': pending['transaction_id'], 'ordinal': 1,
            'phase': 'observe', 'pending': target()['pending']}
        with self.assertRaises(ValueError):
            polkit_guest.exchange(pipe, observer, pending)
        observer.observe.assert_not_called()


if __name__ == '__main__':
    unittest.main()

class PolkitClientTests(unittest.TestCase):
    def test_uncertain_publication_is_retained_and_close_never_overwrites_it(self):
        from scripts.linux_release.installed_input import polkit_client
        from verification_tests.distribution.installed.test_installed_native_unlock_execution import ACCOUNT, EXECUTION
        from unittest.mock import patch
        client = polkit_client.NativePolkitControl.__new__(polkit_client.NativePolkitControl)
        client.execution, client.vm_id, client.account = EXECUTION, 'owned', ACCOUNT
        client.session_id, client.records = 'e' * 32, []
        client.started, client.closed, client.failed = True, False, False
        client.app, client.mailbox = Mock(), Mock()
        client.mailbox.read.return_value = None
        with patch.object(polkit_client.time, 'monotonic', side_effect=[0, 41]):
            with self.assertRaises(polkit_client.CaseUnavailable):
                client.observe()
        client.close()
        self.assertTrue(client.failed)
        self.assertTrue(client.closed)
        self.assertEqual(client.mailbox.write.call_count, 1)
        client.mailbox.clear_completed.assert_not_called()
        self.assertEqual(set(client.records[0]), {'request'})
