"""D4 Differential Checker with Decision 006/009 typed gaps, multiplicity, and strict attribution."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
from enum import Enum

from .change_runner import WriteLog
from .references import ReplaceFileResult, SearchHit, WalkEntry


class Verdict(str, Enum):
    PASS = "PASS"
    PASS_DECLARED_GAP = "PASS_DECLARED_GAP"
    DEFECT = "DEFECT"
    UNKNOWN = "UNKNOWN"


CORE_D4_GAPS: frozenset[str] = frozenset(
    {
        "catalog_incomplete",
        "catalog_warming",
        "catalog_refreshing",
        "catalog_failed",
        "catalog_refresh_failed",
        "executor_error",
        "time_budget",
        "stale_inventory",
        "limited_preview",
        "resync",
    }
)

# Decision 006 (F-CF01-03): officially part of D4's typed gap table, counted separately.
EXTENDED_D4_GAPS: frozenset[str] = frozenset(
    {
        "catalog_bounded",
        "index_warming",
        "files_skipped",
        "result_limit",
        "symbol_pending",
        "symbol_budget",
    }
)

ALL_D4_GAPS: frozenset[str] = CORE_D4_GAPS | EXTENDED_D4_GAPS

GAP_DOMAIN_SUPPORT: dict[str, frozenset[str]] = {
    "catalog_incomplete": frozenset({"catalog", "search", "replace"}),
    "catalog_warming": frozenset({"catalog", "search", "replace"}),
    "catalog_refreshing": frozenset({"catalog", "search", "replace", "feed"}),
    "catalog_failed": frozenset({"catalog", "search", "replace"}),
    "catalog_refresh_failed": frozenset({"catalog", "search", "replace", "feed"}),
    "executor_error": frozenset({"catalog", "search", "replace", "feed"}),
    "time_budget": frozenset({"catalog", "search", "replace"}),
    "stale_inventory": frozenset({"catalog", "search", "feed"}),
    "limited_preview": frozenset({"replace"}),
    "resync": frozenset({"feed"}),
    "catalog_bounded": frozenset({"catalog", "search", "replace"}),
    "index_warming": frozenset({"search"}),
    "files_skipped": frozenset({"search", "replace"}),
    "result_limit": frozenset({"search"}),
    "symbol_pending": frozenset({"search"}),
    "symbol_budget": frozenset({"search"}),
}

SYMBOL_ONLY_GAPS: frozenset[str] = frozenset({"symbol_pending", "symbol_budget"})

# Compatible feed observation operations for each logged write operation.
COMPATIBLE_FEED_OPS: dict[str, frozenset[str]] = {
    "create": frozenset({"create", "change"}),
    "edit": frozenset({"edit", "modify", "write", "change"}),
    "atomic_replace": frozenset({"atomic_replace", "edit", "modify", "rename", "write", "change"}),
    "rename": frozenset({"rename", "move", "create", "delete", "change"}),
    "delete": frozenset({"delete", "remove", "change"}),
    "chmod": frozenset({"chmod", "attrib", "metadata", "change"}),
}


@dataclass(frozen=True)
class DeclaredGap:
    """Typed coverage or freshness gap declared by a product answer (D4 + Decisions 006/009)."""

    reason: str
    root_id: str | None = None
    leg: str | None = None  # "code" | "symbol" | "files" | "store" | None
    paths: tuple[str, ...] = ()
    truncated: bool | None = None
    continuation: str | None = None
    continuation_required: bool = False
    skipped_count: int | None = None
    verifiable: bool = True


@dataclass(frozen=True)
class CheckResult:
    """Result of one D4 differential comparison."""

    verdict: Verdict
    domain: str
    extra_claims: tuple[str, ...] = ()
    missing_claims: tuple[str, ...] = ()
    wrong_claims: tuple[str, ...] = ()
    declared_gaps: tuple[str, ...] = ()
    accounted_gaps: tuple[str, ...] = ()
    reason: str = ""


@dataclass(frozen=True)
class CatalogItem:
    """Normalized catalog entry for D4 comparison."""

    path: str
    kind: str  # "file" | "dir" | "symlink"
    root_id: str = "root-0"
    sha256: str | None = None


@dataclass(frozen=True)
class ProductCatalogAnswer:
    """Product catalog response captured with its declared coverage facts."""

    items: tuple[CatalogItem, ...]
    declared_gaps: tuple[DeclaredGap, ...] = ()
    instrumentation_complete: bool = True


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


@dataclass(frozen=True)
class ScopedSearchHit:
    """Search hit attributed to a root and search leg (code/symbol/files/store)."""

    hit: SearchHit
    root_id: str = "root-0"
    leg: str = "code"

    def key(self) -> tuple[str, str, str, int, int, int, str]:
        return (
            self.root_id,
            self.leg,
            self.hit.path,
            self.hit.line_number,
            self.hit.byte_offset,
            self.hit.byte_end,
            self.hit.match_text,
        )

    def label(self) -> str:
        return f"{self.root_id}:{self.leg}:{self.hit.path}:{self.hit.line_number}:{self.hit.byte_offset}"


@dataclass(frozen=True)
class ProductSearchAnswer:
    """Product search response with hits, declared gaps, and pagination metadata."""

    hits: tuple[ScopedSearchHit, ...]
    declared_gaps: tuple[DeclaredGap, ...] = ()
    instrumentation_complete: bool = True


@dataclass(frozen=True)
class ReplaceFileObservation:
    """Observed product state for one file after replace apply."""

    path: str
    root_id: str = "root-0"
    applied: bool = True
    refused_drift: bool = False
    observed_sha256: str = ""
    expected_before_sha256: str = ""
    drifted_before_apply: bool = False
    drifted_sha256: str | None = None


@dataclass(frozen=True)
class ProductReplaceAnswer:
    """Product replace-apply outcome with declared gaps."""

    files: tuple[ReplaceFileObservation, ...]
    declared_gaps: tuple[DeclaredGap, ...] = ()
    instrumentation_complete: bool = True


def _gap_covers_item(
    gap: DeclaredGap,
    *,
    domain: str,
    root_id: str,
    leg: str | None,
    path: str | None,
    missing_files_in_gap_scope: int = 0,
) -> tuple[bool, str | None]:
    """Return (covered, unknown_reason) enforcing strict root, leg, and path attribution."""
    if gap.reason not in ALL_D4_GAPS:
        return False, None
    if domain not in GAP_DOMAIN_SUPPORT.get(gap.reason, frozenset()):
        return False, None
    if not gap.verifiable:
        return False, f"unverifiable gap attribution for {gap.reason}"

    # Strict root attribution: a root-scoped issue NEVER excuses an unaffected root.
    if gap.root_id is not None and gap.root_id != root_id:
        return False, None

    # Strict leg attribution: symbol-only gaps NEVER excuse non-symbol legs,
    # and a leg-scoped issue NEVER excuses an unaffected leg.
    if gap.reason in SYMBOL_ONLY_GAPS and leg != "symbol":
        return False, None
    if gap.leg is not None and leg is not None and gap.leg != leg:
        return False, None

    # Strict path attribution: ANY gap carrying specific paths ONLY excuses those paths.
    if gap.paths:
        if path is None or path not in gap.paths:
            return False, None
        return True, None

    # Count-only files_skipped without path evidence (Decision 009):
    # - If skipped_count is missing/non-positive or strictly smaller than the number of
    #   distinct missing files in scope -> does NOT cover (DEFECT).
    # - If skipped_count >= distinct missing files in scope but no path evidence exists ->
    #   attribution is unverifiable -> UNKNOWN (never blindly PASS_DECLARED_GAP).
    if gap.reason == "files_skipped":
        if gap.skipped_count is None or gap.skipped_count <= 0:
            return False, None
        if missing_files_in_gap_scope > gap.skipped_count:
            return False, None
        return False, (
            f"files_skipped count-only gap (skipped_count={gap.skipped_count}) lacks "
            "path attribution for missing files"
        )

    return True, None


def _count_missing_files_in_gap_scope(
    gap: DeclaredGap,
    missing_items: list[tuple[str, str | None, str]],
) -> int:
    """Count distinct (root_id, path) omissions falling within gap's root/leg scope."""
    distinct: set[tuple[str, str]] = set()
    for r, lg, p in missing_items:
        if gap.root_id is not None and gap.root_id != r:
            continue
        if gap.leg is not None and lg is not None and gap.leg != lg:
            continue
        distinct.add((r, p))
    return len(distinct)


def _validate_result_limit_gap(
    gap: DeclaredGap,
    reference_hits: tuple[ScopedSearchHit, ...],
    product_hits: tuple[ScopedSearchHit, ...],
) -> tuple[bool, str]:
    """Decision 006: result_limit requires reported truncation, ordered retained prefix, and continuation."""
    if gap.truncated is not True:
        return False, "result_limit declared without truncated=True"
    if gap.continuation_required and not gap.continuation:
        return False, "result_limit missing required continuation token"

    scoped_ref = [
        h
        for h in reference_hits
        if (gap.root_id is None or h.root_id == gap.root_id)
        and (gap.leg is None or h.leg == gap.leg)
        and (not gap.paths or h.hit.path in gap.paths)
    ]
    scoped_prod = [
        h
        for h in product_hits
        if (gap.root_id is None or h.root_id == gap.root_id)
        and (gap.leg is None or h.leg == gap.leg)
        and (not gap.paths or h.hit.path in gap.paths)
    ]
    if len(scoped_prod) > len(scoped_ref):
        return False, "result_limit retained more hits than reference"
    for idx, prod_hit in enumerate(scoped_prod):
        if prod_hit.key() != scoped_ref[idx].key():
            return False, f"result_limit retained hits out of order at index {idx}"
    return True, ""


def _validate_index_warming_interplay(
    gaps: tuple[DeclaredGap, ...],
    missing_roots: set[str],
) -> tuple[bool, str]:
    """Decision 006: for index_warming, verify root scope and time_budget interplay."""
    warming_gaps = [g for g in gaps if g.reason == "index_warming"]
    if not warming_gaps:
        return True, ""
    warming_roots = {g.root_id for g in warming_gaps if g.root_id is not None}
    other_missing_roots = missing_roots - warming_roots
    if warming_roots and other_missing_roots:
        has_time_budget = any(
            g.reason == "time_budget" and (g.root_id is None or g.root_id in other_missing_roots)
            for g in gaps
        )
        has_other_root_gap = all(
            any(g.root_id == r and g.reason != "index_warming" for g in gaps)
            for r in other_missing_roots
        )
        if not (has_time_budget or has_other_root_gap):
            return (
                False,
                f"index_warming on {sorted(warming_roots)} cannot excuse unaffected roots "
                f"{sorted(other_missing_roots)} without time_budget or root-scoped gap",
            )
    return True, ""


def check_catalog(
    reference_items: tuple[CatalogItem, ...],
    answer: ProductCatalogAnswer,
) -> CheckResult:
    """Compare product catalog answer against reference walk in strict D4 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "catalog", reason="missing instrumentation")

    ref_map = {(i.root_id, i.path): i for i in reference_items}
    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))

    # Step 1a: Detect duplicate catalog entries by (root_id, path).
    prod_counts = Counter((i.root_id, i.path) for i in answer.items)
    wrong: list[str] = [
        f"duplicate_catalog_entry:{r}:{p} observed={c}"
        for (r, p), c in sorted(prod_counts.items())
        if c > 1
    ]

    prod_map = {(i.root_id, i.path): i for i in answer.items}
    extra = sorted(f"{r}:{p}" for (r, p) in prod_map if (r, p) not in ref_map)
    for key, prod_item in sorted(prod_map.items()):
        ref_item = ref_map.get(key)
        if ref_item is None:
            continue
        if prod_item.kind != ref_item.kind:
            wrong.append(f"{key[0]}:{key[1]} kind={prod_item.kind}!=ref:{ref_item.kind}")
        elif prod_item.sha256 and ref_item.sha256 and prod_item.sha256 != ref_item.sha256:
            wrong.append(f"{key[0]}:{key[1]} sha256 mismatch")

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "catalog",
            extra_claims=tuple(extra),
            wrong_claims=tuple(wrong),
            declared_gaps=declared_names,
            reason="answer claims extra, duplicate, or wrong catalog entries",
        )

    # Step 2 & 3: Missing claims -> strict scope-attributed gaps.
    missing_keys = sorted(k for k in ref_map if k not in prod_map)
    if not missing_keys:
        return CheckResult(Verdict.PASS, "catalog", declared_gaps=declared_names)

    missing_triples = [(r, None, p) for r, p in missing_keys]
    accounted: list[str] = []
    unaccounted: list[str] = []
    unknown_reasons: list[str] = []
    for root_id, path in missing_keys:
        matched_gap: str | None = None
        for gap in answer.declared_gaps:
            scope_missing = _count_missing_files_in_gap_scope(gap, missing_triples)
            ok, err = _gap_covers_item(
                gap,
                domain="catalog",
                root_id=root_id,
                leg=None,
                path=path,
                missing_files_in_gap_scope=scope_missing,
            )
            if ok:
                matched_gap = gap.reason
                break
            if err:
                unknown_reasons.append(err)
        if matched_gap:
            accounted.append(matched_gap)
        else:
            unaccounted.append(f"{root_id}:{path}")

    if unaccounted:
        if unknown_reasons and len(unknown_reasons) >= len(unaccounted):
            return CheckResult(
                Verdict.UNKNOWN,
                "catalog",
                missing_claims=tuple(unaccounted),
                declared_gaps=declared_names,
                reason="; ".join(dict.fromkeys(unknown_reasons)),
            )
        return CheckResult(
            Verdict.DEFECT,
            "catalog",
            missing_claims=tuple(unaccounted),
            declared_gaps=declared_names,
            reason="missing catalog entries without an accounting in-scope declared gap",
        )

    return CheckResult(
        Verdict.PASS_DECLARED_GAP,
        "catalog",
        missing_claims=tuple(f"{r}:{p}" for r, p in missing_keys),
        declared_gaps=declared_names,
        accounted_gaps=tuple(dict.fromkeys(accounted)),
        reason="omissions accounted by declared gap(s)",
    )


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


def check_change_feed(
    write_log: WriteLog,
    answer: ProductFeedAnswer,
) -> CheckResult:
    """Compare product change-feed events against independent write log in strict D4 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "feed", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))

    # Build per-(root_id, path) ground truth: count, allowed ops, and seq bounds.
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

    # Step 1a: Unlogged (root_id, path) claimed in stream -> DEFECT.
    extra = sorted(f"{r}:{p}" for (r, p) in (observed_keys - logged_keys))

    # Step 1b: Duplicated change, invalid/unrelated op, duplicate/missing effect_id, ordering.
    observed_counts = Counter((e.root_id, e.path) for e in answer.events)
    wrong: list[str] = []
    for (r, p), count in sorted(observed_counts.items()):
        if (r, p) in logged_counts and count > logged_counts[(r, p)]:
            wrong.append(f"duplicated_change:{r}:{p} observed={count} logged={logged_counts[(r, p)]}")

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

    # Step 2 & 3: Missing logged (root_id, path) entries -> strict scope-attributed gaps & resync.
    missing_keys = sorted(logged_keys - observed_keys)
    if not missing_keys:
        return CheckResult(Verdict.PASS, "feed", declared_gaps=declared_names)

    missing_triples = [(r, None, p) for r, p in missing_keys]
    accounted: list[str] = []
    unaccounted: list[str] = []
    unknown_reasons: list[str] = []
    resync_gaps_used: list[DeclaredGap] = []

    for root_id, path in missing_keys:
        matched_gap: DeclaredGap | None = None
        for gap in answer.declared_gaps:
            scope_missing = _count_missing_files_in_gap_scope(gap, missing_triples)
            ok, err = _gap_covers_item(
                gap,
                domain="feed",
                root_id=root_id,
                leg=None,
                path=path,
                missing_files_in_gap_scope=scope_missing,
            )
            if ok:
                matched_gap = gap
                break
            if err:
                unknown_reasons.append(err)
        if matched_gap is not None:
            accounted.append(matched_gap.reason)
            if matched_gap.reason == "resync":
                resync_gaps_used.append(matched_gap)
        else:
            unaccounted.append(f"{root_id}:{path}")

    if unaccounted:
        if unknown_reasons and len(unknown_reasons) >= len(unaccounted):
            return CheckResult(
                Verdict.UNKNOWN,
                "feed",
                missing_claims=tuple(unaccounted),
                declared_gaps=declared_names,
                reason="; ".join(dict.fromkeys(unknown_reasons)),
            )
        return CheckResult(
            Verdict.DEFECT,
            "feed",
            missing_claims=tuple(unaccounted),
            declared_gaps=declared_names,
            reason="missed logged change(s) without an in-scope declared resync or feed gap",
        )

    if resync_gaps_used:
        if answer.reconciled_walk is None or answer.reference_post_walk is None:
            return CheckResult(
                Verdict.UNKNOWN,
                "feed",
                missing_claims=tuple(f"{r}:{p}" for r, p in missing_keys),
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
                missing_claims=tuple(f"{r}:{p}" for r, p in missing_keys),
                extra_claims=reconciled_check.extra_claims,
                wrong_claims=reconciled_check.wrong_claims,
                declared_gaps=declared_names,
                reason="resync declared but reconciled catalog disagrees with reference walk",
            )

    return CheckResult(
        Verdict.PASS_DECLARED_GAP,
        "feed",
        missing_claims=tuple(f"{r}:{p}" for r, p in missing_keys),
        declared_gaps=declared_names,
        accounted_gaps=tuple(dict.fromkeys(accounted)),
        reason="missed feed paths accounted by in-scope declared gap/resync",
    )


def check_search(
    reference_hits: tuple[ScopedSearchHit, ...],
    answer: ProductSearchAnswer,
) -> CheckResult:
    """Compare product search hits against reference search in strict D4 + Decisions 006/009 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "search", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    ref_counts = Counter(h.key() for h in reference_hits)
    prod_counts = Counter(h.key() for h in answer.hits)

    # Step 1: Extra / stale hits OR unexpected duplicate hits -> always DEFECT (never excused).
    extra: list[str] = []
    wrong: list[str] = []
    seen_prod: set[tuple[str, str, str, int, int, int, str]] = set()
    for h in answer.hits:
        k = h.key()
        if k in seen_prod:
            continue
        seen_prod.add(k)
        if k not in ref_counts:
            extra.append(h.label())
        elif prod_counts[k] > ref_counts[k]:
            wrong.append(f"duplicate_hit:{h.label()} observed={prod_counts[k]} expected={ref_counts[k]}")

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "search",
            extra_claims=tuple(sorted(extra)),
            wrong_claims=tuple(sorted(wrong)),
            declared_gaps=declared_names,
            reason="search answer contains stale, extra, or duplicate hit(s)",
        )

    # Step 2 & 3: Missing hits (respecting multiplicity).
    missing_hits: list[ScopedSearchHit] = []
    remaining_prod = Counter(prod_counts)
    for h in reference_hits:
        k = h.key()
        if remaining_prod[k] > 0:
            remaining_prod[k] -= 1
        else:
            missing_hits.append(h)

    if not missing_hits:
        return CheckResult(Verdict.PASS, "search", declared_gaps=declared_names)

    for gap in answer.declared_gaps:
        if gap.reason == "result_limit":
            ok, msg = _validate_result_limit_gap(gap, reference_hits, answer.hits)
            if not ok:
                return CheckResult(
                    Verdict.DEFECT,
                    "search",
                    missing_claims=tuple(h.label() for h in missing_hits),
                    declared_gaps=declared_names,
                    reason=msg,
                )

    missing_roots = {h.root_id for h in missing_hits}
    ok_warm, warm_msg = _validate_index_warming_interplay(answer.declared_gaps, missing_roots)
    if not ok_warm:
        return CheckResult(
            Verdict.DEFECT,
            "search",
            missing_claims=tuple(h.label() for h in missing_hits),
            declared_gaps=declared_names,
            reason=warm_msg,
        )

    missing_triples = [(h.root_id, h.leg, h.hit.path) for h in missing_hits]
    accounted: list[str] = []
    unaccounted: list[str] = []
    unknown_for_missing: list[str] = []

    for hit in missing_hits:
        matched_gap: str | None = None
        hit_unknown: str | None = None
        for gap in answer.declared_gaps:
            scope_missing = _count_missing_files_in_gap_scope(gap, missing_triples)
            ok, err = _gap_covers_item(
                gap,
                domain="search",
                root_id=hit.root_id,
                leg=hit.leg,
                path=hit.hit.path,
                missing_files_in_gap_scope=scope_missing,
            )
            if ok:
                matched_gap = gap.reason
                break
            if err and hit_unknown is None:
                hit_unknown = err
        if matched_gap:
            accounted.append(matched_gap)
        else:
            unaccounted.append(hit.label())
            if hit_unknown is not None:
                unknown_for_missing.append(hit_unknown)

    if unaccounted:
        if len(unknown_for_missing) == len(unaccounted):
            return CheckResult(
                Verdict.UNKNOWN,
                "search",
                missing_claims=tuple(unaccounted),
                declared_gaps=declared_names,
                reason="; ".join(dict.fromkeys(unknown_for_missing)),
            )
        return CheckResult(
            Verdict.DEFECT,
            "search",
            missing_claims=tuple(unaccounted),
            declared_gaps=declared_names,
            reason="missing search hit(s) not accounted by an in-scope declared gap",
        )

    return CheckResult(
        Verdict.PASS_DECLARED_GAP,
        "search",
        missing_claims=tuple(h.label() for h in missing_hits),
        declared_gaps=declared_names,
        accounted_gaps=tuple(dict.fromkeys(accounted)),
        reason="omitted search hits accounted by declared gap(s)",
    )


def _normalize_replace_reference(
    reference_results: dict[str, ReplaceFileResult] | dict[tuple[str, str], ReplaceFileResult],
) -> dict[tuple[str, str], ReplaceFileResult]:
    out: dict[tuple[str, str], ReplaceFileResult] = {}
    for key, val in reference_results.items():
        if isinstance(key, tuple):
            out[(key[0], key[1])] = val
        else:
            out[(val.root_id, key)] = val
    return out


def check_replace(
    reference_results: dict[str, ReplaceFileResult] | dict[tuple[str, str], ReplaceFileResult],
    answer: ProductReplaceAnswer,
) -> CheckResult:
    """Compare product replace outcomes against reference replacement keyed by (root_id, path)."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "replace", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    ref_map = _normalize_replace_reference(reference_results)

    # Detect duplicate (root_id, path) observations.
    obs_counts = Counter((f.root_id, f.path) for f in answer.files)
    wrong: list[str] = [
        f"duplicate_replace_observation:{r}:{p} observed={c}"
        for (r, p), c in sorted(obs_counts.items())
        if c > 1
    ]

    obs_by_key = {(f.root_id, f.path): f for f in answer.files}
    extra = sorted(f"{r}:{p}" for (r, p) in obs_by_key if (r, p) not in ref_map)
    missing_paths: list[tuple[str, str]] = []

    for (root_id, path), obs in sorted(obs_by_key.items()):
        ref = ref_map.get((root_id, path))
        if ref is None:
            continue
        if obs.drifted_before_apply:
            expected_drifted = obs.drifted_sha256 or obs.expected_before_sha256
            if obs.applied or not obs.refused_drift:
                wrong.append(f"overwritten_drifted_file:{root_id}:{path}")
            elif obs.observed_sha256 != expected_drifted:
                wrong.append(f"drifted_file_not_byte_identical:{root_id}:{path}")
        else:
            if obs.applied and obs.observed_sha256 != ref.after_sha256:
                wrong.append(f"replace_hash_mismatch:{root_id}:{path}")
            elif not obs.applied:
                missing_paths.append((root_id, path))

    for root_id, path in sorted(ref_map):
        if (root_id, path) not in obs_by_key:
            missing_paths.append((root_id, path))

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "replace",
            extra_claims=tuple(extra),
            wrong_claims=tuple(wrong),
            declared_gaps=declared_names,
            reason="replace mutated an extra/duplicate/drifted file or produced wrong bytes",
        )

    if not missing_paths:
        return CheckResult(Verdict.PASS, "replace", declared_gaps=declared_names)

    missing_triples = [(r, None, p) for r, p in missing_paths]
    accounted: list[str] = []
    unaccounted: list[str] = []
    unknown_for_missing: list[str] = []
    for root_id, path in missing_paths:
        matched: str | None = None
        miss_unknown: str | None = None
        for gap in answer.declared_gaps:
            scope_missing = _count_missing_files_in_gap_scope(gap, missing_triples)
            ok, err = _gap_covers_item(
                gap,
                domain="replace",
                root_id=root_id,
                leg=None,
                path=path,
                missing_files_in_gap_scope=scope_missing,
            )
            if ok:
                matched = gap.reason
                break
            if err and miss_unknown is None:
                miss_unknown = err
        if matched:
            accounted.append(matched)
        else:
            unaccounted.append(f"{root_id}:{path}")
            if miss_unknown is not None:
                unknown_for_missing.append(miss_unknown)

    if unaccounted:
        if len(unknown_for_missing) == len(unaccounted):
            return CheckResult(
                Verdict.UNKNOWN,
                "replace",
                missing_claims=tuple(unaccounted),
                declared_gaps=declared_names,
                reason="; ".join(dict.fromkeys(unknown_for_missing)),
            )
        return CheckResult(
            Verdict.DEFECT,
            "replace",
            missing_claims=tuple(unaccounted),
            declared_gaps=declared_names,
            reason="expected replacement skipped without an in-scope declared gap",
        )

    return CheckResult(
        Verdict.PASS_DECLARED_GAP,
        "replace",
        missing_claims=tuple(f"{r}:{p}" for r, p in missing_paths),
        declared_gaps=declared_names,
        accounted_gaps=tuple(dict.fromkeys(accounted)),
        reason="unreplaced files accounted by declared gap(s)",
    )


def walk_entries_to_catalog_items(
    entries: list[WalkEntry],
    root_id: str = "root-0",
) -> tuple[CatalogItem, ...]:
    """Convert reference_walk output into CatalogItem tuples for check_catalog."""
    return tuple(
        CatalogItem(path=e.path, kind=e.kind, root_id=root_id, sha256=e.sha256)
        for e in entries
    )


@dataclass
class GapSummaryTracker:
    """Tracks pass, defect, unknown, and per-gap declared-gap counts (D4 + Decision 006)."""

    totals: Counter[str] = field(default_factory=Counter)
    gap_counts: Counter[str] = field(default_factory=Counter)

    def record(self, result: CheckResult) -> None:
        self.totals[result.verdict.value] += 1
        if result.verdict == Verdict.PASS_DECLARED_GAP:
            for gap_name in result.accounted_gaps:
                self.gap_counts[gap_name] += 1

    def to_dict(self) -> dict[str, object]:
        return {
            "totals": dict(sorted(self.totals.items())),
            "gap_counts": dict(sorted(self.gap_counts.items())),
        }
