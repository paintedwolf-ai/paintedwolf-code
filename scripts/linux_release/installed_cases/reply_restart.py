"""A physical mock turn must commit its reply and preserve it across installed restart."""
from __future__ import annotations

from pathlib import Path
import tempfile
import time

from .app import InstalledApp
from .desktop_ui import DesktopUI, interactive_window
from .process import run
from .profile_observer import invocations, project_for_owned_root, sessions_for_project
from .profile_providers import find_gui_provider
from .provider import MODEL, ProviderFixture
from .records import CaseUnavailable, retain
from .reply_observer import committed_reply
from ..installed_runtime import SETUID_LAUNCHER, elf
from ..installed_input.focus import EntryFocus
from ..installed_input.guest import perform
from ..installed_input.protocol import endpoint_operation

def protected_launcher(binding):
    # Select only the setuid image bound to these final package bytes. Its actual
    # execution performs the production policy and namespace admission.
    root = Path(binding['install_root'])
    entry = binding['images'].get(str(root / 'bin/pw-launch-entry'))
    expected = binding['images'].get(str(SETUID_LAUNCHER))
    if entry is None or expected is None:
        raise CaseUnavailable('protected-reply-requires-sealed-bootstrap-and-entry')
    if entry['mode'] != 0o755:
        raise ValueError('protected reply launch entry has the wrong mode')
    actual = elf(SETUID_LAUNCHER, binding['artifact']['architecture'], setuid=True)
    if actual != expected:
        raise ValueError('protected reply bootstrap differs from selected final bytes')
    return {'path': str(SETUID_LAUNCHER), **actual}


def physical_input(app, ui, label, operation, *, password=False):
    native = app.binding.get('native_input')
    if not isinstance(native, dict):
        raise CaseUnavailable('protected-reply-requires-fixed-physical-input')
    return perform(native['binding'], native['vm_id'], EntryFocus(app, ui, label, password=password), operation=operation)


def configure_provider(app, fixture):
    with DesktopUI(app) as ui:
        ui.click('Add AI provider')
        ui.click('LiteLLM')
        physical_input(app, ui, 'Name (optional)', 'provider-name-v1')
        physical_input(app, ui, 'Endpoint URL', endpoint_operation(fixture.server.server_port))
        physical_input(app, ui, 'API key', 'provider-fixture-credential-v1', password=True)
        ui.click('Add AI provider')
        ui.click('Default model')
        ui.click(MODEL)
        # Optional setup can finish without a separate provider test request;
        # the required physical turn below proves the selected provider itself.
        ui.click('Skip')
    witness = find_gui_provider(app, fixture.base_url)
    if witness is None or witness['models'] != [MODEL]:
        raise ValueError('normal GUI did not register the selected fixed provider and model')
    return witness


def await_reply(app, project, provider_id, *, selected_session=None):
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        witness = project_for_owned_root(app, project)
        if witness:
            sessions = [row for row in sessions_for_project(app, witness['project_id'])
                        if row['provider_id'] == provider_id and row['model'] == MODEL]
            if selected_session is not None:
                sessions = [row for row in sessions if row['id'] == selected_session]
            if len(sessions) > 1:
                raise ValueError('fixed reply project has ambiguous session identity')
            if sessions:
                session = sessions[0]
                if session['status'] == 'error':
                    raise ValueError('protected mock turn failed before its durable reply')
                result = committed_reply(app, witness['project_id'], session['id'], provider_id)
                if result is not None:
                    calls = invocations(app, session['id'], ['command'])
                    matching = [row for row in calls if row['tool_call_id'] == 'qualification-0']
                    if len(matching) != 1 or matching[0]['outcome'] != 'completed':
                        raise ValueError('fixed command fixture did not complete successfully')
                    return {**result, 'tool_message_id': matching[0]['message_id']}
        time.sleep(0.1)
    raise CaseUnavailable('protected-mock-reply-or-restart-witness-timeout')


def execute(binding, capability, case, root):
    launcher = protected_launcher(binding)
    observations, streams = {}, {}
    # This case changes only its disposable guest profile via normal UI actions.
    # Preserve the fixture project so a failed restart remains diagnosable.
    project = Path(tempfile.mkdtemp(prefix='reply-project-', dir=root))
    with ProviderFixture.gui([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]) as fixture:
        with InstalledApp(binding, launcher=launcher) as app:
            observations['interactive'] = interactive_window(app)
            provider = configure_provider(app, fixture)
            opened = run(binding, 'pw', ['open', str(project)], timeout=30)
            if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                raise ValueError('installed CLI refused the fixed reply project')
            with DesktopUI(app) as ui:
                physical_input(app, ui, 'Describe your idea', 'reply-fixture-prompt-v1')
                ui.click('Start')
            before = await_reply(app, project, provider['provider_id'])
            if not any(row['completed'] for row in fixture.observations):
                raise ValueError('fixture never completed the full engine turn')
            observations.update(reply_before=before, launch_before=app.observations)
        streams.update({'before-' + key: value for key, value in app.streams().items()})
        with InstalledApp(binding, launcher=launcher) as restarted:
            after = await_reply(restarted, project, provider['provider_id'], selected_session=before['session_id'])
            if after != before or restarted.observations['engine_identity'] == app.observations['engine_identity']:
                raise ValueError('restart changed the fixed reply or reused the previous engine')
            observations.update(reply_after=after, launch_after=restarted.observations,
                                provider_requests=list(fixture.observations), durable_reply=True)
        streams.update({'after-' + key: value for key, value in restarted.streams().items()})
    return retain(binding, capability, case, root, 'passed', observations, streams)
