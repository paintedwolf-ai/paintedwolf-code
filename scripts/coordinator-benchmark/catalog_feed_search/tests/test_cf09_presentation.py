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
    classify_files_tree_notice,
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

    def test_verify_returns_missing_observation_when_uncorrelated_or_unready(self) -> None:
        uncorrelated = PresentationObservation(
            surface="search_stage",
            state="failed",
            platform="linux",
            native_window=True,
            api_response={"issues": [{"executor": "code", "reason": "catalog_failed", "count": 1}]},
            coverage_note_visible=False,
            request_correlated=False,
            workspace_ready=True,
        )
        self.assertEqual(verify_presentation_observation(uncorrelated).verdict, "MISSING_OBSERVATION")

        unready = PresentationObservation(
            surface="search_stage",
            state="failed",
            platform="linux",
            native_window=True,
            api_response={"issues": [{"executor": "code", "reason": "catalog_failed", "count": 1}]},
            coverage_note_visible=False,
            request_correlated=True,
            workspace_ready=False,
        )
        self.assertEqual(verify_presentation_observation(unready).verdict, "MISSING_OBSERVATION")

    def test_verify_returns_unverified_when_requested_state_not_in_effect(self) -> None:
        settled_warming = PresentationObservation(
            surface="search_stage",
            state="warming",
            platform="linux",
            native_window=True,
            api_response={"status": "complete", "exhaustive": True, "issues": []},
            coverage_note_visible=False,
            request_correlated=True,
            workspace_ready=True,
        )
        res = verify_presentation_observation(settled_warming)
        self.assertEqual(res.verdict, "UNVERIFIED")
        self.assertTrue(any("requested_state_not_in_effect" in v for v in res.violations))

    def test_classify_files_tree_root_summary_notices_and_negative_control(self) -> None:
        # Positive controls matching the real ProjectRootSummary coverage card (`files-root-summary-coverage`).
        failed_cls = classify_files_tree_notice(
            "1 folder could not be read and matches may be missing. Retry file index",
            testid="files-root-summary-coverage",
            retry_button_visible=True,
        )
        self.assertTrue(failed_cls.visible)
        self.assertEqual(failed_cls.testid, "files-root-summary-coverage")
        self.assertIn("catalog_failed", failed_cls.categories)
        self.assertTrue(failed_cls.retry_offered)

        bounded_cls = classify_files_tree_notice(
            "1 folder exceeded the indexing budget and matches may be missing. Retry file index",
            testid="files-root-summary-coverage",
            retry_button_visible=True,
        )
        self.assertTrue(bounded_cls.visible)
        self.assertEqual(bounded_cls.testid, "files-root-summary-coverage")
        self.assertIn("catalog_bounded", bounded_cls.categories)
        self.assertTrue(bounded_cls.retry_offered)

        # Negative control (Decision 073): a pane with visible results and NO coverage notice
        # must classify as visible=False and fail verification when a coverage gap is declared.
        plain_pane_cls = classify_files_tree_notice(
            "12 files\nhealthy/target.txt\nwide_dir/item_00.txt",
            testid="",
            retry_button_visible=False,
        )
        self.assertFalse(plain_pane_cls.visible)
        self.assertEqual(plain_pane_cls.categories, ())
        self.assertEqual(plain_pane_cls.testid, "")

        empty_notice_cls = classify_files_tree_notice("", testid="", retry_button_visible=False)
        self.assertFalse(empty_notice_cls.visible)

        no_notice_obs = PresentationObservation(
            surface="files_tree",
            state="failed",
            platform="macos",
            native_window=True,
            api_response={
                "coverage": [
                    {"root_id": "r1", "state": "ready", "discovery_complete": True, "failed_directories": 1}
                ]
            },
            coverage_note_visible=plain_pane_cls.visible,
            coverage_note_testid=plain_pane_cls.testid,
            results_visible_count=12,
            empty_final_shown=False,
        )
        verdict = verify_presentation_observation(no_notice_obs)
        self.assertEqual(verdict.verdict, "DEFECT")
        self.assertTrue(any("missing_coverage_presentation" in v for v in verdict.violations))

        # Decision 075 visible-but-wrong-notice control: a visible catalog_failed notice
        # MUST NOT score credit for a declared catalog_bounded condition.
        wrong_notice_obs = PresentationObservation(
            surface="files_tree",
            state="incomplete",
            platform="macos",
            native_window=True,
            selected_root_id="r-selected",
            api_response={
                "coverage": [
                    {"root_id": "r-other", "state": "ready", "discovery_complete": True, "failed_directories": 1},
                    {"root_id": "r-selected", "state": "ready", "discovery_complete": True, "bounded_directories": 1},
                ]
            },
            coverage_note_visible=failed_cls.visible,
            coverage_note_testid=failed_cls.testid,
            coverage_notice_categories=failed_cls.categories,
            results_visible_count=12,
            empty_final_shown=False,
            notes=failed_cls.raw_text,
        )
        wrong_verdict = verify_presentation_observation(wrong_notice_obs)
        self.assertEqual(wrong_verdict.verdict, "DEFECT")
        self.assertEqual(wrong_verdict.declared_facts, ("catalog_bounded",))
        self.assertTrue(any("mismatched_coverage_presentation" in v for v in wrong_verdict.violations))

        # Matching typed category for the selected root passes.
        matched_obs = PresentationObservation(
            surface="files_tree",
            state="incomplete",
            platform="macos",
            native_window=True,
            selected_root_id="r-selected",
            api_response={
                "coverage": [
                    {"root_id": "r-other", "state": "ready", "discovery_complete": True, "failed_directories": 1},
                    {"root_id": "r-selected", "state": "ready", "discovery_complete": True, "bounded_directories": 1},
                ]
            },
            coverage_note_visible=bounded_cls.visible,
            coverage_note_testid=bounded_cls.testid,
            coverage_notice_categories=bounded_cls.categories,
            results_visible_count=12,
            empty_final_shown=False,
            notes=bounded_cls.raw_text,
        )
        self.assertEqual(verify_presentation_observation(matched_obs).verdict, "PASS")

        # Decision 075 ready-tree-without-correlated-source-index control: must remain unresolved.
        uncorrelated_tree = PresentationObservation(
            surface="files_tree",
            state="failed",
            platform="macos",
            native_window=True,
            selected_root_id="r-selected",
            api_response={},
            coverage_note_visible=False,
            results_visible_count=1,
            request_correlated=False,
            workspace_ready=True,
        )
        self.assertEqual(verify_presentation_observation(uncorrelated_tree).verdict, "MISSING_OBSERVATION")

    def test_refreshing_slot_requires_visible_refresh_indication(self) -> None:
        # Decision 078: catalog_refreshing as the sole fact must NOT auto-pass without visible refresh indication.
        no_refresh_ui = PresentationObservation(
            surface="search_stage",
            state="refreshing",
            platform="linux",
            native_window=True,
            api_response={"issues": [{"executor": "code", "reason": "catalog_refreshing", "count": 1}]},
            coverage_note_visible=False,
        )
        verdict = verify_presentation_observation(no_refresh_ui)
        self.assertEqual(verdict.verdict, "DEFECT")
        self.assertTrue(any("missing_coverage_presentation" in v for v in verdict.violations))

        with_refresh_ui = PresentationObservation(
            surface="files_tree",
            state="refreshing",
            platform="linux",
            native_window=True,
            api_response={"refreshing": True, "coverage": [{"root_id": "r1", "state": "ready", "refreshing": True}]},
            coverage_note_visible=True,
            coverage_note_testid="files-root-summary-coverage",
            coverage_notice_categories=("catalog_refreshing",),
            notes="Files are refreshing in the background.",
        )
        self.assertEqual(verify_presentation_observation(with_refresh_ui).verdict, "PASS")

        # Decision 082: actual rendered copy from search-status.ts for incomplete+refreshing root.
        live_copy_cls = classify_files_tree_notice(
            "Files are still being indexed. Results refresh automatically while this view is open.",
            testid="files-root-summary-coverage",
        )
        self.assertTrue(live_copy_cls.visible)
        self.assertIn("catalog_incomplete", live_copy_cls.categories)
        self.assertIn("catalog_refreshing", live_copy_cls.categories)
        self.assertIn("catalog_warming", live_copy_cls.categories)
        live_obs = PresentationObservation(
            surface="files_tree",
            state="refreshing",
            platform="linux",
            native_window=True,
            selected_root_id="r1",
            api_response={
                "refreshing": True,
                "coverage": [
                    {"root_id": "r1", "state": "ready", "discovery_complete": False, "refreshing": True}
                ],
            },
            coverage_note_visible=live_copy_cls.visible,
            coverage_note_testid=live_copy_cls.testid,
            coverage_notice_categories=live_copy_cls.categories,
            notes=live_copy_cls.raw_text,
        )
        self.assertEqual(verify_presentation_observation(live_obs).verdict, "PASS")

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
