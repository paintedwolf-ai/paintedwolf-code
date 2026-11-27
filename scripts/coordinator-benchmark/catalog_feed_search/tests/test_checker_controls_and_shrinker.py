"""Tests for D4 checker, Decision 006 typed gaps & attribution, all 11 controls, and shrinker."""

from __future__ import annotations

import json
from pathlib import Path
import sys
import tempfile
import unittest

_BENCHMARK_DIR = str(Path(__file__).resolve().parents[2])
if _BENCHMARK_DIR not in sys.path:
    sys.path.insert(0, _BENCHMARK_DIR)

from catalog_feed_search.change_runner import ChangeOp, ChangeScript, WriteLog, WriteLogEntry
from catalog_feed_search.checker import (
    CatalogItem,
    ProductCatalogAnswer,
    ProductSearchAnswer,
    ScopedSearchHit,
    check_catalog,
    check_search,
)
from catalog_feed_search.checker_feed import (
    FeedEvent,
    ProductFeedAnswer,
    check_change_feed,
    check_feed_batch_effects,
)
from catalog_feed_search.checker_replace import (
    ProductReplaceAnswer,
    ReplaceFileObservation,
    check_replace,
)
from catalog_feed_search.checker_types import (
    ALL_D4_GAPS,
    CORE_D4_GAPS,
    EXTENDED_D4_GAPS,
    DeclaredGap,
    GapSummaryTracker,
    Verdict,
)
from catalog_feed_search.controls import run_all_controls, run_broken_reference_proof
from catalog_feed_search.generators import TreeEntrySpec, TreeSpec
from catalog_feed_search.queries import CodeQuerySpec
from catalog_feed_search.references import ReplaceFileResult, SearchHit
from catalog_feed_search.seeds import BASE_SEED_PRIMARY, ReplaySelector
from catalog_feed_search.shrinker import FailingInstance, ddmin, shrink_instance


class ControlsAndProofTests(unittest.TestCase):
    def test_all_runbook_and_rework_checker_controls_pass(self) -> None:
        report = run_all_controls()
        self.assertEqual(len(report.outcomes), 20)
        negatives = [o for o in report.outcomes if o.kind == "negative"]
        positives = [o for o in report.outcomes if o.kind == "positive"]
        self.assertEqual(len(negatives), 16)
        self.assertEqual(len(positives), 4)
        for outcome in report.outcomes:
            self.assertTrue(
                outcome.passed,
                f"control {outcome.name} expected {outcome.expected_verdict}, got {outcome.actual_verdict}",
            )
        self.assertTrue(report.all_passed)
        payload = report.to_dict()
        self.assertEqual(payload["passed"], 20)

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

    def test_decision_009_duplicates_multi_root_replace_count_only_and_feed_scope(self) -> None:
        # 1. Duplicate catalog entry -> DEFECT.
        ref_cat = (CatalogItem("a.txt", "file", "root-0"),)
        dup_cat = check_catalog(ref_cat, ProductCatalogAnswer(items=(ref_cat[0], ref_cat[0])))
        self.assertEqual(dup_cat.verdict, Verdict.DEFECT)

        # 2. Multi-root replace with identical relative path never collides, and duplicate obs -> DEFECT.
        r0 = ReplaceFileResult("src/a.txt", "b0", "a0", b"x", 1, True, root_id="root-0")
        r1 = ReplaceFileResult("src/a.txt", "b1", "a1", b"y", 1, True, root_id="root-1")
        ref_multi = {("root-0", "src/a.txt"): r0, ("root-1", "src/a.txt"): r1}
        obs0 = ReplaceFileObservation("src/a.txt", root_id="root-0", applied=True, observed_sha256="a0")
        obs1 = ReplaceFileObservation("src/a.txt", root_id="root-1", applied=True, observed_sha256="a1")
        self.assertEqual(check_replace(ref_multi, ProductReplaceAnswer(files=(obs0, obs1))).verdict, Verdict.PASS)
        self.assertEqual(check_replace(ref_multi, ProductReplaceAnswer(files=(obs0, obs0, obs1))).verdict, Verdict.DEFECT)

        # 3. Count-only files_skipped with skipped_count >= missing but NO path evidence -> UNKNOWN.
        h1 = ScopedSearchHit(SearchHit("src/a.txt", 1, 1, 0, 5, "alpha", "alpha\n"), "root-0", "code")
        h2 = ScopedSearchHit(SearchHit("src/b.txt", 1, 1, 0, 5, "alpha", "alpha\n"), "root-0", "code")
        res_unknown_count = check_search(
            (h1, h2),
            ProductSearchAnswer(
                hits=(),
                declared_gaps=(DeclaredGap("files_skipped", root_id="root-0", skipped_count=2),),
            ),
        )
        self.assertEqual(res_unknown_count.verdict, Verdict.UNKNOWN)

        # 4. Feed ordering, batch/effect separation, and wrong-root gap rejection.
        log = WriteLog(
            "manual",
            (
                WriteLogEntry(1, "manual", 10, "create", "a.txt", None, None, "h1", root_id="root-0"),
                WriteLogEntry(2, "manual", 20, "edit", "b.txt", None, "h0", "h2", root_id="root-0"),
            ),
        )
        out_of_order = check_change_feed(
            log,
            ProductFeedAnswer(
                events=(
                    FeedEvent("b.txt", "b1", op="edit", root_id="root-0"),
                    FeedEvent("a.txt", "b1", op="create", root_id="root-0"),
                ),
                claims_ordering=True,
            ),
        )
        self.assertEqual(out_of_order.verdict, Verdict.DEFECT)

        missing_effect = check_feed_batch_effects(
            ProductFeedAnswer(events=(FeedEvent("a.txt", "b1", op="create", effect_id=None),))
        )
        self.assertEqual(missing_effect.verdict, Verdict.DEFECT)

    def test_decision_012_resync_reconcile_scope_first_leg_and_unapplied_replace(self) -> None:
        log = WriteLog("manual", (WriteLogEntry(1, "manual", 10, "edit", "a.txt", None, "h0", "h1"),))
        ref_cat = (CatalogItem("a.txt", "file", "root-0"),)

        # 1a. Resync claimed when all logged paths arrived, missing reconciled walk -> UNKNOWN.
        res_resync_missing_walk = check_change_feed(
            log,
            ProductFeedAnswer(
                events=(FeedEvent("a.txt", "b1", op="edit"),),
                declared_gaps=(DeclaredGap("resync"),),
            ),
        )
        self.assertEqual(res_resync_missing_walk.verdict, Verdict.UNKNOWN)

        # 1b. Resync claimed when another gap covers omission, but reconciled walk contradicts ref -> DEFECT.
        res_resync_other_gap = check_change_feed(
            log,
            ProductFeedAnswer(
                events=(),
                declared_gaps=(DeclaredGap("executor_error", root_id="root-0"), DeclaredGap("resync")),
                reconciled_walk=(),
                reference_post_walk=ref_cat,
            ),
        )
        self.assertEqual(res_resync_other_gap.verdict, Verdict.DEFECT)

        # 2. Mixed omissions: root-0 has in-scope verifiable=False, root-1 has NO explanation -> DEFECT.
        two_roots = (CatalogItem("a.txt", "file", "root-0"), CatalogItem("b.txt", "file", "root-1"))
        res_mixed = check_catalog(
            two_roots,
            ProductCatalogAnswer(
                items=(),
                declared_gaps=(DeclaredGap("catalog_failed", root_id="root-0", verifiable=False),),
            ),
        )
        self.assertEqual(res_mixed.verdict, Verdict.DEFECT)

        # 3. Wrong-leg gap on catalog -> DEFECT.
        res_wrong_leg = check_catalog(
            ref_cat,
            ProductCatalogAnswer(
                items=(),
                declared_gaps=(DeclaredGap("catalog_incomplete", root_id="root-0", leg="store"),),
            ),
        )
        self.assertEqual(res_wrong_leg.verdict, Verdict.DEFECT)

        # 4. Non-drifted unapplied file: byte-identical to independent ref.before_sha256 + limited_preview -> PASS_DECLARED_GAP;
        #    mutated bytes + limited_preview -> DEFECT (even if product lies in expected_before_sha256).
        ref_rep = {"a.txt": ReplaceFileResult("a.txt", "orig_sha", "new_sha", b"new", 1, True)}
        res_unchanged = check_replace(
            ref_rep,
            ProductReplaceAnswer(
                files=(ReplaceFileObservation("a.txt", applied=False, observed_sha256="orig_sha"),),
                declared_gaps=(DeclaredGap("limited_preview", root_id="root-0"),),
            ),
        )
        self.assertEqual(res_unchanged.verdict, Verdict.PASS_DECLARED_GAP)

        res_changed = check_replace(
            ref_rep,
            ProductReplaceAnswer(
                files=(
                    ReplaceFileObservation(
                        "a.txt",
                        applied=False,
                        observed_sha256="corrupted_sha",
                        expected_before_sha256="corrupted_sha",
                    ),
                ),
                declared_gaps=(DeclaredGap("limited_preview", root_id="root-0"),),
            ),
        )
        self.assertEqual(res_changed.verdict, Verdict.DEFECT)


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
