"""Bind real active-work sleep inhibition to the captured installed engine and native seat."""
from __future__ import annotations

import math
import os
from pathlib import Path
import time

from .app import engine_image, process_identity
from .native_session import kernel_owner
from .records import CaseUnavailable

TIME_BUDGET_SECONDS = 270


def engine(app):
    app.require_running()
    app.require_engine()
    selected = app.observations['engine_identity']
    actual = kernel_owner(selected['pid'], os.getuid())
    expected = app.binding['images'][str(Path(app.binding['install_root']) / 'bin/pw')]
    if ({key: actual[key] for key in selected} != selected
            or engine_image(selected['pid']) != (expected['device'], expected['inode'], expected['bytes'])
            or process_identity(selected['pid']) != selected):
        raise ValueError('sleep inhibition engine differs from its captured final installed image')
    return actual


def observe(app, native, *, expected=None):
    before = engine(app)
    if expected is not None and before != expected:
        raise ValueError('sleep inhibition replaced its captured engine')
    observation = native.inhibitors()
    if engine(app) != before:
        raise ValueError('sleep inhibition engine changed during native observation')
    rows = [row for row in observation['inhibitors'] if row['owner']['pid'] == before['pid']]
    if any(row['owner'] != before or row['what'] != 'sleep' or row['mode'] != 'block' for row in rows):
        raise ValueError('captured engine has a foreign-owner or non-sleep native inhibitor')
    if len(rows) > 1:
        raise ValueError('captured engine has ambiguous native sleep inhibition')
    return {'engine': before, 'native_session': observation['native_session'], 'engine_inhibitors': rows}


def await_inhibitor(app, native, *, held, expected, deadline):
    if (type(held) is not bool or type(deadline) not in {int, float}
            or not math.isfinite(deadline) or not 0 < deadline - time.monotonic() <= 30):
        raise ValueError('native inhibitor wait requires its fixed expected phase and deadline')
    while True:
        current = observe(app, native, expected=expected)
        if bool(current['engine_inhibitors']) is held:
            return current
        if time.monotonic() >= deadline:
            error = CaseUnavailable('captured-engine-sleep-inhibitor-phase-unverified')
            error.observations = {'inhibitor_phase': 'held' if held else 'released', 'last_inhibitor': current}
            raise error
        time.sleep(0.05)


def execute(binding, capability, case, root):
    import tempfile
    from .app import InstalledApp, graphical_session, session_environment
    from .desktop_launch import protected_launcher
    from .desktop_turn import submit_project_turn
    from .desktop_ui import DesktopUI, interactive_window
    from .fixture_turn import await_fixture_reply
    from .fresh_profile import Reservation
    from .native_session import NativeSessionObserver
    from .process import run
    from .profile_setup import await_session, require_active
    from .provider import MODEL, ProviderFixture
    from .provider_barrier import ResponseBarrier
    from .provider_recovery import load as load_provider_recovery
    from .provider_setup import configure_provider
    from .records import CaseFailed, retain
    from ..installed_input.session_lock import NativeLockObserver
    from ..installed_input.transition_guest import perform_transition
    from ..installed_input.unlock_account import run_privileged_disable, validate_account_shape
    from ..installed_input.unlock_control import NativeUnlockControl
    from ..installed_storage import resolve

    if (capability, case) != ('session', 'inhibitor-lifetime'):
        raise ValueError('keepawake driver executes only session/inhibitor-lifetime')
    launch_binding = {**binding, 'launcher': protected_launcher(binding)}
    project = Path(tempfile.mkdtemp(prefix='keepawake-project-', dir=root))
    app, observations, streams, barrier, reservation = None, {}, {}, None, None
    try:
        native_input = binding.get('native_input')
        if not isinstance(native_input, dict):
            raise CaseUnavailable('keepawake-transitions-require-bound-physical-input')
        execution = native_input['binding']
        vm_id = native_input['vm_id']
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        recovery = load_provider_recovery(binding)
        if recovery is not None:
            observations['provider_recovery'], streams['provider-registration'] = recovery
        storage = resolve(session_environment(graphical_session()))
        reservation = Reservation(launch_binding, storage)
        with reservation:
            try:
                if recovery is None:
                    reservation.check(empty=True)
                barrier = ResponseBarrier()
                with ProviderFixture.gui(calls, acquire='command', response_barrier=barrier,
                        endpoint=recovery[0]['endpoint'] if recovery is not None else None) as fixture:
                    app = InstalledApp(launch_binding)
                    try:
                        app.launch()
                        app.wait_ready()
                        if recovery is None:
                            reservation.check_launch(app)
                        observations['launch'] = app.observations
                        observations['interactive'] = interactive_window(app)
                        with NativeSessionObserver(app.observations['session']) as native, \
                                NativeLockObserver(app.observations['session'], native=native) as lock:
                            initial_inhibitor = await_inhibitor(
                                app, native, held=False, expected=None, deadline=time.monotonic() + 10)
                            observations['inhibitor_initial'] = initial_inhibitor
                            captured_engine = initial_inhibitor['engine']
                            unlock_ctl = NativeUnlockControl(execution, vm_id, lock)
                            validate_account_shape(unlock_ctl.account)
                            observations['unlock_preparation'] = unlock_ctl.prepare()
                            try:
                                provider = configure_provider(app, fixture)
                                opened = run(launch_binding, 'pw', ['open', str(project)], timeout=30)
                                observations['project_open'] = {key: opened[key]
                                    for key in ('launch_identity', 'exit_code', 'timed_out', 'output_limit')
                                    if key in opened}
                                if 'stdout' in opened:
                                    streams['project-open-stdout'] = opened['stdout']
                                if 'stderr' in opened:
                                    streams['project-open-stderr'] = opened['stderr']
                                if opened['exit_code'] or opened['timed_out'] or opened['output_limit']:
                                    raise ValueError('installed CLI refused the owned keepawake project')
                                observations['opened_session'] = await_session(
                                    app, project, provider['provider_id'], MODEL,
                                    status='idle', reason='keepawake-project-session-not-opened')
                                submit_project_turn(app)
                                with DesktopUI(app):
                                    await_session(app, project, provider['provider_id'], MODEL,
                                                  reason='keepawake-fixture-turn-did-not-start')
                                    observations['provider_held_before'] = barrier.await_held()
                                    active = require_active(app, project, provider['provider_id'], MODEL)
                                    if not any(row['completed'] is False for row in fixture.observations):
                                        raise CaseUnavailable('keepawake-active-turn-has-no-fixed-provider-request')
                                    observations['active_before'] = active
                                    observations['before_lock'] = await_inhibitor(
                                        app, native, held=True, expected=captured_engine, deadline=time.monotonic() + 15)
                                    lock_transition = perform_transition(execution, vm_id, lock, 'session-lock-v1')
                                    observations['lock_transition'] = lock_transition
                                    observations['transitions'] = [lock_transition]
                                    during_lock = await_inhibitor(
                                        app, native, held=True, expected=captured_engine, deadline=time.monotonic() + 10)
                                    if not all(during_lock['native_session'][key] for key in ('locked_hint', 'locker_active')):
                                        raise ValueError('sleep inhibition lock phase did not establish both native lock flags')
                                    require_active(app, project, provider['provider_id'], MODEL, active)
                                    barrier.require_held()
                                    observations['during_lock'] = during_lock
                                    observations['unlock_transaction'] = unlock_ctl.unlock()
                                    after_unlock = await_inhibitor(
                                        app, native, held=True, expected=captured_engine, deadline=time.monotonic() + 10)
                                    if any(after_unlock['native_session'][key] for key in ('locked_hint', 'locker_active')):
                                        raise ValueError('sleep inhibition unlock phase did not clear both native lock flags')
                                    observations['active_after_unlock'] = require_active(
                                        app, project, provider['provider_id'], MODEL, active)
                                    observations['provider_held_after_unlock'] = barrier.require_held()
                                    observations['after_unlock'] = after_unlock
                                    barrier.release()
                                    observations['provider_released'] = barrier.await_response_written()
                                    observations['settled_reply'] = await_fixture_reply(
                                        app, project, provider['provider_id'], fixture.acquisition,
                                        selected_session=active['id'])
                                    observations['after_completion'] = await_inhibitor(
                                        app, native, held=False, expected=captured_engine, deadline=time.monotonic() + 15)
                            finally:
                                observations['credential_cleanup'] = run_privileged_disable(
                                    unlock_ctl.account, vm_id=vm_id)
                        observations['provider_requests'] = list(fixture.observations)
                    finally:
                        app.stop()
                        observations['launch'] = app.observations
                        streams.update(app.streams())
                    with NativeSessionObserver(app.observations['session']) as post_native:
                        post_exit = post_native.inhibitors()
                        remaining = [row for row in post_exit['inhibitors']
                                     if row['owner']['pid'] == captured_engine['pid']]
                        if remaining:
                            raise ValueError('captured engine retained a native sleep inhibitor after exit')
                        observations['after_exit'] = {
                            'native_session': post_exit['native_session'],
                            'engine_inhibitors': remaining}
            finally:
                observations['fresh_storage'] = reservation.facts()
    except (OSError, ValueError, TypeError) as error:
        if barrier is not None:
            observations['provider_response_control'] = barrier.snapshot()
        if app is not None:
            observations['launch'] = app.observations
            streams.update(app.streams())
        failure = error if isinstance(error, CaseUnavailable) else CaseFailed(
            'active-work sleep inhibition or lock-unlock continuity failed')
        failure.observations = {**observations, **getattr(error, 'observations', {}),
            'failure_type': type(error).__name__, 'failure_detail': str(error)[:512]}
        failure.streams = streams
        raise failure from None
    return retain(binding, capability, case, root, 'passed', observations, streams)
