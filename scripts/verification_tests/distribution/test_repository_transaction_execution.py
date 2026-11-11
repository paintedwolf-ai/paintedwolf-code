"""Repository transactions retain one lock through independent readback and finalization."""

import fcntl
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import repository_transaction as native
from scripts.linux_release import repository_transaction as transaction
from scripts.verification_tests.distribution.native_repository_fixtures import generation, trust


class RepositoryTransactionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.catalog = trust(self.root / 'trust')
        self.receipt, self.directory = generation('deb-0.0.1', self.root)
        self.store = self.root / 'origin'

    def publish(self):
        return transaction.publish(self.receipt, self.directory, self.store,
            'https://repository.example.invalid', self.catalog, None, True)

    def assert_locked(self):
        with (self.store / '.publication.lock').open() as probe:
            with self.assertRaises(BlockingIOError):
                fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_readback_activation_and_pointer_directory_sync_share_the_lock(self):
        events = []
        admit, activate = transaction.admit, native.activate
        replace, fsync = transaction.replace, os.fsync
        def observed_admit(receipt, directory, *args):
            if directory != self.directory:
                self.assert_locked()
                events.append('readback')
            return admit(receipt, directory, *args)
        def observed_activate(*args):
            self.assert_locked()
            events.append('activate')
            return activate(*args)
        def observed_replace(path, data):
            self.assert_locked()
            replace(path, data)
            if path.name == 'current.json':
                events.append('pointer')
        def observed_fsync(descriptor):
            if events and events[-1] == 'pointer':
                self.assert_locked()
                parent = self.store / 'channels/deb/preview'
                actual, expected = os.fstat(descriptor), parent.stat()
                self.assertEqual((actual.st_dev, actual.st_ino), (expected.st_dev, expected.st_ino))
                events.append('directory-sync')
            fsync(descriptor)
        with patch.object(transaction, 'admit', side_effect=observed_admit), \
                patch.object(native, 'activate', side_effect=observed_activate), \
                patch.object(transaction, 'replace', side_effect=observed_replace), \
                patch.object(os, 'fsync', side_effect=observed_fsync):
            record = self.publish()
        self.assertEqual(events, ['readback', 'activate', 'pointer', 'directory-sync'])
        self.assertIn('configuration', record)
        with (self.store / '.publication.lock').open() as probe:
            fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_corrupted_retained_bytes_refuse_before_activation(self):
        immutable = transaction.immutable
        changed = False
        def corrupt(path, data):
            nonlocal changed
            immutable(path, data)
            if not changed and path.suffix == '.deb':
                path.write_bytes(data + b'corrupt-retained')
                changed = True
        with patch.object(transaction, 'immutable', side_effect=corrupt), \
                patch.object(native, 'activate') as activate:
            with self.assertRaises(ValueError):
                self.publish()
        self.assertTrue(changed)
        activate.assert_not_called()
        self.assertFalse((self.store / 'channels/deb/preview/current.json').exists())

    def test_undeclared_family_refuses_before_storage(self):
        self.receipt = {**self.receipt, 'family': 'pacman'}
        with self.assertRaisesRegex(ValueError, 'declared package family'):
            self.publish()
        self.assertFalse(self.store.exists())
