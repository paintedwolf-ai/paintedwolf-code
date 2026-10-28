"""Observe normal launcher activation and active-work Quit on the same protected desktop."""
from pathlib import Path
import tempfile
import time

from .app import InstalledApp
from .desktop_ui import DesktopUI, interactive_window
from .desktop_launch import activate, protected_launcher
from .process import run
from .profile_setup import active_session, await_session, require_active
from .provider import MODEL, ProviderFixture
from .provider_barrier import ResponseBarrier
from .provider_setup import configure_provider
from .desktop_turn import submit_project_turn
from .provider_recovery import load as load_provider_recovery
from .records import CaseFailed, CaseUnavailable, retain
from ..installed_input.surface import FrameObserver
from ..installed_input.transition_guest import perform_transition
from ..installed_input import surface_wire


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
        recovery = load_provider_recovery(binding)
        if recovery is not None:
            observations['provider_recovery'], streams['provider-registration'] = recovery
        barrier = ResponseBarrier()
        with ProviderFixture.gui(calls, acquire='command', response_barrier=barrier,
                endpoint=recovery[0]['endpoint'] if recovery is not None else None) as fixture:
            app = InstalledApp(launch_binding)
            with app:
                observations['launch'] = app.observations
                observations['interactive'] = interactive_window(app)
                provider = configure_provider(app, fixture)
                opened = run(binding, 'pw', ['open', str(project)], timeout=30)
                if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                    raise ValueError('installed CLI refused the owned workspace project')
                observations['opened_session'] = await_session(
                    app, project, provider['provider_id'], MODEL,
                    status='idle', reason='workspace-activation-project-session-not-opened')
                submit_project_turn(app)
                with DesktopUI(app) as ui:
                    await_session(app, project, provider['provider_id'], MODEL,
                                  reason='normal-workspace-fixture-did-not-start')
                    observations['provider_held_before'] = barrier.await_held()
                    active = require_active(app, project, provider['provider_id'], MODEL)
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
                        require_active(app, project, provider['provider_id'], MODEL, active)
                        barrier.require_held()
                        transition('workspace-request-quit-v1')
                        transition('workspace-cancel-quit-v1')
                        observations['active_after_cancel'] = require_active(app, project, provider['provider_id'], MODEL, active)
                        observations['provider_held_after_cancel'] = barrier.require_held()
                        transition('workspace-request-quit-v1')
                        transition('workspace-focus-quit-v1')
                        require_active(app, project, provider['provider_id'], MODEL, active)
                        barrier.require_held()
                        transition('workspace-approve-quit-v1')
                        observations['provider_cancelled_by_quit'] = barrier.await_peer_cancellation()
                observations['provider_requests'] = list(fixture.observations)
            streams.update(app.streams())
    except Exception as error:
        if barrier is not None:
            observations['provider_response_control'] = barrier.snapshot()
        if app is not None:
            observations['launch'] = app.observations
            streams.update(app.streams())
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed('normal workspace activation or Quit failed')
        failure.observations = {**observations, **getattr(error, 'observations', {}),
            'failure_type': type(error).__name__, 'failure_detail': str(error)[:512]}
        failure.streams = streams
        raise failure from None
    return retain(binding, capability, case, root, 'passed', observations, streams)
