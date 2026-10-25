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


def fact(*, locked=False, screensaver=False, can_lock=True,
         owners=False, logind_owner=':1.4', screensaver_owner=':1.42'):
    row = {
        'session': {
            'id': '2', 'path': '/org/freedesktop/login1/session/_32',
            'uid': 30009, 'session_type': 'wayland', 'active': True,
            'can_lock': can_lock, 'locked': locked},
        'session_error': None,
        'screensaver_locked': screensaver,
        'system_connected': True,
        'session_bus_connected': True,
        'sleeping': False}
    if owners:
        row['logind_owner'] = logind_owner
        row['screensaver_owner'] = screensaver_owner
    return row


def stderr_stream(*facts, challenges=(), invalidations=()):
    lines = [lock_monitor.STEP_AWAY_PREFIX + json.dumps(row, sort_keys=True) + '\n' for row in facts]
    lines.extend(lock_monitor.CHALLENGE_PREFIX + json.dumps(row, sort_keys=True) + '\n' for row in challenges)
    lines.extend(lock_monitor.INVALIDATION_PREFIX + json.dumps(row, sort_keys=True) + '\n' for row in invalidations)
    return ''.join(lines).encode('utf-8')


class StepAwayFactValidationTests(unittest.TestCase):
    def test_case_family_routes_both_059_cases_to_lock_monitor(self):
        self.assertEqual(installed_execution.case_family('session', 'initial-lock-state', 'deb'), 'lock_monitor')
        self.assertEqual(installed_execution.case_family('session', 'lock-during-presence', 'deb'), 'lock_monitor')

    def test_step_away_fact_accepts_observed_lock_sources_and_rejects_can_lock_alone(self):
        unlocked = fact(locked=False, screensaver=False)
        self.assertEqual(lock_monitor.validate_step_away_fact(unlocked, SESSION, locked=False), unlocked)

        unlocked_with_owners = fact(locked=False, screensaver=False, owners=True)
        self.assertEqual(
            lock_monitor.validate_step_away_fact(unlocked_with_owners, SESSION, locked=False),
            unlocked_with_owners)

        locked_row = fact(locked=True, screensaver=True, owners=True)
        self.assertEqual(lock_monitor.validate_step_away_fact(locked_row, SESSION, locked=True), locked_row)

        app = Mock(streams=Mock(return_value={'app-stderr': stderr_stream(unlocked, unlocked_with_owners, locked_row)}))
        self.assertEqual(lock_monitor.step_away_facts(app), [unlocked, unlocked_with_owners, locked_row])

        for bad, expected_locked in (
                (fact(locked=False, screensaver=None, can_lock=True), False),
                (fact(locked=True, screensaver=True), False),
                (fact(locked=False, screensaver=False), True),
                (fact(locked=False, screensaver=False, owners=True, logind_owner=None), False),
                (fact(locked=False, screensaver=False, owners=True, screensaver_owner=None), False)):
            with self.subTest(bad=bad, expected_locked=expected_locked), self.assertRaises(ValueError):
                lock_monitor.validate_step_away_fact(bad, SESSION, locked=expected_locked)


class LockMonitorExecutionTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)

    def _run_case(self, case_name, *, monitor_loss=False, owner_recovery=False, startup_none=False,
                  dialog_surfaces=None, alert_result=None, post_confirm_alerts=None,
                  challenge_rows=(), invalidation_rows=(), native_input_extra=None):
        binding = {
            'install_root': '/opt/painted-wolf-code',
            'native_input': {
                'format_version': 1, 'vm_id': 'owned', 'binding': EXECUTION,
                **(native_input_extra or {})}}
        unlocked_obs = {'locked_hint': False, 'locker_active': False}
        locked_obs = {'locked_hint': True, 'locker_active': True}
        proof = copy.deepcopy(native_proof())

        initial_native = Mock(initial=unlocked_obs)
        initial_native.__enter__ = Mock(return_value=initial_native)
        initial_native.__exit__ = Mock(return_value=False)
        initial_native.observe.side_effect = (
            [ValueError('native session observer is closed')]
            if case_name == 'initial-lock-state'
            else [locked_obs, unlocked_obs])

        fresh_native = Mock(initial=unlocked_obs)
        fresh_native.__enter__ = Mock(return_value=fresh_native)
        fresh_native.__exit__ = Mock(return_value=False)
        fresh_native.observe.side_effect = [locked_obs, unlocked_obs]

        initial_lock = Mock(initial=proof)
        initial_lock.__enter__ = Mock(return_value=initial_lock)
        initial_lock.__exit__ = Mock(return_value=False)
        initial_lock.observe.side_effect = ValueError('native lock observer is closed')

        fresh_lock = Mock(initial=proof)
        fresh_lock.__enter__ = Mock(return_value=fresh_lock)
        fresh_lock.__exit__ = Mock(return_value=False)

        f0 = fact(locked=False, screensaver=None, owners=owner_recovery, screensaver_owner=None)
        f1 = fact(locked=False, screensaver=False, owners=owner_recovery)
        f2 = fact(locked=True, screensaver=True, owners=owner_recovery)
        f3 = fact(locked=False, screensaver=False, owners=owner_recovery)
        loss = fact(locked=False, screensaver=None, owners=owner_recovery, screensaver_owner=None)
        recovered = fact(locked=False, screensaver=False, owners=owner_recovery, screensaver_owner=':1.99')
        prefix = (f0, f1) if startup_none else (f1,)
        final_facts = (*prefix, f2, f3, loss, recovered) if (monitor_loss or owner_recovery) else (*prefix, f2, f3)
        fake_app = Mock(observations={
            'session': {**SESSION, 'Active': 'yes', 'Remote': 'no'},
            'launch_identity': {'pid': 100},
            'engine_identity': {'pid': 200}}, binding=binding)
        if case_name == 'lock-during-presence' and dialog_surfaces and dialog_surfaces[0] is not None:
            stream_seq = [
                {'app-stderr': stderr_stream(*prefix)},
                {'app-stderr': stderr_stream(*prefix)},
                {'app-stderr': stderr_stream(*prefix, challenges=challenge_rows)},
                {'app-stderr': stderr_stream(*prefix, challenges=challenge_rows)},
                {'app-stderr': stderr_stream(*prefix, f2, challenges=challenge_rows)},
                {'app-stderr': stderr_stream(*prefix, f2, f3, challenges=challenge_rows)},
                {'app-stderr': stderr_stream(*final_facts, challenges=challenge_rows, invalidations=invalidation_rows)},
            ]
        elif case_name == 'lock-during-presence':
            stream_seq = [
                {'app-stderr': stderr_stream(*prefix)},
                {'app-stderr': stderr_stream(*prefix)},
                {'app-stderr': stderr_stream(*prefix, f2)},
                {'app-stderr': stderr_stream(*prefix, f2, f3)},
                {'app-stderr': stderr_stream(*final_facts, challenges=challenge_rows, invalidations=invalidation_rows)},
            ]
        else:
            stream_seq = [
                {'app-stderr': stderr_stream(*prefix)},
                {'app-stderr': stderr_stream(*prefix, f2)},
                {'app-stderr': stderr_stream(*prefix, f2, f3)},
                {'app-stderr': stderr_stream(*final_facts, challenges=challenge_rows, invalidations=invalidation_rows)},
            ]
        stream_idx = 0

        def next_streams():
            nonlocal stream_idx
            item = stream_seq[min(stream_idx, len(stream_seq) - 1)]
            stream_idx += 1
            return item

        fake_app.streams.side_effect = next_streams

        fake_reservation = Mock()
        fake_reservation.__enter__ = Mock(return_value=fake_reservation)
        fake_reservation.__exit__ = Mock(return_value=False)
        fake_reservation.facts.return_value = {'empty_before': True}

        fake_unlock = Mock(account=ACCOUNT)
        fake_unlock.prepare.return_value = {'request': {'operation': unlock_wire.PREPARE}, 'acknowledgment': {}}
        fake_unlock.unlock.return_value = {'request': {'operation': unlock_wire.PURPOSE}, 'acknowledgment': {}}

        tmp = self.stack.enter_context(tempfile.TemporaryDirectory())
        config_dir = Path(tmp) / 'config'
        config_dir.mkdir(mode=0o700)
        fake_storage = Mock(home=Path(tmp), config=config_dir)
        fake_fixture = Mock(observations=[], acquisition=Mock())
        fake_ui = Mock()
        fake_ui_ctx = MagicMockContext(fake_ui)
        fake_policy = Mock(observations={'rule_installed': True, 'restored': True})
        fake_policy.__enter__ = Mock(return_value=fake_policy)
        fake_policy.__exit__ = Mock(return_value=False)
        dialogs = [None] if dialog_surfaces is None else list(dialog_surfaces)
        default_alert = {'role': 2, 'visible': True, 'attributes': []} if alert_result is None else alert_result

        self.stack.enter_context(patch.object(
            lock_monitor, 'NativeSessionObserver', side_effect=[initial_native, fresh_native]))
        self.stack.enter_context(patch.object(
            lock_monitor, 'NativeLockObserver', side_effect=[initial_lock, fresh_lock]))
        from linux_release.installed_cases import presence_system
        self.stack.enter_context(patch.object(presence_system, 'run_stale_reveal', return_value={}))
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
                (lock_monitor, 'NativeUnlockControl', fake_unlock),
                (provider.ProviderFixture, 'gui', nullcontext(fake_fixture)),
                (lock_monitor, 'configure_provider', {'provider_id': 'loopback'}),
                (lock_monitor, 'run', {
                    'launch_identity': {'pid': 301}, 'exit_code': 0,
                    'timed_out': False, 'output_limit': False,
                    'stdout': b'opened\n', 'stderr': b''}),
                (lock_monitor, 'perform_transition', {'operation': 'session-lock-v1', 'completed': True}),
                (lock_monitor, 'await_session', {'id': 'sess-1', 'project_id': 'project-1', 'status': 'idle'}),
                (lock_monitor, 'prepare_secret', {'phase': 'ready', 'reveal_ready': True}),
                (lock_monitor, 'submit_project_turn', None),
                (lock_monitor, 'await_fixture_reply', {'settled': True})):
            self.stack.enter_context(patch.object(target_mod, attr, return_value=value))
        disable_mock = self.stack.enter_context(patch.object(
            lock_monitor, 'run_privileged_disable', return_value={'disabled': True, 'shadow_locked': True}))
        self.stack.enter_context(patch.object(lock_monitor, 'retain', side_effect=lambda *args: args))
        outcome = lock_monitor.execute(binding, 'session', case_name, Path(tmp))
        return outcome, disable_mock, fake_policy, fake_ui

    def _dialog_identity(self, index):
        app_proc = {'pid': 100, 'parent_pid': 1, 'start_time_ticks': 80}
        return {'bus_name': ':1.77', 'object_path': f'/org/a11y/atspi/accessible/{index}',
                'process': app_proc, 'lineage': [app_proc]}

    def test_initial_lock_state_keeps_monitor_owner_recovery_unverified_on_ordinary_lock_unlock(self):
        outcome, disable_mock, _, _ = self._run_case('initial-lock-state', monitor_loss=False)
        self.assertEqual(outcome[4], 'unverified')
        obs = outcome[5]
        self.assertTrue(obs['initial_lock_state_verified'])
        self.assertTrue(obs['lock_unlock_verified'])
        self.assertFalse(obs['capability_state_recovery_verified'])
        self.assertIsNone(obs['capability_state_recovery'])
        self.assertFalse(obs['monitor_owner_recovery_verified'])
        self.assertIsNone(obs['monitor_owner_recovery'])
        self.assertEqual(obs['reason'], 'initial-lock-state-and-lock-unlock-observed-monitor-owner-recovery-unverified')
        self.assertIn('native', obs['case_observer_recovery']['stale_refusals'])
        self.assertIn('lock', obs['case_observer_recovery']['stale_refusals'])
        self.assertFalse(obs['step_away_initial']['latest']['screensaver_locked'])
        self.assertTrue(obs['during_lock']['locked_hint'])
        self.assertFalse(obs['after_unlock']['locked_hint'])
        self.assertFalse(obs['step_away_unlocked']['latest']['screensaver_locked'])
        self.assertEqual(obs['opened_session'], {'id': 'sess-1', 'project_id': 'project-1', 'status': 'idle'})
        self.assertEqual(obs['settled_reply'], {'settled': True})
        disable_mock.assert_called_once_with(ACCOUNT, vm_id='owned')

    def test_initial_lock_state_does_not_promote_startup_none_to_false_transition_as_capability_recovery(self):
        outcome, disable_mock, _, _ = self._run_case(
            'initial-lock-state', monitor_loss=False, startup_none=True)
        self.assertEqual(outcome[4], 'unverified')
        obs = outcome[5]
        self.assertTrue(obs['initial_lock_state_verified'])
        self.assertTrue(obs['lock_unlock_verified'])
        self.assertFalse(obs['capability_state_recovery_verified'])
        self.assertIsNone(obs['capability_state_recovery'])
        self.assertFalse(obs['monitor_owner_recovery_verified'])
        self.assertIsNone(obs['monitor_owner_recovery'])
        self.assertEqual(obs['reason'], 'initial-lock-state-and-lock-unlock-observed-monitor-owner-recovery-unverified')
        disable_mock.assert_called_once_with(ACCOUNT, vm_id='owned')

    def test_initial_lock_state_records_capability_state_recovery_while_keeping_monitor_owner_recovery_unverified(self):
        outcome, disable_mock, _, _ = self._run_case('initial-lock-state', monitor_loss=True, startup_none=True)
        self.assertEqual(outcome[4], 'unverified')
        obs = outcome[5]
        self.assertTrue(obs['initial_lock_state_verified'])
        self.assertTrue(obs['lock_unlock_verified'])
        self.assertTrue(obs['capability_state_recovery_verified'])
        rec = obs['capability_state_recovery']
        self.assertLess(rec['baseline_index'], rec['loss_index'])
        self.assertLess(rec['loss_index'], rec['recovered_index'])
        self.assertFalse(rec['baseline_fact']['screensaver_locked'])
        self.assertIsNone(rec['loss_fact']['screensaver_locked'])
        self.assertFalse(rec['recovered_fact']['screensaver_locked'])
        self.assertFalse(obs['monitor_owner_recovery_verified'])
        self.assertIsNone(obs['monitor_owner_recovery'])
        self.assertEqual(obs['reason'], 'initial-lock-state-and-lock-unlock-observed-monitor-owner-recovery-unverified')
        disable_mock.assert_called_once_with(ACCOUNT, vm_id='owned')

    def test_initial_lock_state_passes_when_bound_monitor_owner_recovers_to_new_unique_owner(self):
        outcome, disable_mock, _, _ = self._run_case(
            'initial-lock-state', owner_recovery=True, startup_none=True)
        self.assertEqual(outcome[4], 'passed')
        obs = outcome[5]
        self.assertTrue(obs['initial_lock_state_verified'])
        self.assertTrue(obs['lock_unlock_verified'])
        self.assertTrue(obs['capability_state_recovery_verified'])
        self.assertTrue(obs['monitor_owner_recovery_verified'])
        rec = obs['monitor_owner_recovery']
        self.assertEqual(rec['baseline_fact']['screensaver_owner'], ':1.42')
        self.assertIsNone(rec['loss_fact']['screensaver_owner'])
        self.assertEqual(rec['recovered_fact']['screensaver_owner'], ':1.99')
        self.assertEqual(obs['reason'], 'initial-lock-state-lock-unlock-and-monitor-owner-recovery-verified')
        disable_mock.assert_called_once_with(ACCOUNT, vm_id='owned')

    def test_lock_during_presence_requires_real_system_stale_and_fresh_witnesses(self):
        outcome, disable, policy, ui = self._run_case('lock-during-presence')
        self.assertEqual(outcome[4], 'unverified')
        self.assertNotIn('presence_policy', outcome[5])
        policy.__enter__.assert_not_called()
        policy.__exit__.assert_not_called()
        disable.assert_called_once_with(ACCOUNT, vm_id='owned')

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
