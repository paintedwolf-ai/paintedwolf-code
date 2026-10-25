"""Opaque profile parking preserves originals and refuses unsafe restoration."""
import json
import os
import sys
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release import qa_profile_custody as owner


@unittest.skipUnless(sys.platform == 'linux', 'Linux renameat2 custody contract')
class ProfileCustodyTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.backup = self.base / 'backup'
        self.backup.mkdir(mode=0o700)
        self.quiet = Mock()
        self.custody = owner.Custody(self.backup, self.quiet)
        self.addCleanup(self.custody.stack.close)
        self.custody.backup_fd = os.open(self.backup, os.O_RDONLY | os.O_DIRECTORY)
        self.custody.stack.callback(os.close, self.custody.backup_fd)
        patcher = patch.object(owner, 'UID', os.getuid())
        patcher.start()
        self.addCleanup(patcher.stop)
        # Native ownership admission is tested separately; transaction tests use
        # captured physical directories and the real exclusive rename operation.
        self.custody.validate_parents = Mock()
        for index in range(2):
            parent = self.base / str(index)
            parent.mkdir(mode=0o700)
            descriptor = os.open(parent, os.O_RDONLY | os.O_DIRECTORY)
            self.custody.stack.callback(os.close, descriptor)
            self.custody.parents.append((parent, descriptor, owner.identity(os.fstat(descriptor))))
            self.custody.rows.append(dict(path=str(parent / 'profile'), name='profile', original=None,
                                         parked=f'original-{index}', retained_fixture=f'fixture-{index}', fixture=None))

    def original(self, index):
        path = self.base / str(index) / 'profile'
        path.mkdir(mode=0o700)
        (path / 'opaque-credential').write_bytes(b'original is never read by custody')
        self.custody.rows[index]['original'] = owner.identity(path.stat())
        return path

    def test_present_and_absent_roots_restore_exactly_and_keep_fixture(self):
        original = self.original(0)
        before = owner.identity(original.stat())
        self.custody.record()
        self.custody.park()
        for index in range(2):
            (self.base / str(index) / 'profile' / 'fixture-data').write_bytes(b'fixture')
        self.custody.restore()
        self.assertEqual(owner.identity(original.stat()), before)
        self.assertFalse((self.base / '1/profile').exists())
        self.assertEqual((self.backup / 'fixture-0/fixture-data').read_bytes(), b'fixture')
        self.assertEqual((self.backup / 'fixture-1/fixture-data').read_bytes(), b'fixture')
        manifest = json.loads((self.backup / 'profile-recovery.json').read_bytes())
        self.assertEqual(manifest['phase'], 'restored')
        self.assertEqual(manifest['roots'][0]['original'], before)
        self.assertIsNone(manifest['roots'][1]['original'])

    def test_live_product_refuses_before_any_parking_and_before_restoration(self):
        original = self.original(0)
        self.custody.record()
        self.quiet.side_effect = ValueError('owned product remains live')
        with self.assertRaises(ValueError):
            self.custody.park()
        self.assertTrue(original.exists())
        self.assertFalse((self.backup / 'original-0').exists())
        self.quiet.side_effect = None
        self.custody.park()
        self.quiet.side_effect = ValueError('owned product remains live')
        with self.assertRaises(ValueError):
            self.custody.restore()
        self.assertTrue((self.backup / 'original-0').exists())
        self.assertEqual(json.loads((self.backup / 'profile-recovery.json').read_bytes())['phase'], 'parked')

    def test_pair_validation_refuses_replaced_fixture_without_moving_other_root(self):
        self.original(0)
        self.original(1)
        self.custody.record()
        self.custody.park()
        changed = self.base / '1/profile'
        changed.rename(self.base / '1/foreign-parked')
        changed.mkdir(mode=0o700)
        with self.assertRaises(ValueError):
            self.custody.restore()
        self.assertTrue((self.backup / 'original-0').exists())
        self.assertFalse((self.backup / 'fixture-0').exists())

    def test_recovery_manifest_precedes_first_effect_and_no_replace_preserves_destination(self):
        original = self.original(0)
        with patch.object(owner.Custody, 'prepare', side_effect=OSError('durability failure')):
            with self.assertRaises(OSError), owner.parked_profiles(self.backup, self.quiet):
                self.fail('failed preparation must never yield')
        self.assertTrue(original.exists())
        (self.backup / 'original-0').mkdir(mode=0o700)
        with self.assertRaises(FileExistsError):
            self.custody.park()
        self.assertTrue(original.exists())
        self.assertTrue((self.backup / 'original-0').exists())

    def test_link_roots_and_foreign_mode_are_refused_without_inspecting_contents(self):
        path = self.base / '0/profile'
        path.symlink_to(self.backup, target_is_directory=True)
        with self.assertRaises(ValueError):
            owner.entry(self.custody.parents[0][1], 'profile')
        path.unlink()
        path.mkdir(mode=0o755)
        with self.assertRaises(ValueError):
            owner.entry(self.custody.parents[0][1], 'profile')

    def test_fixed_admission_refuses_nonroot_or_conflicting_account_group(self):
        with patch.object(owner.os, 'geteuid', return_value=1000), self.assertRaises(ValueError):
            self.custody.prepare()
        account = Mock(pw_name='pwqual055', pw_uid=os.getuid(), pw_gid=os.getuid(), pw_dir=str(owner.HOME))
        group = Mock(gr_name='pwqual055', gr_gid=os.getuid())
        for attribute in ('pw_dir', 'pw_name', 'gr_name', 'gr_gid'):
            wrong_account, wrong_group = Mock(), Mock()
            wrong_account.configure_mock(pw_name='pwqual055', pw_uid=os.getuid(),
                                         pw_gid=os.getuid(), pw_dir=str(owner.HOME))
            wrong_group.configure_mock(gr_name='pwqual055', gr_gid=os.getuid())
            setattr(wrong_account if attribute.startswith('pw_') else wrong_group, attribute, 'foreign')
            with self.subTest(attribute=attribute), patch.object(owner.os, 'geteuid', return_value=0), \
                    patch.object(owner.pwd, 'getpwnam', return_value=account), \
                    patch.object(owner.pwd, 'getpwuid', return_value=wrong_account), \
                    patch.object(owner.grp, 'getgrnam', return_value=group), \
                    patch.object(owner.grp, 'getgrgid', return_value=wrong_group):
                with self.assertRaisesRegex(ValueError, 'account identity'):
                    self.custody.prepare()
                self.assertFalse((self.backup / 'profile-recovery.json').exists())

    def test_partial_parking_failure_retains_original_and_pre_effect_manifest(self):
        original = self.original(0)
        self.original(1)
        self.custody.record()
        actual_move = owner.move
        def interrupted(source, name, destination, target):
            self.assertTrue((self.backup / 'profile-recovery.json').is_file())
            if target == 'original-1':
                raise OSError('interruption')
            actual_move(source, name, destination, target)
        with patch.object(owner, 'move', side_effect=interrupted), self.assertRaises(OSError):
            self.custody.park()
        self.assertTrue((self.backup / 'original-0/opaque-credential').is_file())
        self.assertTrue((self.base / '1/profile/opaque-credential').is_file())
        with self.assertRaises(ValueError):
            self.custody.restore()
        self.assertFalse((self.backup / 'fixture-0').exists())
        self.assertEqual((self.backup / 'profile-recovery.json').stat().st_mode & 0o777, 0o600)

    def test_ancestor_link_is_refused_and_handles_are_closed(self):
        link = self.base / 'link'
        link.symlink_to(self.backup, target_is_directory=True)
        from contextlib import ExitStack
        with ExitStack() as stack, self.assertRaises((OSError, ValueError)):
            owner.directory(link, stack)

    def test_original_absence_survives_interruption_without_inventing_original_data(self):
        self.custody.record()
        actual_move = owner.move
        def interrupted(source, name, destination, target):
            if target == 'fixture-1':
                raise OSError('restore interrupted')
            actual_move(source, name, destination, target)
        self.custody.park()
        with patch.object(owner, 'move', side_effect=interrupted), self.assertRaises(OSError):
            self.custody.restore()
        self.assertFalse((self.base / '0/profile').exists())
        self.assertTrue((self.backup / 'fixture-0').is_dir())
        self.assertTrue((self.base / '1/profile').is_dir())
        manifest = json.loads((self.backup / 'profile-recovery.json').read_bytes())
        self.assertTrue(all(row['original'] is None for row in manifest['roots']))
        self.assertEqual(manifest['phase'], 'restoring')
