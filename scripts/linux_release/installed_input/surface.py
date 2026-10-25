"""Observe retained AT-SPI objects across closed app-frame transitions."""
from __future__ import annotations

import os
import select
import time

from ..installed_process import process_identity
from ..installed_cases.records import CaseFailed, CaseUnavailable
from . import accessibility, lineage, surface_wire as wire


class FrameObserver:
    def __init__(self, app, ui):
        self.app, self.ui = app, ui
        self.launch = dict(app.observations['launch_identity'])
        self.engine = dict(app.observations['engine_identity'])
        self.nodes, self.pins = {}, {}
        try:
            self._runtime()
            for name, identity in (('launch', self.launch), ('engine', self.engine)):
                self.pins[name] = os.pidfd_open(identity['pid'])
            self._runtime()
            frames = [node for _, node in ui.windows() if node.getRole() == ui.api.ROLE_FRAME
                      and node.getState().contains(ui.api.STATE_ACTIVE)]
            if len(frames) != 1:
                raise CaseUnavailable('owned-original-active-frame-is-missing-or-ambiguous')
            self.main = self._identity(frames[0])
            self.pins['frame'] = os.pidfd_open(self.main['process']['pid'])
            self._capture(frames[0])
            self.observe()
        except BaseException:
            self.close()
            raise

    def __enter__(self):
        return self

    def __exit__(self, kind, value, traceback):
        self.close()

    def close(self):
        for descriptor in self.pins.values():
            os.close(descriptor)
        self.pins.clear()

    @staticmethod
    def _key(identity):
        return identity['bus_name'], identity['object_path']

    def _runtime(self):
        self.app.require_running()
        selected = [self.launch, self.engine]
        if hasattr(self, 'main'):
            selected.append(self.main['process'])
        for expected in selected:
            try:
                actual = process_identity(expected['pid'])
            except (FileNotFoundError, ProcessLookupError):
                raise CaseFailed('captured frame transition runtime disappeared') from None
            if actual != expected:
                raise CaseFailed('captured frame transition runtime identity changed')

    def _identity(self, node):
        native = accessibility.identity(node)
        process = process_identity(native['pid'])
        identity = wire.object_identity({'bus_name': native['bus_name'], 'object_path': native['path'],
            'process': process, 'lineage': lineage.capture(self.launch, process)}, self.launch)
        if hasattr(self, 'main') and process != self.main['process']:
            raise CaseFailed('accessible surface changed its captured native UI process')
        return identity

    def _parent(self, node):
        matches, relations = [], node.getRelationSet()
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

    def _capture(self, node):
        identity, role = self._identity(node), node.getRole()
        kind, parent = 'frame', None
        if role == self.ui.api.ROLE_DIALOG:
            if node.name not in {"Quit Painted Wolf Code?", "Confirm it's you"}:
                raise CaseUnavailable('unexpected-owned-native-dialog-during-frame-transition')
            kind = 'quit-dialog' if node.name == 'Quit Painted Wolf Code?' else 'presence-dialog'
            parent = self._parent(node)
        elif role != self.ui.api.ROLE_FRAME:
            raise CaseUnavailable('retained-object-is-no-longer-an-owned-frame')
        metadata = {'identity': identity, 'kind': kind, 'parent': parent}
        marker = self._key(identity)
        if marker in self.nodes and self.nodes[marker][1] != metadata:
            raise CaseFailed('retained accessible object identity or parent changed')
        self.nodes[marker] = node, metadata

    def _quit_control(self, surfaces):
        matches = []
        for row in surfaces:
            if row['kind'] != 'quit-dialog' or not wire.active(row):
                continue
            node = self.nodes[self._key(row['identity'])][0]
            for control in accessibility.descendants(node):
                if (control.getRole() == self.ui.api.ROLE_PUSH_BUTTON and control.name in {'Quit', 'Keep working'}
                        and self.ui.visible(control) and control.getState().contains(self.ui.api.STATE_FOCUSED)):
                    matches.append({'identity': self._identity(control), 'kind': 'quit-button' if control.name == 'Quit' else 'keep-working-button',
                                    'parent': row['identity'], 'focused': True})
        if len(matches) > 1:
            raise CaseUnavailable('native-quit-control-focus-is-ambiguous')
        return matches[0] if matches else None

    def observe(self):
        self._runtime()
        for _, node in self.ui.windows():
            self._capture(node)
        if len(self.nodes) > 16:
            raise ValueError('captured app surface inventory exceeds its bound')
        surfaces = []
        for marker, (node, metadata) in sorted(self.nodes.items()):
            if marker != (node.app.bus_name, node.path):
                raise CaseFailed('retained accessible object address changed')
            state = node.getState()
            defunct = bool(state.contains(self.ui.api.STATE_DEFUNCT))
            if not defunct:
                self._capture(node)
            # A defunct retained object keeps its previously verified identity and
            # parent; it cannot authorize input or stand in for a replacement frame.
            surfaces.append({**metadata, 'visible': not defunct and bool(self.ui.visible(node)),
                'active': not defunct and bool(state.contains(self.ui.api.STATE_ACTIVE)),
                'defunct': defunct, 'modal': not defunct and bool(state.contains(self.ui.api.STATE_MODAL))})
        self._runtime()
        return wire.proof({'launch': self.launch, 'engine': self.engine, 'main': self.main,
                           'surfaces': surfaces, 'control': self._quit_control(surfaces)})

    def prepare(self, operation):
        return wire.before(operation, self.observe())

    def _exited(self):
        result = {}
        for name, descriptor in self.pins.items():
            poll = select.poll()
            poll.register(descriptor, select.POLLIN)
            result[name] = bool(poll.poll(0))
        return result

    def during(self, operation, before):
        if operation == 'workspace-approve-quit-v1':
            self._exited()
        else:
            wire.same_runtime(before, self.observe())

    def settle(self, operation, before):
        deadline = time.monotonic() + 15
        after = None
        while True:
            if operation == 'workspace-approve-quit-v1':
                code, exited = self.app.process.poll(), self._exited()
                if all(exited.values()) and len(exited) == 3 and code is not None:
                    after = {'launch': self.launch, 'engine': self.engine, 'main': self.main,
                             'exited': exited, 'exit_code': code}
            else:
                after = self.observe()
            if after is not None and wire.finished(operation, before, after):
                return after
            if time.monotonic() >= deadline:
                error = CaseUnavailable('owned-frame-transition-did-not-reach-its-declared-state')
                error.observations = {'surface_before': before, 'surface_after': after}
                raise error
            time.sleep(0.05)
