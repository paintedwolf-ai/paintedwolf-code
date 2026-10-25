"""Only an independently marked disposable OS account receives one private credential."""
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_input import polkit_account
from verification_tests.distribution.installed.test_installed_native_unlock_execution import ACCOUNT, EXECUTION


class PolkitCredentialTests(unittest.TestCase):
    def credential(self):
        selected = {'host': '127.0.0.1', 'port': 2222, 'user': 'qa'}
        actor = Mock(nonce=EXECUTION['execution_nonce'], value={'vm_id': 'owned', 'ssh': selected})
        value = polkit_account.Credential(actor, selected, EXECUTION, ACCOUNT)
        value._command = Mock(side_effect=lambda action, frame=None: {'prepared': True} if action == 'prepare' else {'disabled': True})
        return value

    def test_private_frame_and_master_are_zeroed_and_custody_can_be_claimed_only_once(self):
        value = self.credential()
        original = bytes(value.secret)
        self.assertEqual(value.prepare(), {'prepared': True})
        frame = value._command.call_args.args[1]
        self.assertEqual(frame, bytearray(44))
        transferred = value.take()
        self.assertEqual(bytes(transferred), original)
        self.assertEqual(value.secret, bytearray(40))
        with self.assertRaises(ValueError):
            value.take()
        transferred[:] = b'\0' * len(transferred)
        value.close()
        value._command.assert_called_with('disable')
        value.close()
        self.assertEqual(value._command.call_count, 2)

    def test_uncertain_preparation_is_consumed_but_still_requires_marked_account_cleanup(self):
        value = self.credential()
        value._command.side_effect = [OSError('uncertain'), {'disabled': True}]
        with self.assertRaises(OSError):
            value.prepare()
        with self.assertRaises(ValueError):
            value.prepare()
        with self.assertRaises(ValueError):
            value.take()
        self.assertEqual(value.close(), {'disabled': True})
        self.assertEqual(value.secret, bytearray(40))
        value._command.assert_called_with('disable')

    def test_no_root_trial_or_foreign_lease_account_can_construct_credential_custody(self):
        selected = {'host': '127.0.0.1', 'port': 2222, 'user': 'qa'}
        actor = Mock(nonce=EXECUTION['execution_nonce'], value={'vm_id': 'owned', 'ssh': selected})
        for name in ('root', 'pwtest', 'pwqual055a1'):
            with self.subTest(name=name), self.assertRaises(ValueError):
                polkit_account.Credential(actor, selected, EXECUTION, {**ACCOUNT, 'name': name})
        with self.assertRaises(ValueError):
            polkit_account.Credential(actor, {**selected, 'port': 2223}, EXECUTION, ACCOUNT)

    def test_rebound_account_never_runs_either_lock_command(self):
        from types import SimpleNamespace
        from scripts.linux_release.installed_input import unlock_account
        original = SimpleNamespace(pw_name=ACCOUNT['name'], pw_uid=ACCOUNT['uid'],
            pw_gid=ACCOUNT['gid'], pw_dir=ACCOUNT['home'])
        rebound = SimpleNamespace(**{**vars(original), 'pw_uid': ACCOUNT['uid'] + 1})
        with patch.object(unlock_account.os, 'geteuid', return_value=0), \
                patch.object(unlock_account, '_verify_marker'), \
                patch.object(unlock_account.pwd, 'getpwnam', return_value=rebound), \
                patch.object(unlock_account.pwd, 'getpwuid', return_value=original), \
                patch.object(unlock_account.grp, 'getgrgid', return_value=SimpleNamespace(
                    gr_name=ACCOUNT['name'], gr_gid=ACCOUNT['gid'])), \
                patch.object(unlock_account.native_command, 'command') as command:
            with self.assertRaises(ValueError):
                unlock_account.disable_account(ACCOUNT, vm_id='owned')
        command.assert_not_called()

    def test_cleanup_failure_still_zeroes_private_master(self):
        value = self.credential()
        value.prepare()
        value._command.side_effect = OSError('cleanup unverified')
        with self.assertRaises(OSError):
            value.close()
        self.assertEqual(value.secret, bytearray(40))
        self.assertTrue(value.closed)


if __name__ == '__main__':
    unittest.main()
