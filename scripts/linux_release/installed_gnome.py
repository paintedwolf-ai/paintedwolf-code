"""Bind the declared native GNOME shell image to its actual session bus owner."""
from __future__ import annotations

import os
from contextlib import nullcontext
from pathlib import Path
import re
import stat

from .installed_cases.logind_context import Reader

IMAGE = Path('/usr/bin/gnome-shell')
BUS_NAME = 'org.gnome.Shell'


def stable(info):
    return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode, info.st_size)


def physical_image(image=IMAGE):
    for path in [*reversed(image.parents), image]:
        info = path.lstat()
        directory = path != image
        if (info.st_uid != 0 or info.st_mode & 0o022
                or not (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode))):
            raise ValueError('selected GNOME image path is not physically root controlled')
    if image.resolve(strict=True) != image or not info.st_mode & 0o111 or info.st_mode & 0o7000:
        raise ValueError('selected native GNOME image contract is unavailable')
    return info


def owner(reader=None, *, expected_uid=None, deadline=None):
    expected_uid = os.getuid() if expected_uid is None else expected_uid
    if (type(expected_uid) is not int or not 0 <= expected_uid < 1 << 31
            or expected_uid != os.getuid() and (os.geteuid() != 0 or reader is None)):
        raise ValueError('GNOME observation requires the current account or an explicit root-selected UID')
    with Reader() if reader is None else nullcontext(reader) as reader:
        def bus_call(method, argument, signature):
            result = reader.call('/org/freedesktop/DBus', 'org.freedesktop.DBus', method,
                ('(s)', (argument,)), '(' + signature + ')', user=True,
                destination='org.freedesktop.DBus', **({} if deadline is None else {'deadline': deadline})).unpack()
            if not isinstance(result, tuple) or len(result) != 1:
                raise ValueError('GNOME bus owner returned an invalid protocol shape')
            return result[0]

        unique = bus_call('GetNameOwner', BUS_NAME, 's')
        if not isinstance(unique, str) or not re.fullmatch(r':[0-9]+\.[0-9]+', unique):
            raise ValueError('GNOME bus owner has an invalid unique identity')
        pid = bus_call('GetConnectionUnixProcessID', unique, 'u')
        if type(pid) is not int or pid <= 1:
            raise ValueError('GNOME bus owner has no live process')
        before = physical_image()
        descriptor = os.open(IMAGE, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            opened = os.fstat(descriptor)
            if stable(opened) != stable(before):
                raise ValueError('selected GNOME image changed while opening')
            process = Path('/proc') / str(pid)
            birth = (process / 'stat').read_text().rpartition(') ')[2].split()[19]
            executable = (process / 'exe').stat()
            if (process.stat().st_uid != expected_uid
                    or (executable.st_dev, executable.st_ino, executable.st_size)
                    != (opened.st_dev, opened.st_ino, opened.st_size)):
                raise ValueError('GNOME bus process differs from the selected native image')
            if (bus_call('GetNameOwner', BUS_NAME, 's') != unique
                    or bus_call('GetConnectionUnixProcessID', unique, 'u') != pid
                    or (process / 'stat').read_text().rpartition(') ')[2].split()[19] != birth
                    or process.stat().st_uid != expected_uid
                    or stable((process / 'exe').stat()) != stable(executable)
                    or stable(physical_image()) != stable(opened)
                    or stable(os.fstat(descriptor)) != stable(opened)):
                raise ValueError('GNOME shell authority changed during observation')
            return {'bus_name': BUS_NAME, 'unique_owner': unique, 'pid': pid,
                    'start_time_ticks': int(birth), 'executable': str(IMAGE),
                    'device': opened.st_dev, 'inode': opened.st_ino,
                    'mode': stat.S_IMODE(opened.st_mode), 'bytes': opened.st_size}
        finally:
            os.close(descriptor)
