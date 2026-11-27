"""Unit tests for GB06 (Findings, verdicts, worker results: W01..W04, D5 tripled)."""
from __future__ import annotations

import unittest

from grounding.checker import check_slot
from grounding.reference_resolver import resolve_citation
from grounding.selectors import WORDING_VARIANTS
from grounding.suites.gb06_writes_verdicts import (
    ALL_GB06_SLOTS,
    SLOTS_BY_SELECTOR,
    build_all_gb06_fixtures,
    build_gb06_fixture,
    validate_gb06_catalog,
)


class TestGB06CatalogAndFixtures(unittest.TestCase):
    def test_catalog_matches_ledger_and_d5_tripling(self) -> None:
        summary = validate_gb06_catalog()
        self.assertEqual(summary["sidecar_slots"], 31)
        self.assertEqual(summary["in_process_slots"], 0)
        self.assertEqual(summary["base_slots"], 31)
        self.assertEqual(summary["d5_tripled_per_platform"], 93)
        self.assertEqual(len(ALL_GB06_SLOTS), 31)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 31)

    def test_all_93_wording_fixtures_pass_and_are_wording_invariant(self) -> None:
        fixtures = build_all_gb06_fixtures()
        self.assertEqual(len(fixtures), 93)
        for spec in ALL_GB06_SLOTS:
            verdicts = []
            for w in WORDING_VARIANTS:
                fx = fixtures[(spec.selector, w)]
                res = check_slot(fx.delivery_log, fx.citations, fx.scope, fx.build_healthy_outcome())
                self.assertTrue(res.passed, f"{spec.selector}/{w} failed: {res.defects}")
                verdicts.append(tuple(resolve_citation(fx.delivery_log, c, fx.scope).verdict_class for c in fx.citations))
            self.assertEqual(verdicts[0], verdicts[1])
            self.assertEqual(verdicts[0], verdicts[2])

    def test_gb01_f1_active_and_deprecated_compatibility_code(self) -> None:
        fx = build_gb06_fixture("W03-04-implementer-missing-artifact-active-code")
        self.assertEqual(fx.expected_code, "WORKER_SUMMARY_NO_ARTIFACT")
        self.assertEqual(fx.deprecated_compat_code, "WORKER_IMPLEMENT_NO_ARTIFACT")
        self.assertFalse(fx.expected_stored)

    def test_w04_artifact_provenance_separates_host_and_spoofed_uuid(self) -> None:
        fx = build_gb06_fixture("W04-06-artifact-provenance-host-vs-fixture")
        v_host = resolve_citation(fx.delivery_log, fx.citations[0], fx.scope)
        v_spoof = resolve_citation(fx.delivery_log, fx.citations[1], fx.scope)
        self.assertEqual(v_host.verdict_class, "grounded")
        self.assertEqual(v_spoof.verdict_class, "ungrounded")


if __name__ == "__main__":
    unittest.main()
