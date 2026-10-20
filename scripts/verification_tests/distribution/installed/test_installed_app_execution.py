"""Actual application identity and platform-owner boundaries fail closed."""
import io
import os
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.installed_cases.app import engine_image, InstalledApp
from scripts.linux_release.installed_cases.records import CaseUnavailable, CaseFailed
from scripts.linux_release.installed_environment import graphical_session, gnome_owner


class InstalledAppTests(unittest.TestCase):
    def test_nondumpable_engine_uses_only_fixed_read_only_image_observer(self):
        identity = {"pid": 321, "parent_pid": 123, "start_time_ticks": 456}
        result = SimpleNamespace(returncode=0, stdout=b"1:2:3\n", stderr=b"")
        with patch("scripts.linux_release.installed_cases.app.process_identity", return_value=identity), patch.object(
                Path, "stat", side_effect=PermissionError), patch(
                "scripts.linux_release.installed_cases.app.subprocess.run", return_value=result) as observer:
            self.assertEqual(engine_image(321), (1, 2, 3))
            self.assertEqual(observer.call_args.args[0], ["/usr/bin/sudo", "-n", "/usr/bin/stat", "-L", "-c", "%d:%i:%s", "/proc/321/exe"])
            result.stdout = b"unstructured image prose"
            with self.assertRaises(ValueError):
                engine_image(321)
            result.returncode = 1
            with self.assertRaises(CaseUnavailable):
                engine_image(321)

    def test_image_observer_rejects_pid_reuse(self):
        with patch("scripts.linux_release.installed_cases.app.process_identity", side_effect=[{"start_time_ticks": 1}, {"start_time_ticks": 2}]), patch.object(
                Path, "stat", return_value=SimpleNamespace(st_dev=1, st_ino=2, st_size=3)):
            with self.assertRaises(ValueError):
                engine_image(321)

    def test_empty_logind_desktop_requires_actual_typed_compositor_owner(self):
        listing = {"exit_code": 0, "stdout": "1 1000 tester seat0\n"}
        details = {"exit_code": 0, "stdout": "User=1000\nActive=yes\nRemote=no\nType=wayland\nDesktop=\n"}
        with patch("scripts.linux_release.installed_environment.os.getuid", return_value=1000), patch(
                "scripts.linux_release.installed_environment.query", side_effect=[listing, details]), patch(
                "scripts.linux_release.installed_environment.gnome_owner", return_value={"pid": 2000}) as owner:
            value = graphical_session()
            self.assertEqual(value["Desktop"], "GNOME")
            self.assertEqual(value["DesktopOwner"], {"pid": 2000})
            owner.assert_called_once_with()
        with patch("scripts.linux_release.installed_environment.query", return_value={"exit_code": 0, "stdout": '{"type":"s","data":["GNOME"]}'}):
            with self.assertRaises(ValueError):
                gnome_owner()

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
        public, private = Mock(), Mock()
        public.request.return_value = {"status": 200, "body": {}}
        private.request.return_value = {"status": 401, "body": {}}
        with patch('scripts.linux_release.installed_cases.app.process_identity', return_value=identity), patch(
                'scripts.linux_release.installed_cases.app.engine_image', return_value=(1, 2, 3)), patch(
                'scripts.linux_release.installed_cases.app.EngineAPI', side_effect=[public, private]), patch(
                'scripts.linux_release.installed_cases.app.os.open', return_value=7), patch(
                'scripts.linux_release.installed_cases.app.os.fdopen', return_value=io.BytesIO(b'stale-token')), patch(
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
