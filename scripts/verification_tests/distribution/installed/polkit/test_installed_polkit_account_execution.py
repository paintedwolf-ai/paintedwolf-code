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


class StaleCredentialTests(unittest.TestCase):
    def fixture(self):
        from copy import deepcopy
        from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target
        selected = {'host': '127.0.0.1', 'port': 2222, 'user': 'qa'}
        actor = Mock(nonce=EXECUTION['execution_nonce'], value={'vm_id': 'owned', 'ssh': selected})
        native = deepcopy(target()['session'])
        value = polkit_account.StaleCredential(actor, selected, EXECUTION, ACCOUNT, native)
        value._command = Mock(return_value={'settled': True})
        return value, native

    def test_one_prepare_two_named_consumed_claims_and_only_final_claim_erases_master(self):
        from copy import deepcopy
        value, native = self.fixture()
        original = bytes(value.secret)
        value.prepare()
        from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target
        pending = target()['pending']
        value.pending(pending)
        with self.assertRaises(ValueError): value.polkit_claim(pending)
        with self.assertRaises(ValueError): value.take()
        locked = deepcopy(native)
        locked.update(locked_hint=True, locker_active=True)
        first = value.unlock_claim(locked)
        self.assertEqual(bytes(first), original)
        self.assertEqual(bytes(value.secret), original)
        with self.assertRaises(ValueError): value.unlock_claim(locked)
        with self.assertRaises(ValueError): value.polkit_claim({'native_session': native, 'attempt': value.attempt})
        value.unlocked(native)
        second = value.polkit_claim({'native_session': native, 'attempt': value.attempt})
        self.assertEqual(bytes(second), original)
        self.assertEqual(value.secret, bytearray(40))
        with self.assertRaises(ValueError): value.polkit_claim({'native_session': native, 'attempt': value.attempt})
        first[:] = second[:] = bytearray(40)
        value.close()
        self.assertEqual([call.args[0] for call in value._command.call_args_list], ['prepare', 'disable'])

    def test_rebound_identity_and_changed_session_owners_never_claim(self):
        from copy import deepcopy
        value, native = self.fixture()
        value.prepare()
        with self.assertRaises(ValueError): value.bind({**ACCOUNT, 'uid': ACCOUNT['uid'] + 1}, native)
        replaced = deepcopy(native)
        replaced['authorities']['shell']['inode'] += 1
        with self.assertRaises(ValueError): value.bind(ACCOUNT, replaced)
        self.assertEqual(value.phase, 'prepared')
        value.close()
        value._command.assert_called_with('disable')

    def test_uncertain_prepare_and_abandoned_first_claim_both_disable_without_replay(self):
        from copy import deepcopy
        for failed_prepare in (True, False):
            value, native = self.fixture()
            if failed_prepare:
                value._command.side_effect = [OSError('uncertain'), {'disabled': True}]
                with self.assertRaises(OSError): value.prepare()
                with self.assertRaises(ValueError): value.prepare()
            else:
                value.prepare()
                from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target
                value.pending(target()['pending'])
                locked = deepcopy(native)
                locked.update(locked_hint=True, locker_active=True)
                copy = value.unlock_claim(locked)
                copy[:] = bytearray(40)
            value.close()
            self.assertEqual(value.secret, bytearray(40))
            value._command.assert_called_with('disable')
            with self.assertRaises(ValueError): value.polkit_claim({'native_session': native, 'attempt': value.attempt})


    def test_cancel_after_unlock_disables_and_erases_unused_original_PAM_claim(self):
        from copy import deepcopy
        from verification_tests.distribution.installed.polkit.test_installed_polkit_stage_execution import target
        value, native = self.fixture()
        value.prepare()
        value.pending(target()['pending'])
        locked = deepcopy(native)
        locked.update(locked_hint=True, locker_active=True)
        copy = value.unlock_claim(locked)
        copy[:] = bytearray(40)
        value.unlocked(native)
        self.assertTrue(any(value.secret))
        value.close()
        value.close()
        self.assertEqual(value.secret, bytearray(40))
        self.assertEqual([call.args[0] for call in value._command.call_args_list], ['prepare', 'disable'])
        with self.assertRaises(ValueError):
            value.polkit_claim({'native_session': native, 'attempt': value.attempt})


if __name__ == '__main__':
    unittest.main()
