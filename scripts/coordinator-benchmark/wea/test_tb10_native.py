"""Unit tests for TB10 X01-X03 native Den runner (real episode + native Den pairing)."""

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

    def _fake_episode(self, variant, v_dir, **kwargs):
        return {
            "session_id": "sess-tb10-1",
            "project_id": "proj-tb10-1",
            "db_path": "",
            "captured_requests": [
                {
                    "request_id": "req-1",
                    "session_id": "sess-tb10-1",
                    "offered_tools": ["command", "promote_overlay", "task", "update_progress", "wait"],
                    "message_count": 3,
                    "request_bytes": 4200,
                    "request_sha256": "c" * 64,
                }
            ],
            "episode_facts": {"kind": variant["episode_kind"], "outside_target_unchanged": True},
            "network_fixtures": [],
            "api_readback": {
                "health": {"status": "ok"},
                "session": {"id": "sess-tb10-1", "status": "idle"},
                "messages": {"messages": [{"role": "user", "content": "hi"}]},
            },
            "sqlite_row_counts": {"sessions": 1, "messages": 4, "worker_jobs": 1, "source_versions": 2},
            "sqlite_total_rows": 8,
        }

    def test_all_nine_x01_x03_variants_pass_with_real_episode_and_manifest(self) -> None:
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
                episode_fn=self._fake_episode,
            )
            self.assertTrue(manifest["all_passed"])
            self.assertEqual(manifest["planned_count"], 9)
            self.assertEqual(manifest["passed_count"], 9)
            self.assertTrue(manifest["provenance"]["product_source"]["verified"])
            self.assertTrue(manifest["provenance"]["product_harness_distinct"])
            for entry in manifest["variants"]:
                self.assertEqual(entry["verdict"], "pass")
                self.assertGreater(entry["sqlite_total_rows"], 0)
                self.assertGreaterEqual(entry["captured_request_count"], 1)

    def test_fail_closed_on_zero_rows_or_unverified_engine_or_bad_guidance(self) -> None:
        g_bad_tool = tb10.verify_guidance_surface(
            offered_tools=("command",),
            suggested_tools=("command", "unpermitted_tool"),
            request_bytes=1024,
        )
        self.assertFalse(g_bad_tool["passed"])

        g_over_ceiling = tb10.verify_guidance_surface(
            offered_tools=("command",),
            suggested_tools=("command",),
            request_bytes=tb10.SHIPPED_REQUEST_CEILING_BYTES + 1,
        )
        self.assertFalse(g_over_ceiling["passed"])

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            app_bin, eng_bin, man_path = self._make_verified_engine(root)

            def fake_native_ok(app_p, out_p, *, engine_bin=None, engine_manifest=None, **kwargs):
                prov = tb10.collect_provenance(engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar-native-den")
                return {
                    "passed": True,
                    "screenshot_changed": True,
                    "api_readback": {"health": {"status": "ok"}},
                    "provenance": prov,
                }

            def empty_episode(variant, v_dir, **kwargs):
                return {
                    "session_id": "",
                    "project_id": "",
                    "db_path": "",
                    "captured_requests": [],
                    "episode_facts": {},
                    "network_fixtures": [],
                    "api_readback": {"health": {"status": "ok"}},
                    "sqlite_row_counts": {},
                    "sqlite_total_rows": 0,
                }

            # Onboarding-only / zero-row episode MUST fail closed!
            res = tb10.run_tb10_suite(
                root / "out-empty",
                app_bin=app_bin,
                engine_bin=eng_bin,
                engine_manifest=man_path,
                variant_ids=["X01-v1-worker-outside-write"],
                native_fn=fake_native_ok,
                episode_fn=empty_episode,
            )
            self.assertFalse(res["all_passed"])
            reasons = res["variants"][0]["failure_reasons"]
            self.assertIn("zero_scenario_sqlite_rows", reasons)
            self.assertIn("missing_session_api_readback", reasons)
            self.assertIn("missing_captured_model_request", reasons)


if __name__ == "__main__":
    unittest.main()
