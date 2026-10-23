"""A physical mock turn must commit its reply and preserve it across installed restart."""
from __future__ import annotations

from pathlib import Path
import tempfile
import time

from .app import InstalledApp
from .desktop_ui import interactive_window
from .process import run
from .profile_observer import project_for_owned_root, sessions_for_project
from .provider import MODEL, ProviderFixture
from .records import CaseFailed, CaseUnavailable, retain
from .desktop_launch import protected_launcher
from .provider_setup import configure_provider
from .desktop_turn import submit_project_turn
from .reply_observer import committed_reply, committed_fixed_tool
from .provider_recovery import load as load_provider_recovery

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
                    acquired = committed_fixed_tool(app, session['id'], 'request_tools')
                    command = committed_fixed_tool(app, session['id'], 'command')
                    if acquired is None or command is None:
                        raise ValueError('fixed acquisition and command fixture did not complete successfully')
                    return {**result, 'tool_message_id': command['message_id'],
                            'schema_acquisition': acquired, 'command_invocation': command}
        time.sleep(0.1)
    raise CaseUnavailable('protected-mock-reply-or-restart-witness-timeout')


def execute(binding, capability, case, root):
    launcher = protected_launcher(binding)
    launch_binding = {**binding, 'launcher': launcher}
    observations, streams = {}, {}
    # This case changes only its disposable guest profile via normal UI actions.
    # Preserve the fixture project so a failed restart remains diagnosable.
    project = Path(tempfile.mkdtemp(prefix='reply-project-', dir=root))
    app = restarted = fixture = None
    try:
        recovery = load_provider_recovery(binding)
        if recovery is not None:
            observations['provider_recovery'], streams['provider-registration'] = recovery
        with ProviderFixture.gui([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}],
                                 endpoint=recovery[0]['endpoint'] if recovery is not None else None,
                                 acquire_command=True) as fixture:
            app = InstalledApp(launch_binding)
            with app:
                observations['launch_before'] = app.observations
                observations['interactive'] = interactive_window(app)
                provider = configure_provider(app, fixture)
                opened = run(binding, 'pw', ['open', str(project)], timeout=30)
                if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                    raise ValueError('installed CLI refused the fixed reply project')
                submit_project_turn(app)
                before = await_reply(app, project, provider['provider_id'])
                if (fixture.acquisition.witness is None or fixture.acquisition.witness['invocation_id']
                        != before['schema_acquisition']['invocation_id']):
                    raise ValueError('provider acquisition receipt differs from its durable invocation')
                if not any(row['completed'] for row in fixture.observations):
                    raise ValueError('fixture never completed the full engine turn')
                observations.update(reply_before=before, launch_before=app.observations)
            streams.update({'before-' + key: value for key, value in app.streams().items()})
            restarted = InstalledApp(launch_binding)
            with restarted:
                after = await_reply(restarted, project, provider['provider_id'], selected_session=before['session_id'])
                if after != before or restarted.observations['engine_identity'] == app.observations['engine_identity']:
                    raise ValueError('restart changed the fixed reply or reused the previous engine')
                observations.update(reply_after=after, launch_after=restarted.observations,
                                    provider_requests=list(fixture.observations), durable_reply=True,
                                    provider_discovery=list(fixture.discovery_observations),
                                    provider_transport={'requests': list(fixture.transport_observations),
                                                        'clipped': fixture.transport_clipped})
            streams.update({'after-' + key: value for key, value in restarted.streams().items()})
    except (OSError, ValueError, TypeError) as error:
        if fixture is not None:
            observations['provider_discovery'] = list(fixture.discovery_observations)
            observations['provider_transport'] = {'requests': list(fixture.transport_observations),
                                                  'clipped': fixture.transport_clipped}
            observations['provider_requests'] = list(fixture.observations)
        for label, instance in (('before', app), ('after', restarted)):
            if instance is not None:
                observations['launch_' + label] = instance.observations
                streams.update({label + '-' + key: value for key, value in instance.streams().items()})
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed('protected reply or restart did not complete')
        failure.observations = {**observations, **getattr(error, 'observations', {}),
                                'failure_type': type(error).__name__, 'failure_detail': str(error)[:512]}
        failure.streams = streams
        raise failure from None
    return retain(binding, capability, case, root, 'passed', observations, streams)
