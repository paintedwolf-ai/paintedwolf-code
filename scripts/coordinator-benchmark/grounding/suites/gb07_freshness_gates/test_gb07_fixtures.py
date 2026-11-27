"""Unit tests for GB07 (Freshness, closeout gates, friction: G01..G05, D5 tripled)."""
from __future__ import annotations

import unittest

from grounding.checker import check_slot
from grounding.reference_resolver import resolve_citation
from grounding.selectors import WORDING_VARIANTS
from grounding.suites.gb07_freshness_gates import (
    ALL_GB07_SLOTS,
    SLOTS_BY_SELECTOR,
    build_all_gb07_fixtures,
    build_gb07_fixture,
    validate_gb07_catalog,
)


class TestGB07CatalogAndFixtures(unittest.TestCase):
    def test_catalog_matches_ledger_and_d5_tripling(self) -> None:
        summary = validate_gb07_catalog()
        self.assertEqual(summary["sidecar_slots"], 28)
        self.assertEqual(summary["in_process_slots"], 6)
        self.assertEqual(summary["base_slots"], 34)
        self.assertEqual(summary["d5_tripled_per_platform"], 102)
        self.assertEqual(len(ALL_GB07_SLOTS), 34)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 34)

    def test_all_102_wording_fixtures_pass_and_are_wording_invariant(self) -> None:
        fixtures = build_all_gb07_fixtures()
        self.assertEqual(len(fixtures), 102)
        for spec in ALL_GB07_SLOTS:
            verdicts = []
            for w in WORDING_VARIANTS:
                fx = fixtures[(spec.selector, w)]
                res = check_slot(fx.delivery_log, fx.citations, fx.scope, fx.build_healthy_outcome())
                self.assertTrue(res.passed, f"{spec.selector}/{w} failed: {res.defects}")
                verdicts.append(tuple(resolve_citation(fx.delivery_log, c, fx.scope).verdict_class for c in fx.citations))
            self.assertEqual(verdicts[0], verdicts[1])
            self.assertEqual(verdicts[0], verdicts[2])

    def test_d8_inprocess_block_mode_escalation_codes(self) -> None:
        fx3 = build_gb07_fixture("G04-06-inprocess-block-mode-3-total-escalates")
        fx2 = build_gb07_fixture("G04-07-inprocess-block-mode-2-consecutive-escalates")
        self.assertEqual(fx3.expected_code, "COORDINATOR_GROUNDING_ESCALATED")
        self.assertEqual(fx2.expected_code, "COORDINATOR_GROUNDING_ESCALATED")
        self.assertTrue(fx3.expected_breaker_tripped)
        self.assertTrue(fx2.expected_breaker_tripped)


if __name__ == "__main__":
    unittest.main()
