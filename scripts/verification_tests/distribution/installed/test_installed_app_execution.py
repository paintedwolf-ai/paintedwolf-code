"""Actual application identity and platform-owner boundaries fail closed."""
import io
import os
import re
import tempfile
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_process import engine_image
from scripts.linux_release.installed_cases.app import InstalledApp
from scripts.linux_release.installed_cases.records import CaseUnavailable, CaseFailed
from scripts.linux_release.installed_environment import graphical_session
from scripts.linux_release.installed_gnome import owner as gnome_owner


class InstalledAppTests(unittest.TestCase):
    def test_nondumpable_engine_uses_only_fixed_read_only_image_observer(self):
        identity = {"pid": 321, "parent_pid": 123, "start_time_ticks": 456}
        result = SimpleNamespace(returncode=0, stdout=b"1:2:3\n", stderr=b"")
        with patch("scripts.linux_release.installed_process.process_identity", return_value=identity), patch.object(
                Path, "stat", side_effect=PermissionError), patch(
                "scripts.linux_release.installed_process.subprocess.run", return_value=result) as observer:
            self.assertEqual(engine_image(321), (1, 2, 3))
            self.assertEqual(observer.call_args.args[0], ["/usr/bin/sudo", "-n", "/usr/bin/stat", "-L", "-c", "%d:%i:%s", "/proc/321/exe"])
            result.stdout = b"unstructured image prose"
            with self.assertRaises(ValueError):
                engine_image(321)
            result.returncode = 1
            with self.assertRaises(CaseUnavailable):
                engine_image(321)

    def test_image_observer_rejects_pid_reuse(self):
        with patch("scripts.linux_release.installed_process.process_identity", side_effect=[{"start_time_ticks": 1}, {"start_time_ticks": 2}]), patch.object(
                Path, "stat", return_value=SimpleNamespace(st_dev=1, st_ino=2, st_size=3)):
            with self.assertRaises(ValueError):
                engine_image(321)

    def test_empty_logind_desktop_requires_actual_typed_compositor_owner(self):
        listing = {"exit_code": 0, "stdout": "1 1000 tester seat0\n"}
        details = {"exit_code": 0, "stdout": "User=1000\nActive=yes\nRemote=no\nType=wayland\nDesktop=\n"}
        with patch("scripts.linux_release.installed_environment.os.getuid", return_value=1000), patch(
                "scripts.linux_release.installed_environment.query", side_effect=[listing, details]), patch(
                "scripts.linux_release.installed_environment.installed_gnome.owner", return_value={"pid": 2000}) as owner:
            value = graphical_session()
            self.assertEqual(value["Desktop"], "GNOME")
            self.assertEqual(value["DesktopOwner"], {"pid": 2000})
            owner.assert_called_once_with()
        invalid = SimpleNamespace(unpack=lambda: (123,))
        reader = SimpleNamespace(call=lambda *args, **kwargs: invalid)
        with self.assertRaises(ValueError):
            gnome_owner(reader)

    def test_x11_display_comes_from_the_captured_desktop_when_logind_omits_it(self):
        from scripts.linux_release.installed_cases.app import desktop_display
        owner = {"pid": 321, "start_time_ticks": 42}
        identity = {"pid": 321, "parent_pid": 100, "start_time_ticks": 42}
        session = {"Display": "", "DesktopOwner": owner}
        with patch("scripts.linux_release.installed_cases.app.process_identity", return_value=identity), patch.object(
                Path, "open", return_value=io.BytesIO(b"IGNORED=not-retained\0DISPLAY=:2.0\0")):
            self.assertEqual(desktop_display(session), ":2.0")
        with patch("scripts.linux_release.installed_cases.app.process_identity", return_value=dict(identity, start_time_ticks=43)):
            with self.assertRaises(ValueError):
                desktop_display(session)
        for display in ("remote.example:0", ":0;bad", "", ":0\n"):
            with self.subTest(display=display), self.assertRaises((ValueError, CaseUnavailable)):
                desktop_display({"Display": display})


    def test_observed_product_exit_and_output_overflow_are_failures_not_missing_fixtures(self):
        for code in (-11, 1, 0):
            app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40})
            app.process = SimpleNamespace(poll=lambda: code)
            with self.subTest(code=code), self.assertRaises(CaseFailed) as captured:
                app.try_ready()
            self.assertNotIsInstance(captured.exception, CaseUnavailable)
            self.assertEqual(captured.exception.observations['exit_code'], code)
            self.assertEqual(captured.exception.streams, {'app-stdout': b'', 'app-stderr': b''})
        app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40})
        app.process = SimpleNamespace(poll=lambda: None)
        app._overflow.set()
        with self.assertRaises(CaseFailed):
            app.require_running()

    def test_readiness_deadline_rechecks_typed_exit_before_classification(self):
        for code, expected in [(-11, CaseFailed), (None, CaseUnavailable)]:
            app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40}, timeout=1)
            app.process = SimpleNamespace(poll=lambda: code)
            app.stop = Mock()
            with self.subTest(code=code), patch('scripts.linux_release.installed_cases.app.time.monotonic', side_effect=[0, 2]):
                with self.assertRaises(expected):
                    app.wait_ready()
            app.stop.assert_called_once_with()

    def test_gui_public_health_does_not_require_or_invent_private_token(self):
        identity = {"pid": 321, "parent_pid": 123, "start_time_ticks": 456}
        app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40})
        app.process = SimpleNamespace(pid=123)
        app.binding = {"install_root": "/opt/app", "images": {"/opt/app/bin/pw": {"device": 1, "inode": 2, "bytes": 3}}}
        api = Mock()
        api.request.return_value = {"status": 200, "body": {"ready": True}}
        with patch('scripts.linux_release.installed_cases.app.process_identity', return_value=identity), patch(
                'scripts.linux_release.installed_cases.app.engine_image', return_value=(1, 2, 3)), patch(
                'scripts.linux_release.installed_cases.app.EngineAPI', return_value=api) as constructor, patch(
                'scripts.linux_release.installed_cases.app.os.open', side_effect=FileNotFoundError):
            self.assertTrue(app._bind_engine({"pid": 321, "port": 12345, "host": "127.0.0.1"}, Path('/unused')))
        constructor.assert_called_once_with(12345)
        api.request.assert_called_once_with('GET', '/health')
        self.assertFalse(app.observations['private_api_available'])

    def test_public_client_refuses_all_private_calls_before_transport(self):
        from scripts.linux_release.installed_cases.app_api import EngineAPI
        api = EngineAPI(12345)
        with patch.object(api._opener, 'open') as transport:
            for method, path, payload in [('GET', '/v1/providers', None), ('POST', '/health', None),
                                           ('GET', '/health?probe=1', None), ('GET', '/health', {})]:
                with self.subTest(method=method, path=path), self.assertRaises(CaseUnavailable):
                    api.request(method, path, payload)
            transport.assert_not_called()

    def test_stale_private_token_cannot_claim_gui_authentication(self):
        identity = {"pid": 321, "parent_pid": 123, "start_time_ticks": 456}
        app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40})
        app.process = SimpleNamespace(pid=123)
        app.binding = {"install_root": "/opt/app", "images": {"/opt/app/bin/pw": {"device": 1, "inode": 2, "bytes": 3}}}
        token_stream = tempfile.TemporaryFile()
        self.addCleanup(token_stream.close)
        token_stream.write(b'stale-token')
        token_stream.seek(0)
        public, private = Mock(), Mock()
        public.request.return_value = {"status": 200, "body": {}}
        private.request.return_value = {"status": 401, "body": {}}
        with patch('scripts.linux_release.installed_cases.app.process_identity', return_value=identity), patch(
                'scripts.linux_release.installed_cases.app.engine_image', return_value=(1, 2, 3)), patch(
                'scripts.linux_release.installed_cases.app.EngineAPI', side_effect=[public, private]), patch(
                'scripts.linux_release.installed_cases.app.os.open', return_value=7), patch(
                'scripts.linux_release.installed_cases.app.os.fdopen', return_value=token_stream), patch(
                'scripts.linux_release.installed_cases.app.os.fstat', return_value=SimpleNamespace(
                    st_mode=0o100600, st_uid=os.getuid(), st_size=11)):
            self.assertTrue(app._bind_engine({"pid": 321, "port": 12345, "host": "127.0.0.1"}, Path('/unused')))
        private.request.assert_called_once_with('GET', '/v1/providers')
        self.assertIs(app.api, public)
        self.assertFalse(app.observations['private_api_available'])

    def test_captured_engine_exit_or_pid_reuse_is_a_product_failure(self):
        identity = {"pid": 321, "parent_pid": 123, "start_time_ticks": 456}
        for changed in (FileNotFoundError(), ProcessLookupError(), dict(identity, start_time_ticks=789)):
            app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40})
            app.process = SimpleNamespace(poll=lambda: None)
            app.observations['engine_identity'] = identity
            options = {'side_effect': changed} if isinstance(changed, Exception) else {'return_value': changed}
            with self.subTest(changed=type(changed).__name__), patch(
                    'scripts.linux_release.installed_cases.app.process_identity', **options):
                with self.assertRaises(CaseFailed) as failure:
                    app.try_ready()
                self.assertNotIsInstance(failure.exception, CaseUnavailable)
                self.assertEqual(failure.exception.observations['engine_identity'], identity)

    def test_desktop_application_scope_verifies_desktop_entry_and_engine_cgroup_ancestry(self):
        from scripts.linux_release.installed_cases.app import enter_desktop_application_scope
        storage = SimpleNamespace(application_identifier="dev.paintedwolf.code")
        desktop_text = "[Desktop Entry]\nExec=/usr/libexec/painted-wolf-code/pw-launch %U\nStartupWMClass=dev.paintedwolf.code\n"
        fake_bus, fake_gio, fake_glib = Mock(), Mock(), Mock()
        fake_gio.bus_get_sync.return_value = fake_bus
        cgroups = ["0::/user.slice/user-30068.slice/session-1961.scope\n",
                   "0::/user.slice/user-30068.slice/session-1961.scope\n",
                   "0::/user.slice/user-30068.slice/user@30068.service/app.slice/app-gnome-dev.paintedwolf.code-5001000.scope\n"]
        def fake_read_text(path_obj, *args, **kwargs):
            if str(path_obj).endswith(".desktop"):
                return desktop_text
            return cgroups.pop(0) if len(cgroups) > 1 else cgroups[0]
        fake_probe = SimpleNamespace(pid=501, wait=lambda timeout=10: 0)
        direct_cli_id = {"pid": 501, "parent_pid": 500, "start_time_ticks": 399}
        with patch.dict("sys.modules", {"gi": SimpleNamespace(repository=SimpleNamespace(Gio=fake_gio, GLib=fake_glib)),
                                        "gi.repository": SimpleNamespace(Gio=fake_gio, GLib=fake_glib)}), \
                patch("scripts.linux_release.installed_cases.app.file_identity",
                      return_value={"sha256": "a" * 64, "bytes": 120, "mode": 0o644, "header": b"[Desktop"}), \
                patch("scripts.linux_release.installed_cases.app.subprocess.Popen", return_value=fake_probe), \
                patch("scripts.linux_release.installed_cases.app.process_identity", return_value=direct_cli_id), \
                patch("scripts.linux_release.installed_cases.app.os.getpid", return_value=500), \
                patch("scripts.linux_release.installed_cases.app.time.monotonic", side_effect=[1.0, 1.0, 1.1]), \
                patch.object(Path, "read_text", autospec=True, side_effect=fake_read_text):
            witness = enter_desktop_application_scope(Path("/usr/libexec/painted-wolf-code/pw-launch"), storage)
        self.assertEqual(witness["unit"], "app-gnome-dev.paintedwolf.code-5001000.scope")
        self.assertEqual(re.match(r"^app-(?:[0-9A-Za-z]+-)?(.+?)(?:-[0-9A-Za-z]*)(?:\.scope|\.slice)$", witness["unit"]).group(1),
                         "dev.paintedwolf.code")
        self.assertEqual(witness["launch_mode"], "controlled-systemd-application-scope")
        self.assertFalse(witness["desktop_entry_launch_proven"])
        self.assertEqual(witness["cli_launch_comparison"], {
            "direct_cli_executable": "/opt/painted-wolf-code/bin/pw",
            "direct_cli_identity": direct_cli_id,
            "direct_cli_exit_code": 0,
            "direct_cli_cgroup": "0::/user.slice/user-30068.slice/session-1961.scope",
            "direct_cli_leaf_unit": "session-1961.scope",
            "direct_cli_attributed_app_id": None,
            "controlled_scope_unit": "app-gnome-dev.paintedwolf.code-5001000.scope",
            "controlled_scope_attributed_app_id": "dev.paintedwolf.code",
        })
        fake_bus.call_sync.assert_called_once()
        engine = {"pid": 321, "parent_pid": 123, "start_time_ticks": 456}
        launcher = {"pid": 123, "parent_pid": 500, "start_time_ticks": 400}
        app = InstalledApp({"artifact": {}, "source_commit": "a" * 40})
        app.process = SimpleNamespace(pid=123)
        app.binding = {"install_root": "/opt/app", "images": {"/opt/app/bin/pw": {"device": 1, "inode": 2, "bytes": 3}}}
        app.observations["application_unit"] = {**witness, "launch_cgroup": witness["caller_cgroup_after"]}
        api = Mock()
        api.request.return_value = {"status": 200, "body": {"ready": True}}
        with patch("scripts.linux_release.installed_cases.app.process_identity",
                   side_effect=lambda pid: engine if pid == 321 else launcher), \
                patch("scripts.linux_release.installed_cases.app.engine_image", return_value=(1, 2, 3)), \
                patch("scripts.linux_release.installed_cases.app.EngineAPI", return_value=api), \
                patch("scripts.linux_release.installed_cases.app.os.open", side_effect=FileNotFoundError), \
                patch.object(Path, "read_text", return_value=witness["caller_cgroup_after"]):
            self.assertTrue(app._bind_engine({"pid": 321, "port": 12345, "host": "127.0.0.1"}, Path("/unused")))
        self.assertEqual(app.observations["application_unit"]["ancestry"], [engine, launcher])
        with patch("scripts.linux_release.installed_cases.app.process_identity", return_value=engine), \
                patch("scripts.linux_release.installed_cases.app.engine_image", return_value=(1, 2, 3)), \
                patch.object(Path, "read_text", return_value="0::/user.slice/session-1961.scope"):
            with self.assertRaisesRegex(ValueError, "escaped its desktop application unit scope"):
                app._bind_engine({"pid": 321, "port": 12345, "host": "127.0.0.1"}, Path("/unused"))


class InstalledAppPipeCaptureTests(unittest.TestCase):
    def launch_pipes(self, directory):
        from contextlib import ExitStack
        from scripts.linux_release.installed_cases import app as owner
        stdout_read, stdout_write = os.pipe()
        stderr_read, stderr_write = os.pipe()
        writes = [stdout_write, stderr_write]
        process = SimpleNamespace(pid=4321, stdout=os.fdopen(stdout_read, 'rb'),
                                  stderr=os.fdopen(stderr_read, 'rb'), returncode=None)
        process.poll = lambda: process.returncode
        def finish(selected):
            self.assertIs(selected, process)
            for descriptor in writes[:]:
                os.close(descriptor)
                writes.remove(descriptor)
            process.returncode = 0
        identity = {'sha256': 'a' * 64, 'bytes': 1, 'mode': 0o755}
        app = InstalledApp({'artifact': {}, 'source_commit': 'a' * 40, 'install_root': '/fixture',
                            'launcher': {'path': str(directory / 'launcher'), **identity}})
        storage = SimpleNamespace(config=directory, facts=lambda: {})
        self.addCleanup(finish, process)
        with ExitStack() as patches:
            patches.enter_context(patch.object(owner, 'require_owned_guest'))
            patches.enter_context(patch.object(owner, 'graphical_session', return_value={}))
            patches.enter_context(patch.object(owner, 'session_environment', return_value={}))
            patches.enter_context(patch.object(owner, 'storage_for_launch', return_value=storage))
            patches.enter_context(patch.object(owner, 'file_identity', return_value={**identity, 'header': b'x'}))
            patches.enter_context(patch.object(owner, 'enter_desktop_application_scope', return_value={'unit': 'fixture.scope'}))
            patches.enter_context(patch.object(owner, 'process_identity', return_value={'pid': 4321}))
            patches.enter_context(patch.object(Path, 'read_text', return_value='0::/fixture.scope'))
            launch = patches.enter_context(patch.object(owner.subprocess, 'Popen', return_value=process))
            app.launch()
        # Keep the exact stop owner through app.stop, while the real pipe readers
        # terminate only when these fixture writers close.
        return app, process, writes, finish, launch

    def await_condition(self, condition):
        import time
        finish = time.monotonic() + 5
        while not condition():
            if time.monotonic() >= finish:
                self.fail('actual pipe capture did not publish its bounded observation')
            time.sleep(0.005)

    def test_small_native_record_is_visible_before_eof_and_stop_joins_both_readers(self):
        from scripts.linux_release.installed_cases import app as owner
        with tempfile.TemporaryDirectory() as root:
            app, process, writes, finish, launch = self.launch_pipes(Path(root))
            records = (b'small native stdout record\n', b'Native startup outcome: {"format_version":1}\n')
            os.write(writes[0], records[0])
            os.write(writes[1], records[1][:17])
            try:
                self.await_condition(lambda: app.streams()['app-stderr'] == records[1][:17])
                self.assertIsNone(process.poll())
                os.write(writes[1], records[1][17:])
                self.await_condition(lambda: app.streams() == dict(zip(('app-stdout', 'app-stderr'), records)))
                self.assertIsNone(process.poll())
                self.assertEqual(len(writes), 2)
                self.assertTrue(all(reader.is_alive() for reader in app._readers))
                launch.assert_called_once()
                with patch.object(owner, 'stop_stack', side_effect=finish) as stop:
                    app.stop()
                stop.assert_called_once_with(process)
                self.assertTrue(app.observations['capture_complete'])
                self.assertFalse(app.observations['output_limit'])
                self.assertEqual(app.observations['exit_code'], 0)
                self.assertTrue(process.stdout.closed and process.stderr.closed)
                self.assertFalse(any(reader.is_alive() for reader in app._readers))
            finally:
                with patch.object(owner, 'stop_stack', side_effect=finish): app.stop()

    def test_incremental_overflow_keeps_bound_and_stops_owned_capture(self):
        from scripts.linux_release.installed_cases import app as owner
        with tempfile.TemporaryDirectory() as root, patch.object(owner, 'LIMIT', 64):
            app, process, writes, finish, _ = self.launch_pipes(Path(root))
            try:
                os.write(writes[1], b'x' * 65)
                self.await_condition(app._overflow.is_set)
                self.assertLessEqual(len(app.streams()['app-stderr']), 64)
                with self.assertRaises(CaseFailed): app.require_running()
                with patch.object(owner, 'stop_stack', side_effect=finish) as stop: app.stop()
                stop.assert_called_once_with(process)
                self.assertTrue(app.observations['output_limit'])
                self.assertTrue(app.observations['capture_complete'])
                self.assertFalse(any(reader.is_alive() for reader in app._readers))
            finally:
                with patch.object(owner, 'stop_stack', side_effect=finish): app.stop()
