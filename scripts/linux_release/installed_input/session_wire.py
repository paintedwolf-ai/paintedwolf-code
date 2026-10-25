"""Closed GNOME lock proof, independent of application frames and editable entries."""
from __future__ import annotations

import re

from . import lineage

OPERATIONS = {'session-lock-v1': (('meta_l', 'l'),)}
LOCKER_NAMES = ('org.gnome.ScreenSaver', 'org.gnome.Shell.ScreenShield', 'org.gnome.Shell')
PATH = re.compile(r'/(?:[A-Za-z0-9_]+/)*[A-Za-z0-9_]+\Z')
UNIQUE = re.compile(r':[0-9]+\.[0-9]+\Z')


def native_owner(value, name, uid):
    allowed = (name,) if isinstance(name, str) else tuple(name)
    if (not isinstance(value, dict) or set(value) != {'name', 'unique', 'process'}
            or value['name'] not in allowed or not isinstance(value['unique'], str)
            or len(value['unique']) > 255 or not UNIQUE.fullmatch(value['unique'])):
        raise ValueError('native lock proof has an invalid unique bus owner')
    process = value['process']
    if (not isinstance(process, dict) or set(process) != {'pid', 'parent_pid', 'start_time_ticks', 'uid'}
            or type(process['uid']) is not int or process['uid'] != uid):
        raise ValueError('native lock proof has a foreign owner account')
    lineage.identity({key: process[key] for key in ('pid', 'parent_pid', 'start_time_ticks')})


def native_session(native):
    if (not isinstance(native, dict) or set(native) != {'session', 'session_object', 'lifecycle',
            'authorities', 'locked_hint', 'locker_active'}
            or any(type(native[key]) is not bool for key in ('locked_hint', 'locker_active'))):
        raise ValueError('native lock session proof has an unknown shape')
    session = native['session']
    if (not isinstance(session, dict) or set(session) != {'id', 'User', 'Type', 'Seat'}
            or any(not isinstance(session[key], str) or not 1 <= len(session[key]) <= 128 for key in session)
            or not session['id'].isalnum() or not session['User'].isascii()
            or not session['User'].isdigit() or str(int(session['User'])) != session['User'] or int(session['User']) <= 0
            or session['Type'] not in {'wayland', 'x11'}):
        raise ValueError('native lock requires its exact local graphical session')
    lifecycle = native['lifecycle']
    if (not isinstance(lifecycle, dict) or set(lifecycle) != {'object_path', 'user', 'seat', 'created_monotonic_usec'}
            or lifecycle['object_path'] != native['session_object']
            or not isinstance(native['session_object'], str)
            or not native['session_object'].startswith('/org/freedesktop/login1/session/')
            or not PATH.fullmatch(native['session_object'])
            or type(lifecycle['created_monotonic_usec']) is not int or lifecycle['created_monotonic_usec'] <= 0):
        raise ValueError('native lock requires the captured session creation facts')
    for key, expected, prefix in [('user', int(session['User']), 'user'), ('seat', session['Seat'], 'seat')]:
        row = lifecycle[key]
        if (not isinstance(row, (list, tuple)) or len(row) != 2 or type(row[0]) is not type(expected)
                or row[0] != expected or not isinstance(row[1], str)
                or not row[1].startswith('/org/freedesktop/login1/' + prefix + '/') or not PATH.fullmatch(row[1])):
            raise ValueError('native lock session changed its account or seat object')
    owners = native['authorities']
    if not isinstance(owners, dict) or set(owners) != {'login', 'locker', 'shell'}:
        raise ValueError('native lock authority proof has an unknown shape')
    native_owner(owners['login'], 'org.freedesktop.login1', 0)
    native_owner(owners['locker'], LOCKER_NAMES, int(session['User']))
    shell = owners['shell']
    if (not isinstance(shell, dict) or set(shell) != {'bus_name', 'unique_owner', 'pid', 'start_time_ticks',
            'executable', 'device', 'inode', 'mode', 'bytes'}
            or shell['bus_name'] != 'org.gnome.Shell' or shell['unique_owner'] != owners['locker']['unique']
            or any(type(shell[key]) is not int or shell[key] <= 0 for key in ('pid', 'start_time_ticks', 'device', 'inode', 'bytes', 'mode'))
            or shell['executable'] != '/usr/bin/gnome-shell' or shell['mode'] & 0o7022
            or not shell['mode'] & 0o111 or shell['mode'] > 0o7777
            or any(shell[key] != owners['locker']['process'][key] for key in ('pid', 'start_time_ticks'))):
        raise ValueError('native lock is not bound to the selected physical GNOME shell')
    return native


def proof(value):
    if not isinstance(value, dict) or set(value) != {'native_session', 'lock_bindings'}:
        raise ValueError('native lock proof has an unknown shape')
    bindings = value['lock_bindings']
    if (not isinstance(bindings, list) or not 1 <= len(bindings) <= 16
            or any(not isinstance(item, str) or not 1 <= len(item) <= 128 for item in bindings)
            or len(set(bindings)) != len(bindings) or bindings.count('<Super>l') != 1):
        raise ValueError('native lock proof has no exact configured supported shortcut')
    native_session(value['native_session'])
    return value


def same_runtime(old, new):
    proof(old)
    proof(new)
    same_session(old['native_session'], new['native_session'])
    if old['lock_bindings'] != new['lock_bindings']:
        raise ValueError('native lock replaced its captured session or locker authority')


def same_session(old, new):
    native_session(old)
    native_session(new)
    lifecycle = lambda value: {**value['lifecycle'],
        'user': list(value['lifecycle']['user']), 'seat': list(value['lifecycle']['seat'])}
    if lifecycle(old) != lifecycle(new) or any(old[key] != new[key]
            for key in ('session', 'session_object', 'authorities')):
        raise ValueError('native session replaced its captured lifecycle or authority')


def before(operation, value):
    proof(value)
    if operation not in OPERATIONS or any(value['native_session'][key] for key in ('locked_hint', 'locker_active')):
        raise ValueError('native lock input requires its captured unlocked session')
    return value


def finished(operation, old, new):
    before(operation, old)
    same_runtime(old, new)
    return all(new['native_session'][key] for key in ('locked_hint', 'locker_active'))
