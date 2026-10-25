"""Opaque two-root custody inside the already admitted pwqual055 seat.

The root seat owner supplies its pinned product-process quiet check. This leaf
never admits a VM, selects an artifact, provisions an account, or reads a profile.
"""
from contextlib import contextmanager, ExitStack
import ctypes
import json
import os
from pathlib import Path
import pwd
import re
import stat

UID = 30005
HOME = Path('/home/pwqual055')
ROOTS = (('.config', 'paintedwolf'), ('.local/share', 'dev.paintedwolf.code'))


def identity(info):
    return dict(device=info.st_dev, inode=info.st_ino, uid=info.st_uid,
                gid=info.st_gid, mode=stat.S_IMODE(info.st_mode))


def directory(path, stack):
    """Open every ancestor without following links, retaining all descriptors."""
    descriptor = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    stack.callback(os.close, descriptor)
    current = Path('/')
    for part in path.parts[1:]:
        current /= part
        descriptor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                             dir_fd=descriptor)
        stack.callback(os.close, descriptor)
        info = os.fstat(descriptor)
        expected_owner = UID if current == HOME or HOME in current.parents else 0
        if info.st_uid != expected_owner or info.st_mode & 0o022:
            raise ValueError('profile custody ancestor is foreign or writable')
    return descriptor


def entry(descriptor, name):
    try:
        info = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
    except FileNotFoundError:
        return None
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != UID or info.st_mode & 0o077:
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
    def __init__(self, backup, quiet):
        self.backup, self.quiet = Path(backup), quiet
        self.rows, self.parents = [], []
        self.stack = ExitStack()
        self.phase = 'prepared'

    def record(self):
        raw = (json.dumps({'schema': 'pwqual055-profile-custody-v1', 'phase': self.phase,
                           'uid': UID, 'home': str(HOME), 'roots': self.rows}, sort_keys=True) + '\n').encode()
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
        account = pwd.getpwnam('pwqual055')
        if account.pw_uid != UID or account.pw_gid != UID or account.pw_dir != str(HOME):
            raise ValueError('QA account identity changed')
        if re.fullmatch(r'/var/backups/paintedwolf-qualification/055-reply-a[2-9][0-9]', str(self.backup)) is None:
            raise ValueError('profile custody requires its existing reply-seat backup')
        self.backup_fd = directory(self.backup, self.stack)
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
            parent = HOME / relative
            descriptor = directory(parent, self.stack)
            info = os.fstat(descriptor)
            if info.st_uid != UID or info.st_mode & 0o022 or info.st_dev != backup_info.st_dev:
                raise ValueError('profile parent is foreign, writable, or on a different device')
            self.parents.append((parent, descriptor, identity(info)))
            original, fixture = f'profile-{index}-original', f'profile-{index}-fixture'
            for target in (original, fixture):
                try:
                    os.stat(target, dir_fd=self.backup_fd, follow_symlinks=False)
                except FileNotFoundError:
                    continue
                raise ValueError('profile custody destination already exists')
            self.rows.append(dict(path=str(parent / name), name=name, original=entry(descriptor, name),
                                  parked=original, retained_fixture=fixture, fixture=None))
        self.quiet()
        self.record()

    def validate_parents(self):
        with ExitStack() as stack:
            if identity(os.fstat(directory(self.backup, stack))) != identity(os.fstat(self.backup_fd)):
                raise ValueError('seat backup changed')
            for path, descriptor, expected in self.parents:
                if identity(os.fstat(directory(path, stack))) != expected or identity(os.fstat(descriptor)) != expected:
                    raise ValueError('profile ancestor changed')

    def park(self):
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            self.validate_parents()
            self.quiet()
            if entry(descriptor, row['name']) != row['original']:
                raise ValueError('profile changed before parking')
            if row['original'] is not None:
                move(descriptor, row['name'], self.backup_fd, row['parked'])
                if entry(self.backup_fd, row['parked']) != row['original']:
                    raise ValueError('parked profile identity changed')
            os.mkdir(row['name'], mode=0o700, dir_fd=descriptor)
            # Keep fixture root root-owned until its identity is durably recorded.
            fixture = os.stat(row['name'], dir_fd=descriptor, follow_symlinks=False)
            row['created_fixture'] = identity(fixture)
            row['fixture'] = {**identity(fixture), 'uid': UID, 'gid': UID}
            self.phase = 'parking'
            self.record()
            os.chown(row['name'], UID, UID, dir_fd=descriptor, follow_symlinks=False)
            os.fsync(descriptor)
        self.phase = 'parked'
        self.record()

    def restore(self):
        self.quiet()
        self.validate_parents()
        # Check the entire pair before restoring either root.
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            if entry(descriptor, row['name']) != row['fixture']:
                raise ValueError('fixture profile changed; originals retained for recovery')
            if entry(self.backup_fd, row['parked']) != row['original']:
                raise ValueError('original profile changed; recovery required')
        self.phase = 'restoring'
        self.record()
        for row, (_, descriptor, _) in zip(self.rows, self.parents):
            if row['fixture'] is not None:
                move(descriptor, row['name'], self.backup_fd, row['retained_fixture'])
                if entry(self.backup_fd, row['retained_fixture']) != row['fixture']:
                    raise ValueError('fixture changed during retention; original remains parked')
            if row['original'] is not None:
                move(self.backup_fd, row['parked'], descriptor, row['name'])
            if entry(descriptor, row['name']) != row['original']:
                raise ValueError('restored profile differs from its original identity')
        self.phase = 'restored'
        self.record()


@contextmanager
def parked_profiles(backup, require_product_quiet):
    """Call only inside existing root seat admission and its finally boundary."""
    custody = Custody(backup, require_product_quiet)
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
