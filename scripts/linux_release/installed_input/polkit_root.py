"""Fixed privileged observer endpoint; private transport only, never a mailbox authority."""
from __future__ import annotations

import os
from pathlib import Path
import select
import sys
import time

from . import protocol, session_wire
from .mailbox import decode
from .polkit_monitor import Monitor
from .polkit_runtime import Runtime
from .polkit_wire import validate
from .unlock_account import validate_account
from ..installed_cases.native_session import NativeSessionObserver

ENVELOPE = {'format_version', 'binding', 'vm_id', 'account', 'sequence', 'operation'}
FIELDS = {'start': {'session', 'runtime'}, 'bind': {'attempt'}, 'observe': set()}


def read_request(deadline):
    raw = bytearray()
    while len(raw) <= protocol.MAX_DOCUMENT:
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([0], [], [], remaining)[0]:
            raise ValueError('Polkit root transport exceeded its fixed deadline')
        block = os.read(0, min(4096, protocol.MAX_DOCUMENT + 1 - len(raw)))
        if not block:
            if not raw:
                return None
            raise ValueError('Polkit root transport ended during its fixed request')
        raw.extend(block)
        if b'\n' in raw:
            if raw.index(10) != len(raw) - 1:
                raise ValueError('Polkit root transport cannot batch or pipeline requests')
            return decode(bytes(raw))
    raise ValueError('Polkit root transport exceeded its fixed document bound')


class Endpoint:
    def __init__(self):
        if os.geteuid() != 0:
            raise ValueError('Polkit root observer requires actual privileged custody')
        self.sequence, self.identity = 0, None
        self.monitor = self.native = self.runtime = None
        self.deadline = time.monotonic() + 150
        self.bound = False

    def handle(self, request):
        operation = request.get('operation') if isinstance(request, dict) else None
        if (operation not in FIELDS or set(request) != ENVELOPE | FIELDS[operation]
                or type(request['format_version']) is not int or request['format_version'] != 1
                or type(request['sequence']) is not int or request['sequence'] != self.sequence
                or self.sequence >= 512 or time.monotonic() >= self.deadline):
            raise ValueError('Polkit root observer lost its closed one-use request sequence')
        self.sequence += 1
        identity = {key: request[key] for key in ('binding', 'vm_id', 'account')}
        if self.identity is None:
            if operation != 'start':
                raise ValueError('Polkit root observer requires its initial capture before selection')
            protocol.binding(identity['binding'])
            # The isolated bootstrap already authenticated every captured Python
            # byte. Bind that physical capture to this exact requested source.
            expected = Path('/var/lib/paintedwolf-qualification') / identity['binding']['driver_tree']
            if Path(__file__).resolve().parents[3] != expected:
                raise ValueError('Polkit root observer source differs from its authenticated capture')
            validate_account(identity['account'], vm_id=identity['vm_id'])
            self.identity = identity
        elif identity != self.identity:
            raise ValueError('Polkit root observer replaced its execution, VM or dedicated account')
        if operation == 'start':
            if self.monitor is not None:
                raise ValueError('Polkit root observer cannot replay its initial capture')
            self.runtime = Runtime(request['runtime'], self.identity['account'])
            self.monitor = Monitor(self.identity['account'], self.identity['vm_id'], deadline=self.deadline)
            # This is the sole selected user bus: physical socket/parents and
            # account are checked by Reader before any root-side session call.
            uid = self.identity['account']['uid']
            reader = self.monitor.reader
            reader.user = reader.connect('/run/user/' + str(uid) + '/bus', uid, deadline=self.deadline)
            self.native = NativeSessionObserver(request['session'], reader=reader,
                expected_uid=uid, deadline=self.deadline)
            self.initial = self.native.initial
            self._current(unlocked=True)
            result = {'monitoring': True}
        elif operation == 'bind':
            if self.bound:
                raise ValueError('Polkit root observer cannot rebind or replay its selected call')
            self.bound = True
            attempt = validate(request['attempt'])
            owners = self._current()
            if (attempt['subject'] != {key: owners['native'][key] for key in ('pid', 'start_time_ticks', 'uid')}
                    or self.monitor.peer(attempt['sender']) != owners['native']):
                raise ValueError('Native Polkit observation differs from the root-pinned actual system subject')
            result = self.monitor.bind(attempt, owners['native'], self.initial['authorities']['locker']['process'])
        else:
            if not self.bound:
                raise ValueError('Polkit root observer has no independently bound selected call')
            self._current()
            result = self.monitor.observe()
        self._current()
        if operation != 'start' and result is not None:
            result = {**result, 'native_session': self.current_session}
        if time.monotonic() >= self.deadline:
            raise ValueError('Polkit root observation outlived its fixed private transport')
        return {**{key: request[key] for key in ENVELOPE}, 'result': result}

    def _current(self, *, unlocked=False):
        if self.native is None or self.runtime is None:
            raise ValueError('Polkit root observer has no actual installed session binding')
        current = self.native.observe(deadline=self.deadline)
        session_wire.same_session(self.initial, current)
        if unlocked and (current['locked_hint'] or current['locker_active']):
            raise ValueError('Polkit root observer selected session is locked')
        self.current_session = current
        return self.runtime.observe()

    def close(self):
        try:
            if self.native is not None:
                self.native.close()
        finally:
            if self.monitor is not None:
                self.monitor.close()


def main():
    endpoint = None
    try:
        endpoint = Endpoint()
        while (request := read_request(endpoint.deadline)) is not None:
            reply = protocol.canonical(endpoint.handle(request))
            sys.stdout.buffer.write(reply)
            sys.stdout.buffer.flush()
    except BaseException:
        # No exception prose, D-Bus bodies, cookies or PAM data leave this process.
        raise SystemExit(2) from None
    finally:
        if endpoint is not None:
            try:
                endpoint.close()
            except BaseException:
                pass
