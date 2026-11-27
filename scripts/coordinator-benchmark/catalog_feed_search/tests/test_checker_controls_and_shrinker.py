"""Tests for D4 checker, Decision 006 typed gaps & attribution, all 11 controls, and shrinker."""

from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

from catalog_feed_search.change_runner import ChangeOp, ChangeScript, WriteLog, WriteLogEntry
from catalog_feed_search.checker import (
    ALL_D4_GAPS,
    CORE_D4_GAPS,
    EXTENDED_D4_GAPS,
    CatalogItem,
    DeclaredGap,
    GapSummaryTracker,
    ProductCatalogAnswer,
    ProductFeedAnswer,
    ProductSearchAnswer,
    ScopedSearchHit,
    Verdict,
    check_catalog,
    check_change_feed,
    check_search,
)
from catalog_feed_search.controls import run_all_controls, run_broken_reference_proof
from catalog_feed_search.generators import TreeEntrySpec, TreeSpec
from catalog_feed_search.queries import CodeQuerySpec
from catalog_feed_search.references import SearchHit
from catalog_feed_search.seeds import BASE_SEED_PRIMARY, ReplaySelector
from catalog_feed_search.shrinker import FailingInstance, ddmin, shrink_instance


class ControlsAndProofTests(unittest.TestCase):
    def test_all_eleven_runbook_checker_controls_pass(self) -> None:
        report = run_all_controls()
        self.assertEqual(len(report.outcomes), 11)
        negatives = [o for o in report.outcomes if o.kind == "negative"]
        positives = [o for o in report.outcomes if o.kind == "positive"]
        self.assertEqual(len(negatives), 7)
        self.assertEqual(len(positives), 4)
        for outcome in report.outcomes:
            self.assertTrue(
                outcome.passed,
                f"control {outcome.name} expected {outcome.expected_verdict}, got {outcome.actual_verdict}",
            )
        self.assertTrue(report.all_passed)
        payload = report.to_dict()
        self.assertEqual(payload["passed"], 11)

    def test_deliberately_broken_reference_proof_catches_shrinks_and_replays(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            proof = run_broken_reference_proof(Path(tmp))
            self.assertEqual(proof.initial_verdict, Verdict.DEFECT)
            self.assertEqual(proof.replayed_verdict, Verdict.DEFECT)
            self.assertTrue(proof.rg_version.startswith("ripgrep 15."))
            self.assertGreater(proof.shrink_result.original_entry_count, proof.shrink_result.shrunk_entry_count)
            self.assertEqual(proof.shrink_result.shrunk_entry_count, 1)
            # File content should also shrink down to 1 line containing PaintedWolf.
            sole_entry = proof.shrink_result.shrunk.tree_spec.entries[0]
            self.assertEqual(len(sole_entry.content.splitlines()), 1)
            self.assertIn(b"PaintedWolf", sole_entry.content)

            # JSON export and roundtrip preserve selector and shrunk instance.
            exported = json.loads(proof.shrink_result.to_json())
            self.assertEqual(exported["replay_cli"], "--seed 20261127 --family S01 --instance 7")
            roundtrip = FailingInstance.from_dict(exported["shrunk_instance"])
            self.assertEqual(roundtrip, proof.shrink_result.shrunk)


class D4AndDecision006CheckerTests(unittest.TestCase):
    def test_extra_claim_is_never_excused_by_declared_gap(self) -> None:
        ref = (CatalogItem("a.txt", "file", "root-0"),)
        prod = ProductCatalogAnswer(
            items=(CatalogItem("a.txt", "file", "root-0"), CatalogItem("ghost.txt", "file", "root-0")),
            declared_gaps=(DeclaredGap("catalog_incomplete", root_id="root-0"),),
        )
        res = check_catalog(ref, prod)
        self.assertEqual(res.verdict, Verdict.DEFECT)
        self.assertIn("root-0:ghost.txt", res.extra_claims)

    def test_missing_instrumentation_yields_unknown(self) -> None:
        res = check_catalog((), ProductCatalogAnswer(items=(), instrumentation_complete=False))
        self.assertEqual(res.verdict, Verdict.UNKNOWN)

    def test_decision_006_extended_gaps_and_strict_scope_attribution(self) -> None:
        self.assertEqual(
            EXTENDED_D4_GAPS,
            {"catalog_bounded", "index_warming", "files_skipped", "result_limit", "symbol_pending", "symbol_budget"},
        )
        self.assertEqual(ALL_D4_GAPS, CORE_D4_GAPS | EXTENDED_D4_GAPS)

        hit_r0_code = ScopedSearchHit(SearchHit("src/a.txt", 1, 1, 0, 5, "alpha", "alpha\n"), "root-0", "code")
        hit_r1_code = ScopedSearchHit(SearchHit("src/b.txt", 1, 1, 0, 5, "alpha", "alpha\n"), "root-1", "code")
        hit_r0_sym = ScopedSearchHit(SearchHit("src/a.go", 3, 1, 20, 25, "Alpha", "func Alpha()"), "root-0", "symbol")
        ref_hits = (hit_r0_code, hit_r1_code, hit_r0_sym)

        # 1. Root-scoped gap on root-1 CANNOT excuse missing hit on unaffected root-0.
        res_bad_root = check_search(
            ref_hits,
            ProductSearchAnswer(
                hits=(hit_r1_code, hit_r0_sym),
                declared_gaps=(DeclaredGap("catalog_bounded", root_id="root-1"),),
            ),
        )
        self.assertEqual(res_bad_root.verdict, Verdict.DEFECT)

        # 2. Symbol-only gap CANNOT excuse missing code-leg hit.
        res_bad_leg = check_search(
            ref_hits,
            ProductSearchAnswer(
                hits=(hit_r1_code, hit_r0_sym),
                declared_gaps=(DeclaredGap("symbol_pending", root_id="root-0"),),
            ),
        )
        self.assertEqual(res_bad_leg.verdict, Verdict.DEFECT)

        # 3. Symbol-only gap DOES excuse missing symbol-leg hit on matching root.
        res_good_sym = check_search(
            ref_hits,
            ProductSearchAnswer(
                hits=(hit_r0_code, hit_r1_code),
                declared_gaps=(DeclaredGap("symbol_budget", root_id="root-0", leg="symbol"),),
            ),
        )
        self.assertEqual(res_good_sym.verdict, Verdict.PASS_DECLARED_GAP)
        self.assertEqual(res_good_sym.accounted_gaps, ("symbol_budget",))

        # 4. File-scoped files_skipped excuses only the attributed path, not another file.
        res_good_file = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(hit_r1_code,),
                declared_gaps=(DeclaredGap("files_skipped", root_id="root-0", paths=("src/a.txt",)),),
            ),
        )
        self.assertEqual(res_good_file.verdict, Verdict.PASS_DECLARED_GAP)

        res_bad_file = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(hit_r1_code,),
                declared_gaps=(DeclaredGap("files_skipped", root_id="root-0", paths=("src/other.txt",)),),
            ),
        )
        self.assertEqual(res_bad_file.verdict, Verdict.DEFECT)

        # 5. result_limit requires truncated=True, ordered prefix, and continuation if required.
        res_limit_ok = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(hit_r0_code,),
                declared_gaps=(
                    DeclaredGap("result_limit", truncated=True, continuation="page-2", continuation_required=True),
                ),
            ),
        )
        self.assertEqual(res_limit_ok.verdict, Verdict.PASS_DECLARED_GAP)

        res_limit_no_trunc = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(hit_r0_code,),
                declared_gaps=(DeclaredGap("result_limit", truncated=False),),
            ),
        )
        self.assertEqual(res_limit_no_trunc.verdict, Verdict.DEFECT)

        res_limit_wrong_order = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(hit_r1_code,),
                declared_gaps=(DeclaredGap("result_limit", truncated=True),),
            ),
        )
        self.assertEqual(res_limit_wrong_order.verdict, Verdict.DEFECT)

        res_limit_no_cont = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(hit_r0_code,),
                declared_gaps=(DeclaredGap("result_limit", truncated=True, continuation_required=True),),
            ),
        )
        self.assertEqual(res_limit_no_cont.verdict, Verdict.DEFECT)

        # 6. index_warming interplay with time_budget across roots.
        res_warm_no_budget = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(),
                declared_gaps=(DeclaredGap("index_warming", root_id="root-1"),),
            ),
        )
        self.assertEqual(res_warm_no_budget.verdict, Verdict.DEFECT)

        res_warm_with_budget = check_search(
            (hit_r0_code, hit_r1_code),
            ProductSearchAnswer(
                hits=(),
                declared_gaps=(
                    DeclaredGap("index_warming", root_id="root-1"),
                    DeclaredGap("time_budget"),
                ),
            ),
        )
        self.assertEqual(res_warm_with_budget.verdict, Verdict.PASS_DECLARED_GAP)

        # 7. Unverifiable attribution -> UNKNOWN.
        res_unverifiable = check_search(
            (hit_r0_code,),
            ProductSearchAnswer(
                hits=(),
                declared_gaps=(DeclaredGap("files_skipped", verifiable=False),),
            ),
        )
        self.assertEqual(res_unverifiable.verdict, Verdict.UNKNOWN)

        # 8. Separate gap counting in GapSummaryTracker.
        tracker = GapSummaryTracker()
        tracker.record(res_good_sym)
        tracker.record(res_good_file)
        tracker.record(res_limit_ok)
        tracker.record(res_warm_with_budget)
        summary = tracker.to_dict()
        self.assertEqual(summary["totals"], {"PASS_DECLARED_GAP": 4})
        self.assertEqual(summary["gap_counts"]["symbol_budget"], 1)
        self.assertEqual(summary["gap_counts"]["files_skipped"], 1)
        self.assertEqual(summary["gap_counts"]["result_limit"], 1)
        self.assertEqual(summary["gap_counts"]["index_warming"], 1)
        self.assertEqual(summary["gap_counts"]["time_budget"], 1)

    def test_resync_without_or_with_bad_reconciled_walk(self) -> None:
        log = WriteLog(
            "manual",
            (WriteLogEntry(1, "manual", 10, "edit", "a.txt", None, None, "abc"),),
        )
        unknown_res = check_change_feed(log, ProductFeedAnswer(events=(), declared_gaps=(DeclaredGap("resync"),)))
        self.assertEqual(unknown_res.verdict, Verdict.UNKNOWN)

        bad_reconcile = check_change_feed(
            log,
            ProductFeedAnswer(
                events=(),
                declared_gaps=(DeclaredGap("resync"),),
                reconciled_walk=(),
                reference_post_walk=(CatalogItem("a.txt", "file"),),
            ),
        )
        self.assertEqual(bad_reconcile.verdict, Verdict.DEFECT)


class ShrinkerUnitTests(unittest.TestCase):
    def test_ddmin_and_change_script_and_query_shrinking(self) -> None:
        items = list(range(20))
        shrunk, _ = ddmin(items, lambda sub: 3 in sub and 17 in sub)
        self.assertEqual(shrunk, [3, 17])

        sel = ReplaySelector(BASE_SEED_PRIMARY, "F01", 9)
        tree = TreeSpec(
            selector=sel,
            entries=(
                TreeEntrySpec("a.txt", "file", b"line1\nline2\n"),
                TreeEntrySpec("b.txt", "file", b"needle\nother\n"),
            ),
        )
        script = ChangeScript(
            selector=sel,
            ops=(
                ChangeOp("edit", "a.txt", content=b"x\n"),
                ChangeOp("delete", "b.txt"),
                ChangeOp("create", "c.txt", content=b"z\n"),
            ),
        )
        query = CodeQuerySpec(
            form="glob_filtered",
            pattern="needle",
            include_globs=("*.txt",),
            exclude_globs=("*.bin",),
            path_filters=("b.txt",),
        )
        inst = FailingInstance(selector=sel, tree_spec=tree, change_script=script, query_spec=query)

        def fails(trial: FailingInstance) -> bool:
            has_b = any(e.path == "b.txt" and b"needle" in e.content for e in trial.tree_spec.entries)
            has_del = trial.change_script is not None and any(
                op.kind == "delete" and op.path == "b.txt" for op in trial.change_script.ops
            )
            return has_b and has_del

        res = shrink_instance(inst, fails)
        self.assertEqual(res.shrunk_entry_count, 1)
        self.assertEqual(res.shrunk_op_count, 1)
        self.assertEqual(res.shrunk.query_spec.include_globs, ())  # type: ignore[union-attr]
        self.assertEqual(res.shrunk.query_spec.exclude_globs, ())  # type: ignore[union-attr]
        self.assertEqual(res.shrunk.query_spec.path_filters, ())  # type: ignore[union-attr]


if __name__ == "__main__":
    unittest.main()
