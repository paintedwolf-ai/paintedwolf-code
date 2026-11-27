"""Unit tests for GB03 (Evidence recording and scope: V01..V07) fixture preparation."""
from __future__ import annotations

import pathlib
import tempfile
import unittest

from grounding.checker import check_slot
from grounding.reference_resolver import resolve_citation
from grounding.suites.gb03_evidence import (
    D16_RAW_SECRET_CANARY,
    GB03_FIXED_SLOTS,
    SLOTS_BY_SELECTOR,
    V01_SLOTS,
    V02_GENERATED_SPEC,
    V03_SLOTS,
    V04_SLOTS,
    V05_SLOTS,
    V06_SLOTS,
    V07_SLOTS,
    build_all_gb03_fixtures,
    gb03_manifest_counts,
    get_gb03_fixture,
    iter_v02_sample,
    validate_gb03_catalog,
)


class TestGB03CatalogManifest(unittest.TestCase):
    def test_catalog_matches_ledger_and_selectors(self) -> None:
        summary = validate_gb03_catalog()
        # V01(14) + V03(8) + V04(10, Decision 005/014) + V05(5) + V06(10) + V07(2+4) = 53
        self.assertEqual(summary["fixed_sidecar_slots"], 49)
        self.assertEqual(summary["fixed_in_process_slots"], 4)
        self.assertEqual(summary["fixed_total_slots"], 53)
        self.assertEqual(summary["v02_in_process_per_seed"], 3000)
        self.assertEqual(summary["v02_sidecar_per_seed"], 100)

        counts = gb03_manifest_counts()
        self.assertEqual(counts["V01"]["sidecar"], 14)
        self.assertEqual(counts["V03"]["sidecar"], 8)
        self.assertEqual(counts["V04"]["sidecar"], 10)
        self.assertEqual(counts["V05"]["sidecar"], 5)
        self.assertEqual(counts["V06"]["sidecar"], 10)
        self.assertEqual(counts["V07"]["sidecar"], 2)
        self.assertEqual(counts["V07"]["in_process"], 4)
        self.assertEqual(len(GB03_FIXED_SLOTS), 53)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 53)


class TestGB03FixedFixtures(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.fixtures = build_all_gb03_fixtures()

    def test_all_53_fixed_fixtures_resolve_and_pass_healthy_check_slot(self) -> None:
        self.assertEqual(len(self.fixtures), 53)
        for selector, fx in self.fixtures.items():
            with self.subTest(selector=selector):
                self.assertEqual(fx.selector, selector)
                for exp in fx.expectations:
                    verdict = resolve_citation(fx.delivery_log, exp.citation, fx.scope)
                    self.assertEqual(
                        verdict.verdict_class,
                        exp.expected_verdict_class,
                        f"{selector} {exp.citation.citation_id}: class mismatch ({verdict})",
                    )
                    self.assertEqual(
                        verdict.expected_exact_verdict,
                        exp.expected_exact_verdict,
                        f"{selector} {exp.citation.citation_id}: exact verdict mismatch ({verdict})",
                    )
                    if exp.expected_reason:
                        self.assertEqual(
                            verdict.reason,
                            exp.expected_reason,
                            f"{selector} {exp.citation.citation_id}: reason mismatch ({verdict})",
                        )
                    if exp.expected_supporting_handles:
                        self.assertEqual(
                            verdict.supporting_handles,
                            exp.expected_supporting_handles,
                            f"{selector} {exp.citation.citation_id}: handles mismatch",
                        )

                # Healthy surface check_slot passes with 0 defects
                outcome = fx.healthy_surface_outcome()
                report = check_slot(
                    fx.delivery_log,
                    tuple(e.citation for e in fx.expectations),
                    fx.scope,
                    outcome,
                )
                self.assertTrue(
                    report.passed,
                    f"{selector} healthy check_slot failed: {report.defects}",
                )

    def test_v04_includes_copied_second_file_tag_and_f3_contrast(self) -> None:
        v04_selectors = [s.selector for s in V04_SLOTS]
        self.assertEqual(len(v04_selectors), 10)
        copied = [s for s in v04_selectors if "copied" in s]
        self.assertTrue(copied, f"Expected copied-second-file-tag slot in {v04_selectors}")
        fx = get_gb03_fixture(copied[0])
        self.assertTrue(fx.expectations)

    def test_v05_secret_canary_screening(self) -> None:
        canary_slots = [
            fx for fx in self.fixtures.values()
            if fx.family_id == "V05" and fx.expected_forbidden_body_substrings
        ]
        self.assertTrue(canary_slots, "Expected D16 secret canary slot in V05")
        for fx in canary_slots:
            self.assertIn(D16_RAW_SECRET_CANARY, fx.expected_forbidden_body_substrings)
            for rec in fx.delivery_log.records:
                if rec.model_visible:
                    self.assertNotIn(
                        D16_RAW_SECRET_CANARY.encode("utf-8"),
                        rec.raw_bytes,
                        f"Secret canary leaked into model-visible record in {fx.selector}",
                    )

    def test_materialize_workspace_writes_deterministic_files(self) -> None:
        fx = get_gb03_fixture(V01_SLOTS[0].selector)
        with tempfile.TemporaryDirectory(prefix="gb03-ws-") as tmp:
            root = fx.materialize_workspace(tmp)
            for rel_path, expected in fx.workspace_files.items():
                actual = (pathlib.Path(root) / rel_path).read_text(encoding="utf-8")
                self.assertEqual(actual, expected)


class TestGB03V02GeneratedLookalikes(unittest.TestCase):
    def test_v02_lookalike_sample_creates_zero_promoted_citations(self) -> None:
        samples = iter_v02_sample(count_per_seed=10)
        self.assertEqual(len(samples), 20)
        for sample in samples:
            self.assertEqual(sample.case.expected_minted_tags, ())
            for cit in sample.spoofed_citations:
                verdict = resolve_citation(sample.delivery_log, cit, sample.scope)
                self.assertEqual(verdict.verdict_class, "ungrounded")
                self.assertEqual(verdict.expected_exact_verdict, "unverifiable")


if __name__ == "__main__":
    unittest.main()
