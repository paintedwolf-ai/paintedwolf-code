"""Private, nonce-selected handoff for a guest's fixed native input requests."""
from __future__ import annotations

import json
import os
import argparse
from pathlib import Path
import stat
import sys
import tempfile

from . import protocol


def decode(raw: bytes) -> dict:
    if len(raw) > protocol.MAX_DOCUMENT:
        raise ValueError('native input document exceeds its bound')
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('native input document repeats a key')
            result[key] = value
        return result
    value = json.loads(raw, object_pairs_hook=pairs,
                       parse_constant=lambda _: (_ for _ in ()).throw(ValueError('invalid JSON number')))
    if not isinstance(value, dict):
        raise ValueError('native input document must be an object')
    protocol.canonical(value)
    return value


class Mailbox:
    def __init__(self, execution: dict, vm_id: str):
        self.execution = protocol.binding(execution)
        if not isinstance(vm_id, str) or not protocol.IDENTIFIER.fullmatch(vm_id):
            raise ValueError('native input requires its selected VM identity')
        self.path = Path('/tmp') / ('paintedwolf-native-input-' + execution['execution_nonce'])
        self.vm_id = vm_id

    def marker(self):
        path = Path('/etc/paintedwolf-input-qualification.json')
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            before = os.fstat(descriptor)
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_gid != 0
                    or before.st_mode & 0o222 or before.st_size > protocol.MAX_DOCUMENT
                    or path.resolve() != path):
                raise ValueError('native input requires its root-owned readonly VM marker')
            raw = os.read(descriptor, protocol.MAX_DOCUMENT + 1)
            after = os.fstat(descriptor)
            identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
            if identity(before) != identity(after) or identity(after) != identity(path.lstat()):
                raise ValueError('native input VM marker changed during observation')
            document = decode(raw)
            if type(document.get('format_version')) is not int or document != {'format_version': 1, 'vm_id': self.vm_id}:
                raise ValueError('native input guest differs from the selected VM identity')
        finally:
            os.close(descriptor)

    def _directory(self):
        value = self.path.lstat()
        if (not stat.S_ISDIR(value.st_mode) or value.st_uid != os.getuid()
                or stat.S_IMODE(value.st_mode) != 0o700 or self.path.resolve() != self.path):
            raise ValueError('native input mailbox lost its physical private directory')
        return value

    def create(self):
        self.marker()
        self.path.mkdir(mode=0o700)
        self._directory()
        self.write('binding.json', {'format_version': 1, 'binding': self.execution, 'vm_id': self.vm_id})

    def attach(self):
        self.marker()
        self._directory()
        if self.read('binding.json') != {'format_version': 1, 'binding': self.execution, 'vm_id': self.vm_id}:
            raise ValueError('native input mailbox differs from its fresh execution')

    @staticmethod
    def _name(name):
        if name not in {'binding.json', 'request.json', 'ack.json'}:
            raise ValueError('native input mailbox refuses caller-selected paths')

    def read(self, name: str) -> dict | None:
        self._name(name)
        before_directory = self._directory()
        try:
            descriptor = os.open(self.path / name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except FileNotFoundError:
            return None
        try:
            before = os.fstat(descriptor)
            if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                    or stat.S_IMODE(before.st_mode) != 0o600
                    or before.st_size > protocol.MAX_DOCUMENT):
                raise ValueError('native input mailbox file lost its private bounded identity')
            raw = os.read(descriptor, protocol.MAX_DOCUMENT + 1)
            after = os.fstat(descriptor)
            identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns)
            if (identity(before) != identity(after) or identity(after) != identity((self.path / name).lstat())
                    or before_directory.st_ino != self._directory().st_ino):
                raise ValueError('native input mailbox changed during its bounded read')
            return decode(raw)
        finally:
            os.close(descriptor)

    def write(self, name: str, document: dict):
        self._name(name)
        self._directory()
        raw = protocol.canonical(document)
        descriptor, temporary = tempfile.mkstemp(prefix='.pending-', dir=self.path)
        try:
            with os.fdopen(descriptor, 'wb') as stream:
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
            os.link(temporary, self.path / name, follow_symlinks=False)
        finally:
            Path(temporary).unlink()

    def clear_completed(self, pending: dict):
        if self.read('request.json') != pending:
            raise ValueError('native input pending request changed before completion')
        acknowledgment = self.read('ack.json')
        protocol.acknowledgment(acknowledgment, pending, self.vm_id)
        (self.path / 'request.json').unlink()
        (self.path / 'ack.json').unlink()


def serve(execution: dict, vm_id: str):
    mailbox = Mailbox(execution, vm_id)
    acknowledged = None
    for _ in range(protocol.MAX_TRANSACTIONS * 256 * 20):
        raw = sys.stdin.buffer.readline(protocol.MAX_DOCUMENT + 1)
        if not raw:
            return
        command = decode(raw)
        if command == {'operation': 'close'}:
            return
        if command == {'operation': 'poll'}:
            if not mailbox.path.exists():
                result = {'state': 'waiting'}
            else:
                mailbox.attach()
                pending = mailbox.read('request.json')
                if pending is None:
                    acknowledged = None
                    result = {'state': 'waiting'}
                else:
                    protocol.request(pending, execution)
                    digest = protocol.request_sha256(pending)
                    result = {'state': 'acknowledged'} if digest == acknowledged else {'state': 'request', 'request': pending}
        elif set(command) == {'operation', 'acknowledgment'} and command['operation'] == 'ack':
            mailbox.attach()
            pending = mailbox.read('request.json')
            protocol.request(pending, execution)
            protocol.acknowledgment(command['acknowledgment'], pending, vm_id)
            if acknowledged == protocol.request_sha256(pending) or mailbox.read('ack.json') is not None:
                raise ValueError('native input acknowledgment cannot be replayed')
            mailbox.write('ack.json', command['acknowledgment'])
            acknowledged = protocol.request_sha256(pending)
            result = {'state': 'acknowledged'}
        else:
            raise ValueError('native input server refuses unknown operations')
        sys.stdout.buffer.write(protocol.canonical(result))
        sys.stdout.buffer.flush()
    raise ValueError('native input server exceeded its fixed frame bound')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binding', required=True)
    parser.add_argument('--vm-id', required=True)
    args = parser.parse_args()
    serve(decode(args.binding.encode()), args.vm_id)


if __name__ == '__main__':
    main()
