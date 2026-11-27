"""Unit tests for TB02 D12 owned-window native Den desktop drivers (Linux WebKitGTK & macOS WKWebView)."""

from __future__ import annotations

import json
import pathlib
import struct
import sys
import tempfile
import unittest
from unittest import mock

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea import desktop_driver, native_den_linux


def _minimal_png_bytes(width: int = 1280, height: int = 800) -> bytes:
    ihdr_data = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + struct.pack(">I", len(ihdr_data)) + b"IHDR" + ihdr_data + b"\x00\x00\x00\x00"


class TestTB02DesktopDrivers(unittest.TestCase):
    def test_parse_png_info_valid_and_invalid(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            valid = root / "ok.png"
            valid.write_bytes(_minimal_png_bytes(1280, 800))
            info = native_den_linux.parse_png_info(valid)
            self.assertTrue(info["valid_png"])
            self.assertEqual(info["width"], 1280)
            self.assertEqual(info["height"], 800)
            self.assertEqual(len(info["sha256"]), 64)

            bad = root / "bad.png"
            bad.write_bytes(b"not a png")
            bad_info = native_den_linux.parse_png_info(bad)
            self.assertFalse(bad_info["valid_png"])

    def test_macos_wkwebview_native_dispatch_and_fail_closed_checks(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            out_ok = root / "mac-ok"

            def fake_run_ok(cmd, **kwargs):
                png_dest = pathlib.Path(cmd[-1])
                png_dest.parent.mkdir(parents=True, exist_ok=True)
                png_dest.write_bytes(_minimal_png_bytes(1280, 800))
                payload = {
                    "schema": "wea-native-den-macos-wkwebview-v1",
                    "tier": "native-den-wkwebview",
                    "owned_window_number": 4242,
                    "pid": 9999,
                    "session_id": "sess-mac-1",
                    "focus_before": {"tagName": "TEXTAREA", "key": "TEXTAREA|||"},
                    "focus_after": {"tagName": "BUTTON", "key": "BUTTON|send||Send"},
                    "focus_changed": True,
                    "api_readback": {"connected": True, "view": "chat"},
                    "screenshot_path": str(png_dest),
                    "screenshot_bytes": len(_minimal_png_bytes(1280, 800)),
                    "passed": True,
                }
                return mock.Mock(returncode=0, stdout=json.dumps(payload) + "\n", stderr="")

            with (
                mock.patch.object(desktop_driver, "collect_provenance", return_value={"product_source": {"verified": True}, "harness_source": {"git_head": "abc"}}),
                mock.patch.object(desktop_driver.subprocess, "run", side_effect=fake_run_ok),
            ):
                rec = desktop_driver.run_desktop_driver(
                    "macos-wkwebview-native",
                    "http://127.0.0.1:4173",
                    out_ok,
                    webkit_harness_bin=root / "webkit-harness",
                )
            self.assertTrue(rec["passed"])
            self.assertEqual(rec["tier"], "native-den-wkwebview")
            self.assertEqual(rec["owned_window_number"], 4242)
            self.assertEqual(rec["screenshot_dimensions"], [1280, 800])
            self.assertEqual(len(rec["screenshot_sha256"]), 64)
            self.assertTrue((out_ok / "native-den-macos-wkwebview-receipt.json").is_file())

            # Fail-closed: focus_changed=False MUST NOT pass even with rc=0 and valid PNG.
            out_fail = root / "mac-fail"

            def fake_run_no_focus(cmd, **kwargs):
                png_dest = pathlib.Path(cmd[-1])
                png_dest.parent.mkdir(parents=True, exist_ok=True)
                png_dest.write_bytes(_minimal_png_bytes(1280, 800))
                payload = {
                    "tier": "native-den-wkwebview",
                    "owned_window_number": 4242,
                    "pid": 9999,
                    "focus_changed": False,
                    "passed": True,
                }
                return mock.Mock(returncode=0, stdout=json.dumps(payload) + "\n", stderr="")

            with (
                mock.patch.object(desktop_driver, "collect_provenance", return_value={"product_source": {"verified": True}, "harness_source": {"git_head": "abc"}}),
                mock.patch.object(desktop_driver.subprocess, "run", side_effect=fake_run_no_focus),
            ):
                rec_fail = desktop_driver.run_desktop_driver(
                    "macos-wkwebview-native",
                    "http://127.0.0.1:4173",
                    out_fail,
                    webkit_harness_bin=root / "webkit-harness",
                )
            self.assertFalse(rec_fail["passed"])

    def test_linux_webkitgtk_native_dispatch_passes_through(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            expected = {"tier": "native-den-webkitgtk", "owned_window_id": 12345, "focus_changed": True, "passed": True}
            with mock.patch.object(desktop_driver, "run_linux_native_den", return_value=expected) as m:
                got = desktop_driver.run_desktop_driver("linux-webkitgtk-native", "http://127.0.0.1:4173", root / "lin")
            self.assertEqual(got, expected)
            m.assert_called_once()

    def test_linux_tauri_native_dispatch_and_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_app = root / "painted-wolf-code"
            fake_app.write_bytes(b"#!/bin/sh\nsleep 30\n")
            fake_app.chmod(0o755)
            out_dir = root / "tauri-out"

            def fake_capture(xid, dest):
                dest.write_bytes(_minimal_png_bytes(1280, 800))
                return {"tool": "import", **native_den_linux.parse_png_info(dest)}

            atspi_seq = [
                {"available": True, "observed": True, "app_name": "painted-wolf-code", "focused_key": "entry|Composer"},
                {"available": True, "observed": True, "app_name": "painted-wolf-code", "focused_key": "push button|Send"},
            ]
            with (
                mock.patch.object(native_den_linux, "_find_x11_window_for_pid", return_value=77777),
                mock.patch.object(
                    native_den_linux,
                    "_wait_for_spawned_engine",
                    return_value={"ready": True, "engine_url": "http://127.0.0.1:41234", "engine_pid": 88888, "health": {"status": "ok"}},
                ),
                mock.patch.object(native_den_linux, "_probe_atspi", side_effect=atspi_seq),
                mock.patch.object(native_den_linux, "_send_x11_key", return_value={"sent": True, "key": "Tab", "window_id": 77777}),
                mock.patch.object(native_den_linux, "_capture_x11_window_png", side_effect=fake_capture),
                mock.patch.object(native_den_linux.time, "sleep", return_value=None),
            ):
                rec = desktop_driver.run_desktop_driver(
                    "linux-tauri-native",
                    "",
                    out_dir,
                    app_bin=fake_app,
                )
            self.assertTrue(rec["passed"])
            self.assertEqual(rec["tier"], "native-den-tauri-app")
            self.assertEqual(rec["owned_window_id"], 77777)
            self.assertEqual(rec["engine_pid"], 88888)
            self.assertEqual(rec["api_readback"]["health"], {"status": "ok"})
            self.assertTrue(rec["atspi_focus_changed"])
            self.assertEqual(rec["screenshot_dimensions"], [1280, 800])
            self.assertTrue((out_dir / "native-den-linux-tauri-receipt.json").is_file())

            # Missing binary fails closed with status=blocked.
            rec_missing = desktop_driver.run_desktop_driver(
                "linux-tauri-native",
                "",
                root / "tauri-missing",
                app_bin=root / "does-not-exist",
            )
            self.assertFalse(rec_missing["passed"])
            self.assertEqual(rec_missing["status"], "blocked")

    def test_find_x11_window_skips_unmapped_gtk_leader(self) -> None:
        def fake_which(name):
            return f"/usr/bin/{name}"

        def fake_run(cmd, **kwargs):
            if cmd[0] == "xdotool":
                return mock.Mock(returncode=0, stdout="2097153\n2097160\n", stderr="")
            if cmd[0] == "xwininfo":
                if cmd[2] == hex(2097153):
                    return mock.Mock(returncode=0, stdout="  Width: 1\n  Height: 1\n  Map State: IsUnMapped\n", stderr="")
                return mock.Mock(returncode=0, stdout="  Width: 1280\n  Height: 800\n  Map State: IsViewable\n", stderr="")
            raise AssertionError(f"unexpected cmd {cmd}")

        with (
            mock.patch.object(native_den_linux.shutil, "which", side_effect=fake_which),
            mock.patch.object(native_den_linux.subprocess, "run", side_effect=fake_run),
        ):
            wid = native_den_linux._find_x11_window_for_pid(1234, timeout_s=2.0)
        self.assertEqual(wid, 2097160)

    def test_macos_tauri_native_missing_bin_blocks_cleanly(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            rec = desktop_driver.run_desktop_driver(
                "macos-tauri-native",
                "",
                root / "mac-tauri-missing",
                app_bin=root / "nonexistent-painted-wolf-code",
            )
            self.assertFalse(rec["passed"])
            self.assertEqual(rec["status"], "blocked")
            self.assertEqual(rec["tier"], "native-den-tauri-app")


if __name__ == "__main__":
    unittest.main()
