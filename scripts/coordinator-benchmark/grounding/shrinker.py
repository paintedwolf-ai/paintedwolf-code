"""Delta-debugging shrinker for failing (delivery_log, citation/markdown) cases (D6).

Shrinks a failing instance to a minimal reproducing delivery log and
citation/markdown input and emits a replayable JSON seed artifact.
"""
from __future__ import annotations

from dataclasses import dataclass, replace
import json
from typing import Any, Callable, Sequence, TypeVar

from .delivery_log import DeliveryLog, DeliveryRecord, LineRange, sha256_bytes
from .generators import SeedIdentity
from .reference_resolver import Citation, PermittedScope


T = TypeVar("T")


def _ddmin(items: Sequence[T], test_fn: Callable[[list[T]], bool]) -> list[T]:
    """Classic Zeller delta-debugging (ddmin) minimizing a failing sequence."""
    current = list(items)
    if len(current) <= 1:
        if len(current) == 1 and test_fn([]):
            return []
        return current

    n = 2
    while len(current) >= 2:
        chunk_size = max(1, len(current) // n)
        reduced = False

        # Try individual subsets first (aggressive reduction)
        for i in range(0, len(current), chunk_size):
            subset = current[i : i + chunk_size]
            if subset and len(subset) < len(current) and test_fn(subset):
                current = subset
                n = 2
                reduced = True
                break
        if reduced:
            continue

        # Try complements (removing one chunk at a time)
        for i in range(0, len(current), chunk_size):
            complement = current[:i] + current[i + chunk_size :]
            if len(complement) < len(current) and test_fn(complement):
                current = complement
                n = max(n - 1, 2)
                reduced = True
                break
        if reduced:
            continue

        if n >= len(current):
            break
        n = min(len(current), n * 2)

    if len(current) == 1 and test_fn([]):
        return []
    return current


@dataclass(frozen=True)
class ShrinkableCase:
    """Failing instance eligible for delta-debugging minimization."""

    delivery_log: DeliveryLog
    scope: PermittedScope
    citation: Citation | None = None
    markdown: str = ""
    seed: SeedIdentity | None = None


@dataclass(frozen=True)
class ShrunkArtifact:
    """Minimal failing instance with replayable JSON seed metadata."""

    minimized: ShrinkableCase
    original_record_count: int
    shrunk_record_count: int
    original_bytes: int
    shrunk_bytes: int
    evaluations: int

    def to_dict(self) -> dict[str, Any]:
        cit = self.minimized.citation
        cit_dict = (
            {
                "citation_type": cit.citation_type,
                "raw": cit.raw,
                "handle": cit.handle,
                "worker_leg": cit.worker_leg,
                "path": cit.path,
                "start_line": cit.start_line,
                "end_line": cit.end_line,
                "excerpt": cit.excerpt,
                "url": cit.url,
                "artifact_id": cit.artifact_id,
                "citation_id": cit.citation_id,
            }
            if cit is not None
            else None
        )
        return {
            "version": 1,
            "seed": self.minimized.seed.to_dict() if self.minimized.seed else None,
            "scope": {
                "session_id": self.minimized.scope.session_id,
                "worker_leg": self.minimized.scope.worker_leg,
                "permitted_legs": sorted(self.minimized.scope.permitted_legs),
                "round_id": self.minimized.scope.round_id,
                "intent_window": self.minimized.scope.intent_window,
                "permitted_artifacts": sorted(self.minimized.scope.permitted_artifacts),
                "platform": self.minimized.scope.platform,
            },
            "delivery_log": self.minimized.delivery_log.to_dict(),
            "citation": cit_dict,
            "markdown": self.minimized.markdown,
            "metrics": {
                "original_record_count": self.original_record_count,
                "shrunk_record_count": self.shrunk_record_count,
                "original_bytes": self.original_bytes,
                "shrunk_bytes": self.shrunk_bytes,
                "evaluations": self.evaluations,
            },
        }

    def to_json(self) -> str:
        return json.dumps(self.to_dict(), sort_keys=True, indent=2)

    @property
    def sha256(self) -> str:
        return sha256_bytes(self.to_json().encode("utf-8"))

    @classmethod
    def from_json(cls, payload: str) -> ShrunkArtifact:
        data = json.loads(payload)
        seed_raw = data.get("seed")
        seed = SeedIdentity.from_dict(seed_raw) if seed_raw else None
        scope_raw = data["scope"]
        scope = PermittedScope(
            session_id=scope_raw["session_id"],
            worker_leg=scope_raw.get("worker_leg", ""),
            permitted_legs=frozenset(scope_raw.get("permitted_legs", ())),
            round_id=scope_raw.get("round_id", ""),
            intent_window=scope_raw.get("intent_window", ""),
            permitted_artifacts=frozenset(scope_raw.get("permitted_artifacts", ())),
            platform=scope_raw.get("platform", "linux"),
        )
        log = DeliveryLog.from_dict(data["delivery_log"])
        cit_raw = data.get("citation")
        cit = Citation(**cit_raw) if cit_raw is not None else None
        minimized = ShrinkableCase(
            delivery_log=log,
            scope=scope,
            citation=cit,
            markdown=str(data.get("markdown", "")),
            seed=seed,
        )
        metrics = data["metrics"]
        return cls(
            minimized=minimized,
            original_record_count=int(metrics["original_record_count"]),
            shrunk_record_count=int(metrics["shrunk_record_count"]),
            original_bytes=int(metrics["original_bytes"]),
            shrunk_bytes=int(metrics["shrunk_bytes"]),
            evaluations=int(metrics["evaluations"]),
        )


def _case_bytes(case: ShrinkableCase) -> int:
    total = sum(len(r.raw_bytes) for r in case.delivery_log.records)
    total += len(case.markdown.encode("utf-8"))
    if case.citation is not None:
        total += len(case.citation.raw.encode("utf-8")) + len(case.citation.excerpt.encode("utf-8"))
    return total


def _shrink_record_lines(
    rec: DeliveryRecord,
    test_record: Callable[[DeliveryRecord], bool],
) -> DeliveryRecord:
    lines = rec.raw_text.splitlines(keepends=True)
    if len(lines) <= 1:
        return rec

    def _check_lines(subset: list[str]) -> bool:
        if not subset:
            return False
        new_bytes = "".join(subset).encode("utf-8")
        new_ranges = rec.delivered_ranges
        if len(rec.delivered_ranges) == 1:
            start = rec.delivered_ranges[0].start
            new_ranges = (LineRange(start, start + len(subset) - 1),)
        cand = replace(rec, raw_bytes=new_bytes, delivered_ranges=new_ranges)
        return test_record(cand)

    shrunk_lines = _ddmin(lines, _check_lines)
    if not shrunk_lines or shrunk_lines == lines:
        return rec
    new_bytes = "".join(shrunk_lines).encode("utf-8")
    new_ranges = rec.delivered_ranges
    if len(rec.delivered_ranges) == 1:
        start = rec.delivered_ranges[0].start
        new_ranges = (LineRange(start, start + len(shrunk_lines) - 1),)
    return replace(rec, raw_bytes=new_bytes, delivered_ranges=new_ranges)


def shrink_case(
    case: ShrinkableCase,
    is_failing: Callable[[ShrinkableCase], bool],
) -> ShrunkArtifact:
    """Shrink a failing ShrinkableCase to a minimal failing instance.

    `is_failing(candidate)` must return True when the candidate still reproduces
    the failure. Raises ValueError if the initial case does not fail.
    """
    evaluations = 0

    def _eval(cand: ShrinkableCase) -> bool:
        nonlocal evaluations
        evaluations += 1
        return bool(is_failing(cand))

    if not _eval(case):
        raise ValueError("Initial case does not satisfy is_failing predicate")

    orig_records = len(case.delivery_log.records)
    orig_bytes = _case_bytes(case)
    current = case

    # 1. Minimize delivery log records via ddmin
    if current.delivery_log.records:
        def _test_records(recs: list[DeliveryRecord]) -> bool:
            return _eval(replace(current, delivery_log=current.delivery_log.with_records(recs)))

        min_recs = _ddmin(current.delivery_log.records, _test_records)
        current = replace(current, delivery_log=current.delivery_log.with_records(min_recs))

    # 2. Minimize raw_bytes lines within each surviving record
    surviving = list(current.delivery_log.records)
    for idx in range(len(surviving)):
        def _test_single_rec(new_rec: DeliveryRecord, i: int = idx) -> bool:
            updated = list(surviving)
            updated[i] = new_rec
            return _eval(replace(current, delivery_log=current.delivery_log.with_records(updated)))

        shrunk_rec = _shrink_record_lines(surviving[idx], _test_single_rec)
        surviving[idx] = shrunk_rec
        current = replace(current, delivery_log=current.delivery_log.with_records(surviving))

    # 3. Minimize markdown lines if present
    if current.markdown:
        md_lines = current.markdown.splitlines(keepends=True)
        if len(md_lines) > 1:
            def _test_md(subset: list[str]) -> bool:
                return _eval(replace(current, markdown="".join(subset)))

            min_md = _ddmin(md_lines, _test_md)
            current = replace(current, markdown="".join(min_md))

    # 4. Minimize citation excerpt tokens if present
    if current.citation is not None and len(current.citation.excerpt) > 1:
        exc_chars = list(current.citation.excerpt)
        def _test_exc(subset: list[str]) -> bool:
            if not subset:
                return False
            new_cit = replace(current.citation, excerpt="".join(subset))  # type: ignore[arg-type]
            return _eval(replace(current, citation=new_cit))

        min_exc = _ddmin(exc_chars, _test_exc)
        if min_exc and "".join(min_exc) != current.citation.excerpt:
            current = replace(current, citation=replace(current.citation, excerpt="".join(min_exc)))

    return ShrunkArtifact(
        minimized=current,
        original_record_count=orig_records,
        shrunk_record_count=len(current.delivery_log.records),
        original_bytes=orig_bytes,
        shrunk_bytes=_case_bytes(current),
        evaluations=evaluations,
    )
