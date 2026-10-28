"""Bind a probe to its real logind session or owned graphical user application unit."""
from __future__ import annotations

import json
import math
import os
from pathlib import Path
import stat
import threading
import time

from .records import CaseUnavailable, MAX_OBSERVATION_BYTES

LOGIN = 'org.freedesktop.login1'
MANAGER = '/org/freedesktop/login1'
PROPERTIES = 'org.freedesktop.DBus.Properties'
SESSION_INVENTORY_LIMIT = 1024
SESSION_FIELD_BYTES = 256
SESSION_INVENTORY_BYTES = MAX_OBSERVATION_BYTES // 4


class CallFailure(Exception):
    def __init__(self, name):
        self.name = name
        super().__init__('native session observation failed')


class Reader:
    deadline = None
    user_only = False

    def __init__(self, *, user_only=False, deadline=None):
        if type(user_only) is not bool or deadline is not None and (
                type(deadline) not in {int, float} or not math.isfinite(deadline) or deadline <= 0):
            raise ValueError('native session reader requires a bounded absolute deadline and bus scope')
        self.user_only, self.deadline = user_only, deadline
        self.system = self.user = None
        self.closed = False
        try:
            from gi.repository import Gio, GLib
        except ImportError:
            raise CaseUnavailable('typed-native-session-observer-unavailable') from None
        self.gio, self.glib = Gio, GLib
        try:
            if user_only:
                self.user = self.connect('/run/user/' + str(os.getuid()) + '/bus', os.getuid())
            else:
                self.system = self.connect('/run/dbus/system_bus_socket', 0)
        except GLib.Error:
            raise CaseUnavailable('typed-native-session-bus-unavailable') from None

    def close(self):
        if self.closed:
            return
        self.closed = True
        errors = []
        for connection in (self.user, self.system):
            if connection is not None:
                try:
                    connection.close_sync(None)
                except Exception as error:
                    errors.append(error)
        self.user = self.system = None
        if errors:
            raise ValueError('native session bus connection cleanup failed') from errors[0]

    def __enter__(self):
        if self.closed:
            raise ValueError('native session reader is closed')
        return self

    def __exit__(self, *_):
        self.close()

    def timeout_ms(self, deadline=None):
        if deadline is not None and (type(deadline) not in {int, float} or not math.isfinite(deadline) or deadline <= 0):
            raise ValueError('native session call requires a finite absolute deadline')
        selected = [value for value in (self.deadline, deadline) if value is not None]
        if not selected:
            return 5000
        remaining = min(selected) - time.monotonic()
        if remaining <= 0:
            raise CaseUnavailable('typed-native-session-observation-deadline')
        return max(1, min(5000, math.ceil(remaining * 1000)))

    def connect(self, address, uid, *, deadline=None):
        path = Path(address)
        for parent in reversed(path.parents):
            info = parent.lstat()
            expected_uid = uid if parent == Path('/run/user') / str(uid) else 0
            if (not stat.S_ISDIR(info.st_mode) or info.st_uid != expected_uid
                    or info.st_mode & 0o022):
                raise ValueError('native session bus parent is not controlled by its expected account')
        before = path.lstat()
        if not stat.S_ISSOCK(before.st_mode) or before.st_uid != uid:
            raise ValueError('native session bus socket belongs to another account')
        cancellation = self.gio.Cancellable()
        timer = threading.Timer(self.timeout_ms(deadline) / 1000, cancellation.cancel)
        timer.daemon = True
        timer.start()
        try:
            connection = self.gio.DBusConnection.new_for_address_sync('unix:path=' + address,
                self.gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | self.gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION,
                None, cancellation)
        finally:
            timer.cancel()
        try:
            after = path.lstat()
            if (before.st_dev, before.st_ino, before.st_uid, before.st_mode) != (
                    after.st_dev, after.st_ino, after.st_uid, after.st_mode):
                raise ValueError('native session bus socket changed during connection')
        except BaseException:
            connection.close_sync(None)
            raise
        return connection

    def call(self, path, interface, method, args, signature, *, user=False, destination=None, deadline=None):
        if self.closed:
            raise ValueError('native session reader is closed')
        if self.user_only and not user:
            raise ValueError('user-only native reader cannot observe a system bus')
        destination = destination or ('org.freedesktop.systemd1' if user else LOGIN)
        parameters = self.glib.Variant(*args) if args else None
        try:
            if user and self.user is None:
                try:
                    self.user = self.connect('/run/user/' + str(os.getuid()) + '/bus', os.getuid(), deadline=deadline)
                except BaseException:
                    self.close()
                    raise
            connection = self.user if user else self.system
            return connection.call_sync(destination, path, interface, method, parameters,
                self.glib.VariantType.new(signature), self.gio.DBusCallFlags.NONE, self.timeout_ms(deadline), None)
        except self.glib.Error as error:
            remote = self.gio.DBusError.get_remote_error(error)
            if remote is None:
                self.close()
            raise CallFailure(remote) from None
        except BaseException:
            self.close()
            raise

    def sessions(self):
        reply = self.call(MANAGER, LOGIN + '.Manager', 'ListSessions', None,
                          '(a(susso))').unpack()
        if (not isinstance(reply, tuple) or len(reply) != 1 or not isinstance(reply[0], list)
                or len(reply[0]) > SESSION_INVENTORY_LIMIT):
            raise ValueError('login manager session inventory has an invalid shape or count')
        rows = []
        for value in reply[0]:
            if (not isinstance(value, tuple) or len(value) != 5
                    or any(not isinstance(value[index], str)
                           or len(value[index].encode()) > SESSION_FIELD_BYTES for index in (0, 2, 3, 4))
                    or not value[0].isalnum() or type(value[1]) is not int
                    or not 0 <= value[1] < 1 << 32
                    or not value[4].startswith(MANAGER + '/session/')):
                raise ValueError('login manager session identity has an invalid shape or bound')
            rows.append(dict(id=value[0], uid=value[1], name=value[2], seat=value[3], object=value[4]))
        if len({row['id'] for row in rows}) != len(rows) or len({row['object'] for row in rows}) != len(rows):
            raise ValueError('login manager session inventory repeats an identity')
        if len(json.dumps(rows, sort_keys=True).encode()) > SESSION_INVENTORY_BYTES:
            raise ValueError('login manager session inventory exceeds its observation budget')
        return sorted(rows, key=lambda row: row['id'])

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
        unit_id = reader.property(unit, 'org.freedesktop.systemd1.Unit', 'Id', 's', user=True)
        unit_type = unit_id.rpartition('.')[2] if isinstance(unit_id, str) else ''
        if unit_type not in {'service', 'scope'}:
            raise CaseUnavailable('native graphical execution requires a service or scope unit')
        interface = 'org.freedesktop.systemd1.' + {'service': 'Service', 'scope': 'Scope'}[unit_type]
        group = reader.property(unit, interface, 'ControlGroup', 's', user=True)
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
        with Reader() as reader:
            return observed(reader, os.getpid(), os.getuid(), cgroups)
    except CallFailure as error:
        unavailable = CaseUnavailable('typed-native-session-observation-unavailable')
        unavailable.observations = {'native_session_error': {'remote_name': error.name}}
        raise unavailable from None
