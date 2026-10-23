"""Observe normal launcher activation and active-work Quit on the same protected desktop."""
from pathlib import Path
import tempfile
import time

from .app import InstalledApp
from .desktop_ui import DesktopUI, interactive_window
from .desktop_launch import activate, protected_launcher
from .process import run
from .profile_observer import project_for_owned_root, sessions_for_project
from .provider import MODEL, ProviderFixture
from .provider_barrier import ResponseBarrier
from .provider_setup import configure_provider, physical_input
from .records import CaseFailed, CaseUnavailable, retain
from ..installed_input.surface import FrameObserver
from ..installed_input.surface_guest import perform_transition
from ..installed_input import surface_wire


def active_session(app, project, provider):
    witness = project_for_owned_root(app, project)
    if witness is None:
        return None
    rows = [row for row in sessions_for_project(app, witness['project_id'])
            if row['provider_id'] == provider and row['model'] == MODEL]
    if len(rows) > 1:
        raise ValueError('workspace fixture has ambiguous durable session identity')
    if rows and rows[0]['status'] == 'error':
        raise ValueError('workspace fixture work failed before Quit')
    return {**witness, **rows[0]} if rows and rows[0]['status'] == 'busy' else None


def require_active(app, project, provider, expected=None):
    current = active_session(app, project, provider)
    if current is None:
        raise CaseUnavailable('normal-workspace-quit-has-no-observed-active-session')
    if expected is not None and current != expected:
        raise ValueError('workspace interaction replaced its active durable session')
    return current


def reopen(app, observer):
    before = observer.observe()
    launcher = activate(app)
    deadline = time.monotonic() + 15
    while True:
        after = observer.observe()
        if surface_wire.reopened(before, after):
            return {'public_launcher': launcher, 'before': before, 'after': after}
        if time.monotonic() >= deadline:
            raise CaseUnavailable('public-activation-did-not-reopen-the-original-workspace')
        time.sleep(0.05)


def execute(binding, capability, case, root):
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    project = Path(tempfile.mkdtemp(prefix='workspace-project-', dir=root))
    app, observations, streams, barrier = None, {}, {}, None
    try:
        # The normal GUI sends a real request to the fixed local provider.
        # Transport latency is controlled independently; product activity still
        # requires the captured engine's typed busy session facts below.
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        barrier = ResponseBarrier()
        with ProviderFixture.gui(calls, response_barrier=barrier) as fixture:
            app = InstalledApp(launch_binding)
            with app:
                observations['launch'] = app.observations
                observations['interactive'] = interactive_window(app)
                provider = configure_provider(app, fixture)
                opened = run(binding, 'pw', ['open', str(project)], timeout=30)
                if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                    raise ValueError('installed CLI refused the owned workspace project')
                with DesktopUI(app) as ui:
                    physical_input(app, ui, 'Describe your idea', 'reply-fixture-prompt-v1')
                    ui.click('Start')
                    deadline = time.monotonic() + 30
                    while active_session(app, project, provider['provider_id']) is None:
                        if time.monotonic() >= deadline:
                            raise CaseUnavailable('normal-workspace-fixture-did-not-start')
                        time.sleep(0.05)
                    observations['provider_held_before'] = barrier.await_held()
                    active = require_active(app, project, provider['provider_id'])
                    if not any(row['completed'] is False for row in fixture.observations):
                        raise CaseUnavailable('workspace-active-turn-has-no-fixed-provider-request')
                    observations['active_before'] = active
                    native = binding.get('native_input')
                    if not isinstance(native, dict):
                        raise CaseUnavailable('workspace-transitions-require-bound-physical-input')
                    records = observations['transitions'] = []
                    with FrameObserver(app, ui) as observer:
                        def transition(operation):
                            records.append(perform_transition(native['binding'], native['vm_id'], observer, operation))
                        transition('workspace-open-peer-v1')
                        observations['activate_from_peer'] = reopen(app, observer)
                        transition('workspace-hide-main-v1')
                        observations['reopen_hidden'] = reopen(app, observer)
                        observations['repeated_activation'] = reopen(app, observer)
                        require_active(app, project, provider['provider_id'], active)
                        barrier.require_held()
                        transition('workspace-request-quit-v1')
                        transition('workspace-cancel-quit-v1')
                        observations['active_after_cancel'] = require_active(app, project, provider['provider_id'], active)
                        observations['provider_held_after_cancel'] = barrier.require_held()
                        transition('workspace-request-quit-v1')
                        transition('workspace-focus-quit-v1')
                        require_active(app, project, provider['provider_id'], active)
                        barrier.require_held()
                        transition('workspace-approve-quit-v1')
                        observations['provider_cancelled_by_quit'] = barrier.await_peer_cancellation()
                observations['provider_requests'] = list(fixture.observations)
            streams = app.streams()
    except (OSError, ValueError, TypeError) as error:
        if barrier is not None:
            observations['provider_response_control'] = barrier.snapshot()
        if app is not None:
            observations['launch'] = app.observations
            streams = app.streams()
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed('normal workspace activation or Quit failed')
        failure.observations = {**observations, **getattr(error, 'observations', {}),
            'failure_type': type(error).__name__, 'failure_detail': str(error)[:512]}
        failure.streams = streams
        raise failure from None
    return retain(binding, capability, case, root, 'passed', observations, streams)
