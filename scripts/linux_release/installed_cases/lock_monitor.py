"""Bind native session lock-state observation and presence lock recovery to the installed runtime."""
from __future__ import annotations

import json, tempfile, time
from pathlib import Path

from .app import InstalledApp, graphical_session, session_environment
from .desktop_launch import activate_visible_frame, protected_launcher
from .desktop_turn import submit_project_turn
from .desktop_ui import DesktopUI, interactive_window
from .fixture_turn import await_fixture_reply
from .fresh_profile import Reservation
from .native_session import NativeSessionObserver
from .process import run
from .profile_setup import await_session
from .provider import MODEL, ProviderFixture
from .provider_setup import configure_provider
from .presence_setup import prepare_secret
from .records import CaseFailed, CaseUnavailable, retain
from ..installed_input import session_wire
from ..installed_input.session_lock import NativeLockObserver
from ..installed_input.transition_guest import perform_transition
from ..installed_input.unlock_account import run_privileged_disable, validate_account_shape
from ..installed_input.unlock_control import NativeUnlockControl
from ..installed_storage import resolve

TIME_BUDGET_SECONDS, STEP_AWAY_PREFIX = 300, 'Step-away capability facts: '
CHALLENGE_PREFIX = 'Step-away presence challenge: '
INVALIDATION_PREFIX = 'Step-away presence invalidation: '
FACT_FIELDS = frozenset({'session', 'session_error', 'screensaver_locked',
                         'system_connected', 'session_bus_connected', 'sleeping'})
OWNER_FIELDS = frozenset({'logind_owner', 'screensaver_owner'})
ACCEPTED_FACT_KEYS = (FACT_FIELDS, FACT_FIELDS | OWNER_FIELDS)
SESSION_FIELDS = {'id', 'path', 'uid', 'session_type', 'active', 'can_lock', 'locked'}
CHALLENGE_FIELDS = {'challenge_id', 'purpose', 'window_label', 'generation'}
INVALIDATION_FIELDS = {'challenge_id', 'purpose', 'window_label', 'generation_before',
                       'generation_after', 'code', 'resolution'}


def valid_unique_owner(value) -> bool:
    return isinstance(value, str) and value.startswith(':') and len(value) > 1


def valid_fact_keys(fact) -> bool:
    if not isinstance(fact, dict) or frozenset(fact) not in ACCEPTED_FACT_KEYS:
        return False
    return all(fact.get(k) is None or valid_unique_owner(fact[k]) for k in OWNER_FIELDS if k in fact)


def step_away_facts(app) -> list[dict]:
    raw = app.streams().get('app-stderr', b'').decode('utf-8', 'replace')
    facts = [json.loads(r[len(STEP_AWAY_PREFIX):]) for r in raw.splitlines() if r.startswith(STEP_AWAY_PREFIX)]
    if any(not valid_fact_keys(fact) for fact in facts):
        raise ValueError('step-away capability facts record has an invalid shape')
    return facts


def step_away_challenges(app) -> list[dict]:
    raw = app.streams().get('app-stderr', b'').decode('utf-8', 'replace')
    rows = [json.loads(r[len(CHALLENGE_PREFIX):]) for r in raw.splitlines() if r.startswith(CHALLENGE_PREFIX)]
    for row in rows:
        if (not isinstance(row, dict) or set(row) != CHALLENGE_FIELDS
                or not isinstance(row['challenge_id'], str) or not row['challenge_id']
                or row['purpose'] != 'reveal' or row['window_label'] != 'main'
                or type(row['generation']) is not int or row['generation'] < 0):
            raise ValueError('step-away presence challenge record has an invalid shape')
    return rows


def step_away_invalidations(app) -> list[dict]:
    raw = app.streams().get('app-stderr', b'').decode('utf-8', 'replace')
    rows = [json.loads(r[len(INVALIDATION_PREFIX):]) for r in raw.splitlines() if r.startswith(INVALIDATION_PREFIX)]
    for row in rows:
        if (not isinstance(row, dict) or set(row) != INVALIDATION_FIELDS
                or not isinstance(row['challenge_id'], str) or not row['challenge_id']
                or row['purpose'] != 'reveal' or row['window_label'] != 'main'
                or type(row['generation_before']) is not int or type(row['generation_after']) is not int
                or not (0 <= row['generation_before'] < row['generation_after'])
                or row['code'] != 'presence_denied' or row['resolution'] != 'step_away'):
            raise ValueError('step-away presence invalidation record has an invalid shape')
    return rows


def validate_step_away_fact(fact: dict, session: dict, *, locked: bool) -> dict:
    if (type(locked) is not bool or not valid_fact_keys(fact)
            or fact['system_connected'] is not True or fact['session_bus_connected'] is not True
            or fact['sleeping'] is not False or fact['session_error'] is not None):
        raise ValueError('step-away capability facts lack healthy bus or sleep state')
    observed, screensaver = fact['session'], fact['screensaver_locked']
    if (not isinstance(observed, dict) or set(observed) != SESSION_FIELDS or observed['id'] != session['id']
            or observed['uid'] != int(session['User']) or observed['session_type'] != session['Type']
            or observed['active'] is not True or type(observed['can_lock']) is not bool or type(observed['locked']) is not bool):
        raise ValueError('step-away capability facts session differs from the captured graphical seat')
    if screensaver is not None and type(screensaver) is not bool:
        raise ValueError('step-away capability facts screensaver state is invalid')
    if OWNER_FIELDS <= frozenset(fact) and (not valid_unique_owner(fact['logind_owner'])
                                            or (screensaver is not None and not valid_unique_owner(fact['screensaver_owner']))):
        raise ValueError('step-away capability facts lack bound D-Bus owner identities')
    if locked and (screensaver is None or (observed['locked'] is not True and screensaver is not True)):
        raise ValueError('step-away capability facts did not observe the locked session')
    if not locked and (observed['locked'] is not False or screensaver is not False):
        raise ValueError('step-away capability facts remained locked or lacked screen saver observation')
    return fact


def await_step_away_fact(app, session: dict, *, locked: bool, minimum_count: int = 1, deadline: float | None = None) -> dict:
    if type(minimum_count) is not int or minimum_count < 1:
        raise ValueError('step-away capability fact wait requires a positive minimum count')
    limit = time.monotonic() + 15 if deadline is None else deadline
    while True:
        app.require_running()
        app.require_engine()
        facts = step_away_facts(app)
        if len(facts) >= minimum_count:
            try:
                return {'count': len(facts), 'latest': validate_step_away_fact(facts[-1], session, locked=locked)}
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
    observations['project_open'] = {k: opened[k] for k in ('launch_identity', 'exit_code', 'timed_out', 'output_limit') if k in opened}
    for key, stream_key in (('stdout', 'project-open-stdout'), ('stderr', 'project-open-stderr')):
        if key in opened:
            streams[stream_key] = opened[key]
    if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
        raise ValueError('installed CLI refused the owned lock-monitor project')


def lock_and_unlock(app, execution: dict, vm_id: str, native, lock, unlock_ctl, observations: dict):
    lock_transition = perform_transition(execution, vm_id, lock, 'session-lock-v1')
    observations['lock_transition'], observations['transitions'] = lock_transition, [lock_transition]
    during_lock = native.observe()
    if not all(during_lock[key] for key in ('locked_hint', 'locker_active')):
        raise ValueError('lock monitor transition did not establish both native lock flags')
    observations['during_lock'] = during_lock
    observations['step_away_locked'] = await_step_away_fact(app, app.observations['session'], locked=True, minimum_count=2)
    observations['unlock_transaction'] = unlock_ctl.unlock()
    after_unlock = native.observe()
    if any(after_unlock[key] for key in ('locked_hint', 'locker_active')):
        raise ValueError('lock monitor unlock did not clear both native lock flags')
    observations['after_unlock'] = after_unlock
    unlocked = await_step_away_fact(app, app.observations['session'], locked=False, minimum_count=3)
    if unlocked['latest']['screensaver_locked'] is not False or unlocked['latest']['session']['locked'] is not False:
        raise ValueError('lock monitor did not restore unlocked screen saver and session observation after unlock')
    observations['step_away_unlocked'] = unlocked


def recover_case_observer(session: dict, native, lock) -> tuple:
    initial_proof = lock.initial
    lock.close()
    native.close()
    refusals = {}
    for label, observer in (('native', native), ('lock', lock)):
        try:
            observer.observe()
        except ValueError as err:
            refusals[label] = str(err)
        else:
            raise ValueError('closed case lock observer did not refuse stale observation')
    fresh_native = NativeSessionObserver(session)
    fresh_lock = NativeLockObserver(session, native=fresh_native)
    try:
        session_wire.same_runtime(initial_proof, fresh_lock.initial)
        if any(fresh_native.initial[key] for key in ('locked_hint', 'locker_active')):
            raise ValueError('reconnected case lock observer drifted from the captured runtime')
    except ValueError:
        fresh_lock.close()
        fresh_native.close()
        raise
    return fresh_native, fresh_lock, {'stale_refusals': refusals, 'reconnected_proof': fresh_lock.initial}


def is_capability_state_loss_fact(fact: dict, session: dict) -> bool:
    if not valid_fact_keys(fact):
        return False
    observed = fact.get('session')
    if isinstance(observed, dict) and (set(observed) != SESSION_FIELDS or observed['id'] != session['id']
                                       or observed['uid'] != int(session['User']) or observed['session_type'] != session['Type']):
        return False
    if observed is not None and not isinstance(observed, dict):
        return False
    return (fact.get('screensaver_locked') is None or fact.get('session_bus_connected') is False
            or fact.get('system_connected') is False
            or (observed is None and isinstance(fact.get('session_error'), str) and bool(fact['session_error'])))


def _valid_unlocked_lock_observed(row: dict, session: dict) -> dict | None:
    try:
        validated = validate_step_away_fact(row, session, locked=False)
    except ValueError:
        return None
    return validated if validated['screensaver_locked'] is False else None


def observe_capability_state_recovery(app, session: dict) -> dict | None:
    facts = step_away_facts(app)
    baseline_index = next((idx for idx, r in enumerate(facts) if _valid_unlocked_lock_observed(r, session) is not None), None)
    if baseline_index is None:
        return None
    loss_index = next((idx for idx in range(baseline_index + 1, len(facts)) if is_capability_state_loss_fact(facts[idx], session)), None)
    if loss_index is None:
        return None
    for idx in range(loss_index + 1, len(facts)):
        recovered = _valid_unlocked_lock_observed(facts[idx], session)
        if recovered is not None:
            return {'baseline_index': baseline_index, 'baseline_fact': facts[baseline_index],
                    'loss_index': loss_index, 'loss_fact': facts[loss_index],
                    'recovered_index': idx, 'recovered_fact': recovered}
    return None


def observe_monitor_owner_recovery(app, session: dict) -> dict | None:
    facts = step_away_facts(app)
    baseline_index = next((i for i, r in enumerate(facts) if _valid_unlocked_lock_observed(r, session) is not None
                           and all(valid_unique_owner(r.get(k)) for k in OWNER_FIELDS)), None)
    if baseline_index is None:
        return None
    base = facts[baseline_index]
    loss_index = next((i for i in range(baseline_index + 1, len(facts))
                       if is_capability_state_loss_fact(facts[i], session) and OWNER_FIELDS <= frozenset(facts[i])
                       and ((facts[i]['logind_owner'] is None and facts[i]['session'] is None)
                            or (facts[i]['screensaver_owner'] is None and facts[i]['screensaver_locked'] is None))), None)
    if loss_index is None:
        return None
    loss = facts[loss_index]
    for idx in range(loss_index + 1, len(facts)):
        rec = _valid_unlocked_lock_observed(facts[idx], session)
        if (rec is not None and all(valid_unique_owner(rec.get(k)) for k in OWNER_FIELDS)
                and (loss['logind_owner'] is not None or rec['logind_owner'] != base['logind_owner'])
                and (loss['screensaver_owner'] is not None or rec['screensaver_owner'] != base['screensaver_owner'])):
            return {'baseline_index': baseline_index, 'baseline_fact': base,
                    'loss_index': loss_index, 'loss_fact': loss, 'recovered_index': idx, 'recovered_fact': rec}
    return None


def run_initial_lock_state(app, execution: dict, vm_id: str, native, lock, unlock_ctl,
                           observations: dict, provider: dict, fixture, project: Path, reservation) -> str:
    lock_and_unlock(app, execution, vm_id, native, lock, unlock_ctl, observations)
    with DesktopUI(app) as ui:
        observations['post_unlock_activation'] = activate_visible_frame(app, ui)
    submit_project_turn(app)
    observations['settled_reply'] = await_fixture_reply(app, project, provider['provider_id'], fixture.acquisition)
    reservation.check_launch(app)
    capability_recovery = observe_capability_state_recovery(app, app.observations['session'])
    owner_recovery = observe_monitor_owner_recovery(app, app.observations['session'])
    verified = owner_recovery is not None
    observations.update(
        initial_lock_state_verified=True, lock_unlock_verified=True,
        capability_state_recovery=capability_recovery, capability_state_recovery_verified=capability_recovery is not None,
        monitor_owner_recovery=owner_recovery, monitor_owner_recovery_verified=verified,
        reason=('initial-lock-state-lock-unlock-and-monitor-owner-recovery-verified' if verified
                else 'initial-lock-state-and-lock-unlock-observed-monitor-owner-recovery-unverified'))
    return 'passed' if verified else 'unverified'


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
    observations, streams, status = {}, {}, 'unverified'
    app, fixture = InstalledApp(launch_binding), None
    calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
    try:
        with reservation:
            reservation.check(empty=True)
            app.launch()
            app.wait_ready()
            reservation.check_launch(app)
            observations['launch'], observations['interactive'] = app.observations, interactive_window(app)
            with DesktopUI(app) as ui:
                observations['initial_activation'] = activate_visible_frame(app, ui)
                reservation.check_launch(app)
            observations['step_away_initial'] = await_step_away_fact(
                app, app.observations['session'], locked=False, minimum_count=1)
            with NativeSessionObserver(app.observations['session']) as native, \
                    NativeLockObserver(app.observations['session'], native=native) as lock:
                if case == 'initial-lock-state':
                    native, lock, observations['case_observer_recovery'] = recover_case_observer(
                        app.observations['session'], native, lock)
                unlock_ctl = NativeUnlockControl(execution, vm_id, lock, stale=case == 'lock-during-presence')
                validate_account_shape(unlock_ctl.account)
                observations['unlock_preparation'] = unlock_ctl.prepare()
                try:
                    acquire = 'command' if case == 'initial-lock-state' else None
                    with ProviderFixture.gui(calls, acquire=acquire) as fixture:
                        provider = configure_provider(app, fixture)
                        reservation.check_launch(app)
                        project = Path(tempfile.mkdtemp(prefix='lock-monitor-project-', dir=storage.home))
                        open_project(launch_binding, project, observations, streams)
                        observations['opened_session'] = await_session(
                            app, project, provider['provider_id'], MODEL,
                            status='idle', reason='lock-monitor-project-session-not-opened')
                        if case == 'initial-lock-state':
                            status = run_initial_lock_state(app, execution, vm_id, native, lock, unlock_ctl,
                                                            observations, provider, fixture, project, reservation)
                        else:
                            with DesktopUI(app) as ui:
                                observations['secret_setup'] = prepare_secret(app, ui, reservation)
                                from .presence_system import run_stale_reveal
                                run_stale_reveal(app, ui, observations['opened_session']['project_id'],
                                    native_input, native, lock, unlock_ctl, observations, reservation)
                                if (observations.get('stale_challenge_refusal_verified') is True
                                        and observations.get('fresh_confirmation_verified') is True):
                                    status = 'passed'
                finally:
                    lock.close()
                    native.close()
                    observations['credential_cleanup'] = run_privileged_disable(unlock_ctl.account, driver_tree=native_input['binding']['driver_tree'], vm_id=vm_id)
    except (OSError, ValueError, TypeError, KeyError) as error:
        observations.update(getattr(error, 'observations', {}))
        observations['failure_type'], observations['failure_detail'] = type(error).__name__, str(error)[:512]
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed(
            'session lock monitor or lock-during-presence continuity failed')
        failure.observations, failure.streams = observations, streams
        raise failure from None
    finally:
        app.stop()
        streams.update(app.streams())
        observations['launch'], observations['fresh_storage'] = app.observations, reservation.facts()
        if fixture is not None:
            observations['provider_requests'] = list(fixture.observations)
    return retain(binding, capability, case, root, status, observations, streams)
