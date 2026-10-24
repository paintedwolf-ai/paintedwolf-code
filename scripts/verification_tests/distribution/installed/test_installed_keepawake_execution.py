"""Sleep ownership is exact engine custody, independent of provider and locker assertions."""
from contextlib import ExitStack
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import keepawake

ENGINE = {'pid': 200, 'parent_pid': 100, 'start_time_ticks': 42}
OWNER = {**ENGINE, 'uid': 30008}


class EngineInhibitorTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.app = Mock(observations={'engine_identity': ENGINE}, binding={
            'install_root': '/opt/painted-wolf-code',
            'images': {'/opt/painted-wolf-code/bin/pw': {'device': 1, 'inode': 2, 'bytes': 3}}})
        self.native = Mock()
        self.native.inhibitors.return_value = {'native_session': {'locked_hint': True}, 'inhibitors': []}
        self.stack.enter_context(patch.object(keepawake.os, 'getuid', return_value=30008))
        self.owner = self.stack.enter_context(patch.object(keepawake, 'kernel_owner', return_value=OWNER))
        self.process = self.stack.enter_context(patch.object(keepawake, 'process_identity', return_value=ENGINE))
        self.image = self.stack.enter_context(patch.object(keepawake, 'engine_image', return_value=(1, 2, 3)))

    def test_actual_engine_sleep_only_row_survives_independent_locked_session_observation(self):
        row = {'what': 'sleep', 'mode': 'block', 'owner': OWNER}
        self.native.inhibitors.return_value['inhibitors'] = [row,
            {'what': 'idle', 'mode': 'block', 'owner': {**OWNER, 'pid': 201}}]
        result = keepawake.observe(self.app, self.native)
        self.assertEqual(result['engine_inhibitors'], [row])
        self.assertEqual(result['engine'], OWNER)
        self.assertTrue(result['native_session']['locked_hint'])
        self.app.require_running.assert_called()
        self.app.require_engine.assert_called()

    def test_broker_pid_cannot_establish_engine_inhibition(self):
        self.native.inhibitors.return_value['inhibitors'] = [
            {'what': 'sleep', 'mode': 'block', 'owner': {**OWNER, 'pid': 201}}]
        self.assertEqual(keepawake.observe(self.app, self.native)['engine_inhibitors'], [])

    def test_wrong_native_scope_owner_mode_or_multiple_engine_rows_refuse(self):
        row = {'what': 'sleep', 'mode': 'block', 'owner': OWNER}
        for rows in ([{**row, 'what': 'idle:sleep'}], [{**row, 'mode': 'delay'}],
                     [{**row, 'owner': {**OWNER, 'start_time_ticks': 43}}], [row, row]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                self.native.inhibitors.return_value['inhibitors'] = rows
                keepawake.observe(self.app, self.native)

    def test_replaced_image_or_engine_during_observation_refuses(self):
        self.image.return_value = (1, 3, 3)
        with self.assertRaises(ValueError):
            keepawake.observe(self.app, self.native)
        self.image.return_value = (1, 2, 3)
        self.owner.side_effect = [OWNER, {**OWNER, 'start_time_ticks': 43}]
        with self.assertRaises(ValueError):
            keepawake.observe(self.app, self.native)

    def test_terminal_release_requires_no_engine_row_while_engine_stays_alive(self):
        held = {'native_session': {}, 'inhibitors': [{'what': 'sleep', 'mode': 'block', 'owner': OWNER}]}
        self.native.inhibitors.side_effect = [held, {'native_session': {}, 'inhibitors': []}]
        with patch.object(keepawake.time, 'sleep'), patch.object(keepawake.time, 'monotonic', return_value=100):
            result = keepawake.await_inhibitor(self.app, self.native, held=False, expected=OWNER, deadline=115)
        self.assertEqual(result['engine_inhibitors'], [])
        self.assertEqual(result['engine'], OWNER)

    def test_execute_proves_inhibitor_across_lock_unlock_and_disables_credential_in_finally(self):
        from contextlib import nullcontext
        from pathlib import Path
        import tempfile
        from linux_release.installed_cases import (
            app as app_mod, desktop_launch, desktop_turn, desktop_ui,
            fixture_turn, native_session, process, profile_setup,
            provider, provider_barrier, provider_recovery, provider_setup, records)
        from linux_release.installed_input import session_lock, transition_guest, unlock_account, unlock_control

        account = {'name': 'pwqual058a1', 'uid': 30008, 'gid': 30008, 'home': '/home/pwqual058a1'}
        binding = {
            'install_root': '/opt/painted-wolf-code',
            'images': {'/opt/painted-wolf-code/bin/pw': {'device': 1, 'inode': 2, 'bytes': 3}},
            'native_input': {
                'format_version': 1, 'vm_id': 'owned',
                'binding': {'format_version': 1, 'execution_nonce': 'a' * 32, 'source_commit': 'b' * 40,
                            'driver_tree': 'c' * 40, 'artifact_id': 'app',
                            'environment_id': 'noble', 'variant_id': 'gnome-wayland'}}}
        unlocked_session = {'locked_hint': False, 'locker_active': False}
        locked_session = {'locked_hint': True, 'locker_active': True}
        held_row = {'what': 'sleep', 'mode': 'block', 'owner': OWNER}
        native_observer = Mock()
        native_observer.__enter__ = Mock(return_value=native_observer)
        native_observer.__exit__ = Mock(return_value=False)
        native_observer.inhibitors.side_effect = [
            {'native_session': unlocked_session, 'inhibitors': []},
            {'native_session': unlocked_session, 'inhibitors': [held_row]},
            {'native_session': locked_session, 'inhibitors': [held_row]},
            {'native_session': unlocked_session, 'inhibitors': [held_row]},
            {'native_session': unlocked_session, 'inhibitors': []},
            {'native_session': unlocked_session, 'inhibitors': []}]

        lock_observer = Mock()
        lock_observer.__enter__ = Mock(return_value=lock_observer)
        lock_observer.__exit__ = Mock(return_value=False)

        fake_app = Mock(
            observations={'engine_identity': ENGINE, 'session': {'id': '2', 'User': '30008'}},
            binding=binding)
        fake_app.__enter__ = Mock(return_value=fake_app)
        fake_app.__exit__ = Mock(return_value=False)
        fake_app.streams.return_value = {}

        fake_fixture = Mock(observations=[{'completed': False}], acquisition=Mock())
        fake_barrier = Mock()
        fake_barrier.await_held.return_value = {'state': 'held'}
        fake_barrier.require_held.return_value = {'state': 'held'}
        fake_barrier.await_response_written.return_value = {'state': 'released', 'response_written': True}

        fake_unlock = Mock(account=account)
        fake_unlock.prepare.return_value = {'request': {'operation': 'native-gnome-qa-prepare-v1'}, 'acknowledgment': {}}
        fake_unlock.unlock.return_value = {'request': {'operation': 'native-gnome-qa-unlock-v1'}, 'acknowledgment': {}}

        with tempfile.TemporaryDirectory() as tmp, \
                patch.object(desktop_launch, 'protected_launcher', return_value={}), \
                patch.object(provider_recovery, 'load', return_value=None), \
                patch.object(provider_barrier, 'ResponseBarrier', return_value=fake_barrier), \
                patch.object(provider.ProviderFixture, 'gui', return_value=nullcontext(fake_fixture)), \
                patch.object(app_mod, 'InstalledApp', return_value=fake_app), \
                patch.object(desktop_ui, 'interactive_window', return_value={'window': 'ok'}), \
                patch.object(desktop_ui, 'DesktopUI', return_value=nullcontext()), \
                patch.object(native_session, 'NativeSessionObserver', return_value=native_observer), \
                patch.object(session_lock, 'NativeLockObserver', return_value=lock_observer), \
                patch.object(unlock_control, 'NativeUnlockControl', return_value=fake_unlock), \
                patch.object(unlock_account, ' run_privileged_disable' if False else 'run_privileged_disable',
                             return_value={'disabled': True, 'shadow_locked': True}) as disable_mock, \
                patch.object(provider_setup, 'configure_provider', return_value={'provider_id': 'loopback'}), \
                patch.object(process, 'run', return_value={'exit_code': 0, 'timed_out': False, 'output_limit': False}), \
                patch.object(desktop_turn, 'submit_project_turn'), \
                patch.object(profile_setup, 'active_session', return_value={'id': 'sess-1'}), \
                patch.object(profile_setup, 'require_active', return_value={'id': 'sess-1'}), \
                patch.object(transition_guest, 'perform_transition', return_value={'operation': 'session-lock-v1', 'completed': True}), \
                patch.object(fixture_turn, 'await_fixture_reply', return_value={'settled': True}), \
                patch.object(records, 'retain', side_effect=lambda *args: args):
            outcome = keepawake.execute(binding, 'session', 'inhibitor-lifetime', Path(tmp))

        self.assertEqual(outcome[4], 'passed')
        retained_obs = outcome[5]
        self.assertEqual(retained_obs['inhibitor_initial']['engine_inhibitors'], [])
        self.assertEqual(retained_obs['before_lock']['engine_inhibitors'], [held_row])
        self.assertEqual(retained_obs['during_lock']['engine_inhibitors'], [held_row])
        self.assertTrue(retained_obs['during_lock']['native_session']['locked_hint'])
        self.assertEqual(retained_obs['after_unlock']['engine_inhibitors'], [held_row])
        self.assertFalse(retained_obs['after_unlock']['native_session']['locked_hint'])
        self.assertEqual(retained_obs['after_completion']['engine_inhibitors'], [])
        self.assertEqual(retained_obs['after_exit']['engine_inhibitors'], [])
        disable_mock.assert_called_once_with(account, vm_id='owned')
