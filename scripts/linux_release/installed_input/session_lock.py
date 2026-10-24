"""Observe the configured GNOME shortcut and its independent native lock transition."""
from __future__ import annotations

from copy import deepcopy
from pathlib import Path
import time

from . import session_wire
from ..installed_gnome import physical_image, stable
from ..installed_cases.app import session_environment
from ..installed_cases.native_session import NativeSessionObserver
from ..installed_cases.native_command import command
from ..installed_cases.records import CaseUnavailable

GSETTINGS = Path('/usr/bin/gsettings')


class NativeLockObserver:
    def __init__(self, session, *, native=None):
        self.session = deepcopy(session)
        self.owns_native = native is None
        self.native = native or NativeSessionObserver(session)
        self.closed = False
        self.progress = (False, False)
        try:
            self.initial = self.observe()
        except BaseException:
            self.close()
            raise

    def __enter__(self):
        if self.closed:
            raise ValueError('native lock observer is closed')
        return self

    def __exit__(self, *_):
        self.close()

    def close(self):
        if not self.closed:
            self.closed = True
            if self.owns_native:
                self.native.close()

    def bindings(self):
        before = physical_image(GSETTINGS)
        code, output, _ = command([str(GSETTINGS), 'get',
            'org.gnome.settings-daemon.plugins.media-keys', 'screensaver'],
            timeout=5, limit=4096, environment=session_environment(self.session))
        if code or not 0 < len(output) <= 4096 or stable(physical_image(GSETTINGS)) != stable(before):
            raise CaseUnavailable('native-gnome-lock-setting-unavailable')
        try:
            glib = self.native.reader.glib
            text = output.decode('utf-8')
            if '\0' in text:
                raise ValueError('native lock setting contains an embedded terminator')
            value = glib.Variant.parse(glib.VariantType.new('as'), text, None, None)
        except (ValueError, UnicodeError, self.native.reader.glib.Error):
            raise ValueError('native GNOME lock setting is not its typed string array') from None
        if value.get_type_string() != 'as':
            raise ValueError('native GNOME lock setting changed its declared type')
        return value.unpack()

    def observe(self):
        if self.closed:
            raise ValueError('native lock observer is closed')
        before = self.native.observe()
        bindings = self.bindings()
        previous = session_wire.proof({'native_session': before, 'lock_bindings': bindings})
        proof = session_wire.proof({'native_session': self.native.observe(), 'lock_bindings': bindings})
        session_wire.same_runtime(previous, proof)
        if any(before[key] and not proof['native_session'][key] for key in ('locked_hint', 'locker_active')):
            raise ValueError('native lock transition reversed during shortcut observation')
        if hasattr(self, 'initial'):
            session_wire.same_runtime(self.initial, proof)
        return proof

    def prepare(self, operation):
        proof = session_wire.before(operation, self.observe())
        self.progress = (False, False)
        return proof

    def during(self, operation, before):
        if operation not in session_wire.OPERATIONS:
            raise ValueError('native lock requires its closed operation')
        after = self.observe()
        session_wire.same_runtime(before, after)
        current = tuple(after['native_session'][key] for key in ('locked_hint', 'locker_active'))
        if any(previous and not actual for previous, actual in zip(self.progress, current)):
            raise ValueError('native lock transition reversed before completion')
        self.progress = current
        return after

    def settle(self, operation, before):
        deadline = time.monotonic() + 15
        while True:
            after = self.during(operation, before)
            if session_wire.finished(operation, before, after):
                return after
            if time.monotonic() >= deadline:
                error = CaseUnavailable('native-lock-transition-unverified')
                error.observations = {'session_before': before, 'session_after': after}
                raise error
            time.sleep(0.05)
