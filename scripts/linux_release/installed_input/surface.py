"""Observe retained AT-SPI objects across closed app-frame transitions."""
from __future__ import annotations

import time

from ..installed_cases.app import process_identity
from ..installed_cases.desktop_ui import owned_process
from ..installed_cases.records import CaseFailed, CaseUnavailable
from . import surface_wire as wire


class FrameObserver:
    def __init__(self, app, ui):
        self.app, self.ui = app, ui
        self.launch = dict(app.observations['launch_identity'])
        self.engine = dict(app.observations['engine_identity'])
        self.nodes = {}
        self._runtime()
        frames = [node for _, node in ui.windows() if node.getRole() == ui.api.ROLE_FRAME
                  and node.getState().contains(ui.api.STATE_ACTIVE)]
        if len(frames) != 1:
            raise CaseUnavailable('owned-original-active-frame-is-missing-or-ambiguous')
        self.main = self._identity(frames[0])
        self.nodes[self._key(self.main)] = frames[0]
        self.observe()

    @staticmethod
    def _key(identity):
        return identity['bus_name'], identity['object_path']

    def _runtime(self):
        self.app.require_running()
        for expected in (self.launch, self.engine):
            try:
                actual = process_identity(expected['pid'])
            except (FileNotFoundError, ProcessLookupError):
                raise CaseFailed('captured frame transition runtime disappeared') from None
            if actual != expected:
                raise CaseFailed('captured frame transition runtime identity changed')

    def _identity(self, node):
        identity = wire.object_identity({'bus_name': node.app.bus_name, 'object_path': node.path,
                                         'process': process_identity(node.get_process_id())})
        if identity['process'] != self.launch or not owned_process(identity['process']['pid'], self.app):
            raise CaseFailed('accessible surface does not belong to the captured desktop')
        return identity

    def _parent(self, node):
        matches = []
        relations = node.getRelationSet()
        if len(relations) > 32:
            raise ValueError('native surface relation inventory exceeds its bound')
        for relation in relations:
            if relation.getRelationType() != self.ui.api.RELATION_SUBWINDOW_OF:
                continue
            if relation.getNTargets() != 1:
                raise CaseUnavailable('native-modal-parent-is-missing-or-ambiguous')
            matches.append(self._identity(relation.getTarget(0)))
        if matches != [self.main]:
            raise CaseUnavailable('native-dialog-has-no-observed-original-frame-parent')
        return matches[0]

    def observe(self):
        self._runtime()
        for _, node in self.ui.windows():
            identity = self._identity(node)
            self.nodes[self._key(identity)] = node
        if len(self.nodes) > 16:
            raise ValueError('captured app surface inventory exceeds its bound')
        surfaces = []
        for marker, node in sorted(self.nodes.items()):
            identity = self._identity(node)
            if marker != self._key(identity):
                raise CaseFailed('retained accessible object identity changed')
            state, role = node.getState(), node.getRole()
            defunct = bool(state.contains(self.ui.api.STATE_DEFUNCT))
            kind, parent = 'frame', None
            if role == self.ui.api.ROLE_DIALOG:
                if node.name not in {"Quit Painted Wolf Code?", "Confirm it's you"}:
                    raise CaseUnavailable('unexpected-owned-native-dialog-during-frame-transition')
                kind = 'quit-dialog' if node.name == 'Quit Painted Wolf Code?' else 'presence-dialog'
                parent = self._parent(node)
            elif role != self.ui.api.ROLE_FRAME:
                raise CaseUnavailable('retained-object-is-no-longer-an-owned-frame')
            surfaces.append({'identity': identity, 'kind': kind,
                'visible': bool(self.ui.visible(node)), 'active': bool(state.contains(self.ui.api.STATE_ACTIVE)),
                'defunct': defunct, 'modal': bool(state.contains(self.ui.api.STATE_MODAL)), 'parent': parent})
        self._runtime()
        return wire.proof({'launch': self.launch, 'engine': self.engine,
                           'main': self.main, 'surfaces': surfaces})

    def prepare(self, operation):
        return wire.before(operation, self.observe())

    def during(self, before):
        wire.same_runtime(before, self.observe())

    def settle(self, operation, before):
        deadline = time.monotonic() + 15
        while True:
            after = self.observe()
            if wire.finished(operation, before, after):
                return after
            if time.monotonic() >= deadline:
                error = CaseUnavailable('owned-frame-transition-did-not-reach-its-declared-state')
                error.observations = {'surface_before': before, 'surface_after': after}
                raise error
            time.sleep(0.05)
