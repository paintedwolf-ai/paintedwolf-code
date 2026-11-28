"""Unit tests for CF05 (F01–F06) deterministic generators, edge slots, and summaries."""

from __future__ import annotations

import unittest

from catalog_feed_search.families.cf05_change_feed import CaseOutcome, summarize_outcomes
from catalog_feed_search.families.cf05_generators import (
    CF05_LEDGER_EDGE_COUNTS,
    CF05_LEDGER_SIDECAR_COUNTS,
    f01_edge_cases,
    f02_edge_cases,
    f03_edge_cases,
    f04_edge_cases,
    f05_edge_cases,
    f06_edge_cases,
    generate_f01_case,
    generate_f02_case,
    generate_f03_case,
    generate_f04_case,
    generate_f05_case,
    generate_f06_case,
)
from catalog_feed_search.seeds import BASE_SEED_PRIMARY


class TestCF05GeneratorsAndEdges(unittest.TestCase):
    def test_edge_counts_match_ledger(self) -> None:
        self.assertEqual(len(f01_edge_cases()), CF05_LEDGER_EDGE_COUNTS["F01"])
        self.assertEqual(len(f02_edge_cases()), CF05_LEDGER_EDGE_COUNTS["F02"])
        self.assertEqual(len(f03_edge_cases()), CF05_LEDGER_EDGE_COUNTS["F03"])
        self.assertEqual(len(f04_edge_cases()), CF05_LEDGER_EDGE_COUNTS["F04"])
        self.assertEqual(len(f05_edge_cases()), CF05_LEDGER_EDGE_COUNTS["F05"])
        self.assertEqual(len(f06_edge_cases()), CF05_LEDGER_EDGE_COUNTS["F06"])

    def test_f01_determinism_and_range(self) -> None:
        c0_a = generate_f01_case(BASE_SEED_PRIMARY, 0)
        c0_b = generate_f01_case(BASE_SEED_PRIMARY, 0)
        self.assertEqual(c0_a, c0_b)
        self.assertEqual(len(c0_a.change_script.ops), 1)
        c1 = generate_f01_case(BASE_SEED_PRIMARY, 1)
        self.assertEqual(len(c1.change_script.ops), 200)
        self.assertGreaterEqual(len(c1.tree_spec.entries), 500)

    def test_f02_range_2000_to_50000(self) -> None:
        first = generate_f02_case(BASE_SEED_PRIMARY, 0)
        last = generate_f02_case(BASE_SEED_PRIMARY, CF05_LEDGER_SIDECAR_COUNTS["F02"] - 1)
        self.assertEqual(first.write_count, 2000)
        self.assertEqual(last.write_count, 50000)

    def test_f03_f04_f05_f06_determinism(self) -> None:
        self.assertEqual(generate_f03_case(BASE_SEED_PRIMARY, 5), generate_f03_case(BASE_SEED_PRIMARY, 5))
        self.assertEqual(generate_f04_case(BASE_SEED_PRIMARY, 3), generate_f04_case(BASE_SEED_PRIMARY, 3))
        self.assertEqual(generate_f05_case(BASE_SEED_PRIMARY, 7), generate_f05_case(BASE_SEED_PRIMARY, 7))
        self.assertEqual(generate_f06_case(BASE_SEED_PRIMARY, 9), generate_f06_case(BASE_SEED_PRIMARY, 9))
        f05_first = generate_f05_case(BASE_SEED_PRIMARY, 0)
        f05_last = generate_f05_case(BASE_SEED_PRIMARY, 99)
        self.assertEqual(len(f05_first.movements), 1)
        self.assertEqual(len(f05_last.movements), 10)
        f06_last = generate_f06_case(BASE_SEED_PRIMARY, 39)
        self.assertEqual(f06_last.client_count, 6)
        self.assertEqual(f06_last.drop_count, 20)

    def test_summarize_outcomes_counts_declared_gaps_and_defects(self) -> None:
        outcomes = [
            CaseOutcome("F01-0000", "F01", False, "PASS", "", [], 10, {}),
            CaseOutcome("F01-0001", "F01", False, "PASS_DECLARED_GAP", "", ["resync"], 12, {}),
            CaseOutcome("F01-EDGE-01", "F01", True, "DEFECT", "missing path", [], 5, {}),
        ]
        summary = summarize_outcomes(outcomes)
        self.assertEqual(summary["total"], 3)
        self.assertEqual(summary["passed"], 2)
        self.assertEqual(summary["pass_clean"], 1)
        self.assertEqual(summary["pass_declared_gap"], 1)
        self.assertEqual(summary["defects"], 1)
        self.assertEqual(summary["declared_gap_counts"], {"resync": 1})


if __name__ == "__main__":
    unittest.main()
