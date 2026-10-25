"""Independently pin the root observer's selected installed process generations.

Kernel birth facts are process generations, not the host API's engine epoch.
The installed app/case observer keeps ownership of the latter and host release.
"""
from __future__ import annotations

from copy import deepcopy
from pathlib import Path
import stat

from ..installed_runtime import unchanged
from ..installed_cases.app import descends, engine_image, process_identity
from ..installed_cases.native_session import kernel_owner
from . import lineage


def selection(app, native):
    app.require_running()
    app.require_engine()
    launch, engine = app.observations['launch_identity'], app.observations.get('engine_identity')
    if engine is None:
        raise ValueError('Polkit observation requires the actual installed engine process')
    root = Path(app.binding['install_root'])
    images = {str(root / 'bin' / role): app.binding['images'][str(root / 'bin' / role)]
              for role in ('painted-wolf-code', 'pw')}
    return {'binding': {'install_root': str(root), 'artifact': {
                'architecture': app.binding['artifact']['architecture']}, 'images': deepcopy(images)},
            'launch': dict(launch), 'engine': dict(engine),
            'native': {key: native[key] for key in ('pid', 'parent_pid', 'start_time_ticks')}}


def controlled_image(path):
    for node in [*reversed(path.parents), path]:
        value = node.lstat()
        if (value.st_uid != 0 or value.st_gid != 0 or value.st_mode & 0o022
                or not (stat.S_ISREG(value.st_mode) if node == path else stat.S_ISDIR(value.st_mode))):
            raise ValueError('Polkit observer image path is not physically system controlled')
    if path.resolve(strict=True) != path:
        raise ValueError('Polkit observer image path is not physical')
    return (value.st_dev, value.st_ino, value.st_uid, value.st_gid, value.st_mode,
            value.st_size, value.st_mtime_ns, value.st_ctime_ns)


class Runtime:
    def __init__(self, value, account):
        if not isinstance(value, dict) or set(value) != {'binding', 'launch', 'native', 'engine'}:
            raise ValueError('Polkit observer requires its exact selected installed runtime')
        self.value, self.uid = deepcopy(value), account['uid']
        for key in ('launch', 'native', 'engine'):
            lineage.identity(self.value[key])
        self.images = {}
        binding = self.value['binding']
        for key, role in (('native', 'painted-wolf-code'), ('engine', 'pw')):
            path = Path(binding['install_root']) / 'bin' / role
            before = controlled_image(path)
            unchanged(binding, role)
            if controlled_image(path) != before:
                raise ValueError('Polkit observer installed image changed during its initial hash binding')
            self.images[key] = path, before
        self.observe()

    def observe(self):
        binding, launch = self.value['binding'], self.value['launch']
        if kernel_owner(launch['pid'], self.uid) != {**launch, 'uid': self.uid}:
            raise ValueError('Polkit observer application launch changed its kernel birth or account')
        owners = {}
        for key, role in (('native', 'painted-wolf-code'), ('engine', 'pw')):
            selected = self.value[key]
            path, image = self.images[key]
            if controlled_image(path) != image:
                raise ValueError('Polkit observer installed image changed after its initial hash binding')
            expected = binding['images'][str(path)]
            owner = kernel_owner(selected['pid'], self.uid)
            if (owner != {**selected, 'uid': self.uid}
                    or (selected != launch and not descends(selected, launch['pid']))
                    or engine_image(selected['pid']) != (
                        expected['device'], expected['inode'], expected['bytes'])):
                raise ValueError('Polkit observer process escaped its selected installed image or ancestry')
            owners[key] = owner
        if process_identity(launch['pid']) != launch:
            raise ValueError('Polkit observer application launch changed during image binding')
        return owners
