"""Bounded failure facts; public label equality never authorizes a desktop action."""
from __future__ import annotations

import time

_TITLE = 'Settings: AI providers'
_PROVIDER = 'Other · painted-wolf/platform'
_COMBINED = _TITLE + ' ' + _PROVIDER


class ControlObservation:
    def __init__(self, deadline, public_result=False):
        self.deadline, self.public_result = deadline, public_result
        self.expired_before = time.monotonic() >= deadline
        self.completed, self.complete = 0, False
        self.rows, self.visited, self.clipped, self.unverified = {}, 0, False, False

    def begin(self):
        self.complete = False
        self.rows, self.visited, self.clipped, self.unverified = {}, 0, False, False

    def observe(self, ui, node):
        if not self.public_result:
            return
        if self.visited >= 512 or time.monotonic() >= self.deadline:
            self.clipped = True
            return
        self.visited += 1
        try:
            role, name = int(node.getRole()), node.name
            names = (name == _TITLE, name == _PROVIDER, name == _COMBINED)
            if role != int(ui.api.ROLE_PUSH_BUTTON) and not any(names):
                return
            state = node.getState()
            states = tuple(bool(state.contains(value)) for value in (
                ui.api.STATE_DEFUNCT, ui.api.STATE_SHOWING, ui.api.STATE_SENSITIVE, ui.api.STATE_ENABLED))
            key = (role, *names, *states)
            if key not in self.rows and len(self.rows) >= 32:
                self.clipped = True
                return
            self.rows[key] = self.rows.get(key, 0) + 1
        except Exception:
            # Diagnostic uncertainty cannot substitute another match or another action.
            self.unverified = True

    def finished(self):
        self.completed += 1
        self.complete = True

    def facts(self):
        result = {'completed_inventory_polls': self.completed, 'inventory_complete': self.complete,
                  'deadline_expired_before_lookup': self.expired_before,
                  'deadline_expired_at_refusal': time.monotonic() >= self.deadline}
        if self.public_result:
            result['public_command_results'] = {
                'observed_nodes': self.visited, 'clipped': self.clipped, 'unverified': self.unverified or self.clipped or not self.complete,
                'inventory_complete': self.complete,
                'rows': [dict(zip(('role', 'name_is_title', 'name_is_provider', 'name_is_combined',
                                  'defunct', 'showing', 'sensitive', 'enabled'), key), count=count)
                         for key, count in sorted(self.rows.items())]}
        return result
