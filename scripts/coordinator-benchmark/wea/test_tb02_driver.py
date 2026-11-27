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

            with mock.patch.object(desktop_driver.subprocess, "run", side_effect=fake_run_ok):
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

            with mock.patch.object(desktop_driver.subprocess, "run", side_effect=fake_run_no_focus):
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


if __name__ == "__main__":
    unittest.main()
