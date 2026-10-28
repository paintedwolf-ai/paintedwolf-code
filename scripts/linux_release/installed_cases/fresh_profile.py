"""Reserve only absent normal product storage, retaining every created leaf."""
from __future__ import annotations

import os
import stat
import time

from .storage import Storage, resolve
from .products import require_owned_guest
from .records import CaseUnavailable


def identity(info):
    return info.st_dev, info.st_ino, info.st_uid, info.st_mode


class Reservation:
    def __init__(self, binding, storage):
        if (not isinstance(storage, Storage) or type(storage.uid) is not int
                or storage != resolve({'HOME': str(storage.home)})):
            raise ValueError('fresh reservation requires the shared normal launch storage facts')
        self.binding, self.storage = binding, storage
        self.parents, self.created = {}, []
        self.entered, self.closed, self.complete, self.deadline = False, False, False, None

    def _parent(self, path):
        uid = self.storage.uid
        for ancestor in (path, *path.parents):
            info = ancestor.lstat()
            if (not stat.S_ISDIR(info.st_mode) or info.st_uid not in {0, uid}
                    or info.st_mode & 0o022 or ancestor.resolve(strict=True) != ancestor):
                raise CaseUnavailable('fresh product storage crosses an unsafe physical ancestor')
        descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            pinned = identity(os.fstat(descriptor))
            if pinned != identity(path.lstat()):
                raise CaseUnavailable('fresh product storage parent changed while being pinned')
        except BaseException:
            os.close(descriptor)
            raise
        self.parents[path] = descriptor, pinned

    def _check_parent(self, path):
        descriptor, pinned = self.parents[path]
        if (pinned != identity(os.fstat(descriptor)) or pinned != identity(path.lstat())
                or path.resolve(strict=True) != path):
            raise CaseUnavailable('fresh product storage parent changed its pinned identity')
        return descriptor

    def __enter__(self):
        if self.entered or self.closed:
            raise ValueError('fresh product storage reservation cannot be reused')
        self.entered = True
        require_owned_guest(self.binding)
        self.deadline = time.monotonic() + 300
        try:
            roots = (self.storage.config, self.storage.webview)
            for path in roots:
                self._parent(path.parent)
            # Establish both absences before creating either leaf. Any concurrent
            # collision makes mkdir fail; it never opens or reuses existing data.
            for path in roots:
                descriptor = self._check_parent(path.parent)
                try:
                    os.stat(path.name, dir_fd=descriptor, follow_symlinks=False)
                except FileNotFoundError:
                    continue
                raise CaseUnavailable('fresh product storage already exists and was preserved')
            for path in roots:
                parent = self._check_parent(path.parent)
                os.mkdir(path.name, mode=0o700, dir_fd=parent)
                row = {'path': path, 'identity': None, 'descriptor': None}
                self.created.append(row)
                info = os.stat(path.name, dir_fd=parent, follow_symlinks=False)
                row['identity'] = identity(info)
                if (not stat.S_ISDIR(info.st_mode) or info.st_uid != self.storage.uid
                        or stat.S_IMODE(info.st_mode) != 0o700):
                    raise CaseUnavailable('fresh product storage creation lacked its fixed private mode')
                row['descriptor'] = os.open(path.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
            self.check(empty=True)
            self.complete = True
            return self
        except BaseException as error:
            error.observations = {**getattr(error, 'observations', {}), 'fresh_storage': self.facts()}
            self.close()
            raise

    def check(self, *, empty=False):
        if self.closed or not self.entered or self.deadline is None or time.monotonic() >= self.deadline:
            raise CaseUnavailable('fresh product storage reservation is closed or expired')
        if self.storage != resolve({'HOME': str(self.storage.home)}):
            raise CaseUnavailable('fresh product storage no longer matches normal launch facts')
        if len(self.created) != 2:
            raise CaseUnavailable('fresh product storage reservation is incomplete')
        for row in self.created:
            path, descriptor, pinned = row['path'], row['descriptor'], row['identity']
            parent = self._check_parent(path.parent)
            if (descriptor is None or pinned != identity(os.fstat(descriptor))
                    or pinned != identity(os.stat(path.name, dir_fd=parent, follow_symlinks=False))):
                raise CaseUnavailable('fresh product storage changed its pinned leaf identity')
            if empty and os.listdir(descriptor):
                raise CaseUnavailable('fresh product storage was populated before the owned launch')

    def check_launch(self, app):
        self.check()
        app.require_running()
        app.require_engine()
        if app.binding != self.binding or app.selected_storage() != self.storage:
            raise CaseUnavailable('fresh reservation differs from actual selected installed launch storage')

    def facts(self):
        created = []
        for row in self.created:
            fact = {'path': str(row['path']), 'identity_verified': row['identity'] is not None}
            if row['identity'] is not None:
                pinned = row['identity']
                fact.update(device=pinned[0], inode=pinned[1], uid=pinned[2], mode=pinned[3])
            created.append(fact)
        return {'storage': self.storage.facts(), 'created': created, 'retained': True,
                'complete': self.complete, 'closed': self.closed}

    def close(self):
        self.closed = True
        for row in self.created:
            if row['descriptor'] is not None:
                os.close(row['descriptor'])
                row['descriptor'] = None
        for descriptor, _ in self.parents.values():
            os.close(descriptor)
        self.parents.clear()
        # These leaves become normal product state. Never remove or reuse them.

    def __exit__(self, *error):
        self.close()
