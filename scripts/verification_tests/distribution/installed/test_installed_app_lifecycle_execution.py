"""The installed app owner launches the selected launcher in the native session and captures its engine."""
import io
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import app as owner
from linux_release.installed_cases.app import InstalledApp
from linux_release.installed_cases.records import CaseUnavailable

STAT = '42 (pw engine) ' + ' '.join(['S', '7'] + ['0'] * 17 + ['999']) + '\n'
SESSION = {'id': '3', 'Type': 'x11', 'Desktop': 'GNOME', 'Display': ':1'}
LAUNCHER = {'path': '/opt/painted-wolf-code/bin/painted-wolf-code', 'sha256': 'a' * 64, 'bytes': 10,
            'mode': 0o755, 'device': 1, 'inode': 2}
IDENTITY = {'pid': 4242, 'parent_pid': 1, 'start_time_ticks': 9}


def binding(**changes):
    return {'artifact': {'id': 'app'}, 'source_commit': 'c' * 40, 'launcher': dict(LAUNCHER), 'images': {},
            'install_root': '/opt/painted-wolf-code', **changes}


class ProcessFactsTests(unittest.TestCase):
    def test_process_identity_reads_the_parent_and_start_time_fields(self):
        with patch.object(Path, 'read_text', lambda self: STAT):
            self.assertEqual(owner.process_identity(42), {'pid': 42, 'parent_pid': 7, 'start_time_ticks': 999})
        with patch.object(Path, 'read_text', lambda self: '42 (short) S 7\n'), self.assertRaises(ValueError):
            owner.process_identity(42)

    def test_private_records_must_be_small_owned_and_unshared(self):
        with tempfile.TemporaryDirectory() as temporary:
            record = Path(temporary) / 'daemon.json'
            record.write_bytes(b'{"pid": 5, "port": 8787}')
            record.chmod(0o600)
            self.assertEqual(owner.private_json(record), {'pid': 5, 'port': 8787})
            record.chmod(0o644)
            with self.assertRaises(ValueError):
                owner.private_json(record)
            record.chmod(0o600)
            record.write_bytes(b'{"pad": "' + b'x' * 4096 + b'"}')
            with self.assertRaises(ValueError):
                owner.private_json(record)
            with self.assertRaises(FileNotFoundError):
                owner.private_json(Path(temporary) / 'absent.json')


class SessionEnvironmentTests(unittest.TestCase):
    def environment(self, session, sockets=('bus',), runtime_uid=None, authority=True):
        uid = os.getuid()
        with tempfile.TemporaryDirectory() as temporary:
            home, runtime = Path(temporary) / 'home', Path(temporary) / 'runtime'
            home.mkdir()
            runtime.mkdir()
            if authority:
                (home / '.Xauthority').write_bytes(b'cookie')
            for name in {*sockets, 'bus'}:
                (runtime / name).write_bytes(b'')
            native_stat, native_lstat, native = Path.stat, Path.lstat, owner.Path

            def facts(path, follow, **kwargs):
                if path.name in sockets:
                    return SimpleNamespace(st_mode=stat.S_IFSOCK | 0o600, st_uid=uid)
                info = (native_stat if follow else native_lstat)(path, **kwargs)
                if path == runtime and runtime_uid is not None:
                    return SimpleNamespace(st_mode=info.st_mode, st_uid=runtime_uid)
                return info
            paths = MagicMock(side_effect=lambda value='.': runtime if str(value) == f'/run/user/{uid}' else native(value))
            paths.home.return_value = home
            with patch.object(Path, 'stat', lambda p, **k: facts(p, True, **k)), \
                    patch.object(Path, 'lstat', lambda p, **k: facts(p, False, **k)), \
                    patch.object(owner, 'Path', paths), \
                    patch.object(owner, 'desktop_display', return_value=':7') as display:
                result = owner.session_environment(session)
            return result, home, runtime, display

    def test_an_x11_session_gets_its_display_bus_and_authority(self):
        result, home, runtime, display = self.environment(SESSION)
        self.assertEqual(result['HOME'], str(home))
        self.assertEqual((result['USER'], result['LOGNAME']), ('home', 'home'))
        self.assertEqual(result['XDG_RUNTIME_DIR'], str(runtime))
        self.assertEqual(result['DBUS_SESSION_BUS_ADDRESS'], 'unix:path=' + str(runtime / 'bus'))
        self.assertEqual((result['XDG_SESSION_ID'], result['XDG_SESSION_TYPE'], result['XDG_CURRENT_DESKTOP']), ('3', 'x11', 'GNOME'))
        self.assertEqual((result['DISPLAY'], result['XAUTHORITY']), (':7', str(home / '.Xauthority')))
        self.assertEqual(result['LC_ALL'], 'C.UTF-8')
        display.assert_called_once_with(SESSION)
        result, _, _, _ = self.environment(SESSION, authority=False)
        self.assertNotIn('XAUTHORITY', result)
        self.assertNotIn('WAYLAND_DISPLAY', result)

    def test_a_wayland_session_selects_its_unique_display_socket(self):
        wayland = dict(SESSION, Type='wayland')
        result, _, _, display = self.environment(wayland, sockets=('bus', 'wayland-0'))
        self.assertEqual(result['WAYLAND_DISPLAY'], 'wayland-0')
        self.assertNotIn('DISPLAY', result)
        display.assert_not_called()
        with self.assertRaises(CaseUnavailable):
            self.environment(wayland, sockets=('bus',))
        with self.assertRaises(CaseUnavailable):
            self.environment(wayland, sockets=('bus', 'wayland-0', 'wayland-1'))

    def test_a_foreign_runtime_directory_or_missing_bus_is_unavailable(self):
        with self.assertRaises(CaseUnavailable):
            self.environment(SESSION, runtime_uid=0)
        with self.assertRaises(CaseUnavailable):
            self.environment(SESSION, sockets=('other',))


class ConstructionTests(unittest.TestCase):
    def test_arguments_must_be_existing_absolute_fixture_paths_and_the_timeout_bounded(self):
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Path(temporary) / 'fixture.txt'
            fixture.write_text('fixture')
            app = InstalledApp(binding(), arguments=[str(fixture), 'file://' + str(fixture)], timeout=5)
            self.assertEqual(app.arguments, [str(fixture), 'file://' + str(fixture)])
            self.assertEqual(app.observations, {'artifact': {'id': 'app'}, 'source_commit': 'c' * 40})
            self.assertEqual(app.streams(), {'app-stdout': b'', 'app-stderr': b''})
            for arguments in [['relative/fixture.txt'], [str(Path(temporary) / 'absent.txt')], ['https://example.invalid/fixture'],
                              [5], [str(fixture) + '\0'], ['file://host' + str(fixture)]]:
                with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                    InstalledApp(binding(), arguments=arguments)
            for timeout in (0, -1, 301):
                with self.subTest(timeout=timeout), self.assertRaises(ValueError):
                    InstalledApp(binding(), timeout=timeout)


class Launch:
    """Patch the guest, session, launcher identity and process around one launch."""

    def __init__(self, *, stdout=b'out', stderr=b'err', identity=None, daemon=None, live_pid=None, limit=owner.LIMIT):
        self.stdout, self.stderr, self.daemon, self.live_pid, self.limit = stdout, stderr, daemon, live_pid, limit
        self.identity = {key: value for key, value in LAUNCHER.items() if key != 'path'} if identity is None else identity

    def __enter__(self):
        self.temporary = tempfile.TemporaryDirectory()
        home = Path(self.temporary.name)
        config = home / '.config/paintedwolf'
        config.mkdir(parents=True)
        if self.daemon is not None:
            (config / 'daemon.json').write_bytes(self.daemon)
            (config / 'daemon.json').chmod(0o600)
        self.process = MagicMock()
        self.process.pid = 4242
        self.process.stdout, self.process.stderr = io.BytesIO(self.stdout), io.BytesIO(self.stderr)
        self.process.poll.return_value = None
        native_exists = Path.exists
        self.patches = [patch.object(owner, 'require_owned_guest'), patch.object(owner, 'graphical_session', return_value=SESSION),
                        patch.object(owner, 'session_environment', return_value={'DISPLAY': ':1'}),
                        patch.object(Path, 'home', return_value=home),
                        patch.object(Path, 'exists', lambda p, **k: True if str(p) == f'/proc/{self.live_pid}' else native_exists(p, **k)),
                        patch.object(owner, 'file_identity', return_value=dict(self.identity, header=b'ELF')),
                        patch.object(owner.subprocess, 'Popen', return_value=self.process),
                        patch.object(owner, 'process_identity', return_value=IDENTITY),
                        patch.object(owner, 'stop_stack'), patch.object(owner, 'LIMIT', self.limit)]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        self.temporary.cleanup()


class LaunchTests(unittest.TestCase):
    def test_the_bound_launcher_starts_in_the_native_session_with_captured_streams(self):
        with Launch() as launch:
            app = InstalledApp(binding(), arguments=['/'])
            self.assertIs(app.launch(), app)
            for reader in app._readers:
                reader.join(5)
            self.assertEqual(launch.mocks[6].call_args.args[0], [LAUNCHER['path'], '/'])
            self.assertEqual(launch.mocks[6].call_args.kwargs['env'], {'DISPLAY': ':1'})
            self.assertTrue(launch.mocks[6].call_args.kwargs['start_new_session'])
            launch.mocks[0].assert_called_once_with(binding())
            launch.mocks[5].assert_called_once_with(Path(LAUNCHER['path']))
            self.assertEqual(app.observations['launcher'], LAUNCHER)
            self.assertEqual((app.observations['session'], app.observations['launch_identity']), (SESSION, IDENTITY))
            self.assertEqual(app.streams(), {'app-stdout': b'out', 'app-stderr': b'err'})
            with self.assertRaises(ValueError):
                app.launch()
            app.stop()
            launch.mocks[8].assert_called_once_with(launch.process)
            self.assertEqual((app.observations['exit_code'], app.observations['output_limit'], app.observations['capture_complete']), (None, False, True))
            self.assertIsNone(app.api)

    def test_output_beyond_the_bound_marks_the_capture_as_overflowed(self):
        with Launch(stdout=b'0123456789', limit=4):
            app = InstalledApp(binding())
            app.launch()
            for reader in app._readers:
                reader.join(5)
            app.stop()
            self.assertTrue(app.observations['output_limit'])
            self.assertEqual(app.streams()['app-stdout'], b'')
            self.assertEqual(app.streams()['app-stderr'], b'err')

    def test_a_changed_launcher_or_a_running_guest_engine_never_launches(self):
        with Launch(identity=dict(LAUNCHER, inode=3)) as launch:
            with self.assertRaises(ValueError):
                InstalledApp(binding()).launch()
            launch.mocks[6].assert_not_called()
        with Launch(daemon=b'{"pid": 77, "port": 1}', live_pid=77) as launch:
            with self.assertRaises(CaseUnavailable):
                InstalledApp(binding()).launch()
            launch.mocks[6].assert_not_called()
        with Launch(daemon=b'{"pid": 77, "port": 1}', live_pid=None) as launch:
            app = InstalledApp(binding())
            app.launch()
            for reader in app._readers:
                reader.join(5)
            launch.mocks[6].assert_called_once()
            app.stop()

    def test_stopping_an_unlaunched_app_changes_nothing(self):
        app = InstalledApp(binding())
        with patch.object(owner, 'stop_stack') as stop:
            app.stop()
        stop.assert_not_called()
        self.assertEqual(app.observations, {'artifact': {'id': 'app'}, 'source_commit': 'c' * 40})


class StartTests(unittest.TestCase):
    def test_start_launches_onboards_and_waits_for_the_engine(self):
        from linux_release.installed_cases import onboarding
        app = InstalledApp(binding())
        with patch.object(InstalledApp, 'launch') as launch, patch.object(onboarding, 'onboard') as onboard, \
                patch.object(InstalledApp, 'wait_ready', return_value='ready') as ready, patch.object(InstalledApp, 'stop') as stop:
            self.assertEqual(app.start(), 'ready')
            with app as entered:
                self.assertEqual(entered, 'ready')
        launch.assert_called()
        onboard.assert_called_with(app)
        ready.assert_called()
        stop.assert_called_once_with()

    def test_a_failed_onboarding_stops_the_app_and_keeps_its_evidence(self):
        from linux_release.installed_cases import onboarding
        app = InstalledApp(binding())
        with patch.object(InstalledApp, 'launch'), patch.object(onboarding, 'onboard', side_effect=CaseUnavailable('no window')), \
                patch.object(InstalledApp, 'stop') as stop:
            with self.assertRaises(CaseUnavailable) as refused:
                app.start()
        stop.assert_called_once_with()
        self.assertIs(refused.exception.observations, app.observations)
        self.assertEqual(refused.exception.streams, {'app-stdout': b'', 'app-stderr': b''})


class ReadinessTests(unittest.TestCase):
    def test_readiness_needs_a_launched_process_and_returns_on_the_first_ready_probe(self):
        app = InstalledApp(binding())
        with self.assertRaises(ValueError):
            app.wait_ready()
        self.assertFalse(app.try_ready())
        app.process = Mock()
        app.process.poll.return_value = None
        with patch.object(InstalledApp, 'try_ready', return_value=True):
            self.assertIs(app.wait_ready(), app)
        with tempfile.TemporaryDirectory() as temporary, patch.object(Path, 'home', return_value=Path(temporary)):
            self.assertFalse(app.try_ready())

    def test_requests_need_a_captured_ready_engine(self):
        app = InstalledApp(binding())
        with self.assertRaises(CaseUnavailable):
            app.request('GET', '/health')
        app.api = Mock()
        app.api.request.return_value = {'status': 200}
        self.assertEqual(app.request('GET', '/health'), {'status': 200})
        app.api.request.assert_called_once_with('GET', '/health', None)


if __name__ == '__main__':
    unittest.main()
