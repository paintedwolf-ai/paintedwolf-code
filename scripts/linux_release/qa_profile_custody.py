"""Opaque two-root custody inside an already admitted fixed QA seat.

The root seat owner supplies its pinned product-process quiet check. This leaf
never admits a VM, selects an artifact, provisions an account, or reads a profile.
"""
from contextlib import contextmanager, ExitStack
import ctypes
import grp
import json
import os
from pathlib import Path
import pwd
import re
import stat
from collections.abc import Mapping
from types import MappingProxyType

UID = 30005
HOME = Path('/home/pwqual055')
PRESENCE_UID = 30060
PRESENCE_HOME = Path('/home/pwqual058a2')
ROOTS = (('.config', 'paintedwolf'), ('.local/share', 'dev.paintedwolf.code'))


def selected_account(account=None):
    """Root callers may select only the two existing fixed qualification accounts."""
    reply = dict(name='pwqual055', uid=UID, gid=UID, home=str(HOME))
    presence = dict(name='pwqual058a2', uid=PRESENCE_UID, gid=PRESENCE_UID, home=str(PRESENCE_HOME))
    value = reply if account is None else account
    if (not isinstance(value, Mapping) or set(value) != set(reply)
            or not any(all(type(value[key]) is type(expected[key]) and value[key] == expected[key]
                           for key in expected) for expected in (reply, presence))):
        raise ValueError('profile custody requires its exact fixed QA account binding')
    return MappingProxyType(dict(value))


def backup_pattern(account):
    return (r'/var/backups/paintedwolf-qualification/055-reply-a[2-9][0-9]'
            if account['name'] == 'pwqual055' else
            r'/var/backups/paintedwolf-qualification/056-presence-058a2-a[1-9][0-9]{0,2}')


def identity(info):
    return dict(device=info.st_dev, inode=info.st_ino, uid=info.st_uid,
                gid=info.st_gid, mode=stat.S_IMODE(info.st_mode))


def directory(path, stack, account=None):
    """Open every ancestor without following links, retaining all descriptors."""
    account = selected_account(account)
    home = Path(account['home'])
    descriptor = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    stack.callback(os.close, descriptor)
    current = Path('/')
    for part in path.parts[1:]:
        current /= part
        descriptor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                             dir_fd=descriptor)
        stack.callback(os.close, descriptor)
        info = os.fstat(descriptor)
        expected_owner = account['uid'] if current == home or home in current.parents else 0
        if info.st_uid != expected_owner or info.st_mode & 0o022:
            raise ValueError('profile custody ancestor is foreign or writable')
    return descriptor


def entry(descriptor, name, account=None):
    account = selected_account(account)
    try:
        info = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
    except FileNotFoundError:
        return None
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != account['uid'] or info.st_mode & 0o077:
        raise ValueError('profile root is not a private physical QA directory')
    return identity(info)


def move(source, name, destination, target):
    # NOREPLACE makes a late destination insertion a refusal, never data loss.
    native = ctypes.CDLL(None, use_errno=True)
    rename = native.renameat2
    rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    rename.restype = ctypes.c_int
    if rename(source, os.fsencode(name), destination, os.fsencode(target), 1):
        error = ctypes.get_errno()
        raise OSError(error, os.strerror(error))
    os.fsync(source)
    os.fsync(destination)


class Custody:
    def __init__(self, backup, quiet, *, account=None):
        self.backup, self.quiet = Path(backup), quiet
        self.account = selected_account(account)
        self.rows, self.parents = [], []
        self.stack = ExitStack()
        self.phase = 'prepared'
        self.pair_parked = False
        self.launch_owned = self.account['name'] == 'pwqual058a2'

    def record(self):
        raw = (json.dumps({'schema': self.account['name'] + '-profile-custody-v1', 'phase': self.phase,
                           'uid': self.account['uid'], 'home': self.account['home'], 'roots': self.rows}, sort_keys=True) + '\n').encode()
        descriptor = os.open('profile-recovery.next', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                             0o600, dir_fd=self.backup_fd)
        with os.fdopen(descriptor, 'wb') as output:
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        os.replace('profile-recovery.next', 'profile-recovery.json',
                   src_dir_fd=self.backup_fd, dst_dir_fd=self.backup_fd)
        os.fsync(self.backup_fd)

    def prepare(self):
        if os.geteuid() != 0:
            raise ValueError('profile custody requires the admitted root seat owner')
        selected = self.account
        uid, gid, name, home = (selected[key] for key in ('uid', 'gid', 'name', 'home'))
        account = pwd.getpwnam(name)
        reverse, group, reverse_group = pwd.getpwuid(uid), grp.getgrnam(name), grp.getgrgid(gid)
        if (account.pw_name != name or account.pw_uid != uid or account.pw_gid != gid or account.pw_dir != home
                or reverse.pw_name != name or reverse.pw_uid != uid
                or reverse.pw_gid != gid or reverse.pw_dir != home
                or group.gr_name != name or group.gr_gid != gid or reverse_group.gr_gid != gid
                or reverse_group.gr_name != name):
            raise ValueError('QA account identity changed')
        if re.fullmatch(backup_pattern(selected), str(self.backup)) is None:
            raise ValueError('profile custody requires its selected account backup family')
        self.backup_fd = directory(self.backup, self.stack, self.account)
        backup_info = os.fstat(self.backup_fd)
        if backup_info.st_uid != 0 or stat.S_IMODE(backup_info.st_mode) != 0o700:
            raise ValueError('seat backup is not root private')
        for name in ('profile-recovery.json', 'profile-recovery.next'):
            try:
                os.stat(name, dir_fd=self.backup_fd, follow_symlinks=False)
            except FileNotFoundError:
                continue
            raise ValueError('profile custody already has recovery state')
        for index, (relative, name) in enumerate(ROOTS):
            parent = Path(self.account['home']) / relative
            descriptor = directory(parent, self.stack, self.account)
            info = os.fstat(descriptor)
            if info.st_uid != self.account['uid'] or info.st_mode & 0o022 or info.st_dev != backup_info.st_dev:
                raise ValueError('profile parent is foreign, writable, or on a different device')
            self.parents.append((parent, descriptor, identity(info)))
            original, fixture = f'profile-{index}-original', f'profile-{index}-fixture'
            for target in (original, fixture):
                try:
                    os.stat(target, dir_fd=self.backup_fd, follow_symlinks=False)
                except FileNotFoundError:
                    continue
                raise ValueError('profile custody destination already exists')
            self.rows.append(dict(path=str(parent / name), name=name, original=entry(descriptor, name, self.account),
                                  parked=original, retained_fixture=fixture, fixture=None))
        self.quiet()
        self.record()

    def validate_parents(self):
        with ExitStack() as stack:
            if identity(os.fstat(directory(self.backup, stack, self.account))) != identity(os.fstat(self.backup_fd)):
                raise ValueError('seat backup changed')
            for path, descriptor, expected in self.parents:
                if identity(os.fstat(directory(path, stack, self.account))) != expected or identity(os.fstat(descriptor)) != expected:
                    raise ValueError('profile ancestor changed')

    def park(self):
        self.pair_parked = False
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            self.validate_parents()
            self.quiet()
            if entry(descriptor, row['name'], self.account) != row['original']:
                raise ValueError('profile changed before parking')
            if row['original'] is not None:
                move(descriptor, row['name'], self.backup_fd, row['parked'])
                if entry(self.backup_fd, row['parked'], self.account) != row['original']:
                    raise ValueError('parked profile identity changed')
            if self.launch_owned:
                if entry(descriptor, row['name'], self.account) is not None:
                    raise ValueError('parked launch profile path is not absent')
                os.fsync(descriptor)
                self.phase = 'parking'
                self.record()
                continue
            os.mkdir(row['name'], mode=0o700, dir_fd=descriptor)
            # Keep fixture root root-owned until its identity is durably recorded.
            fixture = os.stat(row['name'], dir_fd=descriptor, follow_symlinks=False)
            row['created_fixture'] = identity(fixture)
            row['fixture'] = {**identity(fixture), 'uid': self.account['uid'], 'gid': self.account['gid']}
            self.phase = 'parking'
            self.record()
            os.chown(row['name'], self.account['uid'], self.account['gid'], dir_fd=descriptor, follow_symlinks=False)
            os.fsync(descriptor)
        if self.launch_owned:
            self.validate_parents()
            self.quiet()
            for row, (_, descriptor, _) in zip(self.rows, self.parents):
                if (entry(descriptor, row['name'], self.account) is not None
                        or entry(self.backup_fd, row['parked'], self.account) != row['original']):
                    raise ValueError('launch profile pair changed during parking')
        self.phase = 'parked'
        self.record()
        self.pair_parked = True

    def capture_launch_roots(self):
        if not self.pair_parked:
            raise ValueError('partial launch profile parking requires recovery')
        fixtures = []
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            if entry(self.backup_fd, row['parked'], self.account) != row['original']:
                raise ValueError('original profile changed; recovery required')
            fixtures.append(entry(descriptor, row['name'], self.account))
        for row, fixture in zip(self.rows, fixtures):
            row['fixture'] = fixture
        self.phase = 'fixtures_captured'
        self.record()

    def restore(self):
        self.quiet()
        self.validate_parents()
        if self.launch_owned:
            self.capture_launch_roots()
        # Check the entire pair before restoring either root.
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            if entry(descriptor, row['name'], self.account) != row['fixture']:
                raise ValueError('fixture profile changed; originals retained for recovery')
            if entry(self.backup_fd, row['parked'], self.account) != row['original']:
                raise ValueError('original profile changed; recovery required')
        self.phase = 'restoring'
        self.record()
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            if row['fixture'] is not None:
                move(descriptor, row['name'], self.backup_fd, row['retained_fixture'])
                if entry(self.backup_fd, row['retained_fixture'], self.account) != row['fixture']:
                    raise ValueError('fixture changed during retention; original remains parked')
            if row['original'] is not None:
                move(self.backup_fd, row['parked'], descriptor, row['name'])
            if entry(descriptor, row['name'], self.account) != row['original']:
                raise ValueError('restored profile differs from its original identity')
        self.phase = 'restored'
        self.record()


@contextmanager
def parked_profiles(backup, require_product_quiet, *, account=None):
    """Call only inside existing root seat admission and its finally boundary."""
    custody = Custody(backup, require_product_quiet, account=account)
    try:
        custody.prepare()
        try:
            custody.park()
            yield
        finally:
            try:
                custody.restore()
            except BaseException:
                custody.phase = 'recovery_required'
                custody.record()
                raise
    finally:
        custody.stack.close()
