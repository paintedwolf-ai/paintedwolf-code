"""Bind native session lock-state observation and presence lock recovery to the installed runtime."""
from __future__ import annotations

import json
from pathlib import Path
import tempfile
import time

from .app import InstalledApp, graphical_session, session_environment
from .desktop_launch import activate_visible_frame, protected_launcher
from .desktop_turn import submit_project_turn
from .desktop_ui import DesktopUI, interactive_window
from .fixture_turn import await_fixture_reply
from .fresh_profile import Reservation
from .native_session import NativeSessionObserver
from .polkit_policy import PresencePolicy
from .presence_alert import alerts, await_alert
from .process import run
from .provider import ProviderFixture
from .provider_setup import configure_provider, physical_input
from .records import CaseFailed, CaseUnavailable, retain
from ..installed_input.presence_fixture import FIELDS
from ..installed_input.session_lock import NativeLockObserver
from ..installed_input.transition_guest import perform_transition
from ..installed_input.unlock_account import run_privileged_disable, validate_account_shape
from ..installed_input.unlock_control import NativeUnlockControl
from ..installed_storage import resolve

TIME_BUDGET_SECONDS = 300
STEP_AWAY_PREFIX = 'Step-away capability facts: '
FACT_FIELDS = {'session', 'session_error', 'screensaver_locked', 'locked_hint_seen',
               'system_connected', 'session_bus_connected', 'sleeping', 'idle', 'mode'}
SESSION_FIELDS = {'id', 'path', 'uid', 'session_type', 'active', 'can_lock', 'locked'}
IDLE_SOURCES = {'wayland_input_idle', 'mutter_idle_monitor', 'x11_screen_saver'}


def step_away_facts(app) -> list[dict]:
    raw = app.streams().get('app-stderr', b'').decode('utf-8', 'replace')
    facts = []
    for line in raw.splitlines():
        if not line.startswith(STEP_AWAY_PREFIX):
            continue
        fact = json.loads(line[len(STEP_AWAY_PREFIX):])
        if not isinstance(fact, dict) or set(fact) != FACT_FIELDS:
            raise ValueError('step-away capability facts record has an invalid shape')
        facts.append(fact)
    return facts


def validate_step_away_fact(fact: dict, session: dict, *, locked: bool) -> dict:
    if (type(locked) is not bool or not isinstance(fact, dict) or set(fact) != FACT_FIELDS
            or fact['system_connected'] is not True or fact['session_bus_connected'] is not True
            or fact['sleeping'] is not False or fact['session_error'] is not None
            or type(fact['locked_hint_seen']) is not bool):
        raise ValueError('step-away capability facts lack healthy bus or sleep state')
    observed = fact['session']
    if (not isinstance(observed, dict) or set(observed) != SESSION_FIELDS
            or observed['id'] != session['id'] or observed['uid'] != int(session['User'])
            or observed['session_type'] != session['Type'] or observed['active'] is not True
            or type(observed['can_lock']) is not bool or type(observed['locked']) is not bool):
        raise ValueError('step-away capability facts session differs from the captured graphical seat')
    screensaver = fact['screensaver_locked']
    if screensaver is not None and type(screensaver) is not bool:
        raise ValueError('step-away capability facts screensaver state is invalid')
    idle = fact['idle']
    if (not isinstance(idle, dict) or set(idle) != {'source', 'error', 'idle'}
            or type(idle['idle']) is not bool):
        raise ValueError('step-away capability facts idle record has an invalid shape')
    locker_observed = screensaver is not None or fact['locked_hint_seen'] is True
    expected_mode = ('lock_observed' if locker_observed else
                     'idle_fallback' if idle['source'] in IDLE_SOURCES and idle['error'] is None else
                     'unavailable')
    if fact['mode'] != expected_mode or expected_mode == 'unavailable':
        raise ValueError('step-away capability facts reported an invalid or unavailable mode')
    if locked:
        if (fact['mode'] != 'lock_observed'
                or (observed['locked'] is not True and screensaver is not True)):
            raise ValueError('step-away capability facts did not observe the locked session')
    else:
        if (observed['locked'] is not False or screensaver not in {False, None}
                or (fact['mode'] == 'idle_fallback' and idle['idle'] is not False)):
            raise ValueError('step-away capability facts remained locked or idle')
    return fact


def await_step_away_fact(app, session: dict, *, locked: bool, minimum_count: int = 1,
                         deadline: float | None = None) -> dict:
    if type(minimum_count) is not int or minimum_count < 1:
        raise ValueError('step-away capability fact wait requires a positive minimum count')
    limit = time.monotonic() + 15 if deadline is None else deadline
    while True:
        app.require_running()
        app.require_engine()
        facts = step_away_facts(app)
        if len(facts) >= minimum_count:
            try:
                latest = validate_step_away_fact(facts[-1], session, locked=locked)
                return {'count': len(facts), 'latest': latest}
            except ValueError:
                pass
        if time.monotonic() >= limit:
            error = CaseUnavailable('step-away-capability-fact-phase-unverified')
            error.observations = {'expected_locked': locked, 'minimum_count': minimum_count,
                                  'observed_count': len(facts), 'last_fact': facts[-1] if facts else None}
            raise error
        time.sleep(0.05)


def open_project(launch_binding: dict, project: Path, observations: dict, streams: dict):
    opened = run(launch_binding, 'pw', ['open', str(project)], timeout=30)
    observations['project_open'] = {key: opened[key]
                                    for key in ('launch_identity', 'exit_code', 'timed_out', 'output_limit')
                                    if key in opened}
    if 'stdout' in opened:
        streams['project-open-stdout'] = opened['stdout']
    if 'stderr' in opened:
        streams['project-open-stderr'] = opened['stderr']
    if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
        raise ValueError('installed CLI refused the owned lock-monitor project')


def lock_and_unlock(app, execution: dict, vm_id: str, native, lock, unlock_ctl, observations: dict):
    lock_transition = perform_transition(execution, vm_id, lock, 'session-lock-v1')
    observations['lock_transition'] = lock_transition
    observations['transitions'] = [lock_transition]
    during_lock = native.observe()
    if not all(during_lock[key] for key in ('locked_hint', 'locker_active')):
        raise ValueError('lock monitor transition did not establish both native lock flags')
    observations['during_lock'] = during_lock
    observations['step_away_locked'] = await_step_away_fact(
        app, app.observations['session'], locked=True, minimum_count=2)
    observations['unlock_transaction'] = unlock_ctl.unlock()
    after_unlock = native.observe()
    if any(after_unlock[key] for key in ('locked_hint', 'locker_active')):
        raise ValueError('lock monitor unlock did not clear both native lock flags')
    observations['after_unlock'] = after_unlock
    unlocked = await_step_away_fact(
        app, app.observations['session'], locked=False, minimum_count=3)
    if unlocked['latest']['mode'] != 'lock_observed' or unlocked['latest']['locked_hint_seen'] is not True:
        raise ValueError('lock monitor did not preserve lock_observed mode after unlock')
    observations['step_away_unlocked'] = unlocked


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    if capability != 'session' or case not in {'initial-lock-state', 'lock-during-presence'}:
        raise ValueError('lock_monitor driver executes only session/initial-lock-state and session/lock-during-presence')
    native_input = binding.get('native_input')
    if not isinstance(native_input, dict):
        raise CaseUnavailable('lock-monitor-transitions-require-bound-physical-input')
    execution, vm_id = native_input['binding'], native_input['vm_id']
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    storage = resolve(session_environment(graphical_session()))
    reservation = Reservation(launch_binding, storage)
    observations, streams = {}, {}
    app, fixture, policy = InstalledApp(launch_binding), None, None
    calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
    try:
        with reservation:
            reservation.check(empty=True)
            app.launch()
            app.wait_ready()
            reservation.check_launch(app)
            observations['launch'] = app.observations
            observations['interactive'] = interactive_window(app)
            with DesktopUI(app) as ui:
                observations['initial_activation'] = activate_visible_frame(app, ui)
                reservation.check_launch(app)
            observations['step_away_initial'] = await_step_away_fact(
                app, app.observations['session'], locked=False, minimum_count=1)
            with NativeSessionObserver(app.observations['session']) as native, \
                    NativeLockObserver(app.observations['session'], native=native) as lock:
                unlock_ctl = NativeUnlockControl(execution, vm_id, lock)
                validate_account_shape(unlock_ctl.account)
                observations['unlock_preparation'] = unlock_ctl.prepare()
                try:
                    acquire = 'command' if case == 'initial-lock-state' else None
                    with ProviderFixture.gui(calls, acquire=acquire) as fixture:
                        provider = configure_provider(app, fixture)
                        reservation.check_launch(app)
                        project = Path(tempfile.mkdtemp(prefix='lock-monitor-project-', dir=storage.home))
                        open_project(launch_binding, project, observations, streams)
                        if case == 'initial-lock-state':
                            lock_and_unlock(app, execution, vm_id, native, lock, unlock_ctl, observations)
                            with DesktopUI(app) as ui:
                                observations['post_unlock_activation'] = activate_visible_frame(app, ui)
                            submit_project_turn(app)
                            observations['settled_reply'] = await_fixture_reply(
                                app, project, provider['provider_id'], fixture.acquisition)
                            reservation.check_launch(app)
                        else:
                            with DesktopUI(app) as ui:
                                ui.click('Project configuration')
                                ui.click('Secrets', tab=True)
                                ui.click('Add secret')
                                for label, operation, _, password in FIELDS:
                                    reservation.check_launch(app)
                                    physical_input(app, ui, label, operation, password=password)
                                ui.click('Add secret')
                                reservation.check_launch(app)
                                if alerts(app, ui, time.monotonic() + 15):
                                    raise CaseUnavailable('lock-during-presence-already-has-an-owned-alert')
                                policy = PresencePolicy(launch_binding)
                                with policy:
                                    lock_and_unlock(app, execution, vm_id, native, lock, unlock_ctl, observations)
                                    observations['post_unlock_activation'] = activate_visible_frame(app, ui)
                                    reservation.check_launch(app)
                                    ui.click('Reveal value')
                                    observations['reveal_alert'] = await_alert(app, ui)
                                    reservation.check_launch(app)
                finally:
                    observations['credential_cleanup'] = run_privileged_disable(
                        unlock_ctl.account, vm_id=vm_id)
    except (OSError, ValueError, TypeError, KeyError) as error:
        observations.update(getattr(error, 'observations', {}))
        observations['failure_type'] = type(error).__name__
        observations['failure_detail'] = str(error)[:512]
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed(
            'session lock monitor or lock-during-presence continuity failed')
        failure.observations = observations
        failure.streams = streams
        raise failure from None
    finally:
        app.stop()
        streams.update(app.streams())
        observations['launch'] = app.observations
        observations['fresh_storage'] = reservation.facts()
        if fixture is not None:
            observations['provider_requests'] = list(fixture.observations)
        if policy is not None:
            observations['presence_policy'] = policy.observations
    return retain(binding, capability, case, root, 'passed', observations, streams)
