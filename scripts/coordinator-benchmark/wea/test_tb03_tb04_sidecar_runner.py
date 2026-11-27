"""Unit tests for the TB03 & TB04 Workers headless sidecar slot runner."""

from __future__ import annotations

import json
import pathlib
import sys
import unittest
from unittest import mock

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea import tb03_tb04_sidecar_runner as mod


class FakeApp:
    def __init__(self, directory: pathlib.Path) -> None:
        self.directory = directory
        self.ready_calls = 0
        self.close_calls = 0

    def ready(self) -> None:
        self.ready_calls += 1

    def close(self, *, cleanup: bool = False) -> None:
        self.close_calls += 1


class TB03TB04SidecarRunnerTests(unittest.TestCase):
    def test_exact_ledger_slot_counts_and_uniqueness(self) -> None:
        ids = [s.slot_id for s in mod.SLOT_SPECS]
        self.assertEqual(len(ids), len(set(ids)), "duplicate slot_ids in SLOT_SPECS")
        self.assertEqual(len(ids), 66)

        for card, expected_by_family in mod.LEDGER_SIDECAR_COUNTS.items():
            card_specs = mod.select_slots(card=card)
            expected_total = sum(expected_by_family.values())
            self.assertEqual(len(card_specs), expected_total, f"{card} total mismatch")
            for fam, want in expected_by_family.items():
                got = [s for s in card_specs if s.family == fam]
                self.assertEqual(len(got), want, f"{card}/{fam} count mismatch")

        restart_ids = {s.slot_id for s in mod.SLOT_SPECS if s.requires_restart}
        self.assertEqual(restart_ids, {"W10-sidecar-01", "W10-sidecar-02", "W10-sidecar-03"})

    def test_select_slots_filters(self) -> None:
        self.assertEqual(len(mod.select_slots(card="tb03")), 32)
        self.assertEqual(len(mod.select_slots(card="tb04")), 34)
        self.assertEqual(len(mod.select_slots(card="tb03", family="W07")), 12)
        self.assertEqual(len(mod.select_slots(card="tb04", family="W03")), 12)
        single = mod.select_slots(slot="W04-sidecar-03")
        self.assertEqual([s.slot_id for s in single], ["W04-sidecar-03"])

    def test_warmed_sidecar_reuse_receipts_and_missing_reporting(self) -> None:
        fake_prov = {
            "schema": "wea-provenance-v1",
            "lane": "sidecar",
            "host": {"platform": "test-host"},
            "product_source": {"effective_git_head": "bbeb46e72ce3d1cfc052259cadcce29185bb40bd", "verified": True},
            "harness_source": {"git_head": "1c202f1725"},
        }
        created_apps: list[FakeApp] = []

        def fake_create(app_dir: pathlib.Path, **_kwargs):
            app = FakeApp(app_dir)
            created_apps.append(app)
            return app

        def ok_fn(ctx: mod.SlotContext):
            assert ctx.app is not None
            return {
                "typed_result": {"slot": ctx.spec.slot_id},
                "independent_observation": {"hash_ok": True},
                "checks": [{"name": "ok", "passed": True, "detail": "pass"}],
            }

        def boom_fn(_ctx: mod.SlotContext):
            raise RuntimeError("simulated slot failure")

        patched_specs = (
            mod.SlotSpec("W08-sidecar-01", "W08", "tb03", "rev 1", ok_fn),
            mod.SlotSpec("W08-sidecar-02", "W08", "tb03", "rev 2", boom_fn),
            mod.SlotSpec("W11-sidecar-01", "W11", "tb03", "fanout 1", ok_fn),
        )

        import tempfile

        with tempfile.TemporaryDirectory() as td:
            out_dir = pathlib.Path(td) / "ev"
            with (
                mock.patch.object(mod, "SLOT_SPECS", patched_specs),
                mock.patch.object(mod, "_create_application", side_effect=fake_create),
                mock.patch.object(mod, "collect_provenance", return_value=fake_prov),
            ):
                summary = mod.run_sidecar_slots(out_dir, card="tb03", family="W08", run_id="test-run-1")

            # Single warmed sidecar created and closed once across both W08 slots.
            self.assertEqual(len(created_apps), 1)
            self.assertEqual(created_apps[0].ready_calls, 1)
            self.assertEqual(created_apps[0].close_calls, 1)

            # Summary separates implemented, observed, and missing variants.
            self.assertEqual(summary["implemented_count"], 3)
            self.assertEqual(summary["observed_count"], 2)
            self.assertEqual(summary["missing_count"], 1)
            self.assertEqual(summary["missing_slots"], ["W11-sidecar-01"])
            self.assertEqual(summary["passed_count"], 1)
            self.assertEqual(summary["failed_count"], 1)

            # Receipt schema check.
            rec1 = json.loads((out_dir / "tb03" / "W08-sidecar-01" / "receipt.json").read_text(encoding="utf-8"))
            for key in (
                "run_id", "slot_id", "family", "card", "lane",
                "host", "product_source", "harness_source",
                "typed_result", "independent_observation", "verdict",
            ):
                self.assertIn(key, rec1)
            self.assertEqual(rec1["lane"], "sidecar")
            self.assertTrue(rec1["verdict"]["passed"])

            rec2 = json.loads((out_dir / "tb03" / "W08-sidecar-02" / "receipt.json").read_text(encoding="utf-8"))
            self.assertFalse(rec2["verdict"]["passed"])
            self.assertEqual(rec2["verdict"]["status"], "error")

    def test_restart_slot_does_not_spawn_shared_warmed_sidecar(self) -> None:
        fake_prov = {
            "host": {"platform": "test-host"},
            "product_source": {"effective_git_head": "bbeb46e72ce3d1cfc052259cadcce29185bb40bd"},
            "harness_source": {"git_head": "1c202f1725"},
        }
        created_apps: list[FakeApp] = []

        def fake_create(app_dir: pathlib.Path, **_kwargs):
            app = FakeApp(app_dir)
            created_apps.append(app)
            return app

        def restart_fn(ctx: mod.SlotContext):
            assert ctx.app is None
            a1, _ = ctx.start_dedicated_app("r1")
            a1.close(cleanup=False)
            a2, _ = ctx.start_dedicated_app("r1")
            a2.close(cleanup=False)
            return {
                "typed_result": {"restarted": True},
                "independent_observation": {"two_procs": True},
                "checks": [{"name": "restarted", "passed": True}],
            }

        patched_specs = (
            mod.SlotSpec("W10-sidecar-01", "W10", "tb04", "restart land", restart_fn, requires_restart=True),
        )

        import tempfile

        with tempfile.TemporaryDirectory() as td:
            out_dir = pathlib.Path(td) / "ev"
            with (
                mock.patch.object(mod, "SLOT_SPECS", patched_specs),
                mock.patch.object(mod, "_create_application", side_effect=fake_create),
                mock.patch.object(mod, "collect_provenance", return_value=fake_prov),
            ):
                summary = mod.run_sidecar_slots(out_dir, card="tb04", family="W10", run_id="test-restart")

            self.assertEqual(len(created_apps), 2)
            self.assertEqual(summary["passed_count"], 1)
            self.assertEqual(summary["failed_count"], 0)

    def test_cli_list_exits_zero(self) -> None:
        rc = mod.main(["--card", "all", "--list"])
        self.assertEqual(rc, 0)


if __name__ == "__main__":
    unittest.main()
