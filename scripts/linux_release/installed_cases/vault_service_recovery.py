"""One actual service absence/repaired reopen using the shared ordinary vault case owners."""
from pathlib import Path
import tempfile

from .vault_quiet import (
    InstalledApp, Reservation, ProviderFixture, ResponseBarrier, protected_launcher,
    graphical_session, session_environment, resolve, interactive_window,
    configure_provider, authenticated_turn, stopped, setup_state, await_fixture_reply, MODEL)
from .records import CaseFailed, CaseUnavailable, retain
from ..installed_input.recovery_phase import GuestPhases, ABSENT, RESTORE
from .startup_outcome import await_absence

CASE = ('vault', 'service-unavailable-retry')


def execute(binding, capability, case, root):
    if (capability, case) != CASE:
        raise ValueError('recovery executes only the closed custodian absence case')
    selected = binding.get('case_inputs', {}).get('security', {})
    if (not isinstance(selected, dict) or selected not in ({}, {'format_version': 1})
            or any(type(value) is not int for value in selected.values())):
        raise ValueError('recovery permits only fixed native case inputs')
    native = binding.get('native_input')
    if not isinstance(native, dict): raise CaseUnavailable('recovery-requires-its-owned-execution')
    launch = {**binding, 'launcher': protected_launcher(binding)}
    storage = resolve(session_environment(graphical_session()))
    observations, apps, streams = {'phase': 'before-launch'}, [], {}
    reservation = Reservation(launch, storage)
    phases = fixture = None
    try:
        with reservation, ProviderFixture.gui(
                [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}],
                acquire='command', response_barrier=ResponseBarrier()) as fixture:
            phases = GuestPhases(native['binding'], native['vm_id'], reservation.deadline)
            observations['root_phases'] = phases.records
            projects = [Path(tempfile.mkdtemp(prefix='custodian-recovery-', dir=storage.home)) for _ in range(2)]
            first = InstalledApp(launch)
            apps.append(first)
            observations['launch_before'] = first.observations
            with first:
                reservation.check_launch(first)
                if first.observations.get('onboarding') or first.observations.get('fixed_input'):
                    raise ValueError('recovery baseline required startup input')
                observations['interactive_before'] = interactive_window(first)
                provider = configure_provider(first, fixture)
                observations['provider'] = provider
                observations['phase'] = 'before-turn'
                before = authenticated_turn(first, fixture, fixture.response_barrier, projects[0], provider)
                observations['turn_before'] = before
            stopped(first)
            observations['phase'] = 'custodian-absence'
            phases.phase(ABSENT)
            failed = InstalledApp(launch)
            apps.append(failed)
            observations['failed_launch'] = failed.observations
            try:
                failed.launch()
                observations['startup_absence'] = await_absence(failed, reservation)
            finally:
                failed.stop()
                stopped(failed)
            observations['phase'] = 'restore-custodian'
            phases.phase(RESTORE)
            second = InstalledApp(launch)
            apps.append(second)
            observations['launch_after'] = second.observations
            with second:
                reservation.check_launch(second)
                if second.observations['engine_identity'] == first.observations['engine_identity']:
                    raise ValueError('repaired reopen reused the previous engine')
                configured = setup_state(second)
                if configured['onboarding_completed'] is not True or configured['coordinator'] != {'provider_id': provider['provider_id'], 'model': MODEL}:
                    raise ValueError('repaired reopen lost the selected provider')
                restored = configure_provider(second, fixture)
                if restored['provider_id'] != provider['provider_id'] or second.observations.get('fixed_input'):
                    raise ValueError('repaired reopen required new credential input')
                prior = await_fixture_reply(second, projects[0], provider['provider_id'], fixture.acquisition,
                                             selected_session=before['reply']['session_id'])
                if prior != before['reply']: raise ValueError('repaired reopen changed the committed reply')
                observations['reply_reopened'] = prior
                barrier = fixture.begin_next_turn()
                observations['phase'] = 'after-turn'
                after = authenticated_turn(second, fixture, barrier, projects[1], provider)
                if (after['request_start'] < before['request_end'] or any(after['reply'][key] == before['reply'][key]
                        for key in ('project_id', 'session_id', 'turn_id', 'attempt_id'))
                        or after['reply']['schema_acquisition']['invocation_id'] == before['reply']['schema_acquisition']['invocation_id']
                        or after['reply']['tool_invocation']['invocation_id'] == before['reply']['tool_invocation']['invocation_id']):
                    raise ValueError('repaired use reused the previous request')
                observations['turn_after'] = after
            stopped(second)
            observations.update(phase='complete', temporary_absence_repaired_use_verified=True,
                                live_exchange_failure_verified=False, content_equality_claimed=False)
    except Exception as error:
        observations.update(failure_type=type(error).__name__)
        observations.update(getattr(error, 'observations', {}))
        failure = error if isinstance(error, (CaseUnavailable, CaseFailed)) else CaseFailed('custodian recovery did not complete')
        failure.observations, failure.streams = observations, streams
        raise failure from None
    finally:
        cleanup = []
        for index, app in enumerate(apps):
            try:
                app.stop()
                stopped(app)
            except Exception as error: cleanup.append(type(error).__name__)
            streams.update({str(index) + '-' + name: raw for name, raw in app.streams().items()})
        observations['owned_cleanup'] = cleanup
        observations['fresh_storage'] = reservation.facts()
        if fixture is not None:
            observations['provider_transport'] = {'requests': list(fixture.transport_observations),
                                                  'clipped': fixture.transport_clipped}
        # The root seat owns unconditional restoration even if no phase ack survived.
        if cleanup:
            failure = CaseFailed('owned recovery launch cleanup remains unverified')
            failure.observations, failure.streams = observations, streams
            raise failure
    return retain(binding, capability, case, root, 'passed', observations, streams)
