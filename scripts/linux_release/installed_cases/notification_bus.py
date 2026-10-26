"""Passively observe the real desktop notification service without emitting activation signals."""
from __future__ import annotations

from collections import deque
import os
import threading
import time

from .app import process_identity
from .logind_context import Reader
from .notification_delivery import DeliveryTrace, PATH, SERVICE, unique
from .records import CaseFailed, CaseUnavailable

BUS = 'org.freedesktop.DBus'
BUS_PATH = '/org/freedesktop/DBus'


class NotificationBus:
    def __init__(self, app, ui_process, desktop_entry='dev.paintedwolf.code'):
        self.app, self.ui_process, self.desktop_entry = app, dict(ui_process), desktop_entry
        self.reader, self.control, self.monitor, self.filter_id = None, None, None, None
        self.rows, self.lock = deque(), threading.Lock()
        self.error, self.trace, self.monitoring = None, None, False
        self.owner, self.service_process = None, None
        self.pending_calls, self.signal_owners = {}, set()

    def _call(self, name, signature, values, deadline=None):
        if name not in {'GetNameOwner', 'GetConnectionUnixUser', 'GetConnectionUnixProcessID'}:
            raise ValueError('notification observer only reads declared bus authority methods')
        result_type = '(s)' if name == 'GetNameOwner' else '(u)'
        return self.reader.call(BUS_PATH, BUS, name, (signature, values), result_type,
            user=True, destination=BUS, deadline=deadline).unpack()[0]

    def _pid(self, sender, deadline=None):
        unique(sender)
        uid = self._call('GetConnectionUnixUser', '(s)', (sender,), deadline)
        pid = self._call('GetConnectionUnixProcessID', '(s)', (sender,), deadline)
        if type(uid) is not int or uid != os.getuid() or type(pid) is not int:
            raise CaseFailed('notification bus connection changed its graphical account')
        return process_identity(pid)

    def require_owners(self, deadline=None):
        self.app.require_running()
        if process_identity(self.ui_process['pid']) != self.ui_process:
            raise CaseFailed('notification native UI process changed identity')
        if self.owner is not None:
            owner = self._call('GetNameOwner', '(s)', (SERVICE,), deadline)
            if owner != self.owner or self._pid(owner, deadline) != self.service_process:
                raise CaseFailed('notification service changed its captured bus or process owner')
        if self.trace is not None and self._pid(self.trace.sender, deadline) != self.ui_process:
            raise CaseFailed('notification sender changed its captured native UI process')

    def __enter__(self):
        try:
            self.reader = Reader(user_only=True)
            self.Gio, self.GLib = Gio, GLib = self.reader.gio, self.reader.glib
            self.control = self.reader.user
            self.require_owners()
            self.monitor = self.reader.connect('/run/user/' + str(os.getuid()) + '/bus', os.getuid())
            self.filter_id = self.monitor.add_filter(self._filter, None)
            rules = [f"type='method_call',interface='{SERVICE}',path='{PATH}',member='{member}'"
                for member in ('Notify', 'GetCapabilities')] + [
                "type='method_return'",
                f"type='signal',interface='{SERVICE}',path='{PATH}'"]
            self.monitor.call_sync(BUS, BUS_PATH, BUS + '.Monitoring', 'BecomeMonitor',
                GLib.Variant('(asu)', (rules, 0)), None, Gio.DBusCallFlags.NONE, 1000, None)
            self.monitoring = True
            self.require_owners()
            return self
        except Exception:
            self.close()
            raise

    def _filter(self, connection, message, incoming, user_data):
        if not incoming:
            return message
        relevant = False
        try:
            kinds = {self.Gio.DBusMessageType.METHOD_CALL: 'call',
                self.Gio.DBusMessageType.METHOD_RETURN: 'return', self.Gio.DBusMessageType.SIGNAL: 'signal'}
            kind, sender = kinds.get(message.get_message_type()), message.get_sender()
            with self.lock:
                relevant = False
                if (kind == 'call' and message.get_interface() == SERVICE and message.get_path() == PATH
                        and message.get_member() in {'Notify', 'GetCapabilities'}):
                    if len(self.pending_calls) >= 16:
                        raise ValueError('notification observed call count exceeds its bound')
                    destination = message.get_destination()
                    if isinstance(destination, str) and destination.startswith(':'):
                        unique(destination)
                        key = (unique(sender), message.get_serial())
                        if key in self.pending_calls:
                            raise ValueError('notification observed request header was duplicated')
                        self.pending_calls[key] = destination
                        self.signal_owners.add(destination)
                        relevant = True
                elif (kind == 'return' and self.pending_calls.get(
                        (message.get_destination(), message.get_reply_serial())) == sender):
                    self.signal_owners.add(sender)
                    relevant = True
                elif kind == 'signal' and sender in self.signal_owners and message.get_interface() == SERVICE:
                    relevant = True
            if relevant:
                body = message.get_body()
                empty = body is None and kind == 'call' and message.get_member() == 'GetCapabilities'
                if not empty and (body is None or body.get_size() > 64 << 10):
                    raise ValueError('notification bus message exceeds its body bound')
                row = {'type': kind, 'sender': sender, 'destination': message.get_destination(),
                    'serial': message.get_serial(), 'reply_serial': message.get_reply_serial(),
                    'path': message.get_path(), 'interface': message.get_interface(),
                    'member': message.get_member(), 'signature': '()' if empty else body.get_type_string(),
                    'body': () if empty else body.unpack()}
                with self.lock:
                    if len(self.rows) >= 128:
                        raise ValueError('notification bus observation queue exceeds its bound')
                    self.rows.append(row)
        except Exception as error:
            with self.lock:
                self.error = error
        # A monitor must consume observed calls rather than answer them.
        return None if self.monitoring or relevant else message

    def drain(self, deadline=None):
        self.require_owners(deadline)
        with self.lock:
            error, rows = self.error, list(self.rows)
            self.rows.clear()
        if error is not None:
            raise error
        for row in rows:
            if self.trace is None:
                if row['type'] != 'call' or self._pid(row['sender'], deadline) != self.ui_process:
                    continue
                owner = unique(self._call('GetNameOwner', '(s)', (SERVICE,), deadline))
                if row['destination'] not in {owner, SERVICE}:
                    raise CaseFailed('actual notification request has a replaced service destination')
                if self.owner is not None and owner != self.owner:
                    raise CaseFailed('notification service changed before its real delivery')
                self.owner, self.service_process = owner, self._pid(owner, deadline)
                self.trace = DeliveryTrace(row['sender'], self.owner, self.desktop_entry)
            if row['type'] == 'call' and row['sender'] != self.trace.sender:
                if self._pid(row['sender'], deadline) == self.ui_process:
                    raise CaseFailed('native UI notification sender became ambiguous')
                continue
            if row['type'] == 'return' and row['destination'] != self.trace.sender:
                continue
            self.trace.observe(row)
        self.require_owners(deadline)

    def await_delivery(self, deadline):
        while time.monotonic() < deadline:
            self.drain(deadline)
            if self.trace is not None and self.trace.delivery is not None:
                return self.snapshot()
            time.sleep(0.02)
        raise CaseUnavailable('real-desktop-notification-was-not-delivered-before-deadline')

    def arm(self):
        self.drain()
        if self.trace is None:
            raise CaseUnavailable('notification-activation-has-no-observed-delivery')
        return self.trace.arm()

    def await_activation(self, deadline):
        while time.monotonic() < deadline:
            self.drain(deadline)
            if self.trace is not None and self.trace.activation is not None:
                return self.snapshot()
            time.sleep(0.02)
        raise CaseUnavailable('real-notification-default-action-was-not-observed-before-deadline')

    def snapshot(self):
        return {'service_process': dict(self.service_process) if self.service_process is not None else None, 'ui_process': dict(self.ui_process),
            'trace': self.trace.snapshot() if self.trace is not None else None}

    def close(self):
        errors = []
        monitor, control, reader = self.monitor, self.control, self.reader
        self.monitor, self.control, self.reader = None, None, None
        operations = []
        if monitor is not None:
            if self.filter_id is not None:
                operations.append(lambda: monitor.remove_filter(self.filter_id))
            operations.append(lambda: monitor.close_sync(None))
        if reader is not None:
            operations.append(reader.close)
        elif control is not None:
            operations.append(lambda: control.close_sync(None))
        for operation in operations:
            try:
                operation()
            except Exception as error:
                errors.append({'type': type(error).__name__, 'detail': str(error)[:256]})
        if errors:
            error = CaseUnavailable('passive-notification-monitor-cleanup-failed')
            error.observations = {'notification_monitor_cleanup': errors}
            raise error

    def __exit__(self, kind, value, traceback):
        self.close()
