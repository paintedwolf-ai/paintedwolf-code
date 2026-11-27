"""Unit tests for GB05 (Speaking surfaces: S01..S07, D5 tripled) fixture preparation."""
from __future__ import annotations

import unittest

from grounding.checker import check_slot
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


if __name__ == "__main__":
    unittest.main()
