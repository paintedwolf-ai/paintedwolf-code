"""Private, root-only system-bus observation of one real Polkit transaction.

Start before the UI action. Only typed causal envelopes are retained in memory;
raw D-Bus messages, cookies and localized agent prose are never evidence output.
This transport does not grant physical input or host release authority.
"""
from __future__ import annotations

import pwd
import threading
import time

from .polkit_transaction import AGENT, AUTHORITY, Transaction
from .polkit_wire import ACTION, UNIQUE
from .unlock_account import validate_account
from ..installed_cases.logind_context import Reader
from ..installed_cases.native_session import kernel_owner

DBUS = 'org.freedesktop.DBus'
DBUS_PATH = '/org/freedesktop/DBus'
AUTHORITY_NAME = 'org.freedesktop.PolicyKit1'
SIGNATURES = {
    (AUTHORITY, 'CheckAuthorization'): '((sa{sv})sa{ss}us)',
    (AGENT, 'BeginAuthentication'): '(sssa{ss}sa(sa{sv}))',
    (AUTHORITY, 'AuthenticationAgentResponse2'): '(us(sa{sv}))',
    (AGENT, 'CancelAuthentication'): '(s)',
}


class Monitor:
    def __init__(self, account, vm_id, *, deadline=None):
        validate_account(account, vm_id=vm_id)
        self.deadline = time.monotonic() + 150 if deadline is None else min(time.monotonic() + 150, deadline)
        self.reader = Reader(deadline=self.deadline)
        self.connection = None
        self.rows, self.calls = [], set()
        self.failure = None
        self.transaction = None
        self.lock = threading.RLock()
        self.closed = False
        try:
            self.authority = self._call('GetNameOwner', AUTHORITY_NAME, 's')
            process = self.peer(self.authority)
            try:
                daemon_uid = pwd.getpwnam('polkitd').pw_uid
            except KeyError:
                daemon_uid = None
            if process['uid'] not in {0, daemon_uid}:
                raise ValueError('Polkit observer authority is not system owned')
            self.authority_process = process
            self.connection = self.reader.connect('/run/dbus/system_bus_socket', 0)
            self.filter = self.connection.add_filter(self._receive, None)
            rules = ["type='method_call',interface='" + interface + "'" for interface in (AUTHORITY, AGENT)]
            rules += ["type='method_return'", "type='error'",
                      "type='signal',sender='org.freedesktop.DBus',interface='org.freedesktop.DBus',member='NameOwnerChanged'"]
            self.connection.call_sync(DBUS, DBUS_PATH, 'org.freedesktop.DBus.Monitoring',
                'BecomeMonitor', self.reader.glib.Variant('(asu)', (rules, 0)),
                self.reader.glib.VariantType.new('()'), self.reader.gio.DBusCallFlags.NONE,
                self.reader.timeout_ms(), None)
            self._authority()
        except BaseException:
            self.close()
            raise

    def _call(self, method, name, signature):
        value, = self.reader.call(DBUS_PATH, DBUS, method, ('(s)', (name,)),
                                  '(' + signature + ')', destination=DBUS).unpack()
        return value

    def peer(self, name):
        if not isinstance(name, str) or not UNIQUE.fullmatch(name):
            raise ValueError('Polkit observer peer has no unique bus identity')
        pid = self._call('GetConnectionUnixProcessID', name, 'u')
        uid = self._call('GetConnectionUnixUser', name, 'u')
        before = kernel_owner(pid, uid)
        if (self._call('GetConnectionUnixProcessID', name, 'u') != pid
                or self._call('GetConnectionUnixUser', name, 'u') != uid
                or kernel_owner(pid, uid) != before):
            raise ValueError('Polkit observer peer changed during kernel binding')
        return before

    def _authority(self):
        if (self._call('GetNameOwner', AUTHORITY_NAME, 's') != self.authority
                or self.peer(self.authority) != self.authority_process):
            raise ValueError('Polkit observer authority changed')

    def _receive(self, connection, message, incoming, *_):
        if not incoming:
            return message
        with self.lock:
            if self.closed or self.failure:
                return message
            try:
                row = self._decode(message)
                if row is not None:
                    if time.monotonic() >= self.deadline or len(self.rows) >= 128:
                        raise ValueError('Polkit observation exceeded its fixed lifetime or event bound')
                    self.rows.append(row)
            except Exception:
                # Never retain arbitrary protocol exception prose or raw message bodies.
                self.failure = 'Polkit observation lost its typed causal transport'
                self.rows.clear()
        return message

    def _decode(self, message):
        kind = message.get_message_type()
        types = self.reader.gio.DBusMessageType
        sender, destination = message.get_sender(), message.get_destination()
        body = message.get_body()
        if body is not None and body.get_size() > 16384:
            raise ValueError('Polkit observation body exceeded its fixed bound')
        if kind == types.SIGNAL:
            if (sender == DBUS and message.get_interface() == DBUS
                    and message.get_member() == 'NameOwnerChanged'):
                if body is None or body.get_type_string() != '(sss)':
                    raise ValueError('Polkit bus owner signal has an invalid signature')
                name, old, new = body.unpack()
                return {'kind': 'owner-changed', 'name': name, 'old': old, 'new': new}
            return None
        if kind == types.METHOD_CALL:
            interface, member = message.get_interface(), message.get_member()
            signature = SIGNATURES.get((interface, member))
            if signature is None:
                return None
            if body is None or body.get_type_string() != signature:
                raise ValueError('Polkit call has an invalid signature')
            values = body.unpack()
            if member == 'CheckAuthorization' and values[1] != ACTION:
                return None
            if member == 'BeginAuthentication' and values[0] != ACTION:
                return None
            serial = message.get_serial()
            self.calls.add((sender, serial))
            if len(self.calls) > 128:
                raise ValueError('Polkit pending reply inventory exceeded its bound')
            return {'kind': 'call', 'sender': sender, 'destination': destination,
                    'serial': serial, 'path': message.get_path(), 'interface': interface,
                    'member': member, 'body': values}
        if kind in (types.METHOD_RETURN, types.ERROR):
            key = destination, message.get_reply_serial()
            if key not in self.calls:
                return None
            self.calls.remove(key)
            return {'kind': 'return' if kind == types.METHOD_RETURN else 'error',
                    'sender': sender, 'destination': destination, 'reply_serial': key[1],
                    'error_name': message.get_error_name(), 'body': () if body is None else body.unpack()}
        return None

    def bind(self, attempt, app, shell):
        with self.lock:
            if self.closed or self.failure or self.transaction is not None:
                raise ValueError('Polkit observer cannot rebind or recover a lost transaction')
            self._authority()
            if attempt['authority'] != self.authority:
                raise ValueError('Native Polkit attempt differs from the pinned system authority')
            self.transaction = Transaction(attempt, app, shell, self.peer)
        return self.observe()

    def observe(self):
        with self.lock:
            if self.closed or self.failure or self.transaction is None or time.monotonic() >= self.deadline:
                raise ValueError('Polkit observer has no live bound transaction')
            self._authority()
            rows, self.rows = self.rows, []
            for row in rows:
                self.transaction.observe(row)
            self.reader.timeout_ms()
            if self.transaction.finished:
                return self.transaction.witness()
            return self.transaction.pending() if self.transaction.begin is not None else None

    def close(self):
        with self.lock:
            if self.closed:
                return
            self.closed = True
            self.rows.clear()
            self.calls.clear()
            if self.transaction is not None:
                self.transaction.close()
            try:
                if self.connection is not None:
                    if hasattr(self, 'filter'):
                        self.connection.remove_filter(self.filter)
                    self.connection.close_sync(None)
            finally:
                self.reader.close()
