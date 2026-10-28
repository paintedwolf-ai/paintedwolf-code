"""Shipping launch routes preserve real command ownership and refuse invented success."""
import signal
import json
import io
import os
import subprocess
import sys
import time
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

    def test_public_modes_keep_real_owner_and_never_enter_controlled_scope(self):
        for mode in ['unscoped-direct', 'desktop-entry', 'pw-open-cold-start']:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                identity = {'pid': 123, 'parent_pid': 100, 'start_time_ticks': 42}
                process = SimpleNamespace(pid=123, stdout=io.BytesIO(), stderr=io.BytesIO())
                events = [{'event': 'launched', 'owner': identity,
                           'identity': {'pid': 124, 'parent_pid': 123, 'start_time_ticks': 43},
                           'cgroup': '0::/session-42.scope'}]
                owner = SimpleNamespace(process=process, facts=lambda: events)
                storage = SimpleNamespace(config=Path(directory) / 'config', application_identifier=APP,
                                          facts=lambda: {'home': directory})
                binding = {'artifact': {}, 'source_commit': 'a' * 40, 'install_root': '/opt/app',
                           'launcher': {'path': str(BOOTSTRAP), 'sha256': 'b' * 64}}
                app = InstalledApp(binding, arguments=[directory], launch_mode=mode)
                with patch(PREFIX + 'app.require_owned_guest'), \
                        patch(PREFIX + 'app.graphical_session', return_value={'id': '42'}), \
                        patch(PREFIX + 'app.session_environment', return_value={'HOME': directory}), \
                        patch(PREFIX + 'app.storage_for_launch', return_value=storage), \
                        patch(PREFIX + 'app.file_identity', side_effect=lambda _path: {'header': b'', 'sha256': 'b' * 64}), \
                        patch(PREFIX + 'app.process_identity', return_value=identity), \
                        patch(PREFIX + 'app.enter_desktop_application_scope') as synthetic_scope, \
                        patch(PREFIX + 'shipping_launch.launch_command', return_value=(['/fixed-route'], {})) as route, \
                        patch(PREFIX + 'launch_owner.LaunchOwner', return_value=owner), \
                        patch.object(Path, 'read_text', return_value='0::/session-42.scope'):
                    app.launch()
                for reader in app._readers:
                    reader.join(timeout=1)
                synthetic_scope.assert_not_called()
                route.assert_called_once_with(mode, BOOTSTRAP, storage, [directory], binding)
                self.assertIs(app.process, process)
                self.assertEqual(app.observations['application_unit']['command_identity']['pid'], 124)
                self.assertEqual(app.observations['launch_identity'], identity)

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

    def test_public_command_uses_flat_package_identity_while_live_engine_uses_sealed_identity(self):
        logical = '/opt/app/bin/pw'
        flat = {'sha256': 'b' * 64, 'device': 1, 'inode': 42, 'bytes': 100, 'mode': 0o755}
        sealed = {**flat, 'inode': 84}
        binding = {'artifact': {}, 'source_commit': 'a' * 40, 'install_root': '/opt/app',
                   'images': {logical: sealed}, 'package_images': {logical: flat}}
        with tempfile.TemporaryDirectory() as directory:
            with patch(PREFIX + 'shipping_launch.file_identity', return_value={**flat, 'header': b'ELF'}):
                command, facts = launch_command('pw-open-cold-start', BOOTSTRAP, None, [directory], binding)
            self.assertEqual(command, [logical, 'open', directory])
            self.assertEqual(facts['cli_image'], flat)
            for changed in ({**flat, 'inode': 84}, {**flat, 'device': 2}, {**flat, 'sha256': 'c' * 64},
                            {**flat, 'bytes': 99}, {**flat, 'mode': 0o777}):
                with patch(PREFIX + 'shipping_launch.file_identity', return_value={**changed, 'header': b'ELF'}):
                    with self.assertRaisesRegex(ValueError, 'differs'):
                        launch_command('pw-open-cold-start', BOOTSTRAP, None, [directory], binding)
            app = InstalledApp(binding)
            app.process = SimpleNamespace(pid=100)
            app.require_engine = Mock()
            engine = {'pid': 200, 'parent_pid': 100, 'start_time_ticks': 12}
            record = {'pid': 200, 'host': '127.0.0.1', 'port': 12345}
            with patch(PREFIX + 'app.process_identity', return_value=engine), \
                    patch(PREFIX + 'app.descends', return_value=[engine]), \
                    patch(PREFIX + 'app.engine_image', return_value=(1, 42, 100)), \
                    patch(PREFIX + 'app.EngineAPI') as api:
                with self.assertRaisesRegex(ValueError, 'running engine differs'):
                    app._bind_engine(record, Path(directory))
                api.assert_not_called()
            with patch(PREFIX + 'app.process_identity', return_value=engine), \
                    patch(PREFIX + 'app.descends', return_value=[engine]), \
                    patch(PREFIX + 'app.engine_image', return_value=(1, 84, 100)), \
                    patch(PREFIX + 'app.EngineAPI') as api:
                api.return_value.request.return_value = {'status': 200}
                self.assertTrue(app._bind_engine(record, Path(directory)))
                self.assertEqual(app.observations['engine_identity'], engine)

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

    def test_detached_launch_keeps_real_ancestry_and_settles_only_owned_children(self):
        from scripts.linux_release.installed_cases.launch_owner import LaunchOwner
        if sys.platform != 'linux':
            with self.assertRaisesRegex(ValueError, 'requires Linux'):
                LaunchOwner([], {})
            return
        sleeper = [sys.executable, '-c', 'import time; time.sleep(60)']
        command = [sys.executable, '-c',
            'import subprocess,sys; subprocess.Popen([sys.executable,"-c","import time; time.sleep(60)"],start_new_session=True)']
        foreign = tempfile.TemporaryDirectory()
        self.addCleanup(foreign.cleanup)
        package = Path(foreign.name) / 'scripts'
        package.mkdir()
        (package / '__init__.py').write_text('raise AssertionError("ambient scripts package imported")')
        environment = {**os.environ, 'PYTHONPATH': foreign.name}
        bystander = subprocess.Popen(sleeper, start_new_session=True)
        owner, owned = None, []
        try:
            owner = LaunchOwner(command, environment)
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                owned = children(owner.process.pid)
                if owned and any(event.get('event') == 'command-exit' for event in owner.facts()):
                    break
                time.sleep(0.02)
            self.assertTrue(owned)
            self.assertTrue(all(row['parent_pid'] == owner.process.pid for row in owned))
            self.assertEqual(owner.facts()[0]['owner']['pid'], owner.process.pid)
            owner.process.terminate()
            self.assertEqual(owner.process.wait(timeout=5), 0)
            owner.reader.join(timeout=3)
            self.assertTrue(owner.closed.is_set())
            self.assertEqual(owner.facts()[-1], {'event': 'settled', 'remaining': []})
            self.assertTrue(all(not Path('/proc', str(row['pid'])).exists() for row in owned))
            self.assertIsNone(bystander.poll())
        finally:
            if owner is not None:
                if owner.process.poll() is None:
                    owner.process.terminate()
                    try:
                        owner.process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        owner.process.kill()
                        owner.process.wait(timeout=5)
                for row in owned:
                    signal_owned(row, signal.SIGKILL)
                owner.reader.join(timeout=3)
                owner.process.stdout.close()
                owner.process.stderr.close()
            bystander.terminate()
            bystander.wait(timeout=5)

    def test_shipping_mode_is_explicit_and_unknown_modes_refuse(self):
        from scripts.linux_release.installed_cases.shipping_lifecycle import selected_mode
        self.assertEqual(selected_mode({}), 'controlled')
        self.assertEqual(selected_mode({'case_inputs': {'desktop': {'sleep_launch_mode': 'desktop-entry'}}}), 'desktop-entry')
        with self.assertRaisesRegex(ValueError, 'unsupported'):
            selected_mode({'case_inputs': {'desktop': {'sleep_launch_mode': 'pretend-desktop'}}})

    def test_second_turn_failure_preserves_prior_observations_and_raw_streams(self):
        from scripts.linux_release.installed_cases.shipping_lifecycle import repeat_turn
        from scripts.linux_release.installed_cases.records import CaseUnavailable
        barrier = SimpleNamespace(snapshot=lambda: {'request_id': 'second-held-request'})
        fixture = SimpleNamespace(begin_next_turn=lambda: barrier)
        failure = CaseUnavailable('fixed-UI-prerequisite-unavailable')
        failure.observations = {'native_detail': 'retained'}
        opened = {'launch_identity': {'pid': 123}, 'exit_code': 0, 'timed_out': False,
                  'output_limit': False, 'stdout': b'opened project', 'stderr': b''}
        streams = {}
        with tempfile.TemporaryDirectory() as root, \
                patch(PREFIX + 'process.run', return_value=opened), \
                patch(PREFIX + 'profile_setup.await_session', side_effect=[{'id': 'fresh-session'}, failure]), \
                patch(PREFIX + 'desktop_turn.submit_project_turn'), \
                patch(PREFIX + 'desktop_ui.DesktopUI'):
            with self.assertRaises(CaseUnavailable) as captured:
                repeat_turn(None, {}, fixture, {'provider_id': 'fixture'}, None, {}, root, streams)
        observed = captured.exception.observations
        self.assertEqual(observed['native_detail'], 'retained')
        self.assertEqual(observed['shipping_repeat']['opened_session']['id'], 'fresh-session')
        self.assertEqual(observed['shipping_repeat']['response_control']['request_id'], 'second-held-request')
        self.assertEqual(streams['shipping-repeat-project-open-stdout'], b'opened project')
        json.dumps(observed)

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
        native._call.side_effect = [('/org/freedesktop/systemd1/unit/app',),
                                    CallFailure('org.freedesktop.DBus.Error.Disconnected')]
        with self.assertRaises(CaseUnavailable):
            native.application_unit_state(unit)


if __name__ == '__main__':
    unittest.main()
