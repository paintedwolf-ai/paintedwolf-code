"""Prove ordinary protected credential use before and after an owned restart."""
from __future__ import annotations

from pathlib import Path
import tempfile
import time

from .app import InstalledApp, graphical_session, session_environment
from .desktop_launch import protected_launcher
from .desktop_turn import submit_project_turn
from .desktop_ui import interactive_window
from .fixture_turn import await_fixture_reply
from .fresh_profile import Reservation
from .process import run
from .profile_setup import state as setup_state
from .provider import MODEL, ProviderFixture
from .provider_barrier import ResponseBarrier
from .provider_setup import configure_provider
from .records import CaseFailed, CaseUnavailable, retain
from ..installed_storage import resolve
from ..installed_process import process_identity


def authenticated_turn(app, fixture, barrier, project, provider, *, progress=None):
    progress = {} if progress is None else progress
    progress.update(location='project-open', request_start=len(fixture.transport_observations))
    try:
        opened = run(app.binding, 'pw', ['open', str(project)], timeout=30)
        progress['project_open'] = {key: opened[key] for key in ('exit_code', 'timed_out', 'output_limit')}
        if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
            raise ValueError('quiet custody fixture project was not opened')
        progress['location'] = 'submit'
        submit_project_turn(app)
        progress['location'] = 'response-held'
        held = barrier.await_held()
        progress['response_held'] = True
        progress['location'] = 'response-release'
        barrier.release()
        progress['location'] = 'response-written'
        written = barrier.await_response_written()
        progress['response_written'] = True
        progress['location'] = 'durable-reply'
        reply = await_fixture_reply(app, project, provider['provider_id'], fixture.acquisition)
        progress['reply'] = reply
        progress['location'] = 'authenticated-transport'
        requests = list(fixture.transport_observations[progress['request_start']:])
        all_completions = [row for row in requests if row.get('path') == '/v1/chat/completions']
        completions = [row for row in all_completions if row.get('completion_class') != 'standalone']
        if (fixture.transport_clipped or not completions
                or any(row.get('status') != 200 or row.get('fixture_credential_observed') is not True
                       for row in all_completions)
                or written['request_id'] != held['request_id']
                or not fixture.acquisition.completed):
            raise ValueError('fresh turn did not prove successful authenticated fixture transport')
        progress['location'] = 'complete'
        return {'reply': reply, 'request_start': progress['request_start'],
                'request_end': len(fixture.transport_observations),
                'authenticated_completions': completions, 'response': written}
    finally:
        try:
            row = barrier.snapshot()
            progress['response_state'] = None if row is None else {key: row[key]
                for key in ('state', 'connection_closed', 'response_written')}
            acquisition = fixture.acquisition
            with acquisition.lock:
                progress['acquisition'] = {key: getattr(acquisition, key)
                    for key in ('issued', 'tool_issued', 'completed')}
                for key in ('witness', 'tool_witness'):
                    witness = getattr(acquisition, key)
                    progress['acquisition'][key] = None if witness is None else {
                        field: witness[field] for field in ('tool_call_id', 'invocation_id')}
        except Exception as error:
            progress['observation_error'] = type(error).__name__


def stopped(app):
    row = app.observations
    if row.get('exit_code') is None or row.get('capture_complete') is not True or row.get('output_limit') is not False:
        raise ValueError('quiet custody owned launch did not stop and settle its capture')
    engine = row.get('engine_identity')
    if engine is not None:
        try:
            actual = process_identity(engine['pid'])
        except (FileNotFoundError, ProcessLookupError):
            actual = None
        if actual is not None and (actual['pid'], actual['start_time_ticks']) == (
                engine['pid'], engine['start_time_ticks']):
            raise ValueError('quiet custody stopped launch left its captured engine running')


def execute(binding, capability, case, root):
    if (capability, case) != ('vault', 'service-onboarding'):
        raise ValueError('quiet custody executes only vault/service-onboarding')
    selected = binding.get('case_inputs', {}).get('security', {})
    if (not isinstance(selected, dict) or selected not in ({}, {'format_version': 1})
            or any(type(value) is not int for value in selected.values())):
        raise ValueError('quiet custody permits only fixed native case inputs')
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    selected_session = graphical_session()
    storage = resolve(session_environment(selected_session.session))
    observations, streams, apps = {**selected_session.observations, 'phase': 'before-launch'}, {}, []
    reservation, fixture = Reservation(launch_binding, storage), None
    try:
        with reservation, ProviderFixture.gui(
                [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}],
                acquire='command', response_barrier=ResponseBarrier()) as fixture:
            projects = [Path(tempfile.mkdtemp(prefix='quiet-custody-', dir=storage.home)) for _ in range(2)]
            first = InstalledApp(launch_binding)
            apps.append(first)
            observations['launch_before'] = first.observations
            started = time.monotonic()
            with first:
                reservation.check_launch(first)
                if first.observations.get('onboarding') or first.observations.get('fixed_input'):
                    raise ValueError('quiet custody startup required an interaction')
                observations['startup_before'] = {'elapsed_seconds': time.monotonic() - started,
                    'startup_input_performed': False, 'health': first.observations['health']}
                observations['interactive_before'] = interactive_window(first)
                provider = configure_provider(first, fixture)
                observations['provider'] = provider
                observations['phase'] = 'before-turn'
                before = authenticated_turn(first, fixture, fixture.response_barrier, projects[0], provider,
                    progress=observations.setdefault('turn_before_progress', {}))
                observations['turn_before'] = before
            stopped(first)
            observations['phase'] = 'restart'
            second = InstalledApp(launch_binding)
            apps.append(second)
            observations['launch_after'] = second.observations
            started = time.monotonic()
            with second:
                reservation.check_launch(second)
                if second.observations.get('onboarding') or second.observations.get('fixed_input'):
                    raise ValueError('quiet custody restart required a startup interaction')
                if second.observations['engine_identity'] == first.observations['engine_identity']:
                    raise ValueError('quiet custody restart reused the prior engine identity')
                observations['startup_after'] = {'elapsed_seconds': time.monotonic() - started,
                    'startup_input_performed': False, 'health': second.observations['health']}
                configured = setup_state(second)
                if configured['onboarding_completed'] is not True or configured['coordinator'] != {
                        'provider_id': provider['provider_id'], 'model': MODEL}:
                    raise ValueError('quiet custody restart lost the saved provider/model selection')
                restored = configure_provider(second, fixture)
                if restored['provider_id'] != provider['provider_id'] or second.observations.get('fixed_input'):
                    raise ValueError('quiet custody restart required new credential input')
                persisted = await_fixture_reply(second, projects[0], provider['provider_id'], fixture.acquisition,
                                                selected_session=before['reply']['session_id'])
                if persisted != before['reply']:
                    raise ValueError('quiet custody restart changed the committed first reply')
                observations['reply_reopened'] = persisted
                barrier = fixture.begin_next_turn()
                observations['phase'] = 'after-turn'
                after = authenticated_turn(second, fixture, barrier, projects[1], provider,
                    progress=observations.setdefault('turn_after_progress', {}))
                old, new = before['reply'], after['reply']
                if (after['request_start'] < before['request_end']
                        or any(old[key] == new[key] for key in ('project_id', 'session_id', 'turn_id', 'attempt_id'))
                        or old['schema_acquisition']['invocation_id'] == new['schema_acquisition']['invocation_id']
                        or old['tool_invocation']['invocation_id'] == new['tool_invocation']['invocation_id']):
                    raise ValueError('post-restart use reused a prior request or durable invocation')
                observations['turn_after'] = after
            stopped(second)
            observations.update(phase='complete', quiet_startup_without_input_verified=True,
                                fresh_authenticated_use_after_restart_verified=True)
    except Exception as error:
        observations['failure_type'] = type(error).__name__
        observations.update(getattr(error, 'observations', {}))
        failure = error if isinstance(error, (CaseUnavailable, CaseFailed)) else CaseFailed(
            'ordinary credential save, use and restart did not complete')
        failure.observations, failure.streams = observations, streams
        raise failure from None
    finally:
        cleanup = []
        for app in apps:
            try:
                app.stop()
                stopped(app)
            except Exception as error:
                cleanup.append(type(error).__name__)
        for label, app in zip(('before', 'after'), apps):
            streams.update({label + '-' + name: raw for name, raw in app.streams().items()})
        observations['fresh_storage'] = reservation.facts()
        if fixture is not None:
            observations['provider_transport'] = {'requests': list(fixture.transport_observations),
                'clipped': fixture.transport_clipped}
        if cleanup:
            observations['cleanup_failures'] = cleanup
            failure = CaseFailed('quiet custody owned process cleanup did not settle')
            failure.observations, failure.streams = observations, streams
            raise failure
    return retain(binding, capability, case, root, 'passed', observations, streams)
