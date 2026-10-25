"""Observe native session and locker authority independently of application frames."""
from __future__ import annotations

import os
from copy import deepcopy
from pathlib import Path
import re
import time

from .. import installed_gnome
from ..installed_input.session_wire import LOCKER_NAMES
from ..installed_process import process_identity
from .logind_context import CallFailure, Reader, LOGIN, MANAGER
from .records import CaseUnavailable

DBUS = 'org.freedesktop.DBus'
DBUS_PATH = '/org/freedesktop/DBus'
UNIQUE = re.compile(r':[0-9]+\.[0-9]+\Z')


def kernel_owner(pid, uid):
    if type(pid) is not int or pid <= 1 or type(uid) is not int or uid < 0:
        raise ValueError('native session owner identity is invalid')
    before = process_identity(pid)
    actual_uid = Path('/proc', str(pid)).stat().st_uid
    if actual_uid != uid or process_identity(pid) != before:
        raise ValueError('native session owner changed during observation')
    return {**before, 'uid': actual_uid}


class NativeSessionObserver:
    """Pin bus owners, kernel birth facts and the exact captured local seat."""

    def __init__(self, session, *, reader=None, owner=kernel_owner, shell=None, expected_uid=None, deadline=None):
        self.uid = os.getuid() if expected_uid is None else expected_uid
        if (type(self.uid) is not int or not 0 <= self.uid < 1 << 31
                or self.uid != os.getuid() and (os.geteuid() != 0 or reader is None)):
            raise ValueError('native session observation requires an explicit root-selected reader and account')
        if (not isinstance(session, dict) or not isinstance(session.get('id'), str)
                or not session['id'].isalnum() or session.get('User') != str(self.uid)
                or session.get('Type') not in {'wayland', 'x11'}
                or session.get('Active') != 'yes' or session.get('Remote') != 'no'
                or not isinstance(session.get('Seat'), str) or not session['Seat']):
            raise ValueError('native lock observation requires the captured local graphical seat')
        # Each locker adapter must be established from its actual native contract.
        if session.get('Desktop') != 'GNOME':
            raise CaseUnavailable('native-locker-adapter-unverified')
        self.owns_reader = reader is None
        self.closed = False
        self.reader, self.owner = reader or Reader(), owner
        self.default_shell = shell is None
        self.shell = shell or (lambda *, deadline=None: installed_gnome.owner(
            self.reader, expected_uid=self.uid, deadline=deadline))
        self.session = {key: session[key] for key in ('id', 'User', 'Type', 'Seat')}
        self.locker = ('org.gnome.ScreenSaver', '/org/gnome/ScreenSaver')
        try:
            self.authorities = self._authorities(deadline)
            self.initial = self.observe(deadline=deadline)
        except BaseException:
            self.close()
            raise

    def close(self):
        if not self.closed:
            self.closed = True
            if self.owns_reader:
                self.reader.close()

    def __enter__(self):
        if self.closed:
            raise ValueError('native session observer is closed')
        return self

    def __exit__(self, *_):
        self.close()

    def _call(self, destination, path, interface, method, args, signature, *, user=False, deadline=None):
        return self.reader.call(path, interface, method, args, signature,
            user=user, destination=destination, **({} if deadline is None else {'deadline': deadline})).unpack()

    def _bus_owner(self, name, uid, *, user=False, optional=False, deadline=None):
        try:
            unique, = self._call(DBUS, DBUS_PATH, DBUS, 'GetNameOwner',
                                 ('(s)', (name,)), '(s)', user=user, deadline=deadline)
        except CallFailure as error:
            if optional and error.name == DBUS + '.Error.NameHasNoOwner':
                return None
            raise
        if not isinstance(unique, str) or not UNIQUE.fullmatch(unique):
            raise ValueError('native session bus returned an invalid unique owner')
        pid, = self._call(DBUS, DBUS_PATH, DBUS, 'GetConnectionUnixProcessID',
                          ('(s)', (unique,)), '(u)', user=user, deadline=deadline)
        return {'name': name, 'unique': unique, 'process': self.owner(pid, uid)}

    def _authorities(self, deadline=None):
        shell = self.shell(deadline=deadline) if self.default_shell else self.shell()
        locker = None
        for name in LOCKER_NAMES:
            candidate = self._bus_owner(name, self.uid, user=True, optional=True, deadline=deadline)
            if (candidate is not None and candidate['unique'] == shell['unique_owner']
                    and candidate['process']['pid'] == shell['pid']
                    and candidate['process']['start_time_ticks'] == shell['start_time_ticks']):
                locker = candidate
                break
        if locker is None:
            raise ValueError('native locker is not the independently selected GNOME shell')
        return {'login': self._bus_owner(LOGIN, 0, deadline=deadline), 'locker': locker, 'shell': shell}

    def _property(self, path, name, signature, deadline=None):
        reply = self.reader.call(path, 'org.freedesktop.DBus.Properties', 'Get',
            ('(ss)', (LOGIN + '.Session', name)), '(v)',
            destination=self.authorities['login']['unique'], **({} if deadline is None else {'deadline': deadline}))
        value = reply.get_child_value(0).get_variant()
        if value.get_type_string() != signature:
            raise ValueError('native session returned an invalid property signature')
        return value.unpack()

    def observe(self, *, deadline=None):
        if self.closed:
            raise ValueError('native session observer is closed')
        if self._authorities(deadline) != self.authorities:
            raise ValueError('captured native session authority changed')
        path, = self._call(self.authorities['login']['unique'], MANAGER,
            LOGIN + '.Manager', 'GetSession', ('(s)', (self.session['id'],)), '(o)', deadline=deadline)
        if not isinstance(path, str) or not path.startswith(MANAGER + '/session/'):
            raise ValueError('native session has an invalid object path')
        values = {name: self._property(path, name, signature, deadline) for name, signature in
                  [('Id', 's'), ('User', '(uo)'), ('Seat', '(so)'), ('Type', 's'),
                   ('Remote', 'b'), ('Active', 'b'), ('LockedHint', 'b'), ('TimestampMonotonic', 't')]}
        if (not isinstance(values['User'], tuple) or len(values['User']) != 2
                or not isinstance(values['Seat'], tuple) or len(values['Seat']) != 2
                or type(values['User'][0]) is not int
                or not isinstance(values['User'][1], str) or not values['User'][1].startswith(MANAGER + '/user/')
                or not isinstance(values['Seat'][1], str) or not values['Seat'][1].startswith(MANAGER + '/seat/')
                or type(values['TimestampMonotonic']) is not int or values['TimestampMonotonic'] <= 0
                or values['Id'] != self.session['id'] or values['User'][0] != self.uid
                or values['Seat'][0] != self.session['Seat']
                or values['Type'] != self.session['Type'] or values['Remote'] is not False
                or values['Active'] is not True or type(values['LockedHint']) is not bool):
            raise ValueError('captured native graphical session changed')
        lifecycle = {'object_path': path, 'user': list(values['User']), 'seat': list(values['Seat']),
                     'created_monotonic_usec': values['TimestampMonotonic']}
        if hasattr(self, 'initial') and lifecycle != self.initial['lifecycle']:
            raise ValueError('captured native session lifecycle changed')
        active, = self._call(self.authorities['locker']['unique'], self.locker[1],
            self.locker[0], 'GetActive', None, '(b)', user=True, deadline=deadline)
        if type(active) is not bool or self._authorities(deadline) != self.authorities:
            raise ValueError('captured native locker authority changed')
        if deadline is not None and time.monotonic() >= deadline:
            raise CaseUnavailable('typed-native-session-observation-deadline')
        return {'session': deepcopy(self.session), 'session_object': path, 'lifecycle': lifecycle,
                'authorities': deepcopy(self.authorities), 'locked_hint': values['LockedHint'],
                'locker_active': active}

    def inhibitors(self):
        for _ in range(4):
            before = self.observe()
            rows, = self._call(self.authorities['login']['unique'], MANAGER,
                LOGIN + '.Manager', 'ListInhibitors', None, '(a(ssssuu))')
            if not isinstance(rows, (list, tuple)) or len(rows) > 256:
                raise ValueError('native inhibitor observation exceeds its bound')
            result = []
            for row in rows:
                if (not isinstance(row, (list, tuple)) or len(row) != 6
                        or any(not isinstance(value, str) or len(value) > 4096 for value in row[:4])
                        or any(type(value) is not int or value < 0 for value in row[4:])):
                    raise ValueError('native inhibitor row has an invalid typed shape')
                what, _, _, mode, uid, pid = row
                result.append({'what': what, 'mode': mode, 'owner': self.owner(pid, uid)})
            again, = self._call(self.authorities['login']['unique'], MANAGER,
                LOGIN + '.Manager', 'ListInhibitors', None, '(a(ssssuu))')
            if self.observe() != before:
                raise ValueError('native session changed during inhibitor observation')
            if again == rows and all(self.owner(row['owner']['pid'], row['owner']['uid']) == row['owner']
                                     for row in result):
                return {'native_session': before, 'inhibitors': result}
        raise ValueError('native session changed during inhibitor observation')
