"""Unit tests for TB10 X01-X03 native Den runner."""

from __future__ import annotations

import json
import pathlib
import sys
import tempfile
import unittest

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea import tb10_native_runner as tb10
from wea.provenance import file_sha256


class TestTB10NativeRunner(unittest.TestCase):
    def _make_verified_engine(self, root: pathlib.Path) -> tuple[pathlib.Path, pathlib.Path, pathlib.Path]:
        app_bin = root / "painted-wolf-code"
        app_bin.write_bytes(b"#!/bin/sh\necho app\n")
        app_bin.chmod(0o755)
        eng_bin = root / "lycaon-dev"
        eng_bin.write_bytes(b"#!/bin/sh\necho engine\n")
        eng_bin.chmod(0o755)
        eng_sha = file_sha256(eng_bin)
        manifest = root / "build-manifest.json"
        manifest.write_text(
            json.dumps({
                "builds": {
                    "product_baseline": {
                        "label": "product_baseline",
                        "head": "576bbafd7a000000000000000000000000000000",
                        "tree": "1111111111111111111111111111111111111111",
                        "binaries": {
                            "lycaon-dev": {
                                "path": str(eng_bin),
                                "sha256": eng_sha,
                                "go_build_id": "",
                            }
                        },
                    }
                }
            }),
            encoding="utf-8",
        )
        return app_bin, eng_bin, manifest

    def test_all_nine_x01_x03_variants_pass_with_verified_provenance_and_manifest(self) -> None:
        self.assertEqual(len(tb10.TB10_VARIANTS), 9)
        families = [v["family"] for v in tb10.TB10_VARIANTS]
        self.assertEqual(families.count("X01"), 3)
        self.assertEqual(families.count("X02"), 3)
        self.assertEqual(families.count("X03"), 3)

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            app_bin, eng_bin, man_path = self._make_verified_engine(root)

            def fake_native(app_p, out_p, *, engine_bin=None, engine_manifest=None, **kwargs):
                out_p = pathlib.Path(out_p)
                out_p.mkdir(parents=True, exist_ok=True)
                prov = tb10.collect_provenance(engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar-native-den")
                return {
                    "schema": "wea-native-den-linux-tauri-v1",
                    "tier": "native-den-tauri-app",
                    "owned_window_id": 2097160,
                    "app_pid": 1234,
                    "engine_pid": 1235,
                    "screenshot_before_sha256": "a" * 64,
                    "screenshot_sha256": "b" * 64,
                    "screenshot_changed": True,
                    "atspi_focus_changed": True,
                    "api_readback": {"health": {"status": "ok"}, "engine_url": "http://127.0.0.1:41234"},
                    "provenance": prov,
                    "passed": True,
                }

            manifest = tb10.run_tb10_suite(
                root / "out",
                app_bin=app_bin,
                engine_bin=eng_bin,
                engine_manifest=man_path,
                native_fn=fake_native,
            )
            self.assertTrue(manifest["all_passed"])
            self.assertEqual(manifest["planned_count"], 9)
            self.assertEqual(manifest["passed_count"], 9)
            self.assertTrue(manifest["provenance"]["product_source"]["verified"])
            self.assertTrue(manifest["provenance"]["product_harness_distinct"])
            self.assertTrue((root / "out" / "tb10-manifest.json").is_file())
            for entry in manifest["variants"]:
                self.assertEqual(entry["verdict"], "pass")
                self.assertTrue(entry["screenshot_changed"])
                self.assertTrue(pathlib.Path(entry["receipt_path"]).is_file())

    def test_fail_closed_on_unverified_engine_or_disallowed_guidance_or_byte_ceiling(self) -> None:
        g_bad_tool = tb10.verify_guidance_surface(
            offered_tools=("command",),
            suggested_tools=("command", "unpermitted_tool"),
            request_bytes=1024,
        )
        self.assertFalse(g_bad_tool["passed"])
        self.assertEqual(g_bad_tool["disallowed_suggestions"], ["unpermitted_tool"])

        g_over_ceiling = tb10.verify_guidance_surface(
            offered_tools=("command",),
            suggested_tools=("command",),
            request_bytes=tb10.SHIPPED_REQUEST_CEILING_BYTES + 1,
        )
        self.assertFalse(g_over_ceiling["passed"])
        self.assertFalse(g_over_ceiling["within_ceiling"])

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            app_bin, eng_bin, _ = self._make_verified_engine(root)
            bad_manifest = root / "bad-manifest.json"
            bad_manifest.write_text(json.dumps({"executable_sha256": "0" * 64, "git_head": "576bbafd7a"}), encoding="utf-8")

            def fake_native_unverified(app_p, out_p, *, engine_bin=None, engine_manifest=None, **kwargs):
                prov = tb10.collect_provenance(engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar-native-den")
                return {
                    "passed": True,
                    "screenshot_changed": True,
                    "api_readback": {"health": {"status": "ok"}},
                    "provenance": prov,
                }

            res = tb10.run_tb10_suite(
                root / "out-bad",
                app_bin=app_bin,
                engine_bin=eng_bin,
                engine_manifest=bad_manifest,
                variant_ids=["X01-v1-worker-outside-write"],
                native_fn=fake_native_unverified,
            )
            self.assertFalse(res["all_passed"])
            self.assertIn("unverified_product_source", res["variants"][0]["failure_reasons"])


if __name__ == "__main__":
    unittest.main()
