"""D4 Differential Checker with Decision 006 typed gaps and strict scope attribution."""

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


# Original D4 declared gaps (decisions.md).
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

# Supported domains for each typed gap reason.
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


@dataclass(frozen=True)
class DeclaredGap:
    """Typed coverage or freshness gap declared by a product answer (D4 + Decision 006)."""

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


@dataclass(frozen=True)
class ProductFeedAnswer:
    """Product change-feed observation with optional resync and reconciled catalog state."""

    events: tuple[FeedEvent, ...]
    declared_gaps: tuple[DeclaredGap, ...] = ()
    reconciled_walk: tuple[CatalogItem, ...] | None = None
    reference_post_walk: tuple[CatalogItem, ...] | None = None
    instrumentation_complete: bool = True


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
) -> tuple[bool, str | None]:
    """Return (covered, unknown_or_error_reason) enforcing Decision 006 scope rules."""
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

    # Strict file attribution for files_skipped.
    if gap.reason == "files_skipped":
        if gap.paths:
            if path is None or path not in gap.paths:
                return False, None
        elif gap.skipped_count is None or gap.skipped_count <= 0:
            return False, None

    return True, None


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
    ]
    scoped_prod = [
        h
        for h in product_hits
        if (gap.root_id is None or h.root_id == gap.root_id)
        and (gap.leg is None or h.leg == gap.leg)
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
    prod_map = {(i.root_id, i.path): i for i in answer.items}
    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))

    # Step 1: Extra or wrong claims -> always DEFECT, never excused by declared gaps.
    extra = sorted(f"{r}:{p}" for (r, p) in prod_map if (r, p) not in ref_map)
    wrong: list[str] = []
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
            reason="answer claims entries or metadata the reference does not show",
        )

    # Step 2 & 3: Missing claims -> check strict scope-attributed gaps.
    missing_keys = sorted(k for k in ref_map if k not in prod_map)
    if not missing_keys:
        return CheckResult(Verdict.PASS, "catalog", declared_gaps=declared_names)

    accounted: list[str] = []
    unaccounted: list[str] = []
    unknown_reasons: list[str] = []
    for root_id, path in missing_keys:
        matched_gap: str | None = None
        for gap in answer.declared_gaps:
            ok, err = _gap_covers_item(gap, domain="catalog", root_id=root_id, leg=None, path=path)
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
        if unknown_reasons and not accounted:
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


def check_change_feed(
    write_log: WriteLog,
    answer: ProductFeedAnswer,
) -> CheckResult:
    """Compare product change-feed events against independent write log in strict D4 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "feed", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    logged_paths = write_log.touched_paths()
    observed_paths = {e.path for e in answer.events}

    # Step 1a: Unlogged path claimed in stream -> DEFECT.
    extra = sorted(observed_paths - logged_paths)
    # Step 1b: Duplicated change for a single logged write -> DEFECT.
    # Coalesced bursts (fewer events than writes for the same path) are valid;
    # MORE events than logged writes for a path (or duplicate effect_id) is a DEFECT.
    logged_counts = Counter()
    for entry in write_log.entries:
        logged_counts[entry.path] += 1
        if entry.new_path:
            logged_counts[entry.new_path] += 1

    observed_counts = Counter(e.path for e in answer.events)
    wrong: list[str] = []
    for path, count in sorted(observed_counts.items()):
        if path in logged_counts and count > logged_counts[path]:
            wrong.append(f"duplicated_change:{path} observed={count} logged={logged_counts[path]}")

    seen_effects: set[str] = set()
    for ev in answer.events:
        if ev.effect_id is not None:
            if ev.effect_id in seen_effects:
                wrong.append(f"duplicate_effect_id:{ev.effect_id}")
            seen_effects.add(ev.effect_id)

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "feed",
            extra_claims=tuple(extra),
            wrong_claims=tuple(wrong),
            declared_gaps=declared_names,
            reason="feed claimed unlogged or duplicated changes",
        )

    # Step 2 & 3: Missing logged paths.
    missing = sorted(logged_paths - observed_paths)
    if not missing:
        return CheckResult(Verdict.PASS, "feed", declared_gaps=declared_names)

    has_resync = any(g.reason == "resync" and g.verifiable for g in answer.declared_gaps)
    if has_resync:
        if answer.reconciled_walk is None or answer.reference_post_walk is None:
            return CheckResult(
                Verdict.UNKNOWN,
                "feed",
                missing_claims=tuple(missing),
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
                missing_claims=tuple(missing),
                extra_claims=reconciled_check.extra_claims,
                wrong_claims=reconciled_check.wrong_claims,
                declared_gaps=declared_names,
                reason="resync declared but reconciled catalog disagrees with reference walk",
            )
        return CheckResult(
            Verdict.PASS_DECLARED_GAP,
            "feed",
            missing_claims=tuple(missing),
            declared_gaps=declared_names,
            accounted_gaps=("resync",),
            reason="resync declared and reconciled catalog matches reference walk",
        )

    other_feed_gaps = [
        g.reason
        for g in answer.declared_gaps
        if g.verifiable and "feed" in GAP_DOMAIN_SUPPORT.get(g.reason, frozenset())
    ]
    if other_feed_gaps:
        return CheckResult(
            Verdict.PASS_DECLARED_GAP,
            "feed",
            missing_claims=tuple(missing),
            declared_gaps=declared_names,
            accounted_gaps=tuple(dict.fromkeys(other_feed_gaps)),
            reason="missed feed paths accounted by declared feed gap",
        )

    return CheckResult(
        Verdict.DEFECT,
        "feed",
        missing_claims=tuple(missing),
        declared_gaps=declared_names,
        reason="missed logged change(s) without declared resync or feed gap",
    )


def check_search(
    reference_hits: tuple[ScopedSearchHit, ...],
    answer: ProductSearchAnswer,
) -> CheckResult:
    """Compare product search hits against reference search in strict D4 + Decision 006 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "search", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    ref_by_key = {h.key(): h for h in reference_hits}
    prod_by_key = {h.key(): h for h in answer.hits}

    # Step 1: Extra / stale hits -> always DEFECT, never excused by declared gaps.
    extra = sorted(h.label() for k, h in prod_by_key.items() if k not in ref_by_key)
    if extra:
        return CheckResult(
            Verdict.DEFECT,
            "search",
            extra_claims=tuple(extra),
            declared_gaps=declared_names,
            reason="search answer contains stale or extra hit(s) not in reference",
        )

    # Step 2 & 3: Missing hits.
    missing_hits = [h for h in reference_hits if h.key() not in prod_by_key]
    if not missing_hits:
        return CheckResult(Verdict.PASS, "search", declared_gaps=declared_names)

    # Validate result_limit contracts up front if present.
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

    # Validate index_warming <-> time_budget interplay (Decision 006).
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

    accounted: list[str] = []
    unaccounted: list[str] = []
    unknown_reasons: list[str] = []
    for hit in missing_hits:
        matched_gap: str | None = None
        for gap in answer.declared_gaps:
            ok, err = _gap_covers_item(
                gap,
                domain="search",
                root_id=hit.root_id,
                leg=hit.leg,
                path=hit.hit.path,
            )
            if ok:
                matched_gap = gap.reason
                break
            if err:
                unknown_reasons.append(err)
        if matched_gap:
            accounted.append(matched_gap)
        else:
            unaccounted.append(hit.label())

    if unaccounted:
        if unknown_reasons and not accounted:
            return CheckResult(
                Verdict.UNKNOWN,
                "search",
                missing_claims=tuple(unaccounted),
                declared_gaps=declared_names,
                reason="; ".join(dict.fromkeys(unknown_reasons)),
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


def check_replace(
    reference_results: dict[str, ReplaceFileResult],
    answer: ProductReplaceAnswer,
) -> CheckResult:
    """Compare product replace outcomes against reference byte-spliced replacement (R02/R03)."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "replace", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    obs_by_path = {f.path: f for f in answer.files}

    extra = sorted(p for p in obs_by_path if p not in reference_results)
    wrong: list[str] = []
    missing_paths: list[tuple[str, str]] = []

    for path, obs in sorted(obs_by_path.items()):
        ref = reference_results.get(path)
        if ref is None:
            continue
        if obs.drifted_before_apply:
            expected_drifted = obs.drifted_sha256 or obs.expected_before_sha256
            if obs.applied or not obs.refused_drift:
                wrong.append(f"overwritten_drifted_file:{path}")
            elif obs.observed_sha256 != expected_drifted:
                wrong.append(f"drifted_file_not_byte_identical:{path}")
        else:
            if obs.applied and obs.observed_sha256 != ref.after_sha256:
                wrong.append(f"replace_hash_mismatch:{path}")
            elif not obs.applied:
                missing_paths.append((obs.root_id, path))

    for path in sorted(reference_results):
        if path not in obs_by_path:
            missing_paths.append(("root-0", path))

    if extra or wrong:
        return CheckResult(
            Verdict.DEFECT,
            "replace",
            extra_claims=tuple(extra),
            wrong_claims=tuple(wrong),
            declared_gaps=declared_names,
            reason="replace mutated an extra/drifted file or produced wrong bytes",
        )

    if not missing_paths:
        return CheckResult(Verdict.PASS, "replace", declared_gaps=declared_names)

    accounted: list[str] = []
    unaccounted: list[str] = []
    for root_id, path in missing_paths:
        matched: str | None = None
        for gap in answer.declared_gaps:
            ok, _ = _gap_covers_item(gap, domain="replace", root_id=root_id, leg=None, path=path)
            if ok:
                matched = gap.reason
                break
        if matched:
            accounted.append(matched)
        else:
            unaccounted.append(f"{root_id}:{path}")

    if unaccounted:
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
