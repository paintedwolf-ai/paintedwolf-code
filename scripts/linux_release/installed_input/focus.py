"""Bind native input to the selected focused entry and its actual owned active frame."""
from __future__ import annotations
import time

from ..installed_cases.app import process_identity
from ..installed_cases.desktop_ui import owned_process
from ..installed_cases.records import CaseFailed, CaseUnavailable
from .protocol import focus


class _FocusUnavailable(CaseUnavailable):
    pass


class EntryFocus:
    def __init__(self, app, ui, label: str):
        self.app, self.ui, self.label = app, ui, label
        self.deadline = time.monotonic() + 90
        self.node = ui.find(ui.api.ROLE_ENTRY, label)
        self.entry = process_identity(self.node.get_process_id())
        self.launch = dict(app.observations['launch_identity'])
        if self.node.queryComponent().grabFocus() is not True:
            raise CaseUnavailable('normal-owned-editor-focus-refused')
        self.observe()

    def _stable(self, expected: dict):
        try:
            actual = process_identity(expected['pid'])
        except (FileNotFoundError, ProcessLookupError):
            error = CaseFailed('selected native input process disappeared before its required interaction')
            error.observations = {'native_input_process': expected, 'process_present': False}
            raise error from None
        if actual != expected:
            raise CaseFailed('selected native input process identity changed before its required interaction')

    def observe(self) -> dict:
        now = time.monotonic()
        if now >= self.deadline:
            raise CaseUnavailable('fixed native input exceeded its operation observation deadline')
        deadline = min(self.deadline, now + 2)
        while True:
            try:
                return self._observe_once()
            except _FocusUnavailable:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(0.05)

    def _observe_once(self) -> dict:
        self.app.require_running()
        self._stable(self.launch)
        self._stable(self.entry)
        node, ui = self.node, self.ui
        visible = bool(ui.visible(node))
        focused = bool(node.getState().contains(ui.api.STATE_FOCUSED))
        if not visible or not focused:
            # WebKit can replace the accessible entry during a normal editor render.
            # Re-observe the exact owned label; never refocus or replay delivered input.
            replacement = ui.find(ui.api.ROLE_ENTRY, self.label)
            replacement_identity = process_identity(replacement.get_process_id())
            if replacement_identity == self.entry:
                node = replacement
                self.node = replacement
                visible = bool(ui.visible(node))
                focused = bool(node.getState().contains(ui.api.STATE_FOCUSED))
        if not owned_process(self.entry['pid'], self.app) or not visible or not focused:
            error = _FocusUnavailable('normal-owned-editor-focus-unavailable')
            error.observations = {'native_input_focus': {'entry_identity': self.entry,
                'entry_visible': visible, 'entry_focused': focused}}
            raise error
        parent, seen, frame = node, set(), None
        for _ in range(64):
            parent = parent.get_parent()
            if parent is None:
                break
            marker = hash(parent)
            if marker in seen:
                raise ValueError('native input accessible parent chain cycles')
            seen.add(marker)
            if parent.getRole() in {ui.api.ROLE_FRAME, ui.api.ROLE_DIALOG}:
                frame = parent
                break
        if frame is None:
            raise _FocusUnavailable('normal-owned-editor-container-unavailable')
        frame_identity = process_identity(frame.get_process_id())
        active, visible = frame.getState().contains(ui.api.STATE_ACTIVE), bool(ui.visible(frame))
        if (frame_identity != self.launch or not owned_process(frame_identity['pid'], self.app)
                or not active or not visible):
            refusal = _FocusUnavailable('normal-owned-editor-active-frame-unavailable')
            refusal.observations = {'editor_focus': {'entry_identity': self.entry,
                'entry_focused': True, 'container_identity': frame_identity, 'container_active': bool(active)}}
            raise refusal
        self._stable(self.launch)
        self._stable(self.entry)
        return focus({'launch': self.launch, 'entry': self.entry, 'frame': frame_identity,
                      'entry_focused': True, 'frame_active': True, 'frame_visible': True})
