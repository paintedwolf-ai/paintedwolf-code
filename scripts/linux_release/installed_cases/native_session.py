"""Observe native session and locker authority independently of application frames."""
from __future__ import annotations

import os
from copy import deepcopy
from pathlib import Path
import re

from .app import process_identity
from .logind_context import Reader, LOGIN, MANAGER
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

    def __init__(self, session, *, reader=None, owner=kernel_owner):
        if (not isinstance(session, dict) or not isinstance(session.get('id'), str)
                or not session['id'].isalnum() or session.get('User') != str(os.getuid())
                or session.get('Type') not in {'wayland', 'x11'}
                or session.get('Active') != 'yes' or session.get('Remote') != 'no'
                or not isinstance(session.get('Seat'), str) or not session['Seat']):
            raise ValueError('native lock observation requires the captured local graphical seat')
        # Each locker adapter must be established from its actual native contract.
        if session.get('Desktop') != 'GNOME':
            raise CaseUnavailable('native-locker-adapter-unverified')
        self.reader, self.owner = reader or Reader(), owner
        self.session = {key: session[key] for key in ('id', 'User', 'Type', 'Seat')}
        self.locker = ('org.gnome.ScreenSaver', '/org/gnome/ScreenSaver')
        self.authorities = self._authorities()
        self.initial = self.observe()

    def _call(self, destination, path, interface, method, args, signature, *, user=False):
        return self.reader.call(path, interface, method, args, signature,
                                user=user, destination=destination).unpack()

    def _bus_owner(self, name, uid, *, user=False):
        unique, = self._call(DBUS, DBUS_PATH, DBUS, 'GetNameOwner',
                             ('(s)', (name,)), '(s)', user=user)
        if not isinstance(unique, str) or not UNIQUE.fullmatch(unique):
            raise ValueError('native session bus returned an invalid unique owner')
        pid, = self._call(DBUS, DBUS_PATH, DBUS, 'GetConnectionUnixProcessID',
                          ('(s)', (unique,)), '(u)', user=user)
        return {'name': name, 'unique': unique, 'process': self.owner(pid, uid)}

    def _authorities(self):
        return {'login': self._bus_owner(LOGIN, 0),
                'locker': self._bus_owner(self.locker[0], os.getuid(), user=True)}

    def _property(self, path, name, signature):
        reply = self.reader.call(path, 'org.freedesktop.DBus.Properties', 'Get',
            ('(ss)', (LOGIN + '.Session', name)), '(v)',
            destination=self.authorities['login']['unique'])
        value = reply.get_child_value(0).get_variant()
        if value.get_type_string() != signature:
            raise ValueError('native session returned an invalid property signature')
        return value.unpack()

    def observe(self):
        if self._authorities() != self.authorities:
            raise ValueError('captured native session authority changed')
        path, = self._call(self.authorities['login']['unique'], MANAGER,
            LOGIN + '.Manager', 'GetSession', ('(s)', (self.session['id'],)), '(o)')
        if not isinstance(path, str) or not path.startswith(MANAGER + '/session/'):
            raise ValueError('native session has an invalid object path')
        values = {name: self._property(path, name, signature) for name, signature in
                  [('Id', 's'), ('User', '(uo)'), ('Seat', '(so)'), ('Type', 's'),
                   ('Remote', 'b'), ('Active', 'b'), ('LockedHint', 'b')]}
        if (not isinstance(values['User'], tuple) or len(values['User']) != 2
                or not isinstance(values['Seat'], tuple) or len(values['Seat']) != 2
                or type(values['User'][0]) is not int
                or values['Id'] != self.session['id'] or values['User'][0] != os.getuid()
                or values['Seat'][0] != self.session['Seat']
                or values['Type'] != self.session['Type'] or values['Remote'] is not False
                or values['Active'] is not True or type(values['LockedHint']) is not bool):
            raise ValueError('captured native graphical session changed')
        active, = self._call(self.authorities['locker']['unique'], self.locker[1],
            self.locker[0], 'GetActive', None, '(b)', user=True)
        if type(active) is not bool or self._authorities() != self.authorities:
            raise ValueError('captured native locker authority changed')
        return {'session': deepcopy(self.session), 'session_object': path,
                'authorities': deepcopy(self.authorities), 'locked_hint': values['LockedHint'],
                'locker_active': active}

    def inhibitors(self):
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
        if self.observe() != before:
            raise ValueError('native session changed during inhibitor observation')
        return {'native_session': before, 'inhibitors': result}
