"""Collect the normal owned reveal prerequisite without treating alert prose as authority."""
from __future__ import annotations

from pathlib import Path
import tempfile
import time

from .app import InstalledApp, session_environment
from .desktop_launch import protected_launcher, activate_visible_frame
from .desktop_turn import open_project_configuration
from .desktop_ui import DesktopUI
from .fresh_profile import Reservation
from .polkit_policy import PresencePolicy
from .presence_alert import alerts, await_alert
from .process import run
from .profile_setup import await_session
from .provider import MODEL, ProviderFixture
from .provider_setup import configure_provider, physical_input
from .records import CaseFailed, CaseUnavailable
from ..installed_environment import graphical_session
from ..installed_input.presence_fixture import FIELDS
from ..installed_storage import resolve


def unchallenged_presence(binding):
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    storage = resolve(session_environment(graphical_session()))
    reservation = Reservation(launch_binding, storage)
    observations = {'startup_phase': 'launch-and-wait-ready-without-onboarding',
                    'typed_refusal_verified': False, 'password_fallback_verified': False}
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
                    observations['project_configuration_navigation'] = open_project_configuration(app, ui)
                    ui.click('Secrets', tab=True)
                    ui.click('Add secret')
                    for label, operation, _, password in FIELDS:
                        reservation.check_launch(app)
                        physical_input(app, ui, label, operation, password=password)
                    ui.click('Add secret')
                    reservation.check_launch(app)
                    baseline = alerts(app, ui, time.monotonic() + 15)
                    if baseline:
                        raise CaseUnavailable('presence-reveal-prerequisite-already-has-an-owned-alert')
                    policy = PresencePolicy(launch_binding)
                    with policy:
                        reservation.check_launch(app)
                        ui.click('Reveal value')
                        observations['reveal_alert'] = await_alert(app, ui)
                        reservation.check_launch(app)
            observations['reason'] = 'owned-alert-attributes-captured-typed-refusal-and-password-fallback-unverified'
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
