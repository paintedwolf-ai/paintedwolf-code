"""Verify session lock-state observation and presence lock recovery across lock-unlock transitions."""
from contextlib import ExitStack, nullcontext
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release import installed_execution, installed_storage
from linux_release.installed_cases import (
    app as app_mod, desktop_launch, desktop_turn, desktop_ui,
    fixture_turn, fresh_profile, lock_monitor, native_session,
    polkit_policy, presence_alert, process, provider, provider_setup, records)
from linux_release.installed_input import (
    controller, presence_fixture, protocol, session_lock,
    transition_guest, unlock_account, unlock_control, unlock_wire)
from verification_tests.distribution.installed.test_installed_session_lock_execution import native_proof

SESSION = {'id': '2', 'User': '30009', 'Type': 'wayland'}
ACCOUNT = {'name': 'pwqual059a1', 'uid': 30009, 'gid': 30009, 'home': '/home/pwqual059a1'}
EXECUTION = {
    'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40,
    'driver_tree': 'c' * 40, 'artifact_id': 'app', 'environment_id': 'noble',
    'variant_id': 'gnome-wayland'}


def fact(*, locked=False, screensaver=False, hint_seen=False, can_lock=True,
         mode='lock_observed', idle_source='wayland_input_idle', idle=False):
    return {
        'session': {
            'id': '2', 'path': '/org/freedesktop/login1/session/_32',
            'uid': 30009, 'session_type': 'wayland', 'active': True,
            'can_lock': can_lock, 'locked': locked},
        'session_error': None,
        'screensaver_locked': screensaver,
        'locked_hint_seen': hint_seen,
        'system_connected': True,
        'session_bus_connected': True,
        'sleeping': False,
        'idle': {'source': idle_source, 'error': None, 'idle': idle},
        'mode': mode}


def stderr_stream(*facts):
    return b''.join(
        (lock_monitor.STEP_AWAY_PREFIX + json.dumps(row, sort_keys=True) + '\n').encode('utf-8')
        for row in facts)


class StepAwayFactValidationTests(unittest.TestCase):
    def test_case_family_routes_both_059_cases_to_lock_monitor(self):
        self.assertEqual(installed_execution.case_family('session', 'initial-lock-state', 'deb'), 'lock_monitor')
        self.assertEqual(installed_execution.case_family('session', 'lock-during-presence', 'deb'), 'lock_monitor')

    def test_step_away_fact_accepts_lock_observed_and_idle_fallback_but_never_can_lock_alone(self):
        unlocked = fact(locked=False, screensaver=False, hint_seen=False, mode='lock_observed')
        self.assertEqual(lock_monitor.validate_step_away_fact(unlocked, SESSION, locked=False), unlocked)

        fallback = fact(locked=False, screensaver=None, hint_seen=False, mode='idle_fallback')
        self.assertEqual(lock_monitor.validate_step_away_fact(fallback, SESSION, locked=False), fallback)

        locked_row = fact(locked=True, screensaver=True, hint_seen=True, mode='lock_observed')
        self.assertEqual(lock_monitor.validate_step_away_fact(locked_row, SESSION, locked=True), locked_row)

        for bad, expected_locked in (
                (fact(locked=False, screensaver=None, hint_seen=False, can_lock=True,
                      mode='lock_observed', idle_source=None), False),
                (fact(locked=False, screensaver=None, hint_seen=False, can_lock=True,
                      mode='unavailable', idle_source=None), False),
                (fact(locked=True, screensaver=True, hint_seen=True, mode='lock_observed'), False),
                (fact(locked=False, screensaver=False, hint_seen=True, mode='lock_observed'), True),
                (fact(locked=False, screensaver=None, hint_seen=False, mode='idle_fallback', idle=True), False)):
            with self.subTest(bad=bad, expected_locked=expected_locked), self.assertRaises(ValueError):
                lock_monitor.validate_step_away_fact(bad, SESSION, locked=expected_locked)


class LockMonitorExecutionTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)

    def _run_case(self, case_name):
        binding = {
            'install_root': '/opt/painted-wolf-code',
            'native_input': {'format_version': 1, 'vm_id': 'owned', 'binding': EXECUTION}}
        unlocked_obs = {'locked_hint': False, 'locker_active': False}
        locked_obs = {'locked_hint': True, 'locker_active': True}

        native_observer = Mock()
        native_observer.__enter__ = Mock(return_value=native_observer)
        native_observer.__exit__ = Mock(return_value=False)
        native_observer.observe.side_effect = [locked_obs, unlocked_obs]

        lock_observer = Mock()
        lock_observer.__enter__ = Mock(return_value=lock_observer)
        lock_observer.__exit__ = Mock(return_value=False)

        f1 = fact(locked=False, screensaver=False, hint_seen=False, mode='lock_observed')
        f2 = fact(locked=True, screensaver=True, hint_seen=True, mode='lock_observed')
        f3 = fact(locked=False, screensaver=False, hint_seen=True, mode='lock_observed')
        fake_app = Mock(observations={'session': SESSION, 'launch_identity': {'pid': 100},
                                      'engine_identity': {'pid': 200}}, binding=binding)
        fake_app.streams.side_effect = [
            {'app-stderr': stderr_stream(f1)},
            {'app-stderr': stderr_stream(f1, f2)},
            {'app-stderr': stderr_stream(f1, f2, f3)},
            {'app-stderr': stderr_stream(f1, f2, f3)}]

        fake_reservation = Mock()
        fake_reservation.__enter__ = Mock(return_value=fake_reservation)
        fake_reservation.__exit__ = Mock(return_value=False)
        fake_reservation.facts.return_value = {'empty_before': True}

        fake_unlock = Mock(account=ACCOUNT)
        fake_unlock.prepare.return_value = {'request': {'operation': unlock_wire.PREPARE}, 'acknowledgment': {}}
        fake_unlock.unlock.return_value = {'request': {'operation': unlock_wire.PURPOSE}, 'acknowledgment': {}}

        tmp = self.stack.enter_context(tempfile.TemporaryDirectory())
        fake_storage = Mock(home=Path(tmp))
        fake_fixture = Mock(observations=[], acquisition=Mock())
        fake_ui = Mock()
        fake_ui_ctx = MagicMockContext(fake_ui)
        fake_policy = Mock(observations={'rule_installed': True, 'restored': True})
        fake_policy.__enter__ = Mock(return_value=fake_policy)
        fake_policy.__exit__ = Mock(return_value=False)

        for target_mod, attr, value in (
                (lock_monitor, 'protected_launcher', {}),
                (lock_monitor, 'graphical_session', SESSION),
                (lock_monitor, 'session_environment', {'HOME': tmp}),
                (lock_monitor, 'resolve', fake_storage),
                (lock_monitor, 'Reservation', fake_reservation),
                (lock_monitor, 'InstalledApp', fake_app),
                (lock_monitor, 'interactive_window', {'window': 'ok'}),
                (lock_monitor, 'DesktopUI', fake_ui_ctx),
                (lock_monitor, 'activate_visible_frame', {'activated': True}),
                (lock_monitor, 'NativeSessionObserver', native_observer),
                (lock_monitor, 'NativeLockObserver', lock_observer),
                (lock_monitor, 'NativeUnlockControl', fake_unlock),
                (provider.ProviderFixture, 'gui', nullcontext(fake_fixture)),
                (lock_monitor, 'configure_provider', {'provider_id': 'loopback'}),
                (lock_monitor, 'run', {
                    'launch_identity': {'pid': 301}, 'exit_code': 0,
                    'timed_out': False, 'output_limit': False,
                    'stdout': b'opened\n', 'stderr': b''}),
                (lock_monitor, 'perform_transition', {'operation': 'session-lock-v1', 'completed': True}),
                (lock_monitor, 'submit_project_turn', None),
                (lock_monitor, 'await_fixture_reply', {'settled': True}),
                (lock_monitor, 'physical_input', None),
                (lock_monitor, 'alerts', []),
                (lock_monitor, 'PresencePolicy', fake_policy),
                (lock_monitor, 'await_alert', {'role': 2, 'visible': True, 'attributes': []})):
            self.stack.enter_context(patch.object(target_mod, attr, return_value=value))
        disable_mock = self.stack.enter_context(patch.object(
            lock_monitor, 'run_privileged_disable', return_value={'disabled': True, 'shadow_locked': True}))
        self.stack.enter_context(patch.object(lock_monitor, 'retain', side_effect=lambda *args: args))
        outcome = lock_monitor.execute(binding, 'session', case_name, Path(tmp))
        return outcome, disable_mock, fake_policy

    def test_initial_lock_state_executes_lock_unlock_and_cleans_up_credential(self):
        outcome, disable_mock, _ = self._run_case('initial-lock-state')
        self.assertEqual(outcome[4], 'passed')
        obs = outcome[5]
        self.assertEqual(obs['step_away_initial']['latest']['mode'], 'lock_observed')
        self.assertTrue(obs['during_lock']['locked_hint'])
        self.assertFalse(obs['after_unlock']['locked_hint'])
        self.assertTrue(obs['step_away_unlocked']['latest']['locked_hint_seen'])
        self.assertEqual(obs['settled_reply'], {'settled': True})
        disable_mock.assert_called_once_with(ACCOUNT, vm_id='owned')

    def test_lock_during_presence_executes_lock_unlock_under_policy_and_captures_alert(self):
        outcome, disable_mock, fake_policy = self._run_case('lock-during-presence')
        self.assertEqual(outcome[4], 'passed')
        obs = outcome[5]
        self.assertTrue(obs['during_lock']['locked_hint'])
        self.assertFalse(obs['after_unlock']['locked_hint'])
        self.assertEqual(obs['reveal_alert'], {'role': 2, 'visible': True, 'attributes': []})
        self.assertEqual(obs['presence_policy'], {'rule_installed': True, 'restored': True})
        fake_policy.__enter__.assert_called_once_with()
        fake_policy.__exit__.assert_called_once()
        disable_mock.assert_called_once_with(ACCOUNT, vm_id='owned')

    def test_controller_verifies_deliveries_for_initial_lock_state_and_lock_during_presence(self):
        for case_name, extra_ops in (
                ('initial-lock-state', ('reply-fixture-prompt-v1',)),
                ('lock-during-presence', tuple(op for _, op, _, _ in presence_fixture.FIELDS))):
            with self.subTest(case=case_name), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                leaf_dir = root / f'leaf/evidence/session/{case_name}'
                leaf_dir.mkdir(parents=True)
                base_session = copy.deepcopy(native_proof())
                base_session['native_session']['session']['User'] = '30009'
                base_session['native_session']['lifecycle']['user'] = [30009, '/org/freedesktop/login1/user/_30009']
                base_session['native_session']['authorities']['locker']['process']['uid'] = 30009
                unlocked = copy.deepcopy(base_session)
                unlocked['native_session'].update(locked_hint=False, locker_active=False)
                locked_session = copy.deepcopy(base_session)
                locked_session['native_session'].update(locked_hint=True, locker_active=True)

                prep_req = {
                    'format_version': 1, 'binding': EXECUTION, 'transaction_id': '1' * 32,
                    'operation': unlock_wire.PREPARE, 'native_session': unlocked, 'account': ACCOUNT}
                prep_ack = {
                    'format_version': 1, 'request_sha256': unlock_wire.digest(prep_req),
                    'vm_id': 'owned', 'result': {'prepared': True, 'account': ACCOUNT}}
                lock_req = {
                    'format_version': 1, 'binding': EXECUTION, 'transaction_id': '2' * 32,
                    'operation': 'session-lock-v1', 'ordinal': 0, 'native_session': unlocked}
                lock_delivery = {
                    'format_version': 1, 'execution_nonce': EXECUTION['execution_nonce'],
                    'vm_id': 'owned', 'transaction_id': '2' * 32,
                    'operation': 'session-lock-v1', 'ordinal': 0,
                    'event_sha256': protocol.event_sha256('session-lock-v1', 0),
                    'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': 1, 'return': {}}}
                lock_ack = {
                    'format_version': 1, 'request_sha256': protocol.request_sha256(lock_req),
                    'delivery': lock_delivery}
                lock_record = {
                    'format_version': 1, 'vm_id': 'owned', 'transaction_id': '2' * 32,
                    'operation': 'session-lock-v1', 'completed': True, 'text_readback': False,
                    'events': [{'request': lock_req, 'delivery': lock_delivery, 'native_session_after': locked_session}]}
                unl_req = {
                    'format_version': 1, 'binding': EXECUTION, 'transaction_id': '3' * 32,
                    'operation': unlock_wire.PURPOSE, 'native_session': locked_session, 'account': ACCOUNT}
                unl_ack = {
                    'format_version': 1, 'request_sha256': unlock_wire.digest(unl_req),
                    'vm_id': 'owned', 'result': {
                        'purpose': unlock_wire.PURPOSE, 'transaction_id': '3' * 32,
                        'delivered': True, 'text_readback': False, 'session_after': unlocked}}

                proc = {'pid': 300, 'parent_pid': 1, 'start_time_ticks': 50}
                focus = {
                    'launch': proc, 'entry': proc, 'frame': proc,
                    'frame_lineage': [proc], 'entry_lineage': [proc],
                    'accessible_chain': [{'bus_name': ':1.55', 'path': '/org/a11y/atspi/accessible/2', 'pid': 300},
                                         {'bus_name': ':1.55', 'path': '/org/a11y/atspi/accessible/1', 'pid': 300}],
                    'entry_focused': True, 'frame_active': True, 'frame_visible': True}
                fixed_records, fixed_rows = [], []
                ops = ('provider-name-v1', 'provider-endpoint-v1-43210',
                       'provider-fixture-credential-v1', *extra_ops)
                for idx, op in enumerate(ops, start=4):
                    tid = f'{idx:032x}'
                    events = []
                    for ordinal in range(len(protocol.steps(op))):
                        req = {'format_version': 1, 'binding': EXECUTION, 'transaction_id': tid,
                               'operation': op, 'ordinal': ordinal, 'focus': focus}
                        delivery = {
                            'format_version': 1, 'execution_nonce': EXECUTION['execution_nonce'],
                            'vm_id': 'owned', 'transaction_id': tid,
                            'operation': op, 'ordinal': ordinal,
                            'event_sha256': protocol.event_sha256(op, ordinal),
                            'lease': {'vm_id': 'owned'}, 'qmp_ack': {'id': ordinal + 1, 'return': {}}}
                        ack = {'format_version': 1, 'request_sha256': protocol.request_sha256(req),
                               'delivery': delivery}
                        events.append({'request': req, 'delivery': delivery, 'focus_after': focus})
                        fixed_rows.append({'request': req, 'acknowledgment': ack})
                    fixed_records.append({
                        'format_version': 1, 'vm_id': 'owned', 'transaction_id': tid,
                        'operation': op, 'completed': True, 'text_readback': False, 'events': events})

                (leaf_dir / 'execution.json').write_text(json.dumps({
                    'artifact': {'id': 'app'},
                    'observations': {
                        'launch': {'fixed_input': fixed_records, 'session': {'User': '30009'}},
                        'unlock_preparation': {'request': prep_req, 'acknowledgment': prep_ack},
                        'lock_transition': lock_record,
                        'unlock_transaction': {'request': unl_req, 'acknowledgment': unl_ack}}}))
                log_rows = [
                    {'request': prep_req, 'acknowledgment': prep_ack},
                    *fixed_rows,
                    {'request': lock_req, 'acknowledgment': lock_ack},
                    {'request': unl_req, 'acknowledgment': unl_ack}]
                (root / 'native-input.jsonl').write_bytes(b''.join(protocol.canonical(r) for r in log_rows))
                (root / 'native-unlock.json').write_bytes(protocol.canonical({
                    'format_version': 1, 'binding': EXECUTION, 'vm_id': 'owned', 'account': ACCOUNT,
                    'prepare_attempt_id': '1' * 32, 'unlock_attempt_id': '3' * 32,
                    'secret_sha256': 'f' * 64, 'pre_locked_at': 10.0,
                    'post_unlocked_at': 20.0, 'cleanup_completed_at': 30.0}))

                selected = {'format_version': 1, 'kind': 'qemu-fixed-input', 'lease': '/tmp/lease.json', 'vm_id': 'owned'}
                controller.verify_deliveries(root, EXECUTION, selected)


class MagicMockContext:
    def __init__(self, value):
        self.value = value

    def __enter__(self):
        return self.value

    def __exit__(self, exc_type, exc, tb):
        return False
