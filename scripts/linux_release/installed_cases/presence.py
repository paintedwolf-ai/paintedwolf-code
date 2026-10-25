"""Collect the normal owned reveal prerequisite without treating alert prose as authority."""
from __future__ import annotations

from pathlib import Path
import tempfile
import time

from .app import InstalledApp, session_environment
from .desktop_launch import protected_launcher, activate_visible_frame
from .desktop_ui import DesktopUI
from .fresh_profile import Reservation
from .polkit_policy import PresencePolicy
from .presence_alert import await_alert
from .process import run
from .profile_setup import await_session
from .provider import MODEL, ProviderFixture
from .provider_setup import configure_provider
from .presence_setup import prepare_secret
from .presence_confirmation import await_confirmation, confirmation_rows
from .records import CaseFailed, CaseUnavailable
from ..installed_environment import graphical_session
from ..installed_storage import resolve


def unchallenged_presence(binding):
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    storage = resolve(session_environment(graphical_session()))
    reservation = Reservation(launch_binding, storage)
    observations = {'startup_phase': 'launch-and-wait-ready-without-onboarding',
                    'typed_refusal_verified': False, 'system_authentication_verified': False,
                    'cancel_verified': False, 'expiry_verified': False}
    streams = {}
    app, fixture, policy = InstalledApp(launch_binding), None, None
    try:
        with reservation:
            reservation.check(empty=True)
            app.launch()
            app.wait_ready()
            reservation.check_launch(app)
            with DesktopUI(app) as ui:
                observations['initial_activation'] = activate_visible_frame(app, ui)
                reservation.check_launch(app)
            with ProviderFixture.gui([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]) as fixture:
                provider = configure_provider(app, fixture)
                reservation.check_launch(app)
                # The project is disposable fixture state, retained on all outcomes.
                project = Path(tempfile.mkdtemp(prefix='presence-project-', dir=storage.home))
                opened = run(launch_binding, 'pw', ['open', str(project)], timeout=30)
                observations['project_open'] = {
                    key: opened[key]
                    for key in ('launch_identity', 'exit_code', 'timed_out', 'output_limit')
                    if key in opened
                }
                for key, stream_key in (('stdout', 'project-open-stdout'), ('stderr', 'project-open-stderr')):
                    if isinstance(opened.get(key), bytes):
                        streams[stream_key] = opened[key]
                if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                    raise CaseUnavailable('normal installed CLI refused the owned presence project')
                observations['opened_session'] = await_session(app, project, provider['provider_id'], MODEL, status='idle',
                                                               reason='presence-project-session-not-opened')
                with DesktopUI(app) as ui:
                    observations['secret_setup'] = prepare_secret(app, ui, reservation, project, observations['opened_session'])
                    policy = PresencePolicy(launch_binding)
                    with policy:
                        reservation.check_launch(app)
                        before = len(confirmation_rows(app))
                        ui.click('Reveal value')
                        observations['reveal_alert'] = await_alert(app, ui)
                        outcome = await_confirmation(app, after_count=before)
                        observations['native_confirmation'] = outcome
                        if outcome['status'] != 'refused' or outcome['code'] != 'presence_unavailable':
                            raise CaseFailed('unchallenged-polkit-did-not-refuse-native-presence')
                        observations['typed_refusal_verified'] = True
                        reservation.check_launch(app)
                if isinstance(binding.get('native_input'), dict):
                    from .presence_system import run_reveal
                    project_id = observations['opened_session']['project_id']
                    with DesktopUI(app) as ui:
                        cancelled = run_reveal(app, ui, project_id, binding['native_input'], observations, cancel=True)
                        observations['cancel_verified'] = cancelled['cancel_verified']
                        authenticated = run_reveal(app, ui, project_id, binding['native_input'], observations, cancel=False)
                        observations['system_authentication_verified'] = authenticated['system_authentication_verified']
            observations['reason'] = ('presence-refusal-system-authentication-and-cancel-observed-host-expiry-unverified'
                if observations['system_authentication_verified'] and observations['cancel_verified'] else
                'typed-unchallenged-presence-refusal-observed-system-authentication-cancel-and-expiry-unverified')
    except (OSError, ValueError, TypeError, KeyError) as error:
        observations.update(getattr(error, 'observations', {}))
        observations['failure_type'] = type(error).__name__
        observations['failure_detail'] = str(error)[:512]
        observations.setdefault('reason', str(error))
        failure = CaseUnavailable('normal-presence-prerequisite-unverified') if isinstance(error, CaseUnavailable) else CaseFailed('normal-presence-prerequisite-failed')
        failure.observations = observations
        # The finally block adds partial launch, policy, and storage facts before propagation.
        failure.streams = streams
        raise failure from None
    finally:
        app.stop()
        streams.update(app.streams())
        observations['launch'] = app.observations
        observations['fresh_storage'] = reservation.facts()
        if fixture is not None:
            observations['provider_discovery'] = list(fixture.discovery_observations)
        if policy is not None:
            observations['presence_policy'] = policy.observations
    return 'unverified', observations, streams
