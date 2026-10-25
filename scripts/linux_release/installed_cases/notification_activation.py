"""Click a real background-work notification and observe the expected chat in the original workspace."""
from pathlib import Path
import tempfile
import time

from .app import InstalledApp
from .desktop_ui import DesktopUI, interactive_window
from .desktop_launch import protected_launcher
from .desktop_turn import submit_project_turn
from .notification_bus import NotificationBus
from .notification_observer import NotificationObserver, selected_chat
from .process import run
from .profile_setup import await_session, require_active
from .provider import MODEL, ProviderFixture
from .provider_barrier import ResponseBarrier
from .provider_recovery import load as load_provider_recovery
from .provider_setup import configure_provider
from .records import CaseFailed, CaseUnavailable, retain
from .workspace_activation import reopen
from ..installed_input.notification_guest import perform
from ..installed_input.surface import FrameObserver
from ..installed_input.surface_guest import perform_transition


def open_project(app, binding, project, provider):
    opened = run(binding, 'pw', ['open', str(project)], timeout=30)
    if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
        raise CaseUnavailable('normal-CLI-did-not-open-the-notification-fixture-project')
    return await_session(app, project, provider, MODEL, status='idle',
        reason='notification-fixture-project-session-was-not-opened')


def execute(binding, capability, case, root):
    if capability != 'notifications' or case != 'activation-token-and-chat':
        raise ValueError('notification activation driver requires its exact declared case')
    project = Path(tempfile.mkdtemp(prefix='notification-target-', dir=root))
    decoy = Path(tempfile.mkdtemp(prefix='notification-decoy-', dir=root))
    app, bus, barrier, fixture, observations, streams = None, None, None, None, {}, {}
    try:
        native = binding.get('native_input')
        if not isinstance(native, dict):
            raise CaseUnavailable('notification-activation-requires-bound-physical-input')
        recovery = load_provider_recovery(binding)
        if recovery is not None:
            observations['provider_recovery'], streams['provider-registration'] = recovery
        barrier = ResponseBarrier()
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        with ProviderFixture.gui(calls, acquire_command=True, response_barrier=barrier,
                endpoint=recovery[0]['endpoint'] if recovery is not None else None) as fixture:
            app = InstalledApp({**binding, 'launcher': protected_launcher(binding)})
            with app:
                observations['launch'] = app.observations
                observations['interactive'] = interactive_window(app)
                provider = configure_provider(app, fixture)['provider_id']
                target = open_project(app, binding, project, provider)
                observations['target_session'] = target
                submit_project_turn(app)
                await_session(app, project, provider, MODEL, reason='notification-fixture-turn-did-not-start')
                held = observations['provider_held'] = barrier.await_held()
                observations['active_before'] = require_active(app, project, provider, MODEL)
                observations['decoy_session'] = open_project(app, binding, decoy, provider)
                before = observations['selected_before'] = selected_chat(app)
                if before is None or before['project_id'] == target['project_id'] or before['session_id'] == target['id']:
                    raise CaseUnavailable('notification-fixture-did-not-select-a-different-chat-before-delivery')
                with DesktopUI(app) as ui, FrameObserver(app, ui) as frames:
                    transitions = observations['transitions'] = []
                    transitions.append(perform_transition(native['binding'], native['vm_id'], frames, 'workspace-open-peer-v1'))
                    observations['return_to_main_before_hide'] = reopen(app, frames)
                    transitions.append(perform_transition(native['binding'], native['vm_id'], frames, 'workspace-hide-main-v1'))
                    require_active(app, project, provider, MODEL, observations['active_before'])
                    barrier.require_held()
                    with NotificationBus(app, frames.main['process']) as bus:
                        # The product's settled turn clock requires four seconds
                        # before a finished-work notification is meaningful.
                        remaining = 4.1 - (time.monotonic() - held['received_at'])
                        if remaining > 0:
                            time.sleep(remaining)
                        barrier.release()
                        observations['provider_response_written'] = barrier.await_response_written()
                        observations['notification_delivered'] = bus.await_delivery(time.monotonic() + 20)
                        observer = NotificationObserver(app, frames, bus, target['id'], target['project_id'], project)
                        observations['notification_click'] = perform(native['binding'], native['vm_id'], observer)
                        observations['notification_result'] = bus.snapshot()
                        after = observations['selected_after'] = selected_chat(app)
                        if (after is None or after['project_id'] != target['project_id']
                                or after['session_id'] != target['id'] or after['workspace_path'] != str(project)):
                            raise CaseFailed('real notification activation selected a different project or chat')
                    observations['provider_requests'] = list(fixture.observations)
            streams.update(app.streams())
    except Exception as error:
        if fixture is not None:
            observations['provider_requests'] = list(fixture.observations)
        if barrier is not None:
            observations['provider_response_control'] = barrier.snapshot()
        if bus is not None:
            observations['notification_result'] = bus.snapshot()
        if app is not None:
            observations['launch'] = app.observations
            streams.update(app.streams())
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed('real notification activation failed')
        failure.observations = {**observations, **getattr(error, 'observations', {}),
            'failure_type': type(error).__name__, 'failure_detail': str(error)[:512]}
        failure.streams = streams
        raise failure from None
    return retain(binding, capability, case, root, 'passed', observations, streams)
