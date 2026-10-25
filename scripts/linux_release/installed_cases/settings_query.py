"""Verify only the closed public Actions query on its pinned owned entry."""
from __future__ import annotations

import time

from ..installed_input.focus import EntryFocus
from ..installed_input.guest import perform
from .records import CaseUnavailable


class SettingsQuery:
    def __init__(self, app, ui, deadline):
        native = app.binding.get('native_input')
        if not isinstance(native, dict):
            raise CaseUnavailable('protected-reply-requires-fixed-physical-input')
        self.app, self.native, self.deadline = app, native, deadline
        self.actor = EntryFocus(app, ui, 'Crossbar…', deadline=deadline)

    def enter(self):
        record = perform(self.native['binding'], self.native['vm_id'], self.actor,
                         operation='settings-providers-query-v1', deadline=self.deadline)
        self.app.observations.setdefault('fixed_input', []).append(record)
        if record.get('completed') is not True:
            raise ValueError('project configuration query did not complete its fixed operation')
        self.require_current()
        return record

    def require_current(self):
        expected = '> Settings: AI providers'
        witness = {'fixed_query_matches': False, 'explicit_actions_arm': False}
        self.app.observations['project_configuration_query_readback'] = witness
        if time.monotonic() >= self.deadline:
            raise CaseUnavailable('normal-project-configuration-query-deadline')
        before = self.actor.observe()
        node = self.actor.node
        try:
            text = node.queryText()
            # Read no arbitrary field content: only this exact public query's bounded span.
            matches = text.characterCount == len(expected) and text.getText(0, len(expected)) == expected
        except Exception:
            raise CaseUnavailable('normal-project-configuration-query-readback-unavailable') from None
        if self.actor.observe() != before or self.actor.node is not node:
            raise ValueError('project configuration query identity changed during readback')
        if time.monotonic() >= self.deadline:
            raise CaseUnavailable('normal-project-configuration-query-deadline')
        witness.update(fixed_query_matches=bool(matches), explicit_actions_arm=bool(matches))
        if not matches:
            raise CaseUnavailable('normal-project-configuration-fixed-query-mismatch')
