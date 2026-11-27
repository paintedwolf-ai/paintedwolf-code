"""Core D4 verdicts, typed gap definitions, and strict scope-first attribution."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
from enum import Enum


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


@dataclass(frozen=True)
class DeclaredGap:
    """Typed coverage or freshness gap declared by a product answer (D4 + Decisions 006/009/012)."""

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


def count_missing_files_in_gap_scope(
    gap: DeclaredGap,
    missing_items: list[tuple[str, str | None, str]],
) -> int:
    """Count distinct (root_id, path) omissions falling within gap's root/leg scope."""
    distinct: set[tuple[str, str]] = set()
    for r, lg, p in missing_items:
        if gap.root_id is not None and gap.root_id != r:
            continue
        if gap.leg is not None and gap.leg != lg:
            continue
        distinct.add((r, p))
    return len(distinct)


def gap_covers_item(
    gap: DeclaredGap,
    *,
    domain: str,
    root_id: str,
    leg: str | None,
    path: str | None,
    missing_files_in_gap_scope: int = 0,
) -> tuple[bool, str | None]:
    """Return (covered, uncertain_reason) enforcing scope-first attribution (Decisions 006/009/012).

    Scope (domain, root_id, leg, paths) is ALWAYS matched before considering
    verifiability or count-only uncertainty, so an unverifiable gap on an
    unrelated root/leg/path can never mask a demonstrated omission elsewhere.
    """
    if gap.reason not in ALL_D4_GAPS:
        return False, None
    if domain not in GAP_DOMAIN_SUPPORT.get(gap.reason, frozenset()):
        return False, None

    # 1. Strict root scope.
    if gap.root_id is not None and gap.root_id != root_id:
        return False, None

    # 2. Strict leg scope (including when observation has leg=None, e.g. catalog/feed/replace).
    if gap.reason in SYMBOL_ONLY_GAPS and leg != "symbol":
        return False, None
    if gap.leg is not None and gap.leg != leg:
        return False, None

    # 3. Strict path scope for any gap carrying specific paths.
    if gap.paths and (path is None or path not in gap.paths):
        return False, None

    # 4. Attribution quality (evaluated only for in-scope gaps).
    if not gap.verifiable:
        return False, f"unverifiable gap attribution for {gap.reason}"

    if gap.reason == "files_skipped" and not gap.paths:
        if gap.skipped_count is None or gap.skipped_count <= 0:
            return False, None
        if missing_files_in_gap_scope > gap.skipped_count:
            return False, None
        return False, (
            f"files_skipped count-only gap (skipped_count={gap.skipped_count}) lacks "
            "path attribution for missing files"
        )

    return True, None


def evaluate_omissions(
    *,
    domain: str,
    missing_items: list[tuple[str, str | None, str, str]],
    declared_gaps: tuple[DeclaredGap, ...],
) -> CheckResult:
    """Evaluate a non-empty list of (root_id, leg, path, label) omissions against declared_gaps.

    If ANY omission has no in-scope accounting or uncertain gap, returns DEFECT.
    Only if every unaccounted omission has an in-scope uncertain explanation does it return UNKNOWN.
    """
    declared_names = tuple(dict.fromkeys(g.reason for g in declared_gaps))
    scope_triples = [(r, lg, p) for r, lg, p, _ in missing_items]
    accounted: list[str] = []
    unexplained: list[str] = []
    uncertain_labels: list[str] = []
    uncertain_reasons: list[str] = []

    for root_id, leg, path, label in missing_items:
        matched_gap: str | None = None
        item_uncertain: str | None = None
        for gap in declared_gaps:
            scope_missing = count_missing_files_in_gap_scope(gap, scope_triples)
            ok, err = gap_covers_item(
                gap,
                domain=domain,
                root_id=root_id,
                leg=leg,
                path=path,
                missing_files_in_gap_scope=scope_missing,
            )
            if ok:
                matched_gap = gap.reason
                break
            if err and item_uncertain is None:
                item_uncertain = err
        if matched_gap is not None:
            accounted.append(matched_gap)
        elif item_uncertain is not None:
            uncertain_labels.append(label)
            uncertain_reasons.append(item_uncertain)
        else:
            unexplained.append(label)

    if unexplained:
        return CheckResult(
            Verdict.DEFECT,
            domain,
            missing_claims=tuple(unexplained + uncertain_labels),
            declared_gaps=declared_names,
            reason=f"missing {domain} item(s) without an accounting in-scope declared gap",
        )

    if uncertain_labels:
        return CheckResult(
            Verdict.UNKNOWN,
            domain,
            missing_claims=tuple(uncertain_labels),
            declared_gaps=declared_names,
            reason="; ".join(dict.fromkeys(uncertain_reasons)),
        )

    return CheckResult(
        Verdict.PASS_DECLARED_GAP,
        domain,
        missing_claims=tuple(lbl for _, _, _, lbl in missing_items),
        declared_gaps=declared_names,
        accounted_gaps=tuple(dict.fromkeys(accounted)),
        reason=f"omitted {domain} item(s) accounted by declared gap(s)",
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
