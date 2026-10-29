"""The owned System V fixture creates, verifies and always releases its host objects."""
import errno
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import ipc


class FakeLibc:
    """System V calls keyed by object kind; -1 marks a refused creation."""

    def __init__(self, ids=(11, 12, 13), fail=None, ctl_result=0, lookup=None):
        self.ids = dict(zip(('shmget', 'semget', 'msgget'), ids))
        self.fail, self.ctl_result, self.lookup = fail, ctl_result, lookup
        self.calls = []

    def get(self, name, *arguments):
        self.calls.append((name, arguments))
        if name == self.fail:
            return -1
        if arguments[-1] == 0 and self.lookup is not None:
            return self.lookup
        return self.ids[name]

    def ctl(self, name, *arguments):
        self.calls.append((name, arguments))
        return self.ctl_result

    def __getattr__(self, name):
        if name.endswith('get'):
            return lambda *arguments: self.get(name, *arguments)
        return lambda *arguments: self.ctl(name, *arguments)


def owned(libc: FakeLibc):
    fake = SimpleNamespace(CDLL=lambda *_, **__: libc, get_errno=lambda: errno.ENOSPC, c_void_p=lambda: None)
    return patch.object(ipc, 'ctypes', fake)


class OwnedIPCTests(unittest.TestCase):
    def test_fixture_creates_private_objects_and_releases_each_one(self):
        libc = FakeLibc()
        with owned(libc), ipc.OwnedIPC() as host:
            self.assertEqual(host.created, {'shared_memory': 11, 'semaphore': 12, 'message_queue': 13})
            self.assertTrue(0 < host.key < 1 << 31)
            creations = [call for call in libc.calls if call[0].endswith('get') and call[1][-1] == 0o3600]
            self.assertEqual([call[1][0] for call in creations], [host.key] * 3)
            self.assertEqual([call[1] for call in libc.calls if call[0] == 'shmget'][0], (host.key, 4096, 0o3600))
            self.assertTrue(host.intact())
        self.assertEqual([call for call in libc.calls if call[0].endswith('ctl')],
                         [('shmctl', (11, 0, None)), ('semctl', (12, 0, 0)), ('msgctl', (13, 0, None))])
        self.assertEqual(host.created, {})

    def test_refused_creation_releases_what_was_already_made(self):
        libc = FakeLibc(fail='semget')
        with owned(libc), self.assertRaises(OSError) as caught:
            with ipc.OwnedIPC():
                self.fail('fixture entered without every object')
        self.assertEqual(caught.exception.errno, errno.ENOSPC)
        self.assertEqual([call for call in libc.calls if call[0].endswith('ctl')], [('shmctl', (11, 0, None))])

    def test_changed_identity_during_setup_is_refused_and_released(self):
        libc = FakeLibc(lookup=99)
        with owned(libc), self.assertRaises(ValueError):
            with ipc.OwnedIPC():
                self.fail('fixture entered with a changed identity')
        self.assertEqual(len([call for call in libc.calls if call[0].endswith('ctl')]), 3)

    def test_failed_release_is_reported_after_every_object_was_attempted(self):
        libc = FakeLibc(ctl_result=-1)
        with owned(libc), self.assertRaisesRegex(OSError, 'cleanup failed'):
            with ipc.OwnedIPC():
                pass
        self.assertEqual(len([call for call in libc.calls if call[0].endswith('ctl')]), 3)


if __name__ == '__main__':
    unittest.main()
