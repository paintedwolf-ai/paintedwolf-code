"""Unit tests for CF09 presentation harness and opt-in dev diagnostic checker."""

from __future__ import annotations

import json
import os
from pathlib import Path
import stat
import tempfile
import unittest

from catalog_feed_search.families.cf09_diagnostic_harness import (
    WorkCounterComparison,
    load_diagnostic_jsonl,
    verify_diagnostic_qualification,
)
from catalog_feed_search.families.cf09_presentation import (
    COVERAGE_STATES,
    NEEDLE_TOKEN,
    PresentationObservation,
    build_coverage_fixture,
    extract_declared_facts,
    mutate_for_refreshing,
    verify_presentation_observation,
    write_presentation_evidence,
)


class TestCF09Presentation(unittest.TestCase):
    def test_build_fixtures_all_four_states(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp)
            for state in COVERAGE_STATES:
                fx = build_coverage_fixture(base, state, warming_file_count=80)
                try:
                    self.assertTrue(fx.needle_file.exists())
                    self.assertIn(NEEDLE_TOKEN, fx.needle_file.read_text(encoding="utf-8"))
                    if state == "failed":
                        self.assertIsNotNone(fx.locked_dir)
                        assert fx.locked_dir is not None
                        mode = stat.S_IMODE(os.stat(fx.locked_dir).st_mode)
                        self.assertEqual(mode, 0o000)
                    elif state == "incomplete":
                        scope = fx.root_dir / ".paintedwolf" / "source-scope.yaml"
                        self.assertTrue(scope.exists())
                        self.assertIn("directory_entries: 2", scope.read_text(encoding="utf-8"))
                    elif state == "refreshing":
                        added = mutate_for_refreshing(fx, burst_count=12)
                        self.assertEqual(added, 12)
                        self.assertTrue((fx.root_dir / "refresh_burst" / "burst_0000.txt").exists())
                finally:
                    fx.cleanup()

    def test_extract_declared_facts_from_api_shapes(self) -> None:
        search_resp = {
            "issues": [
                {"executor": "code", "reason": "catalog_incomplete", "count": 1},
                {"executor": "code", "reason": "catalog_bounded", "count": 3},
            ],
            "exhaustive": False,
        }
        self.assertEqual(
            extract_declared_facts(search_resp),
            ("catalog_bounded", "catalog_incomplete"),
        )

        index_resp = {
            "state": "warming",
            "refreshing": True,
            "coverage": [
                {
                    "root_id": "r1",
                    "state": "failed",
                    "error": "permission denied",
                    "failed_directories": 1,
                }
            ],
        }
        self.assertEqual(
            extract_declared_facts(index_resp),
            ("catalog_failed", "catalog_refreshing", "catalog_warming", "executor_error"),
        )

    def test_verify_flags_false_final_empty_while_warming_or_failed(self) -> None:
        obs = PresentationObservation(
            surface="search_stage",
            state="warming",
            platform="linux",
            native_window=True,
            api_response={"issues": [{"executor": "code", "reason": "catalog_warming", "count": 1}]},
            coverage_note_visible=True,
            empty_final_shown=True,
        )
        verdict = verify_presentation_observation(obs)
        self.assertEqual(verdict.verdict, "DEFECT")
        self.assertTrue(any("false_final_empty" in v for v in verdict.violations))

    def test_verify_flags_missing_coverage_note_and_non_native_window(self) -> None:
        obs = PresentationObservation(
            surface="quick_open",
            state="failed",
            platform="macos",
            native_window=False,
            api_response={
                "coverage": [{"root_id": "r1", "state": "ready", "discovery_complete": True, "failed_directories": 1}]
            },
            coverage_note_visible=False,
            empty_final_shown=False,
        )
        verdict = verify_presentation_observation(obs)
        self.assertEqual(verdict.verdict, "DEFECT")
        self.assertTrue(any("not_native_window" in v for v in verdict.violations))
        self.assertTrue(any("missing_coverage_presentation" in v for v in verdict.violations))

    def test_verify_replace_limited_requires_apply_all_disabled(self) -> None:
        bad = PresentationObservation(
            surface="replace_review",
            state="incomplete",
            platform="linux",
            native_window=True,
            api_response={
                "state": "limited",
                "issues": [{"executor": "code", "reason": "catalog_bounded", "count": 1}],
            },
            coverage_note_visible=True,
            apply_all_disabled=False,
        )
        self.assertEqual(verify_presentation_observation(bad).verdict, "DEFECT")

        good = PresentationObservation(
            surface="replace_review",
            state="incomplete",
            platform="linux",
            native_window=True,
            api_response={
                "state": "limited",
                "issues": [{"executor": "code", "reason": "catalog_bounded", "count": 1}],
            },
            coverage_note_visible=True,
            apply_all_disabled=True,
        )
        self.assertEqual(verify_presentation_observation(good).verdict, "PASS")

    def test_write_presentation_evidence_writes_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "CF09"
            verdict = verify_presentation_observation(
                PresentationObservation(
                    surface="search_stage",
                    state="incomplete",
                    platform="linux",
                    native_window=True,
                    api_response={"issues": [{"executor": "code", "reason": "catalog_bounded", "count": 1}]},
                    coverage_note_visible=True,
                )
            )
            report_path = write_presentation_evidence(
                out,
                platform="linux",
                baseline_commit="3bb1464d22dd0a38a149d37610810e7a0324e8cf",
                binary_path="/bin/lycaon-dev",
                binary_sha256="abc",
                verdicts=[verdict],
            )
            self.assertTrue(report_path.exists())
            manifest = json.loads((out / "sha256_manifest.json").read_text(encoding="utf-8"))
            self.assertIn("cf09_presentation_linux.json", manifest)


class TestCF09DiagnosticHarness(unittest.TestCase):
    def test_qualification_passes_on_clean_injected_and_declared_gap_records(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            jsonl = Path(tmp) / "diag.jsonl"
            injected = [
                {
                    "project_id": "p1",
                    "root_id": "r1",
                    "generation_instance": 1,
                    "generation_revision": 2,
                    "disagreements": [
                        {"path": "a.txt", "kind": "missing_in_catalog"},
                        {"path": "b.txt", "kind": "extra_in_catalog"},
                        {"path": "c.txt", "kind": "kind_mismatch"},
                    ],
                }
            ]
            jsonl.write_text("\n".join(json.dumps(r) for r in injected) + "\n", encoding="utf-8")
            loaded = load_diagnostic_jsonl(jsonl)
            res = verify_diagnostic_qualification(
                injected_records=loaded,
                declared_gap_records=[{"project_id": "p1", "root_id": "r1", "disagreements": []}],
                work_comparison=WorkCounterComparison(
                    disabled_product_work={"dir_reads": 5, "metadata_reads": 12},
                    enabled_product_work={"dir_reads": 5, "metadata_reads": 12},
                    diagnostic_reads={"dir_reads": 2, "entry_reads": 8},
                    answers_equal=True,
                ),
            )
            self.assertEqual(res.verdict, "PASS")
            self.assertTrue(res.deduped_ok)
            self.assertTrue(res.bounded_ok)

    def test_qualification_flags_work_counter_drift_and_gap_false_positive(self) -> None:
        res = verify_diagnostic_qualification(
            injected_records=[
                {
                    "project_id": "p1",
                    "root_id": "r1",
                    "generation_instance": 1,
                    "generation_revision": 1,
                    "disagreements": [
                        {"path": "a.txt", "kind": "missing_in_catalog"},
                        {"path": "a.txt", "kind": "missing_in_catalog"},
                    ],
                }
            ],
            declared_gap_records=[
                {"project_id": "p1", "root_id": "r1", "disagreements": [{"path": "x", "kind": "missing_in_catalog"}]}
            ],
            work_comparison=WorkCounterComparison(
                disabled_product_work={"dir_reads": 5},
                enabled_product_work={"dir_reads": 7},
                diagnostic_reads={"dir_reads": 2},
                answers_equal=True,
            ),
        )
        self.assertEqual(res.verdict, "DEFECT")
        self.assertFalse(res.deduped_ok)
        self.assertEqual(res.false_positives_on_declared_gaps, 1)
        self.assertTrue(any("product_work_counters_differ" in v for v in res.violations))


if __name__ == "__main__":
    unittest.main()
