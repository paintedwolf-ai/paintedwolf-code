"""Observe KScreen JSON only through the active user's independently bound KWin socket."""
from __future__ import annotations

import json
import math
import os
from pathlib import Path
import re
import socket
import stat
import struct

from .installed_environment import query
from .installed_cases.records import CaseUnavailable


def process(pid, names):
    root = Path('/proc') / str(pid)
    fields = (root / 'stat').read_bytes().rpartition(b') ')[2].split()
    status = (root / 'status').read_text()
    uids = [line.split()[1:] for line in status.splitlines() if line.startswith('Uid:')]
    executable = (root / 'exe').resolve(strict=True)
    info = executable.stat()
    if (len(fields) < 20 or uids != [[str(os.getuid())] * 4] or executable.name not in names
            or not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022):
        raise ValueError('active KWin process has an unexpected executable or owner')
    return {'pid': pid, 'parent_pid': int(fields[1]), 'start_time_ticks': int(fields[19]),
            'executable': str(executable), 'executable_identity': [info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns]}


def owner(session):
    if (session.get('Desktop', '').casefold() not in {'kde', 'plasma', 'plasmawayland'}
            or session.get('Type') != 'wayland' or session.get('Active') != 'yes'
            or session.get('Remote') != 'no' or session.get('User') != str(os.getuid())):
        raise CaseUnavailable('actual-active-plasma-wayland-session-required')
    response = query(['busctl', '--address=unix:path=/run/user/' + str(os.getuid()) + '/bus', '--json=short',
                      'call', 'org.freedesktop.DBus', '/org/freedesktop/DBus', 'org.freedesktop.DBus',
                      'GetConnectionUnixProcessID', 's', 'org.kde.KWin'])
    if response['exit_code'] != 0:
        raise CaseUnavailable('actual-kwin-bus-owner-unavailable')
    value = json.loads(response['stdout'])
    if (not isinstance(value, dict) or set(value) != {'type', 'data'} or value['type'] != 'u'
            or not isinstance(value['data'], list) or len(value['data']) != 1
            or type(value['data'][0]) is not int or value['data'][0] <= 1):
        raise ValueError('KWin bus owner has an unexpected typed identity')
    window = process(value['data'][0], {'kwin_wayland'})
    parent = process(window['parent_pid'], {'kwin_wayland_wrapper'})
    return {'window': window, 'socket_owner': parent}


def display(authority):
    uid = os.getuid()
    runtime = Path('/run/user') / str(uid)
    info = runtime.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != uid or info.st_mode & 0o077:
        raise ValueError('KWin runtime directory is not private to its session owner')
    candidates = [item for item in runtime.iterdir() if re.fullmatch(r'wayland-[0-9]{1,3}', item.name)]
    if len(candidates) > 16:
        raise ValueError('KWin display socket inventory exceeds its fixed bound')
    matches = []
    for item in candidates:
        before = item.lstat()
        if not stat.S_ISSOCK(before.st_mode) or before.st_uid != uid or before.st_mode & 0o022:
            raise ValueError('KWin display socket has an unsafe physical identity')
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
            connection.settimeout(2)
            connection.connect(str(item))
            peer = struct.unpack('iII', connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        after = item.lstat()
        identity = (before.st_dev, before.st_ino, before.st_uid, before.st_mode)
        if identity != (after.st_dev, after.st_ino, after.st_uid, after.st_mode):
            raise ValueError('KWin display socket changed during observation')
        if peer == (authority['socket_owner']['pid'], uid, os.getgid()):
            matches.append({'path': str(item), 'name': item.name, 'identity': list(identity), 'peer': list(peer)})
    if len(matches) != 1:
        raise CaseUnavailable('actual-unique-kwin-display-socket-required')
    return matches[0]


def configuration(value):
    if not isinstance(value, dict) or not isinstance(value.get('outputs'), list) or not 1 <= len(value['outputs']) <= 16:
        raise ValueError('KScreen output inventory has an invalid shape or bound')
    scales, identifiers = [], set()
    for output in value['outputs']:
        if (not isinstance(output, dict) or type(output.get('id')) is not int or output['id'] < 1
                or output['id'] in identifiers or type(output.get('connected')) is not bool
                or type(output.get('enabled')) is not bool):
            raise ValueError('KScreen output has an invalid or repeated identity')
        identifiers.add(output['id'])
        if not output['enabled']:
            continue
        scale, modes, selected = output.get('scale'), output.get('modes'), output.get('currentModeId')
        if (not output['connected'] or type(scale) not in {int, float} or not math.isfinite(scale)
                or not 1 <= scale <= 4 or scale * 100 != int(scale * 100)
                or not isinstance(modes, list) or not 1 <= len(modes) <= 256
                or not isinstance(selected, str) or not 1 <= len(selected) <= 128):
            raise ValueError('active KScreen output lacks a connected mode or exact scale')
        known = {}
        for mode in modes:
            if (not isinstance(mode, dict) or not isinstance(mode.get('id'), str) or not 1 <= len(mode['id']) <= 128
                    or mode['id'] in known):
                raise ValueError('KScreen modes have invalid or repeated identities')
            known[mode['id']] = mode
        size = known.get(selected, {}).get('size')
        if (not isinstance(size, dict) or set(size) != {'width', 'height'}
                or any(type(size[key]) is not int or not 1 <= size[key] <= 16384 for key in size)):
            raise ValueError('active KScreen mode has no valid physical dimensions')
        scales.append(int(scale * 100))
    if not scales:
        raise CaseUnavailable('actual-active-kscreen-output-required')
    return {'scales_percent': sorted(set(scales)), 'state': value}


def observe(session):
    authority = owner(session)
    selected = display(authority)
    uid = str(os.getuid())
    result = query(['env', 'XDG_RUNTIME_DIR=/run/user/' + uid, 'DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/' + uid + '/bus',
                    'WAYLAND_DISPLAY=' + selected['name'], 'QT_QPA_PLATFORM=wayland', 'kscreen-doctor', '-j'])
    if result['exit_code'] != 0:
        raise CaseUnavailable('actual-kscreen-monitor-state-unavailable')
    observed = configuration(json.loads(result['stdout']))
    if owner(session) != authority or display(authority) != selected:
        raise ValueError('active KWin authority changed during monitor observation')
    observed['state'] = {'configuration': observed['state'], 'kwin': authority, 'display': selected}
    return observed
