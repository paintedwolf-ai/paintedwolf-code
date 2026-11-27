"""GB08 · Den presentation, feedback readback contract, and opt-in dev diagnostic.

Matches `issues/GB08.md` and Decision 021:
- Native Den presentation states: grounded (navigable link), exempt (no evidence state),
  host-added (labeled), refused (typed), withheld mid-turn note (absent).
- Feedback readback contract: every offered admissible sample is in the DeliveryLog,
  and every named offender is the actual failing occurrence.
- Opt-in dev diagnostic (typed records only, bounded deduplicated local report):
  1. Grounded verdict with no supporting record in scope
  2. Refusal where exactly one record in scope satisfies every supplied field
  3. Closeout recorded as accepted after a refusal
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Sequence

from grounding.delivery_log import DeliveryLog, ProductVerdict, SurfaceOutcome
from grounding.reference_resolver import Citation, PermittedScope, resolve_citation


GB08_BASE_COMMIT: str = "40c593ac1cc85bb31bb1a4f21812f20ab347e407"
GROUNDED_VERDICTS: frozenset[str] = frozenset({"matched", "traced", "bound"})


@dataclass(frozen=True)
class DenPresentationScenario:
    selector: str
    state: str  # "grounded" | "exempt" | "host_added" | "refused" | "withheld_note"
    expect_navigable_link: bool
    expect_evidence_badge: bool
    expect_host_added_label: bool
    expect_visible_in_feed: bool
    assertion: str


DEN_PRESENTATION_SCENARIOS: tuple[DenPresentationScenario, ...] = (
    DenPresentationScenario(
        selector="GB08-DEN-01-grounded-navigable-link",
        state="grounded",
        expect_navigable_link=True,
        expect_evidence_badge=True,
        expect_host_added_label=False,
        expect_visible_in_feed=True,
        assertion="Grounded citation renders as a navigable link opening the cited observation.",
    ),
    DenPresentationScenario(
        selector="GB08-DEN-02-exempt-no-evidence-state",
        state="exempt",
        expect_navigable_link=False,
        expect_evidence_badge=False,
        expect_host_added_label=False,
        expect_visible_in_feed=True,
        assertion="Exempt speech renders cleanly with no evidence state badge.",
    ),
    DenPresentationScenario(
        selector="GB08-DEN-03-host-added-labeled",
        state="host_added",
        expect_navigable_link=True,
        expect_evidence_badge=True,
        expect_host_added_label=True,
        expect_visible_in_feed=True,
        assertion="Host-assembled references render explicitly labeled as host-added.",
    ),
    DenPresentationScenario(
        selector="GB08-DEN-04-refused-typed-state",
        state="refused",
        expect_navigable_link=False,
        expect_evidence_badge=True,
        expect_host_added_label=False,
        expect_visible_in_feed=True,
        assertion="Refused state displays its typed refusal without grounding invalid refs.",
    ),
    DenPresentationScenario(
        selector="GB08-DEN-05-withheld-note-absent",
        state="withheld_note",
        expect_navigable_link=False,
        expect_evidence_badge=False,
        expect_host_added_label=False,
        expect_visible_in_feed=False,
        assertion="Withheld mid-turn note is absent from the user-facing Den feed.",
    ),
)


@dataclass(frozen=True)
class FeedbackAuditResult:
    passed: bool
    inadmissible_samples: tuple[str, ...]
    misnamed_offenders: tuple[str, ...]


def audit_refusal_feedback(
    delivery_log: DeliveryLog,
    *,
    offered_handles: Sequence[str],
    named_offenders: Sequence[str],
    actual_failing_citations: Sequence[str],
) -> FeedbackAuditResult:
    """Verify every offered admissible handle is in DeliveryLog and every named offender actually failed."""
    valid_handles = {r.handle for r in delivery_log.records if r.model_visible}
    inadmissible = tuple(sorted(h for h in offered_handles if h not in valid_handles))
    actual_set = set(actual_failing_citations)
    misnamed = tuple(sorted(o for o in named_offenders if o not in actual_set))
    return FeedbackAuditResult(
        passed=(not inadmissible and not misnamed),
        inadmissible_samples=inadmissible,
        misnamed_offenders=misnamed,
    )


@dataclass(frozen=True)
class DiagnosticContradiction:
    kind: str  # "grounded_without_support" | "refusal_with_unique_support" | "accepted_after_refusal"
    citation_id: str
    detail: str


def run_dev_diagnostic(
    delivery_log: DeliveryLog,
    citations: Sequence[Citation],
    scope: PermittedScope,
    outcome: SurfaceOutcome,
    *,
    enabled: bool = True,
    had_prior_refusal: bool = False,
    recorded_accepted: bool = False,
    max_items: int = 32,
) -> tuple[DiagnosticContradiction, ...]:
    """Opt-in dev diagnostic reading typed records only; bounded and deduplicated."""
    if not enabled:
        return ()

    seen: set[tuple[str, str]] = set()
    findings: list[DiagnosticContradiction] = []

    def _add(kind: str, cid: str, detail: str) -> None:
        key = (kind, cid)
        if key in seen or len(findings) >= max_items:
            return
        seen.add(key)
        findings.append(DiagnosticContradiction(kind=kind, citation_id=cid, detail=detail))

    verdicts_by_id: dict[str, ProductVerdict] = {v.citation_id: v for v in outcome.verdicts}
    for cit in citations:
        ref = resolve_citation(delivery_log, cit, scope)
        pv = verdicts_by_id.get(cit.citation_id)
        if pv is None:
            continue
        is_prod_grounded = pv.verdict in GROUNDED_VERDICTS
        if is_prod_grounded and ref.verdict_class != "grounded":
            _add(
                "grounded_without_support",
                cit.citation_id,
                f"product={pv.verdict} but reference={ref.verdict_class} ({ref.reason})",
            )
        elif (not is_prod_grounded) and ref.verdict_class == "grounded" and len(ref.supporting_handles) == 1:
            _add(
                "refusal_with_unique_support",
                cit.citation_id,
                f"product={pv.verdict} but unique supporting handle={ref.supporting_handles[0]}",
            )

    if had_prior_refusal and recorded_accepted:
        _add(
            "accepted_after_refusal",
            "closeout",
            "closeout recorded as accepted after an unresolved refusal",
        )

    return tuple(findings)
