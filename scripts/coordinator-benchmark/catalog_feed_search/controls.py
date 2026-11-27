"""All 11 runbook checker controls (7 negative + 4 positive) and broken-reference proof."""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
from pathlib import Path

from .change_runner import WriteLog, WriteLogEntry
from .checker import (
    CatalogItem,
    CheckResult,
    DeclaredGap,
    FeedEvent,
    ProductCatalogAnswer,
    ProductFeedAnswer,
    ProductReplaceAnswer,
    ProductSearchAnswer,
    ReplaceFileObservation,
    ScopedSearchHit,
    Verdict,
    check_catalog,
    check_change_feed,
    check_replace,
    check_search,
)
from .generators import TreeGenOptions, generate_tree_spec, materialize_tree
from .queries import CodeQuerySpec
from .references import ReplaceFileResult, SearchHit, reference_search
from .seeds import BASE_SEED_PRIMARY
from .shrinker import FailingInstance, ShrinkResult, shrink_instance


@dataclass(frozen=True)
class ControlOutcome:
    """Outcome of executing one runbook checker control."""

    name: str
    kind: str  # "negative" | "positive"
    expected_verdict: Verdict
    actual_verdict: Verdict
    passed: bool
    result: CheckResult


@dataclass(frozen=True)
class ControlSuiteReport:
    """Aggregated qualification report for all 11 checker controls."""

    outcomes: tuple[ControlOutcome, ...]

    @property
    def all_passed(self) -> bool:
        return all(o.passed for o in self.outcomes)

    def to_dict(self) -> dict[str, object]:
        return {
            "all_passed": self.all_passed,
            "total": len(self.outcomes),
            "passed": sum(1 for o in self.outcomes if o.passed),
            "controls": [
                {
                    "name": o.name,
                    "kind": o.kind,
                    "expected": o.expected_verdict.value,
                    "actual": o.actual_verdict.value,
                    "passed": o.passed,
                    "reason": o.result.reason,
                }
                for o in self.outcomes
            ],
        }


def _sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _healthy_catalog() -> tuple[CatalogItem, ...]:
    return (
        CatalogItem(path="src", kind="dir", root_id="root-0"),
        CatalogItem(path="src/a.txt", kind="file", root_id="root-0", sha256=_sha(b"alpha\n")),
        CatalogItem(path="src/b.txt", kind="file", root_id="root-0", sha256=_sha(b"beta\n")),
    )


def _healthy_write_log() -> WriteLog:
    return WriteLog(
        clock_domain="manual",
        entries=(
            WriteLogEntry(
                seq=1,
                clock_domain="manual",
                timestamp_ns=100,
                op="edit",
                path="src/a.txt",
                new_path=None,
                before_sha256=_sha(b"alpha\n"),
                after_sha256=_sha(b"alpha_v2\n"),
            ),
            WriteLogEntry(
                seq=2,
                clock_domain="manual",
                timestamp_ns=200,
                op="edit",
                path="src/b.txt",
                new_path=None,
                before_sha256=_sha(b"beta\n"),
                after_sha256=_sha(b"beta_v2\n"),
            ),
        ),
    )


def _healthy_search_hits() -> tuple[ScopedSearchHit, ...]:
    return (
        ScopedSearchHit(
            hit=SearchHit("src/a.txt", 1, 1, 0, 5, "alpha", "alpha\n"),
            root_id="root-0",
            leg="code",
        ),
        ScopedSearchHit(
            hit=SearchHit("src/b.txt", 1, 1, 0, 5, "alpha", "alpha beta\n"),
            root_id="root-0",
            leg="code",
        ),
    )


def _healthy_replace_reference() -> dict[str, ReplaceFileResult]:
    before = b"alpha_1\n"
    after = b"rewritten_1\n"
    return {
        "src/a.txt": ReplaceFileResult(
            path="src/a.txt",
            before_sha256=_sha(before),
            after_sha256=_sha(after),
            resulting_bytes=after,
            match_count=1,
            changed=True,
        ),
    }


def run_all_controls() -> ControlSuiteReport:
    """Execute all 11 runbook checker controls (7 negative + 4 positive)."""
    outcomes: list[ControlOutcome] = []

    def record(name: str, kind: str, expected: Verdict, res: CheckResult) -> None:
        outcomes.append(
            ControlOutcome(
                name=name,
                kind=kind,
                expected_verdict=expected,
                actual_verdict=res.verdict,
                passed=(res.verdict == expected),
                result=res,
            )
        )

    ref_cat = _healthy_catalog()
    ref_log = _healthy_write_log()
    ref_hits = _healthy_search_hits()
    ref_rep = _healthy_replace_reference()

    # --- 7 NEGATIVE CONTROLS (each injected defect MUST yield DEFECT) ---

    # 1. Missing file
    record(
        "missing_file",
        "negative",
        Verdict.DEFECT,
        check_catalog(ref_cat, ProductCatalogAnswer(items=ref_cat[:-1])),
    )

    # 2. Extra file
    extra_cat = ref_cat + (CatalogItem(path="src/ghost.txt", kind="file", root_id="root-0"),)
    record(
        "extra_file",
        "negative",
        Verdict.DEFECT,
        check_catalog(ref_cat, ProductCatalogAnswer(items=extra_cat)),
    )

    # 3. Missed change
    record(
        "missed_change",
        "negative",
        Verdict.DEFECT,
        check_change_feed(
            ref_log,
            ProductFeedAnswer(events=(FeedEvent(path="src/a.txt", batch_id="b1"),)),
        ),
    )

    # 4. Duplicated change
    record(
        "duplicated_change",
        "negative",
        Verdict.DEFECT,
        check_change_feed(
            ref_log,
            ProductFeedAnswer(
                events=(
                    FeedEvent(path="src/a.txt", batch_id="b1"),
                    FeedEvent(path="src/a.txt", batch_id="b2"),
                    FeedEvent(path="src/b.txt", batch_id="b2"),
                )
            ),
        ),
    )

    # 5. Skipped matching file
    record(
        "skipped_matching_file",
        "negative",
        Verdict.DEFECT,
        check_search(ref_hits, ProductSearchAnswer(hits=(ref_hits[0],))),
    )

    # 6. Stale hit
    stale = ScopedSearchHit(
        hit=SearchHit("src/deleted.txt", 3, 1, 20, 25, "alpha", "alpha\n"),
        root_id="root-0",
        leg="code",
    )
    record(
        "stale_hit",
        "negative",
        Verdict.DEFECT,
        check_search(ref_hits, ProductSearchAnswer(hits=ref_hits + (stale,))),
    )

    # 7. Overwritten drifted file
    drifted_sha = _sha(b"external_edit\n")
    record(
        "overwritten_drifted_file",
        "negative",
        Verdict.DEFECT,
        check_replace(
            ref_rep,
            ProductReplaceAnswer(
                files=(
                    ReplaceFileObservation(
                        path="src/a.txt",
                        applied=True,
                        refused_drift=False,
                        observed_sha256=ref_rep["src/a.txt"].after_sha256,
                        expected_before_sha256=ref_rep["src/a.txt"].before_sha256,
                        drifted_before_apply=True,
                        drifted_sha256=drifted_sha,
                    ),
                )
            ),
        ),
    )

    # --- 4 POSITIVE CONTROLS (each healthy input MUST pass as intended) ---

    # 1. Declared incomplete answer
    record(
        "declared_incomplete_answer",
        "positive",
        Verdict.PASS_DECLARED_GAP,
        check_catalog(
            ref_cat,
            ProductCatalogAnswer(
                items=ref_cat[:-1],
                declared_gaps=(DeclaredGap(reason="catalog_incomplete", root_id="root-0"),),
            ),
        ),
    )

    # 2. Coalesced burst (3 writes to the same file coalesced into 1 observed event)
    burst_log = WriteLog(
        clock_domain="manual",
        entries=tuple(
            WriteLogEntry(
                seq=i,
                clock_domain="manual",
                timestamp_ns=i * 10,
                op="edit",
                path="src/a.txt",
                new_path=None,
                before_sha256=_sha(f"v{i - 1}".encode()),
                after_sha256=_sha(f"v{i}".encode()),
            )
            for i in range(1, 4)
        ),
    )
    record(
        "coalesced_burst",
        "positive",
        Verdict.PASS,
        check_change_feed(
            burst_log,
            ProductFeedAnswer(events=(FeedEvent(path="src/a.txt", batch_id="burst-1"),)),
        ),
    )

    # 3. Declared resync with independently verified reconciled state
    record(
        "declared_resync_reconciled",
        "positive",
        Verdict.PASS_DECLARED_GAP,
        check_change_feed(
            ref_log,
            ProductFeedAnswer(
                events=(),
                declared_gaps=(DeclaredGap(reason="resync"),),
                reconciled_walk=ref_cat,
                reference_post_walk=ref_cat,
            ),
        ),
    )

    # 4. Refused drifted file left byte-identical
    record(
        "refused_drifted_file_byte_identical",
        "positive",
        Verdict.PASS,
        check_replace(
            ref_rep,
            ProductReplaceAnswer(
                files=(
                    ReplaceFileObservation(
                        path="src/a.txt",
                        applied=False,
                        refused_drift=True,
                        observed_sha256=drifted_sha,
                        expected_before_sha256=ref_rep["src/a.txt"].before_sha256,
                        drifted_before_apply=True,
                        drifted_sha256=drifted_sha,
                    ),
                )
            ),
        ),
    )

    return ControlSuiteReport(outcomes=tuple(outcomes))


@dataclass(frozen=True)
class BrokenReferenceProof:
    """Acceptance proof: catch injected discrepancy, shrink to minimal repro, replay on fresh dir."""

    initial_verdict: Verdict
    shrink_result: ShrinkResult
    replayed_verdict: Verdict
    rg_version: str


def run_broken_reference_proof(
    work_root: Path,
    *,
    base_seed: str = BASE_SEED_PRIMARY,
    family_id: str = "S01",
    instance_index: int = 7,
    rg_bin: str = "rg",
) -> BrokenReferenceProof:
    """Deliberately broken comparison proof (CF02 acceptance criterion).

    Simulates a buggy search engine that silently drops hits in files whose
    content contains 'PaintedWolf', catches the DEFECT with the D4 checker,
    shrinks the tree to a 1-file minimal reproducer, and replays from seed
    on a fresh directory.
    """
    tree_spec = generate_tree_spec(
        base_seed,
        family_id,
        instance_index,
        options=TreeGenOptions(
            entry_count=16,
            max_depth=3,
            include_symlinks=False,
            include_binary=False,
            include_vcs=False,
            include_boundaries=False,
            include_nested_checkouts=False,
            include_paintedwolf=False,
            include_hidden=False,
            include_ignored=False,
            include_empty_dirs=False,
            corpus_tokens=("alpha", "beta", "PaintedWolf"),
        ),
    )
    query = CodeQuerySpec(form="literal", pattern="PaintedWolf", case_sensitive=False)
    eval_counter = 0
    captured_rg_version = ""

    def evaluate(inst: FailingInstance) -> CheckResult:
        nonlocal eval_counter, captured_rg_version
        eval_counter += 1
        trial_dir = Path(work_root) / f"eval_{eval_counter}"
        materialize_tree(inst.tree_spec, trial_dir)
        q = inst.query_spec or query
        ref = reference_search(trial_dir, q, rg_bin=rg_bin, include_hidden=False)
        captured_rg_version = ref.rg_version
        ref_scoped = tuple(ScopedSearchHit(hit=h) for h in ref.hits)
        # Deliberately broken product: silently drops hits on every file matching PaintedWolf.
        buggy_hits = tuple(h for h in ref_scoped if "paintedwolf" not in h.hit.match_text.lower())
        return check_search(ref_scoped, ProductSearchAnswer(hits=buggy_hits))

    initial_instance = FailingInstance(
        selector=tree_spec.selector,
        tree_spec=tree_spec,
        query_spec=query,
        failure_summary="deliberately broken comparison drops PaintedWolf hits",
    )
    initial_check = evaluate(initial_instance)
    shrunk = shrink_instance(
        initial_instance,
        predicate=lambda inst: evaluate(inst).verdict == Verdict.DEFECT,
    )

    # Replay from the shrunk instance on a brand-new directory.
    fresh_dir = Path(work_root) / "fresh_replay"
    materialize_tree(shrunk.shrunk.tree_spec, fresh_dir)
    replay_ref = reference_search(fresh_dir, shrunk.shrunk.query_spec or query, rg_bin=rg_bin, include_hidden=False)
    replay_scoped = tuple(ScopedSearchHit(hit=h) for h in replay_ref.hits)
    replay_buggy = tuple(h for h in replay_scoped if "paintedwolf" not in h.hit.match_text.lower())
    replayed_check = check_search(replay_scoped, ProductSearchAnswer(hits=replay_buggy))

    return BrokenReferenceProof(
        initial_verdict=initial_check.verdict,
        shrink_result=shrunk,
        replayed_verdict=replayed_check.verdict,
        rg_version=captured_rg_version,
    )
