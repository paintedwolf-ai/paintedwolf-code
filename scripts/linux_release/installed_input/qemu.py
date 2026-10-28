"""Deliver sealed keyboard steps only to a root-leased, captured QEMU process."""
from __future__ import annotations

from contextlib import contextmanager
import copy
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import socket
import stat
import struct
import threading
import time

from .protocol import event_sha256, steps

LIMIT = 16384


def decode(raw):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError('input authority contains a duplicate field')
            value[key] = item
        return value
    return json.loads(raw, object_pairs_hook=unique)


def integer(value, minimum=0):
    return type(value) is int and minimum <= value < 1 << 63


def token(value):
    return isinstance(value, str) and re.fullmatch('[a-f0-9]{32}', value) is not None


def stable(info):
    return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode,
            info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def ancestors(path, owners):
    for parent in path.parents:
        info = parent.lstat()
        sticky = info.st_uid == 0 and info.st_mode & stat.S_ISVTX
        if (not stat.S_ISDIR(info.st_mode) or info.st_uid not in owners
                or (info.st_mode & 0o022 and not sticky)):
            raise ValueError('input authority has an unsafe physical ancestor')


def process_stat(path):
    with path.open('rb') as stream:
        raw = stream.read(8193)
    fields = raw.rpartition(b') ')[2].split()
    if len(raw) > 8192 or len(fields) < 20:
        raise ValueError('leased QEMU stat exceeds its fixed shape or bound')
    return fields


def selected(value, vm_id):
    fields = {'format_version', 'vm_id', 'uid', 'pid', 'start_time_ticks', 'argv_sha256', 'qmp', 'ssh'}
    if not isinstance(value, dict) or set(value) != fields or type(value['format_version']) is not int or value['format_version'] != 1:
        raise ValueError('QEMU input requires its exact root lease schema')
    if (not isinstance(vm_id, str) or re.fullmatch('[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}', vm_id) is None
            or value['vm_id'] != vm_id or not integer(value['uid'], 1) or value['uid'] != os.getuid()
            or not integer(value['pid'], 2) or not integer(value['start_time_ticks'], 1)
            or not isinstance(value['argv_sha256'], str) or re.fullmatch('[a-f0-9]{64}', value['argv_sha256']) is None):
        raise ValueError('QEMU input lease differs from its selected VM and process owner')
    qmp, ssh = value['qmp'], value['ssh']
    if (not isinstance(qmp, dict) or set(qmp) != {'path', 'device', 'inode'}
            or not isinstance(qmp['path'], str) or len(qmp['path'].encode()) > 107
            or not Path(qmp['path']).is_absolute() or '\0' in qmp['path']
            or not integer(qmp['device']) or not integer(qmp['inode'], 1)):
        raise ValueError('QEMU input requires its exact physical socket identity')
    if (not isinstance(ssh, dict) or set(ssh) != {'host', 'port', 'user'} or ssh['host'] != '127.0.0.1'
            or not integer(ssh['port'], 1) or ssh['port'] > 65535 or not isinstance(ssh['user'], str)
            or re.fullmatch('[a-z_][a-z0-9_-]{0,31}', ssh['user']) is None):
        raise ValueError('QEMU input lease has no fixed local guest endpoint')
    return value


class Actor:
    def __init__(self, descriptor, path, identity, value, execution_nonce):
        self.descriptor, self.path, self._identity = descriptor, path, copy.deepcopy(identity)
        self.value, self.nonce = copy.deepcopy(value), execution_nonce
        self.connection, self.buffer, self.command_id = None, b'', 0
        self.transactions, self.current, self.operation, self.ordinal = set(), None, None, 0
        self.failed, self.closed = False, False
        self.lock = threading.Lock()

    @property
    def identity(self):
        return copy.deepcopy(self._identity)

    def validate(self):
        info = os.fstat(self.descriptor)
        if stable(info) != tuple(self._identity['file']) or stable(self.path.lstat()) != tuple(self._identity['file']):
            raise ValueError('root input lease changed during the target')
        os.lseek(self.descriptor, 0, os.SEEK_SET)
        if hashlib.sha256(os.read(self.descriptor, LIMIT + 1)).hexdigest() != self._identity['sha256']:
            raise ValueError('root input lease bytes changed during the target')
        pid, uid = self.value['pid'], self.value['uid']
        process = Path('/proc') / str(pid)
        fields = process_stat(process / 'stat')
        with (process / 'status').open('r') as stream:
            status = stream.read(65537)
        if len(status) > 65536:
            raise ValueError('leased QEMU status exceeds its bound')
        uids = [line.split()[1:] for line in status.splitlines() if line.startswith('Uid:')]
        with (process / 'cmdline').open('rb') as stream:
            argv = stream.read(65537)
        if (len(fields) < 20 or int(fields[19]) != self.value['start_time_ticks']
                or uids != [[str(uid)] * 4] or not argv or len(argv) > 65536
                or hashlib.sha256(argv).hexdigest() != self.value['argv_sha256']
                or process_stat(process / 'stat')[19] != fields[19]):
            raise ValueError('leased QEMU process changed identity')
        path = Path(self.value['qmp']['path'])
        if path.resolve(strict=True) != path:
            raise ValueError('leased QMP socket is not physical')
        ancestors(path, {0, uid})
        info = path.lstat()
        if (not stat.S_ISSOCK(info.st_mode) or info.st_uid != uid or info.st_mode & 0o022
                or (info.st_dev, info.st_ino) != (self.value['qmp']['device'], self.value['qmp']['inode'])):
            raise ValueError('leased QMP socket changed identity')
        if self.connection is not None:
            peer = struct.unpack('iII', self.connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            if peer[:2] != (pid, uid):
                raise ValueError('QMP connection is not the leased process')

    def frame(self):
        while b'\n' not in self.buffer:
            if len(self.buffer) >= LIMIT:
                raise ValueError('QMP frame exceeds its fixed bound')
            block = self.connection.recv(min(4096, LIMIT - len(self.buffer)))
            if not block:
                raise ValueError('owned QMP connection ended')
            self.buffer += block
        raw, self.buffer = self.buffer.split(b'\n', 1)
        if len(raw) > LIMIT:
            raise ValueError('QMP frame exceeds its fixed bound')
        value = decode(raw)
        if not isinstance(value, dict):
            raise ValueError('QMP returned a non-object frame')
        return value

    def command(self, name, arguments=None):
        self.command_id += 1
        request = {'execute': name, 'id': self.command_id}
        if arguments is not None:
            request['arguments'] = arguments
        self.connection.sendall(json.dumps(request, separators=(',', ':')).encode() + b'\n')
        for _ in range(32):
            reply = self.frame()
            if 'event' in reply:
                if not isinstance(reply['event'], str):
                    raise ValueError('QMP event identity is invalid')
                continue
            if (set(reply) != {'id', 'return'} or type(reply['id']) is not int
                    or reply['id'] != self.command_id or not isinstance(reply['return'], dict)):
                raise ValueError('QMP did not acknowledge the exact owned command')
            return reply
        raise ValueError('QMP exceeded its bounded asynchronous event allowance')

    def connect(self):
        self.validate()
        self.connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.connection.settimeout(3)
        self.connection.connect(self.value['qmp']['path'])
        self.validate()
        greeting = self.frame()
        if set(greeting) != {'QMP'} or not isinstance(greeting['QMP'], dict):
            raise ValueError('leased socket has no QMP greeting')
        if self.command('qmp_capabilities')['return'] != {}:
            raise ValueError('leased QMP capabilities were not acknowledged')

    def send(self, operation, ordinal, *, transaction_id):
        with self.lock:
            if self.failed or self.closed:
                raise ValueError('input actor is no longer available')
            try:
                sequence = steps(operation)
                if (not token(transaction_id) or type(ordinal) is not int or not 0 <= ordinal < len(sequence)
                        or len(sequence) > 256):
                    raise ValueError('input step is outside the sealed transaction')
                if transaction_id != self.current:
                    if (self.current is not None and self.ordinal != len(steps(self.operation))
                            or transaction_id in self.transactions or len(self.transactions) >= 128 or ordinal != 0):
                        raise ValueError('input transactions cannot overlap or replay')
                    self.transactions.add(transaction_id)
                    self.current, self.operation, self.ordinal = transaction_id, operation, 0
                if operation != self.operation or ordinal != self.ordinal:
                    raise ValueError('input operation or ordinal differs from the pinned transaction')
                self.validate()
                status = self.command('query-status')['return']
                if status.get('running') is not True or status.get('status') != 'running':
                    raise ValueError('leased guest is not running')
                keys = [{'type': 'qcode', 'data': key} for key in sequence[ordinal]]
                reply = self.command('send-key', {'keys': keys, 'hold-time': 80})
                if reply['return'] != {}:
                    raise ValueError('QMP did not acknowledge its fixed keyboard step')
                # QMP acknowledges before its automatic key-release timer fires.
                time.sleep(0.1)
                self.validate()
                self.ordinal += 1
                return {'format_version': 1, 'execution_nonce': self.nonce, 'vm_id': self.value['vm_id'],
                        'transaction_id': transaction_id, 'operation': operation, 'ordinal': ordinal,
                        'event_sha256': event_sha256(operation, ordinal), 'lease': self.identity,
                        'qmp_ack': reply}
            except BaseException:
                self.failed = True
                raise

    def close(self):
        self.closed = True
        if self.connection is not None:
            self.connection.close()
            self.connection = None


@contextmanager
def open_lease(path: Path, *, execution_nonce: str, vm_id: str):
    if platform.system() != 'Linux' or not token(execution_nonce) or not path.is_absolute() or path.resolve(strict=True) != path:
        raise ValueError('fixed QEMU input requires a physical Linux host lease and fresh nonce')
    ancestors(path, {0})
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    actor = None
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o222 or not 0 < info.st_size <= LIMIT:
            raise ValueError('input lease must be an immutable root-owned regular file')
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        raw = os.read(descriptor, LIMIT + 1)
        if len(raw) != info.st_size or stable(path.lstat()) != stable(info):
            raise ValueError('root input lease changed while being selected')
        value = selected(decode(raw), vm_id)
        identity = {'vm_id': vm_id, 'sha256': hashlib.sha256(raw).hexdigest(), 'file': list(stable(info)), 'vm': value}
        actor = Actor(descriptor, path, identity, value, execution_nonce)
        actor.connect()
        yield actor
    finally:
        if actor is not None:
            actor.close()
        os.close(descriptor)
