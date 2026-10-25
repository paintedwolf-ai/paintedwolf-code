"""Passively bind an actual GNOME lock window and focused native password object."""
from __future__ import annotations

import time

from . import accessibility, session_wire, unlock_wire
from ..installed_cases.desktop_ui import accessibility_bus, load_api
from ..installed_cases.records import CaseUnavailable


class NativeUnlockObserver:
    def __init__(self, lock):
        self.lock = lock
        self.environment = accessibility_bus(lock.session)
        self.entered = False
        self.window = None

    def __enter__(self):
        self.environment.__enter__()
        self.entered = True
        try:
            self.api = load_api()
            return self
        except BaseException:
            self.close()
            raise

    def __exit__(self, *_):
        self.close()

    def close(self):
        if self.entered:
            self.entered = False
            self.environment.__exit__(None, None, None)

    def session_proof(self):
        value = session_wire.proof({'native_session': self.lock.native.observe(),
                                   'lock_bindings': self.lock.initial['lock_bindings']})
        session_wire.same_runtime(self.lock.initial, value)
        return value

    def _showing(self, state):
        return (not state.contains(self.api.STATE_DEFUNCT)
                and state.contains(self.api.STATE_SHOWING)
                and state.contains(self.api.STATE_VISIBLE))

    def _visible(self, state):
        return (self._showing(state)
                and state.contains(self.api.STATE_ENABLED)
                and state.contains(self.api.STATE_SENSITIVE))

    def _showing_tree(self, root, deadline, *, skip_root=False):
        rows = []
        def include(node, depth):
            if skip_root and depth == 0:
                return True
            state = node.getState()
            if not self._showing(state):
                return False
            rows.append((node, node.getRole(), state))
            return True
        for _ in accessibility.descendants(root, deadline, include=include):
            pass
        return rows

    def _state(self, node, *, state=None, window=False):
        state = node.getState() if state is None else state
        result = {key: bool(state.contains(getattr(self.api, 'STATE_' + key.upper())))
                  for key in ('focused', 'enabled', 'sensitive', 'defunct')}
        result['visible'] = bool(state.contains(self.api.STATE_VISIBLE) and state.contains(self.api.STATE_SHOWING))
        if window:
            result['active'] = bool(state.contains(self.api.STATE_ACTIVE))
        return result

    def observe(self):
        if not self.entered:
            raise ValueError('native unlock observer is closed')
        before = self.session_proof()
        pid = before['native_session']['authorities']['shell']['pid']
        deadline = time.monotonic() + 5
        while True:
            try:
                desktop = self.api.Registry.getDesktop(0)
                count = desktop.childCount
                if type(count) is not int or not 0 <= count <= 256:
                    raise ValueError('native shell application inventory exceeds its bound')
                applications = [desktop[index] for index in range(count)]
                if any(node is None for node in applications):
                    raise accessibility.InventoryChanged()
                applications = [node for node in applications if node.get_process_id() == pid]
                if len(applications) != 1:
                    raise CaseUnavailable('native-shell-accessibility-owner-missing-or-ambiguous')
                rows = self._showing_tree(applications[0], deadline, skip_root=True)
                windows = [(node, state) for node, role, state in rows
                           if role == self.api.ROLE_WINDOW and self._visible(state)
                           and state.contains(self.api.STATE_ACTIVE)]
                if len(windows) != 1:
                    raise CaseUnavailable('native-locked-shell-window-missing-or-ambiguous')
                window, window_raw_state = windows[0]
                identity = accessibility.identity(window)
                if self.window is not None and self.window != identity:
                    raise ValueError('native unlock changed its captured shell window')
                if applications[0].childCount != 1 or accessibility.identity(applications[0][0]) != identity:
                    rows = self._showing_tree(window, deadline)
                visible_passwords = [(node, state) for node, role, state in rows
                    if role == self.api.ROLE_PASSWORD_TEXT and self._visible(state)]
                passwords = [(node, state) for node, state in visible_passwords
                    if state.contains(self.api.STATE_FOCUSED)]
                clock_focused = (not visible_passwords and (
                    bool(window_raw_state.contains(self.api.STATE_FOCUSED))
                    or any(node is not window and self._visible(state) and (
                        state.contains(self.api.STATE_FOCUSED)
                        or role == self.api.ROLE_WINDOW) for node, role, state in rows)))
                break
            except accessibility.InventoryChanged:
                if time.monotonic() >= deadline:
                    raise CaseUnavailable('normal-native-control-observation-deadline') from None
                time.sleep(0.05)
        if len(passwords) > 1:
            raise CaseUnavailable('native-shell-password-focus-is-ambiguous')
        password, chain = None, []
        if passwords:
            _, chain = accessibility.capture(passwords[0][0], [(None, window)])
            password = accessibility.identity(passwords[0][0])
        elif not clock_focused:
            raise CaseUnavailable('native-clock-window-has-no-positive-focus-proof')
        if (accessibility.identity(window) != identity or passwords and
                accessibility.identity(passwords[0][0]) != password):
            raise ValueError('native unlock target changed during observation')
        after = self.session_proof()
        session_wire.same_runtime(before, after)
        if any(before['native_session'][key] != after['native_session'][key]
               for key in ('locked_hint', 'locker_active')):
            raise CaseUnavailable('native-unlock-flags-changed-during-target-observation')
        window_state = self._state(window, state=window_raw_state, window=True)
        if password is None and clock_focused:
            window_state['focused'] = True
        value = unlock_wire.target({'session': after, 'window': identity,
                                   'password': password, 'ancestors': chain,
            'window_state': window_state,
            'password_state': self._state(passwords[0][0], state=passwords[0][1]) if passwords else None})
        self.window = identity
        return value
