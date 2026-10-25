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
        patcher = patch.object(owner, 'UID', os.getuid())
        patcher.start()
        self.addCleanup(patcher.stop)
        self.custody = owner.Custody(self.backup, self.quiet)
        self.addCleanup(self.custody.stack.close)
        self.custody.backup_fd = os.open(self.backup, os.O_RDONLY | os.O_DIRECTORY)
        self.custody.stack.callback(os.close, self.custody.backup_fd)
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


    def presence_custody(self):
        patcher = patch.object(owner, 'PRESENCE_UID', os.getuid())
        patcher.start()
        self.addCleanup(patcher.stop)
        account = dict(name='pwqual058a2', uid=os.getuid(), gid=os.getuid(), home='/home/pwqual058a2')
        prior = self.custody
        presence = owner.Custody(self.backup, self.quiet, account=account)
        self.addCleanup(presence.stack.close)
        presence.backup_fd, presence.parents, presence.rows = prior.backup_fd, prior.parents, prior.rows
        presence.validate_parents = prior.validate_parents
        self.custody = presence
        return presence

    def test_presence_launch_owns_creation_and_preserves_original_pair_with_zero_one_or_two_roots(self):
        # Native admission is tested separately; real rename operations project
        # the fixed account's ownership into this isolated test process.
        presence = self.presence_custody()
        originals = [self.original(index) for index in range(2)]
        before = [owner.identity(path.stat()) for path in originals]
        for count in range(3):
            with self.subTest(created=count):
                presence.record()
                presence.park()
                self.assertTrue(presence.pair_parked)
                self.assertFalse(any(path.exists() for path in originals))
                for path in originals[:count]:
                    path.mkdir(mode=0o700)
                    (path / 'new-attempt').write_bytes(b'opaque new trial')
                presence.restore()
                self.assertEqual([owner.identity(path.stat()) for path in originals], before)
                for index in range(count):
                    retained = self.backup / f'fixture-{index}'
                    self.assertEqual((retained / 'new-attempt').read_bytes(), b'opaque new trial')
                    retained.rename(self.backup / f'attempt-{count}-fixture-{index}')
                manifest = json.loads((self.backup / 'profile-recovery.json').read_bytes())
                self.assertEqual(manifest['schema'], 'pwqual058a2-profile-custody-v1')
                self.assertEqual(manifest['phase'], 'restored')
                self.assertEqual([row['original'] for row in manifest['roots']], before)

    def test_presence_partial_park_never_captures_untouched_original(self):
        presence = self.presence_custody()
        self.original(0)
        self.original(1)
        presence.record()
        actual_move = owner.move
        def interrupted(source, name, destination, target):
            if target == 'original-1': raise OSError('interruption')
            actual_move(source, name, destination, target)
        with patch.object(owner, 'move', side_effect=interrupted), self.assertRaises(OSError):
            presence.park()
        self.assertFalse(presence.pair_parked)
        with patch.object(owner, 'move') as moved, self.assertRaisesRegex(ValueError, 'partial'):
            presence.restore()
        moved.assert_not_called()
        self.assertIsNone(presence.rows[1]['fixture'])
        self.assertTrue((self.base / '1/profile/opaque-credential').is_file())
        self.assertTrue((self.backup / 'original-0/opaque-credential').is_file())

    def test_presence_failed_durable_park_does_not_admit_capture(self):
        presence = self.presence_custody()
        self.original(0)
        presence.record()
        record = presence.record
        def interrupted():
            if presence.phase == 'parked': raise OSError('durability failure')
            record()
        with patch.object(presence, 'record', side_effect=interrupted), self.assertRaises(OSError): presence.park()
        self.assertFalse(presence.pair_parked)
        with patch.object(owner, 'move') as moved, self.assertRaisesRegex(ValueError, 'partial'): presence.restore()
        moved.assert_not_called()
        self.assertTrue((self.backup / 'original-0/opaque-credential').is_file())

    def test_presence_capture_validates_whole_pair_and_is_durable_before_any_restoration(self):
        presence = self.presence_custody()
        self.original(0)
        self.original(1)
        presence.record()
        presence.park()
        first, second = (self.base / str(index) / 'profile' for index in range(2))
        first.mkdir(mode=0o700)
        second.mkdir(mode=0o755)
        with patch.object(owner, 'move') as moved, self.assertRaises(ValueError): presence.restore()
        moved.assert_not_called()
        self.assertTrue(all(row['fixture'] is None for row in presence.rows))
        second.chmod(0o700)
        record = presence.record
        def failed_capture():
            if presence.phase == 'fixtures_captured': raise OSError('durability failure')
            record()
        with patch.object(presence, 'record', side_effect=failed_capture), \
                patch.object(owner, 'move') as moved, self.assertRaises(OSError): presence.restore()
        moved.assert_not_called()
        actual_move = owner.move
        def witnessed_move(*args):
            manifest = json.loads((self.backup / 'profile-recovery.json').read_bytes())
            self.assertEqual(manifest['phase'], 'restoring')
            self.assertTrue(all(row['fixture'] is not None for row in manifest['roots']))
            actual_move(*args)
        with patch.object(owner, 'move', side_effect=witnessed_move): presence.restore()


class FixedProfileAccountTests(unittest.TestCase):
    PRESENCE = dict(name='pwqual058a2', uid=30060, gid=30060, home='/home/pwqual058a2')
    REPLY = dict(name='pwqual055', uid=30005, gid=30005, home='/home/pwqual055')

    def test_only_exact_existing_accounts_can_be_selected_and_selection_is_pinned(self):
        self.assertEqual(dict(owner.selected_account()), self.REPLY)
        self.assertEqual(dict(owner.selected_account(self.PRESENCE)), self.PRESENCE)
        for changes in ({'name': 'pwtest'}, {'name': 'pwqual058a3'}, {'uid': 30005}, {'gid': 30005},
                        {'uid': True}, {'home': '/home/pwqual055'}, {'home': '/home/pwqual058a2/../pwqual055'}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                owner.selected_account({**self.PRESENCE, **changes})
        for value in ({**self.PRESENCE, 'backup': '/var/backups/foreign'}, {'name': 'pwqual058a2'}):
            with self.assertRaises(ValueError): owner.selected_account(value)
        supplied = dict(self.PRESENCE)
        custody = owner.Custody('/var/backups/paintedwolf-qualification/056-presence-058a2-a2', Mock(), account=supplied)
        self.addCleanup(custody.stack.close)
        supplied['uid'] = 0
        self.assertEqual(dict(custody.account), self.PRESENCE)
        with self.assertRaises(TypeError): custody.account['uid'] = 0

    def test_cross_account_and_old_attempt_backup_families_refuse_before_open_or_move(self):
        from types import SimpleNamespace
        cases = [(self.PRESENCE, '/var/backups/paintedwolf-qualification/055-reply-a20'),
                 (self.REPLY, '/var/backups/paintedwolf-qualification/056-presence-058a2-a2'),
                 (self.REPLY, '/var/backups/paintedwolf-qualification/vault-quiet-058a2-a1'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/vault-quiet-058a2-a0'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/vault-quiet-058a2-a1000'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/vault-quiet-058a3-a1'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/vault-quiet-058a2-a1/../foreign'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/056-existing058a2-8372-a1'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/056-presence-058a2-a0'),
                 (self.PRESENCE, '/var/backups/paintedwolf-qualification/056-presence-058a2-a2/../foreign')]
        for selected, backup in cases:
            with self.subTest(selected=selected, backup=backup):
                account = SimpleNamespace(pw_name=selected['name'], pw_uid=selected['uid'],
                    pw_gid=selected['gid'], pw_dir=selected['home'])
                group = SimpleNamespace(gr_name=selected['name'], gr_gid=selected['gid'])
                custody = owner.Custody(backup, Mock(), account=selected)
                self.addCleanup(custody.stack.close)
                with patch.object(owner.os, 'geteuid', return_value=0), \
                        patch.object(owner.pwd, 'getpwnam', return_value=account), \
                        patch.object(owner.pwd, 'getpwuid', return_value=account), \
                        patch.object(owner.grp, 'getgrnam', return_value=group), \
                        patch.object(owner.grp, 'getgrgid', return_value=group), \
                        patch.object(owner, 'directory') as opened, patch.object(owner, 'move') as moved:
                    with self.assertRaisesRegex(ValueError, 'backup family'): custody.prepare()
                opened.assert_not_called()
                moved.assert_not_called()
                custody.quiet.assert_not_called()

    def test_quiet_and_presence_families_reach_the_same_fixed_account_descriptor_owner(self):
        from types import SimpleNamespace
        selected = self.PRESENCE
        account = SimpleNamespace(pw_name=selected['name'], pw_uid=selected['uid'],
            pw_gid=selected['gid'], pw_dir=selected['home'])
        group = SimpleNamespace(gr_name=selected['name'], gr_gid=selected['gid'])
        for family in ('056-presence', 'vault-quiet'):
            for attempt in (1, 999):
                backup = Path(f'/var/backups/paintedwolf-qualification/{family}-058a2-a{attempt}')
                custody = owner.Custody(backup, Mock(), account=selected)
                self.addCleanup(custody.stack.close)
                with self.subTest(family=family, attempt=attempt), \
                        patch.object(owner.os, 'geteuid', return_value=0), \
                        patch.object(owner.pwd, 'getpwnam', return_value=account), \
                        patch.object(owner.pwd, 'getpwuid', return_value=account), \
                        patch.object(owner.grp, 'getgrnam', return_value=group), \
                        patch.object(owner.grp, 'getgrgid', return_value=group), \
                        patch.object(owner, 'directory', side_effect=LookupError('descriptor boundary')) as opened, \
                        patch.object(owner, 'move') as moved:
                    with self.assertRaisesRegex(LookupError, 'descriptor boundary'): custody.prepare()
                opened.assert_called_once_with(backup, custody.stack, custody.account)
                self.assertTrue(custody.launch_owned)
                moved.assert_not_called()
                custody.quiet.assert_not_called()
