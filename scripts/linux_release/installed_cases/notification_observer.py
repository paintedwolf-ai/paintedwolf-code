"""Bind actual notification activation to the original frame and the renderer's selected chat."""
import hashlib
from pathlib import Path
import time

from .profile_observer import identifier, physical, profile
from .profile_setup import slice_document, LAST_ACTIVE_PROJECT, LAST_SESSION_SNAPSHOT
from .records import CaseFailed, CaseUnavailable
from ..installed_input import notification_shell, notification_wire


def selected_chat(app):
    with profile(app) as directory:
        slices = directory / 'app-state-v1'
        if not (slices.exists() or slices.is_symlink()):
            return None
        physical(slices, True)
        active, active_digest = slice_document(slices, LAST_ACTIVE_PROJECT, 'lastActiveProjectId')
        snapshot, snapshot_digest = slice_document(slices, LAST_SESSION_SNAPSHOT, 'lastSessionSnapshot', limit=262144)
        _, active_after = slice_document(slices, LAST_ACTIVE_PROJECT, 'lastActiveProjectId')
        _, snapshot_after = slice_document(slices, LAST_SESSION_SNAPSHOT, 'lastSessionSnapshot', limit=262144)
        if active_digest != active_after or snapshot_digest != snapshot_after:
            return None
    if snapshot is None or active is None:
        return None
    if not isinstance(snapshot, dict) or not isinstance(snapshot.get('scope'), dict):
        raise ValueError('notification renderer selection has an invalid declared shape')
    scope, session = snapshot['scope'], snapshot.get('session')
    if set(scope) != {'projectId', 'sessionId'} or not isinstance(session, dict):
        raise ValueError('notification renderer selection has an invalid chat scope')
    project_id, session_id = identifier(scope['projectId']), identifier(scope['sessionId'])
    if active != project_id or session.get('id') != session_id or session.get('project_id') != project_id:
        return None
    root = session.get('workspace_path')
    if not isinstance(root, str) or not Path(root).is_absolute():
        raise ValueError('notification renderer selection has no declared project root')
    return {'observer': 'owned-app-state-JSON', 'project_id': project_id, 'session_id': session_id,
        'workspace_path': root, 'last_active_project_sha256': active_digest,
        'last_session_snapshot_sha256': snapshot_digest,
        'renderer_revision_sha256': hashlib.sha256((active_digest + snapshot_digest).encode()).hexdigest()}


class NotificationObserver:
    def __init__(self, app, frames, bus, session_id, project_id, project):
        self.app, self.frames, self.bus = app, frames, bus
        self.target, self.last_selected = None, None
        self.session_id, self.project_id, self.project = identifier(session_id), identifier(project_id), str(project)
        bus.require_owners()
        if bus.trace is None or bus.trace.delivery is None:
            raise CaseUnavailable('notification-click-requires-an-actual-delivery')
        self.service = {'bus_name': bus.owner, 'process': dict(bus.service_process)}
        shell = bus._call('GetNameOwner', '(s)', ('org.gnome.Shell',))
        self.shell = {'bus_name': shell, 'process': bus._pid(shell)}
        self.delivery = {'notification_id': bus.trace.delivery['notification_id'],
            'request_serial': bus.trace.call['serial'], 'sender': bus.trace.sender,
            'session_id': self.session_id, 'title_sha256': bus.trace.call['title_sha256'],
            'body_sha256': bus.trace.call['body_sha256']}

    def require_runtime(self):
        self.app.require_engine()
        self.bus.require_owners()
        if (self.bus._call('GetNameOwner', '(s)', ('org.gnome.Shell',)) != self.shell['bus_name']
                or self.bus._pid(self.shell['bus_name']) != self.shell['process']):
            raise CaseFailed('notification shell changed during physical activation')

    def observe(self, deadline=None):
        deadline = time.monotonic() + 15 if deadline is None else deadline
        self.require_runtime()
        result = notification_shell.observe(self.frames.observe(deadline), self.service, self.shell,
            self.delivery, deadline=deadline, expected=self.target)
        self.target = result['target']
        return result

    def arm(self):
        self.require_runtime()
        self.bus.arm()

    def after(self, deadline):
        self.require_runtime()
        self.bus.drain()
        selected = self.last_selected = selected_chat(self.app)
        actual = (selected['session_id'] if selected is not None and selected['project_id'] == self.project_id
            and selected['workspace_path'] == self.project else None)
        return {'surface': self.frames.observe(deadline), 'service': self.service, 'shell': self.shell,
            'delivery': self.delivery, 'activation': self.bus.trace.activation, 'selected_session_id': actual}

    def settle(self, previous, deadline):
        after = None
        while time.monotonic() < deadline:
            after = self.after(deadline)
            if notification_wire.finished(previous, after):
                return after
            time.sleep(0.02)
        confirmed_action = after is not None and after['activation'] is not None
        failure = CaseFailed if confirmed_action and self.last_selected is not None else CaseUnavailable
        error = failure('notification-activation-did-not-select-the-original-frame-and-expected-chat')
        error.observations = {'notification_before': previous, 'notification_after': after,
            'notification_selected_scope': self.last_selected}
        raise error
