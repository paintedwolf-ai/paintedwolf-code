"""Private fixture streams bind real Linux peers and never use disk secret files."""
from concurrent.futures import ThreadPoolExecutor
import os
import errno
from pathlib import Path
import socket
import struct
import sys
import tempfile
import time
import threading
import unittest
from unittest.mock import patch

from linux_release.installed_cases.app import process_identity
from linux_release.installed_input import password_socket as private


@unittest.skipUnless(sys.platform == 'linux', 'requires Linux kernel SO_PEERCRED')
class PasswordSocketTests(unittest.TestCase):
    def setUp(self):
        self.expected = {'process': process_identity(os.getpid()), 'uid': os.getuid(),
                         'session_nonce': 'a' * 32}
        self.secret = 'aB3' * 13 + '7'

    def pair(self):
        first, second = socket.socketpair()
        self.addCleanup(first.close)
        self.addCleanup(second.close)
        deadline = time.monotonic() + 2
        return private.Channel(first, self.expected, deadline), private.Channel(second, self.expected, deadline)

    def test_actual_peer_identity_and_framed_private_value_round_trip(self):
        first, second = self.pair()
        first.send_fixture_value(self.secret)
        self.assertEqual(second.receive_fixture_value(), self.secret)
        second.send_metadata({'transaction_id': 'b' * 32, 'delivered': 41, 'completed': True})
        self.assertEqual(first.receive_metadata(), {'transaction_id': 'b' * 32, 'delivered': 41, 'completed': True})

    def test_peer_pid_uid_and_birth_are_independently_required(self):
        first, second = self.pair()
        for changed in ({**self.expected, 'uid': self.expected['uid'] + 1},
                        {**self.expected, 'process': {**self.expected['process'], 'pid': os.getpid() + 1}},
                        {**self.expected, 'process': {**self.expected['process'], 'start_time_ticks': 1}}):
            with self.assertRaises(ValueError):
                private.Channel(first.stream, changed, time.monotonic() + 1)
        private.peer(second.stream, self.expected)

    def test_oversized_truncated_and_invalid_private_frames_are_refused_without_echo(self):
        for body in (struct.pack('!I', 129), struct.pack('!I', 40) + b'!',
                     struct.pack('!I', 40) + b'!' * 40,
                     struct.pack('!I', 40) + b'\xff' * 40):
            first, second = self.pair()
            first.stream.sendall(body)
            first.stream.shutdown(socket.SHUT_WR)
            with self.assertRaises(ValueError) as refused:
                second.receive_fixture_value()
            self.assertNotIn(self.secret, str(refused.exception))
            self.assertNotIn('!!!!', str(refused.exception))
        first, second = self.pair()
        first.stream.sendall(struct.pack('!I', 5) + b'false')
        with self.assertRaises(ValueError):
            second.receive_metadata()

    def test_one_deadline_is_not_extended_by_a_partial_frame(self):
        first, second = self.pair()
        second.deadline = time.monotonic() - 1
        with self.assertRaises(ValueError):
            second.receive_metadata()
        first.stream.sendall(struct.pack('!I', 1))
        second.deadline = time.monotonic() + 0.05
        with self.assertRaises(TimeoutError):
            second.receive_metadata()

    def mailbox(self, directory):
        class Box:
            path = directory
            def attach(self):
                if self._directory().st_mode & 0o777 != 0o700:
                    raise ValueError('fixture directory lost its private mode')
            def _directory(self):
                value = self.path.lstat()
                if not self.path.is_dir() or value.st_uid != os.getuid():
                    raise ValueError('fixture directory changed')
                return value
        return Box()

    def test_case_listens_and_authenticated_ssh_peer_connects_then_exact_endpoint_is_removed(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            box = self.mailbox(directory)
            with private.Listener(box, self.expected) as listener:
                self.assertEqual(listener.path.stat().st_mode & 0o777, 0o600)
                def endpoint():
                    with private.connect(box, self.expected, time.monotonic() + 2) as channel:
                        return channel.receive_fixture_value()
                with ThreadPoolExecutor(max_workers=1) as pool:
                    future = pool.submit(endpoint)
                    channel = listener.accept(time.monotonic() + 2)
                    channel.send_fixture_value(self.secret)
                    self.assertEqual(future.result(timeout=3), self.secret)
                self.assertEqual(sorted(path.name for path in directory.iterdir()), [private.NAME])
            self.assertEqual(list(directory.iterdir()), [])

    def test_existing_or_replaced_socket_is_preserved_and_never_adopted(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            box = self.mailbox(directory)
            path = directory / private.NAME
            path.write_bytes(b'preexisting')
            with self.assertRaises(OSError):
                with private.Listener(box, self.expected):
                    pass
            self.assertEqual(path.read_bytes(), b'preexisting')
            path.unlink()
            listener = private.Listener(box, self.expected).__enter__()
            with socket.socket(socket.AF_UNIX) as replacement:
                other = directory / 'replacement'
                replacement.bind(str(other))
                os.replace(other, path)
                with self.assertRaises(ValueError):
                    listener.close()
                self.assertTrue(path.exists())

    def test_symlink_and_other_account_cannot_select_a_private_channel(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            box = self.mailbox(directory)
            with self.assertRaises(ValueError):
                private.Listener(box, {**self.expected, 'uid': os.getuid() + 1})
            (directory / private.NAME).symlink_to(directory / 'elsewhere')
            with self.assertRaises((OSError, ValueError)):
                with private.connect(box, self.expected, time.monotonic() + 1):
                    pass

    def test_private_bind_does_not_change_another_threads_file_creation_mask(self):
        entered, release = threading.Event(), threading.Event()
        real_umask = os.umask
        def paused(mask):
            previous = real_umask(mask)
            if mask == 0o177:
                entered.set()
                if not release.wait(2):
                    raise AssertionError('fixture probe did not observe the private mask')
            return previous
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            selected = [row.split()[1] for row in Path('/proc/self/status').read_text().splitlines()
                        if row.startswith('Umask:')]
            self.assertEqual(len(selected), 1)
            expected = 0o666 & ~int(selected[0], 8)
            def probe():
                try:
                    if not entered.wait(2):
                        raise AssertionError('private bind did not reach its isolated umask')
                    descriptor = os.open(directory / 'parallel-file', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o666)
                    try:
                        return os.fstat(descriptor).st_mode & 0o777
                    finally:
                        os.close(descriptor)
                finally:
                    release.set()
            with ThreadPoolExecutor(max_workers=1) as pool:
                result = pool.submit(probe)
                with patch.object(private.os, 'umask', side_effect=paused):
                    with private.Listener(self.mailbox(directory), self.expected):
                        self.assertEqual(result.result(timeout=3), expected)
            self.assertEqual((directory / 'parallel-file').stat().st_mode & 0o777, expected)

    def test_replacement_after_bind_is_preserved_without_any_foreign_mode_change(self):
        original = private._bind_private
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            path = directory / private.NAME
            identity = []
            def replaced(stream, selected):
                created = original(stream, selected)
                foreign = directory / 'foreign-object'
                descriptor = os.open(foreign, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o640)
                try:
                    os.fchmod(descriptor, 0o640)
                    os.write(descriptor, b'foreign fixture object')
                finally:
                    os.close(descriptor)
                os.replace(foreign, path)
                identity.append(private._file(path.lstat()))
                return created
            with patch.object(private, '_bind_private', side_effect=replaced), \
                    patch.object(Path, 'chmod', side_effect=AssertionError('pathname chmod forbidden')):
                with self.assertRaises(ValueError):
                    with private.Listener(self.mailbox(directory), self.expected):
                        pass
            self.assertEqual(private._file(path.lstat()), identity[0])
            self.assertEqual(path.read_bytes(), b'foreign fixture object')
            self.assertEqual(path.stat().st_mode & 0o777, 0o640)

    def test_refused_filesystem_unshare_never_changes_umask_or_binds_a_socket(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            with patch.object(private.os, 'unshare', side_effect=PermissionError(errno.EPERM, 'fixture denied'), create=True), \
                    patch.object(private.os, 'umask', side_effect=AssertionError('global umask forbidden')):
                with self.assertRaises(OSError) as failure:
                    with private.Listener(self.mailbox(directory), self.expected):
                        pass
            self.assertEqual(failure.exception.errno, errno.EPERM)
            self.assertEqual(list(directory.iterdir()), [])
