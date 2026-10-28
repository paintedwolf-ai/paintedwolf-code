"""Actual application identity and platform-owner boundaries fail closed."""
from scripts.linux_release.installed_environment import GraphicalSessionObservation
import io
import json
import os
import re
import tempfile
from pathlib import Path
from contextlib import contextmanager, redirect_stderr
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, Mock, patch

from scripts.linux_release.installed_process import engine_image
from scripts.linux_release.installed_cases.app import InstalledApp
from scripts.linux_release.installed_cases.records import CaseUnavailable, CaseFailed
from scripts.linux_release import installed_environment as owner
from scripts.linux_release.installed_cases.logind_context import CallFailure, Reader
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

    def test_compositor_owner_rejects_invalid_typed_protocol(self):
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
            patches.enter_context(patch('scripts.linux_release.installed_generation.require_current'))
            patches.enter_context(patch.object(owner, 'graphical_session', return_value=GraphicalSessionObservation({}, {})))
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


SESSION = '/org/freedesktop/login1/session/current'
FOREIGN = '/org/freedesktop/login1/session/foreign'
OTHER = '/org/freedesktop/login1/session/other'
DISPLAY = ('7', 1000, 'fixture', 'seat0', SESSION)
UNRELATED = ('8', 2000, 'fixture', '', FOREIGN)


def details(uid=1000, *, active=True, remote=False, kind='wayland', desktop='GNOME', seat='seat0'):
    return {'User': (uid, '/org/freedesktop/login1/user/account'),
            'Seat': (seat, '/org/freedesktop/login1/seat/seat0' if seat else '/'),
            'Type': kind, 'Desktop': desktop, 'Class': 'user', 'Display': '',
            'Active': active, 'Remote': remote}


class GraphicalSessionTests(unittest.TestCase):
    @contextmanager
    def observe(self, before=None, after=None, properties=None, failures=None):
        before = [DISPLAY] if before is None else before
        after = before if after is None else after
        values = {SESSION: details(), FOREIGN: details(2000, remote=True, kind='tty', seat='')}
        values.update(properties or {})
        failures = failures or {}
        def property_value(path, interface, name, signature):
            if path in failures:
                raise failures[path]
            return values[path][name]
        reader = Mock()
        inventories = iter([before, after])
        last = before
        def inventory(*_args, **_kwargs):
            nonlocal last
            last = next(inventories, last)
            return SimpleNamespace(unpack=lambda: (last,))
        reader.call.side_effect = inventory
        reader.sessions.side_effect = lambda: Reader.sessions(reader)
        reader.property.side_effect = property_value
        context = MagicMock()
        context.__enter__.return_value = reader
        cli_listing = {'exit_code': 0, 'stdout': '8 2000 fixture -\n7 1000 fixture seat0\n'}
        cli_refusal = {'exit_code': 1, 'stdout': '', 'stderr': 'diagnostic only'}
        account = SimpleNamespace(pw_name='fixture', pw_uid=1000)
        with patch.object(owner.os, 'getuid', return_value=1000), \
                patch.object(owner.pwd, 'getpwuid', return_value=account), \
                patch.object(owner.pwd, 'getpwall', return_value=[account]), \
                patch.object(owner, 'Reader', return_value=context, create=True), \
                patch.object(owner, 'query', side_effect=[cli_listing, cli_refusal]) as query:
            yield reader, query

    def test_unrelated_unreadable_session_cannot_block_the_selected_account(self):
        with self.observe([UNRELATED, DISPLAY], [DISPLAY], failures={FOREIGN: CallFailure('org.freedesktop.DBus.Error.UnknownObject')}) as (reader, query):
            selection = owner.graphical_session(deadline=123)
            result = selection.session
            owner.Reader.assert_called_once_with(deadline=123)
        self.assertEqual(result['id'], '7')
        self.assertEqual(result['User'], '1000')
        self.assertEqual(result['Active'], 'yes')
        self.assertEqual(result['Remote'], 'no')
        facts = selection.observations['graphical_session']
        self.assertEqual([row['id'] for row in facts['inventory_before']], ['7', '8'])
        self.assertEqual([row['id'] for row in facts['inventory_after']], ['7'])
        self.assertEqual({call.args[0] for call in reader.property.call_args_list}, {SESSION})
        query.assert_not_called()

    def test_selected_unreadability_retains_exact_identity_property_and_remote_status(self):
        for name in ('org.freedesktop.DBus.Error.UnknownObject', 'org.freedesktop.DBus.Error.AccessDenied', None):
            with self.subTest(remote_name=name), self.observe(failures={SESSION: CallFailure(name)}), self.assertRaises(CaseFailed) as failure:
                owner.graphical_session()
            observed = failure.exception.observations['graphical_session']
            self.assertEqual(observed['session_id'], '7')
            self.assertEqual(observed['session_object'], SESSION)
            self.assertEqual(observed['property'], 'User')
            self.assertEqual(observed['remote_name'], name)
            self.assertEqual(observed['phase'], 'properties-before')

    def test_foreign_properties_are_never_an_authority_prerequisite(self):
        for name in ('org.freedesktop.DBus.Error.UnknownObject', 'org.freedesktop.DBus.Error.AccessDenied', None):
            for after in ([UNRELATED, DISPLAY], [DISPLAY]):
                with self.subTest(remote_name=name, after=after), self.observe([UNRELATED, DISPLAY], after, failures={FOREIGN: CallFailure(name)}) as (reader, _):
                    self.assertEqual(owner.graphical_session(deadline=123).session['id'], '7')
                self.assertEqual({call.args[0] for call in reader.property.call_args_list}, {SESSION})

    def test_any_needed_selected_account_property_failure_refuses(self):
        remote = ('9', 1000, 'fixture', '', OTHER)
        for error in ('org.freedesktop.DBus.Error.UnknownObject', 'org.freedesktop.DBus.Error.AccessDenied', None):
            with self.subTest(remote_name=error), self.observe([DISPLAY, remote], properties={OTHER: details(remote=True, kind='tty', seat='')}, failures={OTHER: CallFailure(error)}), self.assertRaises(CaseFailed) as failure:
                owner.graphical_session()
            self.assertEqual(failure.exception.observations['graphical_session']['session_id'], '9')

    def test_selected_disappearance_or_replacement_refuses(self):
        for after in ([], [('9', 1000, 'fixture', 'seat0', OTHER)]):
            with self.subTest(after=after), self.observe(after=after), self.assertRaises(CaseFailed) as failure:
                owner.graphical_session()
            observation = failure.exception.observations['graphical_session']
            self.assertEqual(observation['phase'], 'inventory-after')
            self.assertIn('inventory_after', observation)

    def test_duplicate_active_graphical_sessions_refuse(self):
        additional = ('9', 1000, 'fixture', 'seat0', OTHER)
        with self.observe([DISPLAY, additional], properties={OTHER: details()}), self.assertRaises(CaseFailed):
            owner.graphical_session()

    def test_selected_properties_must_remain_stable(self):
        with self.observe() as (reader, _):
            calls = 0
            def observe(path, interface, name, signature):
                nonlocal calls
                if name == 'Active':
                    calls += 1
                    return calls == 1
                return details()[name]
            reader.property.side_effect = observe
            with self.assertRaises(CaseFailed) as failure:
                owner.graphical_session()
        self.assertEqual(failure.exception.observations['graphical_session']['phase'], 'properties-after')

    def test_malformed_inventory_or_selected_property_refuses(self):
        for row in (('7', True, 'fixture', 'seat0', SESSION), ('7', 1000, 'fixture', 'seat0', 'foreign')):
            with self.subTest(row=row), self.observe([row]), self.assertRaises(CaseFailed):
                owner.graphical_session()
        with self.observe(properties={SESSION: {**details(), 'Active': 'yes'}}), self.assertRaises(CaseFailed):
            owner.graphical_session()
        with self.observe(properties={SESSION: {**details(), 'User': (2000, '/org/freedesktop/login1/user/account')}}), self.assertRaises(CaseFailed):
            owner.graphical_session()

    def test_invalid_inventory_envelope_and_duplicate_identities_refuse(self):
        for payload in (None, ([], []), ('invalid',), ([DISPLAY, DISPLAY],)):
            with self.subTest(payload=payload), self.observe() as (reader, _):
                reader.call.side_effect = None
                reader.call.return_value = SimpleNamespace(unpack=lambda: payload)
                with self.assertRaises(CaseFailed):
                    owner.graphical_session()

    def test_remote_inactive_and_non_graphical_selected_sessions_never_pass(self):
        for selected in (details(remote=True), details(active=False), details(kind='tty')):
            with self.subTest(properties=selected), self.observe(properties={SESSION: selected}), self.assertRaises(CaseFailed):
                owner.graphical_session()

    def test_empty_desktop_keeps_the_actual_typed_compositor_owner(self):
        with self.observe(properties={SESSION: details(desktop='')}), patch.object(owner.installed_gnome, 'owner', return_value={'pid': 2000}) as compositor:
            result = owner.graphical_session().session
        self.assertEqual(result['Desktop'], 'GNOME')
        self.assertEqual(result['DesktopOwner'], {'pid': 2000})
        compositor.assert_called_once_with()


    def test_inventory_transport_failure_retains_the_exact_manager_call(self):
        with self.observe() as (reader, _):
            reader.call.side_effect = CallFailure(None)
            with self.assertRaises(CaseFailed) as failure:
                owner.graphical_session(deadline=123)
        facts = failure.exception.observations['graphical_session']
        self.assertEqual(facts['failed_calls'], [{'object': '/org/freedesktop/login1',
            'interface': 'org.freedesktop.login1.Manager', 'method': 'ListSessions', 'remote_name': None}])
        self.assertEqual(facts['phase'], 'inventory-before')

    def test_compositor_failure_uses_the_same_observation_contract(self):
        with self.observe(properties={SESSION: details(desktop='')}), patch.object(owner.installed_gnome, 'owner', side_effect=ValueError('fixture compositor')), self.assertRaises(CaseFailed) as failure:
            owner.graphical_session(deadline=123)
        self.assertEqual(failure.exception.observations['graphical_session']['phase'], 'compositor-owner')

    def test_missing_or_ambiguous_passwd_account_refuses_before_session_reads(self):
        selected = SimpleNamespace(pw_name='fixture', pw_uid=1000)
        for entries in ([], [selected, selected], [SimpleNamespace(pw_name='alias', pw_uid=1000), selected]):
            with self.subTest(entries=entries), self.observe() as (reader, _), patch.object(owner.pwd, 'getpwall', return_value=entries), self.assertRaises(CaseFailed) as failure:
                owner.graphical_session(account_name='fixture', deadline=123)
            facts = failure.exception.observations['graphical_session']
            self.assertEqual(facts['phase'], 'account-lookup')
            self.assertEqual(facts['account_name'], 'fixture')
            self.assertEqual(facts['account_matches'], [{'name': row.pw_name, 'uid': row.pw_uid} for row in entries])
            reader.call.assert_not_called()
        with self.observe(), patch.object(owner.pwd, 'getpwuid', side_effect=KeyError), self.assertRaises(CaseFailed):
            owner.graphical_session(deadline=123)


class GraphicalSessionExecutionBoundaryTests(unittest.TestCase):
    def test_execution_cli_keeps_structured_pre_case_observation_failure(self):
        from scripts.linux_release import installed_execution as execution
        error = CaseFailed('typed refusal')
        error.observations = {'graphical_session': {'phase': 'inventory-before', 'remote_name': None}}
        arguments = ['--final-artifacts', 'final.json', '--source-commit', 'a' * 40,
            '--artifact-id', 'app', '--file-root', '/owned/files', '--install-root', '/owned/install',
            '--environment-id', 'noble', '--output', '/owned/output', '--driver-tree', 'b' * 40,
            '--execution-nonce', 'c' * 32, '--variant-id', 'gnome-wayland', '--snapshot', '{}']
        with patch.object(execution, 'read', return_value={}), \
                patch.object(execution, 'execute', side_effect=error), redirect_stderr(io.StringIO()) as output:
            self.assertEqual(execution.main(arguments), 2)
        self.assertEqual(json.loads(output.getvalue()), {'status': 'failed', 'observations': error.observations})
