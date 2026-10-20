"""Actual application identity and platform-owner boundaries fail closed."""
import io
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_cases.app import engine_image
from scripts.linux_release.installed_cases.records import CaseUnavailable
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
