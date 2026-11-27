"""D4 Differential Checker for change-feed, batch/effect identity, and resync reconciliation."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass

from .change_runner import WriteLog
from .checker import CatalogItem, ProductCatalogAnswer, check_catalog
from .checker_types import CheckResult, DeclaredGap, Verdict, evaluate_omissions

COMPATIBLE_FEED_OPS: dict[str, frozenset[str]] = {
    "create": frozenset({"create", "change"}),
    "edit": frozenset({"edit", "modify", "write", "change"}),
    "atomic_replace": frozenset({"atomic_replace", "edit", "modify", "rename", "write", "change"}),
    "rename": frozenset({"rename", "move", "create", "delete", "change"}),
    "delete": frozenset({"delete", "remove", "change"}),
    "chmod": frozenset({"chmod", "attrib", "metadata", "change"}),
}


@dataclass(frozen=True)
class FeedEvent:
    """One observed event in the product's source_changed stream."""

    path: str
    batch_id: str
    op: str = "change"
    effect_id: str | None = None
    root_id: str = "root-0"
    seq: int | None = None


@dataclass(frozen=True)
class ProductFeedAnswer:
    """Product change-feed observation with optional resync and reconciled catalog state."""

    events: tuple[FeedEvent, ...]
    declared_gaps: tuple[DeclaredGap, ...] = ()
    reconciled_walk: tuple[CatalogItem, ...] | None = None
    reference_post_walk: tuple[CatalogItem, ...] | None = None
    instrumentation_complete: bool = True
    claims_operation: bool = True
    claims_ordering: bool = False
    claims_batch_effect: bool = False


def check_feed_batch_effects(answer: ProductFeedAnswer) -> CheckResult:
    """Verify batch/effect identity claims separately from path membership (Decision 009)."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "feed_batch_effect", reason="missing instrumentation")
    wrong: list[str] = []
    seen_effects: set[str] = set()
    for idx, ev in enumerate(answer.events):
        if not ev.batch_id:
            wrong.append(f"missing_batch_id:{ev.root_id}:{ev.path}[{idx}]")
        if not ev.effect_id:
            wrong.append(f"missing_effect_id:{ev.root_id}:{ev.path}[{idx}]")
        elif ev.effect_id in seen_effects:
            wrong.append(f"duplicate_effect_id:{ev.effect_id}")
        else:
            seen_effects.add(ev.effect_id)
    if wrong:
        return CheckResult(
            Verdict.DEFECT,
            "feed_batch_effect",
            wrong_claims=tuple(wrong),
            reason="batch or effect identity missing or duplicated",
        )
    return CheckResult(Verdict.PASS, "feed_batch_effect")


def _verify_claimed_resync(
    answer: ProductFeedAnswer,
    declared_names: tuple[str, ...],
    missing_labels: tuple[str, ...],
) -> CheckResult | None:
    """Decision 012: whenever a resync is claimed, ALWAYS verify settled reconciliation."""
    if not any(g.reason == "resync" for g in answer.declared_gaps):
        return None
    if answer.reconciled_walk is None or answer.reference_post_walk is None:
        return CheckResult(
            Verdict.UNKNOWN,
            "feed",
            missing_claims=missing_labels,
            declared_gaps=declared_names,
            reason="resync declared without reconciled walk evidence",
        )
    reconciled_check = check_catalog(
        answer.reference_post_walk,
        ProductCatalogAnswer(items=answer.reconciled_walk),
    )
    if reconciled_check.verdict != Verdict.PASS:
        return CheckResult(
            Verdict.DEFECT,
            "feed",
            missing_claims=missing_labels,
            extra_claims=reconciled_check.extra_claims,
            wrong_claims=reconciled_check.wrong_claims,
            declared_gaps=declared_names,
            reason="resync declared but reconciled catalog disagrees with reference walk",
        )
    return None


def check_change_feed(
    write_log: WriteLog,
    answer: ProductFeedAnswer,
) -> CheckResult:
    """Compare product change-feed events against independent write log in strict D4 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "feed", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))

    logged_counts: Counter[tuple[str, str]] = Counter()
    logged_ops: dict[tuple[str, str], set[str]] = {}
    logged_first_seq: dict[tuple[str, str], int] = {}
    logged_last_seq: dict[tuple[str, str], int] = {}

    for entry in write_log.entries:
        keys = [(entry.root_id, entry.path)]
        if entry.new_path:
            keys.append((entry.root_id, entry.new_path))
        for k in keys:
            logged_counts[k] += 1
            logged_ops.setdefault(k, set()).add(entry.op)
            if k not in logged_first_seq:
                logged_first_seq[k] = entry.seq
            logged_last_seq[k] = entry.seq

    logged_keys = set(logged_counts)
    observed_keys = {(e.root_id, e.path) for e in answer.events}

    extra = sorted(f"{r}:{p}" for (r, p) in (observed_keys - logged_keys))
    observed_counts = Counter((e.root_id, e.path) for e in answer.events)
    wrong: list[str] = [
        f"duplicated_change:{r}:{p} observed={c} logged={logged_counts[(r, p)]}"
        for (r, p), c in sorted(observed_counts.items())
        if (r, p) in logged_counts and c > logged_counts[(r, p)]
    ]

    seen_effects: set[str] = set()
    prev_max_seq = 0
    for idx, ev in enumerate(answer.events):
        key = (ev.root_id, ev.path)
        if answer.claims_batch_effect:
            if not ev.batch_id:
                wrong.append(f"missing_batch_id:{ev.root_id}:{ev.path}[{idx}]")
            if not ev.effect_id:
                wrong.append(f"missing_effect_id:{ev.root_id}:{ev.path}[{idx}]")
        if ev.effect_id is not None:
            if ev.effect_id in seen_effects:
                wrong.append(f"duplicate_effect_id:{ev.effect_id}")
            seen_effects.add(ev.effect_id)

        if key in logged_ops and (answer.claims_operation or ev.op):
            allowed: set[str] = set()
            for lop in logged_ops[key]:
                allowed.update(COMPATIBLE_FEED_OPS.get(lop, {lop}))
            if ev.op not in allowed:
                wrong.append(
                    f"incompatible_op:{ev.root_id}:{ev.path} observed={ev.op} "
                    f"logged={sorted(logged_ops[key])}"
                )

        if answer.claims_ordering and key in logged_last_seq:
            if logged_last_seq[key] < prev_max_seq:
                wrong.append(
                    f"out_of_order_event:{ev.root_id}:{ev.path} "
                    f"last_seq={logged_last_seq[key]}<prev={prev_max_seq}"
                )
            prev_max_seq = max(prev_max_seq, logged_first_seq[key])

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "feed",
            extra_claims=tuple(extra),
            wrong_claims=tuple(wrong),
            declared_gaps=declared_names,
            reason="feed claimed unlogged, duplicated, out-of-order, or incompatible-op changes",
        )

    missing_keys = sorted(logged_keys - observed_keys)
    missing_labels = tuple(f"{r}:{p}" for r, p in missing_keys)

    # Decision 012: verify any claimed resync BEFORE returning PASS or PASS_DECLARED_GAP,
    # even when all logged paths arrived or another gap accounts for omissions.
    resync_err = _verify_claimed_resync(answer, declared_names, missing_labels)
    if resync_err is not None:
        return resync_err

    if not missing_keys:
        return CheckResult(Verdict.PASS, "feed", declared_gaps=declared_names)

    return evaluate_omissions(
        domain="feed",
        missing_items=[(r, None, p, f"{r}:{p}") for r, p in missing_keys],
        declared_gaps=answer.declared_gaps,
    )
