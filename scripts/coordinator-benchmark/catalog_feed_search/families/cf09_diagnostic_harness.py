"""CF09 opt-in dev diagnostic qualification checker and evidence recorder.

Verifies the typed JSONL report produced by the opt-in catalog diagnostic:
- Detects each injected disagreement (missing_in_catalog, extra_in_catalog, kind_mismatch).
- Raises nothing on declared coverage gaps (warming, incomplete, refreshing, failed, bounded, lazy).
- Produces bounded, deduplicated local reports using typed records and identities only.
- Preserves identical ordinary product answers and work counters with diagnostic enabled vs disabled,
  while separately counting the diagnostic's own bounded reads (Decision 002).
"""

from __future__ import annotations

from dataclasses import asdict, dataclass
import json
from pathlib import Path
from typing import Any


REQUIRED_DISAGREEMENT_KINDS: frozenset[str] = frozenset(
    {
        "missing_in_catalog",
        "extra_in_catalog",
        "kind_mismatch",
    }
)


@dataclass(frozen=True)
class WorkCounterComparison:
    """Comparison of ordinary product work counters with diagnostic off vs on."""

    disabled_product_work: dict[str, int]
    enabled_product_work: dict[str, int]
    diagnostic_reads: dict[str, int]
    answers_equal: bool

    @property
    def product_work_equal(self) -> bool:
        return self.disabled_product_work == self.enabled_product_work

    @property
    def passed(self) -> bool:
        return self.answers_equal and self.product_work_equal


@dataclass(frozen=True)
class DiagnosticQualificationResult:
    """Overall qualification verdict for the CF09 opt-in dev diagnostic."""

    verdict: str  # "PASS" | "DEFECT"
    detected_kinds: tuple[str, ...]
    false_positives_on_declared_gaps: int
    deduped_ok: bool
    bounded_ok: bool
    work_comparison: WorkCounterComparison
    violations: tuple[str, ...] = ()

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


def load_diagnostic_jsonl(path: Path) -> list[dict[str, Any]]:
    """Read typed JSONL records emitted by the opt-in catalog diagnostic."""
    records: list[dict[str, Any]] = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line:
            continue
        parsed = json.loads(line)
        if isinstance(parsed, dict):
            records.append(parsed)
    return records


def verify_diagnostic_qualification(
    *,
    injected_records: list[dict[str, Any]],
    declared_gap_records: list[dict[str, Any]],
    work_comparison: WorkCounterComparison,
    max_disagreements_bound: int = 64,
) -> DiagnosticQualificationResult:
    """Verify all CF09 opt-in dev diagnostic acceptance invariants."""
    violations: list[str] = []

    detected_kinds: set[str] = set()
    deduped_ok = True
    bounded_ok = True

    for rec in injected_records:
        disagreements = rec.get("disagreements") or []
        if len(disagreements) > max_disagreements_bound:
            bounded_ok = False
            violations.append(
                f"unbounded_report: {len(disagreements)} > {max_disagreements_bound}"
            )
        seen_keys: set[tuple[Any, ...]] = set()
        for item in disagreements:
            if not isinstance(item, dict):
                continue
            kind = str(item.get("kind") or "")
            detected_kinds.add(kind)
            key = (
                rec.get("project_id"),
                rec.get("root_id"),
                rec.get("generation_instance"),
                rec.get("generation_revision"),
                item.get("path"),
                kind,
            )
            if key in seen_keys:
                deduped_ok = False
                violations.append(f"duplicate_disagreement: {key}")
            seen_keys.add(key)

    missing_kinds = sorted(REQUIRED_DISAGREEMENT_KINDS - detected_kinds)
    if missing_kinds:
        violations.append(f"undetected_injected_disagreements: {missing_kinds}")

    false_positives = 0
    for rec in declared_gap_records:
        disagreements = rec.get("disagreements") or []
        false_positives += len(disagreements)
    if false_positives > 0:
        violations.append(
            f"false_positive_on_declared_gap: raised {false_positives} disagreements on declared gaps"
        )

    if not work_comparison.answers_equal:
        violations.append("product_answers_differ: enabled vs disabled answers diverged")
    if not work_comparison.product_work_equal:
        violations.append(
            f"product_work_counters_differ: disabled={work_comparison.disabled_product_work} "
            f"enabled={work_comparison.enabled_product_work}"
        )

    return DiagnosticQualificationResult(
        verdict="PASS" if not violations else "DEFECT",
        detected_kinds=tuple(sorted(detected_kinds)),
        false_positives_on_declared_gaps=false_positives,
        deduped_ok=deduped_ok,
        bounded_ok=bounded_ok,
        work_comparison=work_comparison,
        violations=tuple(violations),
    )
