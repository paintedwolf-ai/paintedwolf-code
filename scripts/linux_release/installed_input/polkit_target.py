"""Observe the single actual GNOME modal password surface for a pending Polkit call."""
from __future__ import annotations

import time

from . import accessibility, polkit_stage, session_wire
from ..installed_cases.desktop_ui import accessibility_bus, load_api
from ..installed_cases.native_session import NativeSessionObserver
from ..installed_cases.records import CaseUnavailable


class Target:
    def __init__(self, session):
        self.session = session
        self.environment = accessibility_bus(session)
        self.entered = False
        self.native = None
        self.original = None

    def __enter__(self):
        self.environment.__enter__()
        self.entered = True
        try:
            self.api = load_api()
            self.native = NativeSessionObserver(self.session)
            return self
        except BaseException:
            self.close()
            raise

    def close(self):
        try:
            if self.native is not None:
                self.native.close()
        finally:
            if self.entered:
                self.entered = False
                self.environment.__exit__(None, None, None)

    def __exit__(self, *_):
        self.close()

    def _state(self, node, *, window=False, modal=False):
        raw = node.getState()
        result = {key: bool(raw.contains(getattr(self.api, 'STATE_' + key.upper())))
                  for key in ('focused', 'enabled', 'sensitive', 'defunct')}
        result['visible'] = bool(raw.contains(self.api.STATE_VISIBLE) and raw.contains(self.api.STATE_SHOWING))
        if window:
            result['active'] = bool(raw.contains(self.api.STATE_ACTIVE))
        if modal:
            result['modal'] = bool(raw.contains(self.api.STATE_MODAL))
        return result

    @staticmethod
    def _live(state):
        return (not state['defunct'] and
                all(state[key] for key in ('visible', 'enabled', 'sensitive')))

    def _inventory(self, pid, deadline):
        desktop = self.api.Registry.getDesktop(0)
        count = desktop.childCount
        if type(count) is not int or not 0 <= count <= 256:
            raise ValueError('Polkit native application inventory exceeded its bound')
        applications = [desktop[index] for index in range(count)]
        if any(node is None for node in applications):
            raise accessibility.InventoryChanged()
        selected = [node for node in applications if node.get_process_id() == pid]
        if len(selected) != 1:
            raise CaseUnavailable('polkit-native-agent-application-missing-or-ambiguous')
        application = selected[0]
        identity = accessibility.identity(application)
        rows = []
        def showing(node, depth):
            if depth == 0:
                return True
            state = self._state(node)
            if not state['visible'] or state['defunct']:
                return False
            rows.append((node, node.getRole(), state))
            return True
        for _ in accessibility.descendants(application, deadline, include=showing):
            pass
        if (desktop.childCount != count or accessibility.identity(application) != identity
                or [accessibility.identity(node) for node in applications]
                != [accessibility.identity(desktop[index]) for index in range(count)]):
            raise accessibility.InventoryChanged()
        return rows

    def observe(self, pending):
        if not self.entered:
            raise ValueError('Polkit native target observer is closed')
        before = session_wire.native_session(self.native.observe())
        if before['locked_hint'] or before['locker_active']:
            raise CaseUnavailable('polkit-native-session-is-locked')
        deadline = time.monotonic() + 5
        while True:
            try:
                rows = self._inventory(before['authorities']['shell']['pid'], deadline)
                windows = [node for node, role, state in rows if role == self.api.ROLE_WINDOW
                           and self._live(state) and self._state(node, window=True)['active']]
                modals = [node for node, role, state in rows if role == self.api.ROLE_DIALOG
                          and self._live(state) and self._state(node, modal=True)['modal']]
                passwords = [node for node, role, state in rows
                             if role == self.api.ROLE_PASSWORD_TEXT and self._live(state)]
                if len(windows) != 1 or len(modals) != 1 or len(passwords) != 1:
                    raise CaseUnavailable('polkit-native-modal-password-surface-missing-or-ambiguous')
                window, modal, password = windows[0], modals[0], passwords[0]
                _, ancestors = accessibility.capture(password, [(None, window)])
                modal_identity = accessibility.identity(modal)
                if modal_identity not in ancestors[1:]:
                    raise ValueError('Polkit password does not descend from its actual native modal')
                value = {'session': self.native.observe(), 'pending': pending,
                    'window': accessibility.identity(window), 'password': accessibility.identity(password),
                    'modal': modal_identity, 'modal_state': self._state(modal, modal=True),
                    'ancestors': ancestors, 'window_state': self._state(window, window=True),
                    'password_state': self._state(password)}
                session_wire.same_session(before, value['session'])
                polkit_stage.target(value)
                # A second complete native inventory cannot introduce another
                # modal/password candidate after the initial parent capture.
                again = self._inventory(before['authorities']['shell']['pid'], deadline)
                keys = lambda rows: [(accessibility.identity(node), role, state)
                                     for node, role, state in rows]
                if (keys(rows) != keys(again) or value['window_state'] != self._state(window, window=True)
                        or value['modal_state'] != self._state(modal, modal=True)
                        or value['password_state'] != self._state(password)):
                    raise accessibility.InventoryChanged()
                final = self.native.observe()
                session_wire.same_session(value['session'], final)
                if final['locked_hint'] or final['locker_active']:
                    raise CaseUnavailable('polkit-native-session-locked-during-observation')
                break
            except Exception as error:
                domain, code = getattr(error, 'domain', None), getattr(error, 'code', None)
                native = (domain in {'atspi_error', 'g-dbus-error-quark', 'g-io-error-quark'}
                          and type(code) is int and 0 <= code < 1 << 31)
                if (not isinstance(error, (IndexError, accessibility.InventoryChanged))
                        and not native):
                    raise
                current = self.native.observe()
                session_wire.same_session(before, current)
                if current['locked_hint'] or current['locker_active']:
                    raise CaseUnavailable('polkit-native-session-locked-during-observation') from None
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('polkit-native-target-observation-deadline') from None
                time.sleep(0.05)
        if self.original is not None:
            polkit_stage.same_target(self.original, value)
        self.original = value
        return value
