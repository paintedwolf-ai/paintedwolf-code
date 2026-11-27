"""D4 Differential Checker for catalog and search domains (Decisions 006/009/012)."""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass

from .checker_types import (
    CheckResult,
    DeclaredGap,
    Verdict,
    evaluate_omissions,
)
from .references import SearchHit, WalkEntry


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

    missing_keys = sorted(k for k in ref_map if k not in prod_map)
    if not missing_keys:
        return CheckResult(Verdict.PASS, "catalog", declared_gaps=declared_names)

    return evaluate_omissions(
        domain="catalog",
        missing_items=[(r, None, p, f"{r}:{p}") for r, p in missing_keys],
        declared_gaps=answer.declared_gaps,
    )


def check_search(
    reference_hits: tuple[ScopedSearchHit, ...],
    answer: ProductSearchAnswer,
) -> CheckResult:
    """Compare product search hits against reference search in strict D4 order."""
    if not answer.instrumentation_complete:
        return CheckResult(Verdict.UNKNOWN, "search", reason="missing instrumentation")

    declared_names = tuple(dict.fromkeys(g.reason for g in answer.declared_gaps))
    ref_counts = Counter(h.key() for h in reference_hits)
    prod_counts = Counter(h.key() for h in answer.hits)

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

    return evaluate_omissions(
        domain="search",
        missing_items=[(h.root_id, h.leg, h.hit.path, h.label()) for h in missing_hits],
        declared_gaps=answer.declared_gaps,
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
