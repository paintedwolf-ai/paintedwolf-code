"""Bind a probe to its real logind session or owned graphical user application unit."""
from __future__ import annotations

import os
from pathlib import Path

from .records import CaseUnavailable

LOGIN = 'org.freedesktop.login1'
MANAGER = '/org/freedesktop/login1'
PROPERTIES = 'org.freedesktop.DBus.Properties'


class CallFailure(Exception):
    def __init__(self, name):
        self.name = name
        super().__init__('native session observation failed')


class Reader:
    def __init__(self):
        try:
            from gi.repository import Gio, GLib
        except ImportError:
            raise CaseUnavailable('typed-native-session-observer-unavailable') from None
        self.gio, self.glib = Gio, GLib
        try:
            self.system = Gio.bus_get_sync(Gio.BusType.SYSTEM, None)
        except GLib.Error:
            raise CaseUnavailable('typed-native-session-bus-unavailable') from None
        self.user = None

    def call(self, path, interface, method, args, signature, *, user=False):
        destination = 'org.freedesktop.systemd1' if user else LOGIN
        parameters = self.glib.Variant(*args) if args else None
        try:
            if user and self.user is None:
                self.user = self.gio.bus_get_sync(self.gio.BusType.SESSION, None)
            connection = self.user if user else self.system
            return connection.call_sync(destination, path, interface, method, parameters,
                self.glib.VariantType.new(signature), self.gio.DBusCallFlags.NONE, 5000, None)
        except self.glib.Error as error:
            raise CallFailure(self.gio.DBusError.get_remote_error(error)) from None

    def object(self, method, pid, *, user=False):
        base = '/org/freedesktop/systemd1' if user else MANAGER
        interface = ('org.freedesktop.systemd1' if user else LOGIN) + '.Manager'
        return self.call(base, interface, method, ('(u)', (pid,)), '(o)', user=user).unpack()[0]

    def property(self, path, interface, name, signature, *, user=False):
        reply = self.call(path, PROPERTIES, 'Get', ('(ss)', (interface, name)), '(v)', user=user)
        value = reply.get_child_value(0).get_variant()
        if value.get_type_string() != signature:
            raise ValueError('native session property has an invalid protocol signature')
        return value.unpack()


def observed(reader, pid: int, uid: int, cgroups: str) -> dict:
    kind = 'direct-session'
    try:
        path = reader.object('GetSessionByPID', pid)
    except CallFailure as error:
        if error.name != LOGIN + '.NoSessionForPID':
            raise CaseUnavailable('typed-native-session-observation-unavailable') from None
        # GNOME application units belong to the user manager rather than a login
        # session. Bind their actual cgroup before observing that user's display.
        kind = 'graphical-user-unit'
        unit = reader.object('GetUnitByPID', pid, user=True)
        if not isinstance(unit, str) or not unit.startswith('/org/freedesktop/systemd1/unit/'):
            raise ValueError('native application unit has an invalid object path')
        group = reader.property(unit, 'org.freedesktop.systemd1.Unit', 'ControlGroup', 's', user=True)
        if not isinstance(group, str) or not group.startswith('/') or cgroups != '0::' + group + '\n':
            raise ValueError('native application unit differs from the executing process cgroup')
        owner = reader.object('GetUserByPID', pid)
        if not isinstance(owner, str) or not owner.startswith(MANAGER + '/user/'):
            raise ValueError('native application has an invalid logind owner')
        actual_uid = reader.property(owner, LOGIN + '.User', 'UID', 'u')
        if type(actual_uid) is not int or actual_uid != uid:
            raise ValueError('native application belongs to another logind user')
        display = reader.property(owner, LOGIN + '.User', 'Display', '(so)')
        if not isinstance(display, tuple) or len(display) != 2 or not display[0]:
            raise CaseUnavailable('native application owner has no graphical display session')
        path = display[1]
    if not isinstance(path, str) or not path.startswith(MANAGER + '/session/'):
        raise ValueError('native execution has an invalid session object')
    owner = reader.property(path, LOGIN + '.Session', 'User', '(uo)')
    if not isinstance(owner, tuple) or len(owner) != 2 or type(owner[0]) is not int or owner[0] != uid:
        raise ValueError('native execution session belongs to another user')
    result = {'pid': pid, 'session_object': path, 'binding': kind}
    for name, signature, expected in [('Remote', 'b', bool), ('Active', 'b', bool), ('Type', 's', str)]:
        value = reader.property(path, LOGIN + '.Session', name, signature)
        if type(value) is not expected:
            raise ValueError('native execution has an invalid typed session property')
        result[name] = value
    return result


def execution_session() -> dict:
    try:
        path = Path('/proc/self/cgroup')
        if path.stat().st_size > 65536:
            raise ValueError('native execution cgroup observation exceeds its bound')
        with path.open() as stream:
            cgroups = stream.read(65537)
        if len(cgroups) > 65536:
            raise ValueError('native execution cgroup observation exceeds its bound')
        return observed(Reader(), os.getpid(), os.getuid(), cgroups)
    except CallFailure:
        raise CaseUnavailable('typed-native-session-observation-unavailable') from None
