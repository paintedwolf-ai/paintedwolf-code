"""Closed fixture password targets; unnamed entries require the owned native modal."""
from __future__ import annotations

from ..installed_cases.records import CaseFailed, CaseUnavailable
from . import password_wire, surface_wire
from .focus import EntryFocus


class FixtureFocus:
    def __init__(self, app, ui, stage, observer=None):
        if stage not in password_wire.STAGES:
            raise ValueError('password focus requires a declared fixture stage')
        self.stage, self.observer = stage, observer
        self.entry, self.original = None, None
        if stage in password_wire.LABELS:
            self.entry = EntryFocus(app, ui, password_wire.LABELS[stage], password=True)
        elif observer is None or observer.app is not app or observer.ui is not ui:
            raise ValueError('native password focus requires the captured shared frame observer')
        self.observe()

    def observe(self):
        if self.entry is not None:
            result = {'kind': 'entry', 'label': password_wire.LABELS[self.stage], 'focus': self.entry.observe()}
        else:
            observer = self.observer
            proof = surface_wire.before('presence-cancel-v1', observer.observe())
            dialog = next(row for row in proof['surfaces'] if surface_wire.active(row))
            node = observer.nodes[observer._key(dialog['identity'])][0]
            ui = observer.ui
            controls = [control for control in ui.descendants(node)
                if control.getRole() == ui.api.ROLE_PASSWORD_TEXT and ui.visible(control)]
            if len(controls) != 1 or not controls[0].getState().contains(ui.api.STATE_FOCUSED):
                raise CaseUnavailable('owned-native-password-control-is-missing-ambiguous-or-unfocused')
            result = {'kind': 'native-presence', 'surface': proof, 'password': {
                'identity': observer._identity(controls[0]), 'parent': dialog['identity'],
                'focused': True, 'visible': True}}
        password_wire.target(self.stage, result)
        if self.original is not None and result != self.original:
            raise CaseFailed('password fixture target changed during its one-use input stage')
        self.original = result
        return result
