"""Lease one fixed presence rule in a disposable guest, then restore its absence."""
from __future__ import annotations

import hashlib
from contextlib import ExitStack
import json
import os
from pathlib import Path
import pwd
import re
import select
import signal
import stat
import sys

ACTION = 'dev.paintedwolf.code.confirm-presence'
RULES = Path('/etc/polkit-1/rules.d')
SHARED_RULES = Path('/usr/share/polkit-1/rules.d')
MARKER = Path('/etc/paintedwolf-qualification.json')
MAX_RULE_BYTES = 1 << 20


def _identity(info):
    return {'device': info.st_dev, 'inode': info.st_ino, 'uid': info.st_uid,
            'gid': info.st_gid, 'mode': info.st_mode, 'links': info.st_nlink,
            'bytes': info.st_size, 'mtime_ns': info.st_mtime_ns, 'ctime_ns': info.st_ctime_ns}


def _controlled(info, directory=False):
    kind = stat.S_ISDIR if directory else stat.S_ISREG
    if not kind(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        raise ValueError('polkit rule object is not controlled by its privileged custodian')


def _directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        _controlled(os.fstat(descriptor), directory=True)
        return descriptor
    except BaseException:
        os.close(descriptor)
        raise


def _file(directory, name):
    descriptor = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
    try:
        before = os.fstat(descriptor)
        _controlled(before)
        if before.st_size > MAX_RULE_BYTES:
            raise ValueError('polkit rule exceeds the bounded policy inventory')
        with os.fdopen(descriptor, 'rb', closefd=False) as stream:
            raw = stream.read(MAX_RULE_BYTES + 1)
        if (len(raw) > MAX_RULE_BYTES or _identity(before) != _identity(os.fstat(descriptor))
                or _identity(before) != _identity(os.stat(name, dir_fd=directory, follow_symlinks=False))):
            raise ValueError('polkit rule changed during its policy snapshot')
        return {**_identity(before), 'sha256': hashlib.sha256(raw).hexdigest()}
    finally:
        os.close(descriptor)


def _inventory(directory):
    names = sorted(name for name in os.listdir(directory) if name.endswith('.rules'))
    if len(names) > 256:
        raise ValueError('polkit policy inventory exceeds its fixed rule count')
    return {name: _file(directory, name) for name in names}


class RuleLease:
    """Keep the directory FD and exact created object until the rule is removed."""
    def __init__(self, directory, nonce, username):
        if not isinstance(nonce, str) or not re.fullmatch('[a-f0-9]{32}', nonce):
            raise ValueError('presence rule requires its fixed execution nonce')
        if not isinstance(username, str) or not username or len(username) > 128:
            raise ValueError('presence rule requires the selected campaign account')
        self.directory, self.created = directory, None
        self.name = '00-paintedwolf-qualification-' + nonce + '.rules'
        self.body = ('polkit.addRule(function(action, subject) {\n'
            f'  if (action.id === {json.dumps(ACTION)} && subject.user === {json.dumps(username)}) {{\n'
            '    return polkit.Result.YES;\n  }\n});\n').encode()
        self.before = _inventory(directory)

    def install(self):
        descriptor = os.open(self.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                             0o644, dir_fd=self.directory)
        try:
            # Remember the exclusive creation before writing, including partial-write failure.
            self.created = {**_identity(os.fstat(descriptor)), 'sha256': hashlib.sha256(b'').hexdigest()}
            os.fchmod(descriptor, 0o644)
            self.created = {**_identity(os.fstat(descriptor)), 'sha256': hashlib.sha256(b'').hexdigest()}
            offset = 0
            while offset < len(self.body):
                written = os.write(descriptor, self.body[offset:])
                if written <= 0:
                    raise OSError('presence rule write did not advance')
                offset += written
                self.created = {**_identity(os.fstat(descriptor)),
                                'sha256': hashlib.sha256(self.body[:offset]).hexdigest()}
            os.fsync(descriptor)
            if _file(self.directory, self.name) != self.created:
                raise ValueError('exclusive presence rule changed during installation')
            return {'name': self.name, 'action_id': ACTION, 'object': self.created,
                    'before': self.before}
        finally:
            os.close(descriptor)

    def remove(self):
        if self.created is None:
            return {'created': False, 'restored': _inventory(self.directory) == self.before}
        actual = _file(self.directory, self.name)
        if actual != self.created:
            raise ValueError('owned presence rule changed; replacement is preserved for recovery')
        os.unlink(self.name, dir_fd=self.directory)
        os.fsync(self.directory)
        self.created = None
        after = _inventory(self.directory)
        return {'created': True, 'removed': True, 'restored': after == self.before, 'after': after}


def _campaign_account():
    if os.geteuid() != 0:
        raise ValueError('presence rule custodian requires the guest administrator')
    uid = os.environ.get('SUDO_UID', '')
    if not re.fullmatch('[0-9]{1,10}', uid) or int(uid) == 0:
        raise ValueError('presence rule requires its nonroot sudo campaign account')
    info = MARKER.lstat()
    _controlled(info)
    if info.st_size > 4096:
        raise ValueError('owned guest marker exceeds its fixed bound')
    marker = json.loads(MARKER.read_bytes())
    if (not isinstance(marker, dict) or set(marker) != {'format_version', 'desktop', 'package'}
            or type(marker['format_version']) is not int or marker['format_version'] != 1
            or marker['desktop'] not in {'gnome', 'plasma'}
            or marker['package'] != '/opt/painted-wolf-code'):
        raise ValueError('presence rule requires the declared native disposable desktop guest')
    return pwd.getpwuid(int(uid)).pw_name


def _emit(value):
    print(json.dumps(value, sort_keys=True), flush=True)


def _interrupted(number, frame):
    raise InterruptedError('presence policy lease interrupted')


def main(nonce):
    username = _campaign_account()
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(number, _interrupted)
    with ExitStack() as resources:
        directory = _directory(RULES)
        resources.callback(os.close, directory)
        shared = _directory(SHARED_RULES)
        resources.callback(os.close, shared)
        lease = RuleLease(directory, nonce, username)
        baseline = _inventory(shared)
        try:
            installed = lease.install()
            _emit({'phase': 'installed', 'pid': os.getpid(), 'rule': installed})
            readable, _, _ = select.select([sys.stdin], [], [], 300)
            if not readable or sys.stdin.buffer.readline(16) != b'release\n':
                raise ValueError('presence policy lease ended without its closed release request')
        finally:
            for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
                signal.signal(number, signal.SIG_IGN)
            restored = lease.remove()
            _emit({'phase': 'restored', 'rule': restored,
                   'shared_policy_unchanged': _inventory(shared) == baseline})


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('presence rule custodian requires one execution nonce')
    main(sys.argv[1])
