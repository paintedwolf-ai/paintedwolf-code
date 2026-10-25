"""Shipping launch routes preserve real command ownership and refuse invented success."""
import signal
import tempfile
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_cases.app import InstalledApp
from scripts.linux_release.installed_cases.application_scope import observe_engine_scope
from scripts.linux_release.installed_cases.launch_owner import children, reap, signal_owned
from scripts.linux_release.installed_cases.shipping_launch import launch_command

PREFIX = 'scripts.linux_release.installed_cases.'
BOOTSTRAP = Path('/usr/libexec/painted-wolf-code/pw-launch')
APP = 'dev.paintedwolf.code'


class ShippingLaunchTests(unittest.TestCase):
    def test_only_declared_routes_and_final_protected_launcher_are_selectable(self):
        binding = {'artifact': {}, 'source_commit': 'a' * 40}
        with self.assertRaisesRegex(ValueError, 'unsupported'):
            InstalledApp(binding, launch_mode='synthetic-success')
        with self.assertRaisesRegex(ValueError, 'protected bootstrap'):
            launch_command('unscoped-direct', Path('/tmp/desktop'), None, [], binding)
        command, facts = launch_command('unscoped-direct', BOOTSTRAP, None, [], binding)
        self.assertEqual(command, [str(BOOTSTRAP)])
        self.assertFalse(facts['desktop_entry_launch_proven'])
        self.assertEqual(InstalledApp(binding).launch_mode, 'controlled')

    def test_cold_cli_opens_existing_project_with_exact_bound_image(self):
        with tempfile.TemporaryDirectory() as directory:
            expected = {'sha256': 'b' * 64, 'inode': 42}
            binding = {'install_root': '/opt/app', 'images': {'/opt/app/bin/pw': expected}}
            with patch(PREFIX + 'shipping_launch.file_identity', return_value={**expected, 'header': b'ELF'}):
                command, facts = launch_command('pw-open-cold-start', BOOTSTRAP, None, [directory], binding)
                self.assertEqual(command, ['/opt/app/bin/pw', 'open', directory])
                self.assertEqual(facts['cli_image'], expected)
            with patch(PREFIX + 'shipping_launch.file_identity', return_value={'header': b'ELF', 'inode': 43}):
                with self.assertRaisesRegex(ValueError, 'differs'):
                    launch_command('pw-open-cold-start', BOOTSTRAP, None, [directory], binding)
            with self.assertRaisesRegex(ValueError, 'one existing project'):
                launch_command('pw-open-cold-start', BOOTSTRAP, None, [], binding)

    def test_desktop_route_invokes_gio_and_refuses_dbus_or_alternate_exec(self):
        text = ('[Desktop Entry]\nType=Application\nExec=' + str(BOOTSTRAP)
                + ' %U\nStartupWMClass=' + APP + '\n')
        storage = SimpleNamespace(application_identifier=APP)
        with patch(PREFIX + 'shipping_launch.file_identity', return_value={'header': b'', 'mode': 0o644}), \
                patch.object(Path, 'lstat', return_value=SimpleNamespace(st_uid=0)), \
                patch.object(Path, 'read_text', return_value=text):
            command, facts = launch_command('desktop-entry', BOOTSTRAP, storage, [], {})
            self.assertEqual(command, ['/usr/bin/gio', 'launch', '/usr/share/applications/' + APP + '.desktop'])
            self.assertTrue(facts['desktop_entry_invoked'])
            self.assertFalse(facts['desktop_entry_launch_proven'])
        for invalid in [text.replace(str(BOOTSTRAP), '/bin/true'), text + 'DBusActivatable=true\n']:
            with patch(PREFIX + 'shipping_launch.file_identity', return_value={'header': b'', 'mode': 0o644}), \
                    patch.object(Path, 'lstat', return_value=SimpleNamespace(st_uid=0)), \
                    patch.object(Path, 'read_text', return_value=invalid):
                with self.assertRaisesRegex(ValueError, 'does not execute'):
                    launch_command('desktop-entry', BOOTSTRAP, storage, [], {})

    def test_unscoped_engine_may_only_inherit_or_enter_its_own_application_unit(self):
        initial = '0::/user.slice/user-1000.slice/session-42.scope'
        info = {'launch_mode': 'unscoped-direct', 'launch_cgroup': initial, 'application_id': APP}
        own = '0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-paintedwolf-' + APP + '-321.scope'
        with patch(PREFIX + 'application_scope.os.getuid', return_value=1000):
            for allowed in [initial, own]:
                with patch.object(Path, 'read_text', return_value=allowed):
                    self.assertEqual(observe_engine_scope(info, 321)['engine_cgroup'], allowed)
            for denied in [own.replace(APP, 'org.foreign.App'), own.replace('user@1000', 'user@1001'), own + '\n0::/other']:
                with patch.object(Path, 'read_text', return_value=denied):
                    with self.assertRaisesRegex(ValueError, 'outside its own'):
                        observe_engine_scope(info, 321)
            with patch.object(Path, 'read_text', return_value=own):
                with self.assertRaisesRegex(ValueError, 'escaped'):
                    observe_engine_scope({**info, 'launch_mode': 'controlled-systemd-application-scope', 'unit': 'old.scope'}, 321)

    def test_pid_reuse_refuses_signal_and_reparenting_preserves_birth_identity(self):
        captured = {'pid': 321, 'parent_pid': 123, 'start_time_ticks': 42}
        with patch(PREFIX + 'launch_owner.os.pidfd_open', return_value=77, create=True), \
                patch(PREFIX + 'launch_owner.os.close') as close, \
                patch(PREFIX + 'launch_owner.signal.pidfd_send_signal', create=True) as kill, \
                patch(PREFIX + 'launch_owner.process_identity', return_value={**captured, 'start_time_ticks': 43}):
            with self.assertRaisesRegex(ValueError, 'identity changed'):
                signal_owned(captured, signal.SIGTERM)
            kill.assert_not_called()
            close.assert_called_once_with(77)
        with patch(PREFIX + 'launch_owner.os.pidfd_open', return_value=78, create=True), \
                patch(PREFIX + 'launch_owner.os.close') as close, \
                patch(PREFIX + 'launch_owner.signal.pidfd_send_signal', create=True) as kill, \
                patch(PREFIX + 'launch_owner.process_identity', return_value={**captured, 'parent_pid': 999}):
            signal_owned(captured, signal.SIGTERM)
            kill.assert_called_once_with(78, signal.SIGTERM)
            close.assert_called_once_with(78)

    def test_cleanup_reaps_adopted_children_without_stealing_popen_wait_ownership(self):
        rows = [{'pid': 1, 'parent_pid': 100}, {'pid': 2, 'parent_pid': 100}, {'pid': 3, 'parent_pid': 2}]
        with patch(PREFIX + 'launch_owner.os.getpid', return_value=100), \
                patch(PREFIX + 'launch_owner.children', return_value=rows), \
                patch(PREFIX + 'launch_owner.os.waitpid') as wait:
            reap(1)
            wait.assert_called_once_with(2, 1)

    def test_descendant_capture_includes_children_created_by_nonleader_threads(self):
        task1, task2 = Mock(), Mock()
        task1.read_text.return_value = ''
        task2.read_text.return_value = '321'
        row = {'pid': 321, 'parent_pid': 100, 'start_time_ticks': 42}
        def tasks(path, _pattern):
            return [task1, task2] if str(path) == '/proc/100/task' else []
        with patch.object(Path, 'glob', autospec=True, side_effect=tasks), \
                patch(PREFIX + 'launch_owner.process_identity', return_value=row):
            self.assertEqual(children(100), [row])

    def test_shipping_mode_is_explicit_and_unknown_modes_refuse(self):
        from scripts.linux_release.installed_cases.shipping_lifecycle import selected_mode
        self.assertEqual(selected_mode({}), 'controlled')
        self.assertEqual(selected_mode({'case_inputs': {'desktop': {'sleep_launch_mode': 'desktop-entry'}}}), 'desktop-entry')
        with self.assertRaisesRegex(ValueError, 'unsupported'):
            selected_mode({'case_inputs': {'desktop': {'sleep_launch_mode': 'pretend-desktop'}}})

    def test_native_inhibition_flags_are_pinned_to_session_manager_owner(self):
        from scripts.linux_release.installed_cases.native_session import NativeSessionObserver
        native = NativeSessionObserver.__new__(NativeSessionObserver)
        owner = {'unique': ':1.42', 'process': {'pid': 42}}
        native._bus_owner = Mock(return_value=owner)
        native._call = Mock(return_value=(4,))
        self.assertEqual(native.suspend_inhibition(), {'owner': owner, 'inhibited_actions': 4})
        self.assertEqual(native._call.call_args.args[0], ':1.42')
        native._bus_owner.side_effect = [owner, {'unique': ':1.43'}]
        with self.assertRaisesRegex(ValueError, 'changed'):
            native.suspend_inhibition()

    def test_unit_query_errors_are_not_treated_as_successful_cleanup(self):
        from scripts.linux_release.installed_cases.native_session import NativeSessionObserver
        from scripts.linux_release.installed_cases.logind_context import CallFailure
        from scripts.linux_release.installed_cases.records import CaseUnavailable
        native = NativeSessionObserver.__new__(NativeSessionObserver)
        unit = 'app-paintedwolf-' + APP + '-321.scope'
        native._call = Mock(side_effect=CallFailure('org.freedesktop.DBus.Error.AccessDenied'))
        with self.assertRaises(CaseUnavailable):
            native.application_unit_state(unit)
        native._call.side_effect = CallFailure('org.freedesktop.systemd1.NoSuchUnit')
        self.assertEqual(native.application_unit_state(unit)['active_state'], 'absent')
        native._call.side_effect = [('/org/freedesktop/systemd1/unit/app',), ('active',)]
        with self.assertRaisesRegex(ValueError, 'scope active'):
            native.application_unit_state(unit)


if __name__ == '__main__':
    unittest.main()
