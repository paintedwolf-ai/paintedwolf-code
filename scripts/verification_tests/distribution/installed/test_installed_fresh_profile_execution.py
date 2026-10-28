"""Exclusive normal-storage reservation preserves existing, partial and replaced leaves."""
import dataclasses
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.installed_cases import storage as installed_storage
from linux_release.installed_cases import fresh_profile


class FreshProfileTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='.pw-fresh-profile-test-', dir=Path.home().resolve())
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name).resolve()
        self.home.chmod(0o700)
        for path in (self.home / '.config', self.home / '.local', self.home / '.local/share'):
            path.mkdir(mode=0o700)
        selected = patch.object(Path, 'home', return_value=self.home)
        selected.start()
        self.addCleanup(selected.stop)
        guest = patch.object(fresh_profile, 'require_owned_guest')
        guest.start()
        self.addCleanup(guest.stop)
        self.storage = installed_storage.resolve({'HOME': str(self.home)})

    def test_new_leaves_are_pinned_private_and_retained_after_normal_product_writes(self):
        reservation = fresh_profile.Reservation({}, self.storage)
        with reservation:
            reservation.check(empty=True)
            self.assertEqual(len(reservation.created), 2)
            for path in (self.storage.config, self.storage.webview):
                self.assertEqual(path.stat().st_mode & 0o777, 0o700)
            (self.storage.config / 'normal-product-state').write_bytes(b'fixture state')
            reservation.check()
            with self.assertRaises(ValueError):
                reservation.check(empty=True)
        self.assertTrue(self.storage.webview.is_dir())
        self.assertEqual((self.storage.config / 'normal-product-state').read_bytes(), b'fixture state')
        self.assertTrue(reservation.facts()['complete'])
        with self.assertRaises(ValueError):
            reservation.__enter__()
        with self.assertRaises(ValueError):
            fresh_profile.Reservation({}, self.storage).__enter__()

    def test_both_absences_precede_creation_and_existing_webview_is_preserved(self):
        self.storage.webview.mkdir(mode=0o700)
        marker = self.storage.webview / 'existing'
        marker.write_bytes(b'existing profile')
        with self.assertRaises(ValueError):
            fresh_profile.Reservation({}, self.storage).__enter__()
        self.assertFalse(self.storage.config.exists())
        self.assertEqual(marker.read_bytes(), b'existing profile')

    def test_existing_symlink_and_its_target_are_never_opened_or_removed(self):
        target = self.home / 'existing-profile'
        target.mkdir(mode=0o700)
        marker = target / 'existing'
        marker.write_bytes(b'untouched')
        self.storage.config.symlink_to(target, target_is_directory=True)
        with self.assertRaises(ValueError):
            fresh_profile.Reservation({}, self.storage).__enter__()
        self.assertTrue(self.storage.config.is_symlink())
        self.assertEqual(marker.read_bytes(), b'untouched')
        self.assertFalse(self.storage.webview.exists())

    def test_late_collision_retains_partial_owned_creation_and_foreign_data(self):
        mkdir = os.mkdir
        def collision(name, mode=0o777, *, dir_fd=None):
            if name == self.storage.webview.name:
                mkdir(name, 0o700, dir_fd=dir_fd)
                (self.storage.webview / 'concurrent').write_bytes(b'preserve collision')
                raise FileExistsError('fixture collision')
            return mkdir(name, mode, dir_fd=dir_fd)
        reservation = fresh_profile.Reservation({}, self.storage)
        with patch.object(fresh_profile.os, 'mkdir', side_effect=collision), self.assertRaises(FileExistsError) as failure:
            reservation.__enter__()
        self.assertEqual((self.storage.webview / 'concurrent').read_bytes(), b'preserve collision')
        self.assertTrue(self.storage.config.is_dir())
        self.assertFalse(failure.exception.observations['fresh_storage']['complete'])
        self.assertEqual(len(failure.exception.observations['fresh_storage']['created']), 1)

    def test_replaced_leaf_is_refused_and_no_foreign_mode_or_content_changes(self):
        with fresh_profile.Reservation({}, self.storage) as reservation:
            old = self.home / 'retained-original'
            self.storage.config.rename(old)
            self.storage.config.mkdir(mode=0o755)
            marker = self.storage.config / 'replacement'
            marker.write_bytes(b'foreign data')
            with self.assertRaises(ValueError):
                reservation.check()
        self.assertEqual(marker.read_bytes(), b'foreign data')
        self.assertEqual(self.storage.config.stat().st_mode & 0o777, 0o755)
        self.assertTrue(old.is_dir())

    def test_unknown_created_identity_is_retained_without_claiming_a_verified_inode(self):
        original = os.stat
        def interrupted(path, *args, **kwargs):
            result = original(path, *args, **kwargs)
            if path == self.storage.config.name:
                raise PermissionError('fixture identity unavailable after creation')
            return result
        with patch.object(fresh_profile.os, 'stat', side_effect=interrupted), self.assertRaises(PermissionError) as failure:
            fresh_profile.Reservation({}, self.storage).__enter__()
        created = failure.exception.observations['fresh_storage']['created']
        self.assertEqual(created, [{'path': str(self.storage.config), 'identity_verified': False}])
        self.assertTrue(self.storage.config.is_dir())
        self.assertFalse(self.storage.webview.exists())

    def test_expired_reservation_or_caller_selected_alternate_storage_cannot_continue(self):
        alternate = dataclasses.replace(self.storage, config=self.home / 'alternate')
        with self.assertRaises(ValueError):
            fresh_profile.Reservation({}, alternate)
        with fresh_profile.Reservation({}, self.storage) as reservation:
            with patch.object(fresh_profile.time, 'monotonic', return_value=reservation.deadline + 1), self.assertRaises(ValueError):
                reservation.check()
        self.assertTrue(self.storage.config.is_dir())
