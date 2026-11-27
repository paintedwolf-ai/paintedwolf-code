"""Unit tests for GB05 (Speaking surfaces: S01..S07, D5 tripled) fixture preparation & full runner."""
from __future__ import annotations

import pathlib
import unittest

from grounding.checker import check_slot
from grounding.inprocess_driver import validate_test_only_overlay
from grounding.reference_resolver import resolve_citation
from grounding.selectors import WORDING_VARIANTS
from grounding.suites.gb05_surfaces import (
    ALL_GB05_SLOTS,
    S06_SLOTS,
    SLOTS_BY_SELECTOR,
    build_all_gb05_fixtures,
    build_gb05_fixture,
    validate_gb05_catalog,
)
from grounding.suites.gb05_surfaces.full_runner import (
    INPROCESS_SLOTS,
    OVERLAY_SOURCE_REL,
    OVERLAY_TARGET_REL,
    SIDECAR_SLOT_HANDLERS,
    select_sidecar_runs,
)


class TestGB05CatalogAndFixtures(unittest.TestCase):
    def test_catalog_matches_ledger_and_d5_tripling(self) -> None:
        summary = validate_gb05_catalog()
        self.assertEqual(summary["sidecar_slots"], 49)
        self.assertEqual(summary["in_process_slots"], 2)
        self.assertEqual(summary["base_slots"], 51)
        self.assertEqual(summary["d5_tripled_per_platform"], 153)
        self.assertEqual(len(ALL_GB05_SLOTS), 51)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 51)
        self.assertEqual(len(S06_SLOTS), 15)  # 13 sidecar + 2 in-process (Decision 008/014)
        self.assertIn("S06-13-historical-unknown-intent-boundary", SLOTS_BY_SELECTOR)

    def test_all_153_wording_fixtures_pass_healthy_check_slot_and_are_wording_invariant(self) -> None:
        fixtures = build_all_gb05_fixtures()
        self.assertEqual(len(fixtures), 153)
        for spec in ALL_GB05_SLOTS:
            verdicts_by_wording: list[tuple[str, ...]] = []
            for wording in WORDING_VARIANTS:
                fx = fixtures[(spec.selector, wording)]
                outcome = fx.build_healthy_outcome()
                res = check_slot(fx.delivery_log, fx.citations, fx.scope, outcome)
                self.assertTrue(res.passed, f"{spec.selector}/{wording} failed: {res.defects}")
                verdicts_by_wording.append(
                    tuple(resolve_citation(fx.delivery_log, c, fx.scope).verdict_class for c in fx.citations)
                )
            # D5 invariant: outcomes identical across all 3 wordings
            self.assertEqual(verdicts_by_wording[0], verdicts_by_wording[1], spec.selector)
            self.assertEqual(verdicts_by_wording[0], verdicts_by_wording[2], spec.selector)

    def test_s06_friction_charged_only_for_unresolved_handles_and_urls(self) -> None:
        fx_h = build_gb05_fixture("S06-02-midturn-unresolved-handle-withheld-charges-friction")
        fx_u = build_gb05_fixture("S06-03-midturn-unresolved-url-withheld-charges-friction")
        fx_b = build_gb05_fixture("S06-04-midturn-brief-only-status-no-friction")
        self.assertTrue(fx_h.expected_friction_charged)
        self.assertTrue(fx_u.expected_friction_charged)
        self.assertFalse(fx_b.expected_friction_charged)
        self.assertTrue(fx_h.expected_tools_executed)
        self.assertTrue(fx_u.expected_tools_executed)

    def test_all_51_base_slots_mapped_to_executable_handlers(self) -> None:
        sidecar_catalog = {s.selector for s in ALL_GB05_SLOTS if s.lane == "sidecar"}
        inproc_catalog = {s.selector for s in ALL_GB05_SLOTS if s.lane == "in_process"}

        self.assertEqual(len(sidecar_catalog), 49)
        self.assertEqual(len(inproc_catalog), 2)
        self.assertEqual(set(SIDECAR_SLOT_HANDLERS.keys()), sidecar_catalog)
        self.assertEqual(set(INPROCESS_SLOTS), inproc_catalog)
        self.assertEqual(set(SIDECAR_SLOT_HANDLERS.keys()) | set(INPROCESS_SLOTS), set(SLOTS_BY_SELECTOR.keys()))
        self.assertEqual(len(set(SIDECAR_SLOT_HANDLERS.keys()) | set(INPROCESS_SLOTS)), 51)

        for sel, handler in SIDECAR_SLOT_HANDLERS.items():
            self.assertTrue(callable(handler), f"Handler for {sel} must be callable")

        # Verify in-process Go overlay exists, is test-only, and covers both in-process slots + all 3 D5 wordings
        grounding_dir = pathlib.Path(__file__).resolve().parents[2]
        overlay_src = grounding_dir / OVERLAY_SOURCE_REL
        self.assertTrue(overlay_src.is_file(), f"Missing overlay file: {overlay_src}")
        validated = validate_test_only_overlay({str(OVERLAY_TARGET_REL): str(overlay_src)})
        self.assertEqual(len(validated), 1)
        go_src = overlay_src.read_text(encoding="utf-8")
        for sel in INPROCESS_SLOTS:
            self.assertIn(sel, go_src)
        for w in WORDING_VARIANTS:
            self.assertIn(f'"{w}"', go_src)

    def test_sharding_partitions_all_147_sidecar_runs_disjointly_without_duplicates(self) -> None:
        full_runs = select_sidecar_runs()
        self.assertEqual(len(full_runs), 147)
        full_keys = [(spec.selector, wording) for spec, wording in full_runs]
        self.assertEqual(len(set(full_keys)), 147)

        for num_shards in (1, 2, 3, 4, 7, 10, 49):
            seen: set[tuple[str, str]] = set()
            total = 0
            for k in range(num_shards):
                shard_runs = select_sidecar_runs(shard=k, shards=num_shards)
                shard_keys = [(spec.selector, wording) for spec, wording in shard_runs]
                # No duplicates within shard
                self.assertEqual(len(shard_keys), len(set(shard_keys)))
                # Disjoint across shards
                overlap = seen.intersection(shard_keys)
                self.assertEqual(overlap, set(), f"Overlap detected for shards={num_shards}, shard={k}: {overlap}")
                seen.update(shard_keys)
                total += len(shard_keys)
            self.assertEqual(total, 147, f"Total mismatch for shards={num_shards}")
            self.assertEqual(seen, set(full_keys), f"Coverage mismatch for shards={num_shards}")

        # Family and wording filtering
        s01_confident = select_sidecar_runs(families=("S01",), wordings=("confident",))
        self.assertEqual(len(s01_confident), 5)
        self.assertTrue(all(spec.family_id == "S01" and w == "confident" for spec, w in s01_confident))

        with self.assertRaises(ValueError):
            select_sidecar_runs(shard=2, shards=2)
        with self.assertRaises(ValueError):
            select_sidecar_runs(shard=0, shards=0)
        with self.assertRaises(ValueError):
            select_sidecar_runs(families=("NOPE",))
        with self.assertRaises(ValueError):
            select_sidecar_runs(wordings=("invalid",))


if __name__ == "__main__":
    unittest.main()
