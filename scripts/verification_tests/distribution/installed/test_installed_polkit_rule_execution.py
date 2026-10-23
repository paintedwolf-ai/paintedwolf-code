"""The guest-only presence policy lease cannot replace or discard another rule."""
import json
import os
from pathlib import Path
import tempfile
import subprocess
import sys
import unittest
from unittest import mock

from linux_release.installed_cases import polkit_rule as rule


class RuleLeaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.original = self.directory / '50-existing.rules'
        self.original.write_bytes(b'// existing administrator policy\n')
        self.original.chmod(0o644)
        self.descriptor = rule._directory(self.directory)
        self.addCleanup(os.close, self.descriptor)
        self.lease = rule.RuleLease(self.descriptor, 'a' * 32, 'campaign-user')
        self.path = self.directory / self.lease.name

    def test_exact_owned_rule_is_removed_and_prior_policy_is_unchanged(self):
        before = rule._identity(self.original.stat())
        installed = self.lease.install()
        self.assertEqual(installed['action_id'], rule.ACTION)
        self.assertEqual(installed['object']['mode'] & 0o777, 0o644)
        self.assertIn(b'subject.user === "campaign-user"', self.path.read_bytes())
        restored = self.lease.remove()
        self.assertTrue(restored['restored'])
        self.assertFalse(self.path.exists())
        self.assertEqual(rule._identity(self.original.stat()), before)
        self.assertEqual(rule._inventory(self.descriptor), installed['before'])

    def test_only_closed_execution_nonce_and_quoted_account_enter_the_rule(self):
        for nonce in ('', '../outside', 'A' * 32, 'a' * 33):
            with self.assertRaises(ValueError):
                rule.RuleLease(self.descriptor, nonce, 'campaign-user')
        account = 'quote"\\line\n'
        lease = rule.RuleLease(self.descriptor, 'b' * 32, account)
        self.assertIn(('subject.user === ' + json.dumps(account)).encode(), lease.body)
        self.assertEqual(lease.body.count(b'polkit.Result.YES'), 1)
        self.assertEqual(lease.body.count(rule.ACTION.encode()), 1)

    def test_preexisting_same_name_is_never_overwritten_or_removed(self):
        self.path.write_bytes(b'// another campaign\n')
        self.path.chmod(0o644)
        lease = rule.RuleLease(self.descriptor, 'a' * 32, 'campaign-user')
        with self.assertRaises(FileExistsError):
            lease.install()
        self.assertEqual(lease.remove(), {'created': False, 'restored': True})
        self.assertEqual(self.path.read_bytes(), b'// another campaign\n')

    def test_same_bytes_at_a_replacement_inode_are_preserved(self):
        self.lease.install()
        replacement = self.directory / 'replacement'
        replacement.write_bytes(self.lease.body)
        replacement.chmod(0o644)
        os.replace(replacement, self.path)
        with self.assertRaises(ValueError):
            self.lease.remove()
        self.assertEqual(self.path.read_bytes(), self.lease.body)

    def test_modified_owned_file_or_added_hardlink_requires_recovery(self):
        self.lease.install()
        self.path.write_bytes(b'// changed policy\n')
        with self.assertRaises(ValueError):
            self.lease.remove()
        self.assertEqual(self.path.read_bytes(), b'// changed policy\n')
        second = rule.RuleLease(self.descriptor, 'b' * 32, 'campaign-user')
        second.install()
        target = self.directory / second.name
        os.link(target, self.directory / 'extra-link')
        with self.assertRaises(ValueError):
            second.remove()
        self.assertTrue(target.exists())

    def test_symlink_replacement_does_not_follow_or_remove_its_target(self):
        self.lease.install()
        self.path.unlink()
        self.path.symlink_to(self.original)
        with self.assertRaises(OSError):
            self.lease.remove()
        self.assertTrue(self.path.is_symlink())
        self.assertEqual(self.original.read_bytes(), b'// existing administrator policy\n')

    def test_unrelated_policy_change_is_reported_without_restoring_over_it(self):
        self.lease.install()
        self.original.write_bytes(b'// concurrent administrator change\n')
        restored = self.lease.remove()
        self.assertFalse(restored['restored'])
        self.assertFalse(self.path.exists())
        self.assertEqual(self.original.read_bytes(), b'// concurrent administrator change\n')

    def test_partial_write_or_sync_failure_still_removes_the_exact_created_file(self):
        write = os.write
        def partial(descriptor, raw):
            if raw == self.lease.body:
                return write(descriptor, raw[:17])
            raise OSError('fixture disk failure')
        with mock.patch.object(rule.os, 'write', side_effect=partial):
            with self.assertRaises(OSError):
                self.lease.install()
        self.assertTrue(self.lease.remove()['restored'])
        with mock.patch.object(rule.os, 'fsync', side_effect=OSError('fixture sync failure')):
            with self.assertRaises(OSError):
                self.lease.install()
        self.assertTrue(self.lease.remove()['restored'])

    def test_replacement_during_install_is_not_adopted_as_the_owned_object(self):
        read = rule._file
        def replace(directory, name):
            replacement = self.directory / 'replacement'
            replacement.write_bytes(b'// replacement during install\n')
            replacement.chmod(0o644)
            os.replace(replacement, self.path)
            return read(directory, name)
        with mock.patch.object(rule, '_file', side_effect=replace):
            with self.assertRaises(ValueError):
                self.lease.install()
        with self.assertRaises(ValueError):
            self.lease.remove()
        self.assertEqual(self.path.read_bytes(), b'// replacement during install\n')

    def test_uncontrolled_directory_or_policy_link_is_refused_before_install(self):
        self.directory.chmod(0o777)
        with self.assertRaises(ValueError):
            rule._directory(self.directory)
        self.directory.chmod(0o700)
        link = self.directory / '20-link.rules'
        link.symlink_to(self.original)
        with self.assertRaises(OSError):
            rule.RuleLease(self.descriptor, 'b' * 32, 'campaign-user')
        self.assertFalse(self.path.exists())

    def test_privileged_entry_requires_a_nonroot_sudo_campaign_account(self):
        with mock.patch.object(rule.os, 'geteuid', return_value=1234):
            with self.assertRaises(ValueError):
                rule._campaign_account()
        with mock.patch.object(rule.os, 'geteuid', return_value=0):
            for value in ('', '0', '-1', 'not-a-uid'):
                with mock.patch.dict(rule.os.environ, {'SUDO_UID': value}):
                    with self.assertRaises(ValueError):
                        rule._campaign_account()

    def test_termination_during_a_write_defers_until_cleanup_knows_the_exact_object(self):
        code = """
import importlib.util, os, signal, sys
spec = importlib.util.spec_from_file_location('owned_rule_fixture', sys.argv[1])
rule = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rule)
directory = rule._directory(rule.Path(sys.argv[2]))
lease = rule.RuleLease(directory, 'c' * 32, 'campaign-user')
original_write = os.write
def interrupted_write(descriptor, raw):
    written = original_write(descriptor, raw)
    os.kill(os.getpid(), signal.SIGTERM)
    return written
signal.signal(signal.SIGTERM, rule._interrupted)
os.write = interrupted_write
try:
    try:
        lease.install()
    except InterruptedError:
        pass
    else:
        raise AssertionError('pending termination was not delivered')
finally:
    os.write = original_write
    assert lease.remove()['restored'] is True
    os.close(directory)
"""
        result = subprocess.run([sys.executable, '-I', '-c', code, str(Path(rule.__file__).resolve()),
                                 str(self.directory)], capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors='replace'))
        self.assertFalse((self.directory / ('00-paintedwolf-qualification-' + 'c' * 32 + '.rules')).exists())
