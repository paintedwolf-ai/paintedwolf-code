"""One-use private app-password fixture stream in the pinned execution mailbox."""
from __future__ import annotations

from contextlib import contextmanager
import os
import errno
import socket
import stat
import struct
import time
import threading

from ..installed_cases.app import process_identity
from . import password_wire, protocol

# The campaign owns this QA UID; hostile controllers of that same UID are outside this fixture boundary.
NAME = 'app-password.sock'


def _file(info):
    return info.st_dev, info.st_ino, info.st_uid, info.st_mode, info.st_ctime_ns


def peer(stream, expected):
    """SO_PEERCRED is the socket peer, not the listener's or a public file's PID."""
    password_wire.actor(expected)
    pid, uid, _ = struct.unpack('iII', stream.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
    if pid != expected['process']['pid'] or uid != expected['uid']:
        raise ValueError('private fixture stream has the wrong kernel peer')
    actual = process_identity(pid)
    with open(f'/proc/{pid}/status', 'r') as source:
        status = source.read(65537)
    uids = [row.split()[1:] for row in status.splitlines() if row.startswith('Uid:')]
    if (len(status) > 65536 or uids != [[str(uid)] * 4] or actual != expected['process']
            or process_identity(pid) != actual):
        raise ValueError('private fixture peer changed its credentials or birth identity')
    return expected


def _bind_private(stream, path):
    # CLONE_FS gives only this short-lived thread a private umask/cwd/root.
    # The case's other threads must never observe a temporary process umask.
    result, failure = [], []
    def create():
        try:
            unshare, flag = getattr(os, 'unshare', None), getattr(os, 'CLONE_FS', None)
            if unshare is None or flag is None:
                raise OSError(errno.ENOSYS, 'private fixture filesystem isolation unavailable')
            unshare(flag)
            previous = os.umask(0o177)
            try:
                stream.bind(str(path))
                info = path.lstat()
                if (not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid()
                        or stat.S_IMODE(info.st_mode) != 0o600):
                    raise ValueError('private fixture socket was not created with its fixed mode')
                result.append(info)
            finally:
                os.umask(previous)
        except BaseException as error:
            failure.append(error)
    worker = threading.Thread(target=create, name='private-fixture-bind', daemon=True)
    worker.start()
    worker.join(3)
    if worker.is_alive():
        stream.close()
        raise OSError(errno.ETIMEDOUT, 'private fixture socket creation did not settle')
    if failure:
        error = failure[0]
        if isinstance(error, OSError):
            raise OSError(error.errno, 'private fixture socket creation unavailable') from None
        raise ValueError('private fixture socket creation refused') from None
    if len(result) != 1:
        raise ValueError('private fixture socket creation omitted its identity')
    return result[0]


class Channel:
    def __init__(self, stream, expected, deadline):
        self.stream, self.expected, self.deadline = stream, expected, deadline
        peer(stream, expected)

    def check(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise ValueError('private fixture stream expired')
        peer(self.stream, self.expected)
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise ValueError('private fixture stream expired')
        self.stream.settimeout(remaining)

    def _read(self, count):
        chunks = bytearray()
        while len(chunks) < count:
            self.check()
            block = self.stream.recv(count - len(chunks))
            if not block:
                raise ValueError('private fixture stream ended before completion')
            chunks.extend(block)
        return bytes(chunks)

    def _receive(self, maximum):
        return self._read(password_wire.frame_size(self._read(4), maximum))

    def _send(self, raw):
        self.check()
        self.stream.sendall(password_wire.frame(raw, protocol.MAX_DOCUMENT))
        self.check()

    def receive_metadata(self):
        from .mailbox import decode
        try:
            return decode(self._receive(protocol.MAX_DOCUMENT))
        except (ValueError, UnicodeError):
            raise ValueError('private fixture metadata has an invalid fixed shape') from None

    def send_metadata(self, document):
        self._send(protocol.canonical(document))

    def receive_fixture_value(self):
        try:
            return password_wire.private_secret(self._receive(128).decode('ascii'))
        except (ValueError, UnicodeError):
            raise ValueError('private fixture value has an invalid fixed shape') from None

    def send_fixture_value(self, value):
        self._send(password_wire.private_secret(value).encode('ascii'))


class Listener:
    """The case listens; only the independently selected SSH endpoint may connect."""
    def __init__(self, mailbox, expected_actor):
        self.mailbox, self.expected = mailbox, password_wire.actor(expected_actor)
        if self.expected['uid'] != os.getuid():
            raise ValueError('private fixture actor differs from its owned guest account')
        self.path = mailbox.path / NAME
        self.stream, self.connection, self.created = None, None, None
        self.directory, self.directory_fd = None, None

    def __enter__(self):
        self.mailbox.attach()
        self.directory = _file(self.mailbox._directory())[:4]
        self.directory_fd = os.open(self.mailbox.path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            self.stream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            if self.directory != _file(os.fstat(self.directory_fd))[:4]:
                raise ValueError('private fixture directory changed before socket creation')
            # bind refuses an existing endpoint; no stale or public socket is reused.
            self.created = _file(_bind_private(self.stream, self.path))
            self.check()
            self.stream.listen(1)
            return self
        except BaseException:
            self.close()
            raise

    def check(self):
        actual = self.path.lstat()
        if (self.created != _file(actual) or not stat.S_ISSOCK(actual.st_mode)
                or actual.st_uid != os.getuid() or stat.S_IMODE(actual.st_mode) != 0o600
                or self.directory != _file(self.mailbox._directory())[:4]
                or self.directory != _file(os.fstat(self.directory_fd))[:4]):
            raise ValueError('private fixture endpoint changed its physical identity')

    def accept(self, deadline):
        self.check()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise ValueError('private fixture input expired before connection')
        self.stream.settimeout(remaining)
        self.connection, _ = self.stream.accept()
        self.check()
        return Channel(self.connection, self.expected, deadline)

    def close(self):
        if self.connection is not None:
            self.connection.close()
        if self.stream is not None:
            self.stream.close()
        try:
            if self.created is not None:
                actual = os.stat(NAME, dir_fd=self.directory_fd, follow_symlinks=False)
                if (self.created != _file(actual)
                        or self.directory != _file(self.mailbox._directory())[:4]
                        or self.directory != _file(os.fstat(self.directory_fd))[:4]):
                    raise ValueError('private fixture endpoint replacement is preserved for recovery')
                os.unlink(NAME, dir_fd=self.directory_fd)
                self.created = None
        finally:
            if self.directory_fd is not None:
                os.close(self.directory_fd)
                self.directory_fd = None

    def __exit__(self, *error):
        self.close()


@contextmanager
def connect(mailbox, expected_case, deadline):
    """The authenticated SSH endpoint connects to the captured case's listener."""
    mailbox.attach()
    directory = _file(mailbox._directory())[:4]
    descriptor = os.open(mailbox.path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if directory != _file(os.fstat(descriptor))[:4]:
            raise ValueError('private fixture case directory changed before connection')
        path = mailbox.path / NAME
        before = path.lstat()
        if (not stat.S_ISSOCK(before.st_mode) or before.st_uid != os.getuid()
                or stat.S_IMODE(before.st_mode) != 0o600 or path.resolve(strict=True) != path):
            raise ValueError('private fixture case has no controlled physical endpoint')
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as stream:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise ValueError('private fixture connection expired')
            stream.settimeout(remaining)
            stream.connect(str(path))
            if (_file(path.lstat()) != _file(before) or directory != _file(mailbox._directory())[:4]
                    or directory != _file(os.fstat(descriptor))[:4]):
                raise ValueError('private fixture endpoint changed during connection')
            yield Channel(stream, expected_case, deadline)
    finally:
        os.close(descriptor)
