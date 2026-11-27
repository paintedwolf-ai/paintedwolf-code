"""Independent grounding checker implementing D4 comparison order and D10 friction.

Comparison order (D4):
1. false_accept: product grounds a reference that no delivery in scope supports
   (or grounds an ambiguous non-handle citation).
2. false_reject: product refuses a reference that exactly one delivery in scope supports.
3. recording_defect: product's minted records differ from the delivery log in
   kind, delivered_ranges, body, worker_leg, or source_revision (or missing/extra).
4. wording_defect: outcome differs across the 3 D5 wordings
   (confident, hedged, completion_asserting).
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Sequence

from .delivery_log import (
    WORDINGS,
    DeliveryLog,
    DeliveryRecord,
    FrictionCounters,
    ProductRecord,
    ProductVerdict,
    SurfaceOutcome,
)
from .reference_resolver import (
    Citation,
    PermittedScope,
    ReferenceVerdict,
    has_citable_observation,
    resolve_citation,
)


DEFECT_PRIORITY: dict[str, int] = {
    "false_accept": 1,
    "false_reject": 2,
    "recording_defect": 3,
    "wording_defect": 4,
    "surface_policy_defect": 5,
}

VALID_MID_TURN_NOTE_CODES: frozenset[str] = frozenset(
    {
        "MID_TURN_NOTE_UNGROUNDED",
        "MID_TURN_NOTE_HANDLE_UNGROUND",
        "MID_TURN_NOTE_URL_ONLY_UNGROUND",
        "MID_TURN_NOTE_INVALID_SYNTAX",
        "MID_TURN_NOTE_ARTIFACT_UNKNOWN",
        "MID_TURN_NOTE_MARKDOWN_EMBED",
        "MID_TURN_NOTE_IMAGE_UNOBSERVED",
        "MID_TURN_NOTE_EVIDENCE_UNREADABLE",
    }
)


@dataclass(frozen=True)
class Defect:
    kind: str  # false_accept | false_reject | recording_defect | wording_defect | surface_policy_defect
    detail: str
    citation_id: str = ""
    handle: str = ""

    @property
    def priority(self) -> int:
        return DEFECT_PRIORITY.get(self.kind, 99)


@dataclass(frozen=True)
class CheckResult:
    passed: bool
    status: str  # "pass" | "fail" | "unknown"
    defects: tuple[Defect, ...] = ()
    reference_verdicts: tuple[ReferenceVerdict, ...] = ()
    friction_delta: FrictionCounters | None = None
    missing_instrumentation: str = ""


def _compare_citation_verdict(
    citation: Citation,
    ref_verdict: ReferenceVerdict,
    prod_verdict: ProductVerdict,
) -> Defect | None:
    """Compare one product verdict against the independent reference verdict (D4)."""
    # Priority 1: False accept
    if prod_verdict.is_grounded:
        if ref_verdict.verdict_class == "ungrounded":
            return Defect(
                kind="false_accept",
                citation_id=prod_verdict.citation_id,
                handle=prod_verdict.resolved_handle,
                detail=(
                    f"Product grounded citation ({prod_verdict.verdict}) where reference "
                    f"resolver says ungrounded ({ref_verdict.reason})"
                ),
            )
        if ref_verdict.verdict_class == "ambiguous" and not citation.names_handle:
            return Defect(
                kind="false_accept",
                citation_id=prod_verdict.citation_id,
                handle=prod_verdict.resolved_handle,
                detail=(
                    f"Product grounded non-handle citation ({prod_verdict.verdict}) where "
                    f"reference resolver found multiple deliveries ({ref_verdict.supporting_handles})"
                ),
            )
        return None

    # Priority 2: False reject
    if ref_verdict.verdict_class == "grounded":
        return Defect(
            kind="false_reject",
            citation_id=prod_verdict.citation_id,
            handle=prod_verdict.resolved_handle,
            detail=(
                f"Product refused citation ({prod_verdict.verdict}) where reference "
                f"resolver found supporting delivery {ref_verdict.supporting_handles} ({ref_verdict.reason})"
            ),
        )

    # Healthy ambiguous: reference says ambiguous (without handle) and product says ambiguous
    if ref_verdict.verdict_class == "ambiguous" and not citation.names_handle:
        if prod_verdict.verdict == "ambiguous":
            return None
        return Defect(
            kind="false_reject",
            citation_id=prod_verdict.citation_id,
            detail=(
                f"Expected ambiguous verdict for multi-delivery citation, got {prod_verdict.verdict}"
            ),
        )

    # Both ungrounded -> true reject
    return None


def compare_records(
    delivery_log: DeliveryLog,
    product_records: Sequence[ProductRecord],
) -> list[Defect]:
    """Compare product-minted records against the independent DeliveryLog (D4 step 3)."""
    defects: list[Defect] = []
    if not delivery_log.readable:
        return defects

    # Index expected model-visible records by (session_id, handle)
    expected_by_key: dict[tuple[str, str], DeliveryRecord] = {}
    for rec in delivery_log.records:
        if rec.model_visible:
            expected_by_key[(rec.session_id, rec.handle)] = rec

    seen_keys: set[tuple[str, str]] = set()
    for prod in product_records:
        key = (prod.session_id, prod.handle)
        seen_keys.add(key)
        expected = expected_by_key.get(key)
        if expected is None:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Product minted record {prod.handle!r} not present in independent delivery log",
                )
            )
            continue

        if prod.kind != expected.kind:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} kind mismatch: product={prod.kind!r}, log={expected.kind!r}",
                )
            )
        if prod.delivered_ranges != expected.delivered_ranges:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=(
                        f"Record {prod.handle!r} range mismatch: "
                        f"product={prod.delivered_ranges!r}, log={expected.delivered_ranges!r}"
                    ),
                )
            )
        prod_digest = prod.effective_body_sha256()
        if prod_digest and prod_digest != expected.body_sha256:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} body digest mismatch",
                )
            )
        if prod.worker_leg != expected.worker_leg:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=(
                        f"Record {prod.handle!r} worker_leg mismatch: "
                        f"product={prod.worker_leg!r}, log={expected.worker_leg!r}"
                    ),
                )
            )
        if prod.source_revision != expected.source_revision:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=(
                        f"Record {prod.handle!r} source_revision mismatch: "
                        f"product={prod.source_revision!r}, log={expected.source_revision!r}"
                    ),
                )
            )

    for key, expected in expected_by_key.items():
        if key not in seen_keys:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=expected.handle,
                    detail=f"Delivery log record {expected.handle!r} missing from product records",
                )
            )

    return defects


def check_slot(
    delivery_log: DeliveryLog,
    citations: Sequence[Citation],
    scope: PermittedScope,
    outcome: SurfaceOutcome,
    *,
    require_friction: bool = False,
    check_records: bool = True,
) -> CheckResult:
    """Evaluate one slot outcome strictly in D4 priority order."""
    # D10: Missing instrumentation yields an unknown verdict when required
    friction_delta: FrictionCounters | None = None
    if outcome.friction_before is not None and outcome.friction_after is not None:
        friction_delta = outcome.friction_after.delta(outcome.friction_before)
    elif require_friction:
        return CheckResult(
            passed=False,
            status="unknown",
            missing_instrumentation="Missing D10 friction counters (friction_before / friction_after)",
        )

    ref_verdicts: list[ReferenceVerdict] = [
        resolve_citation(delivery_log, c, scope) for c in citations
    ]
    defects: list[Defect] = []

    if len(citations) != len(outcome.verdicts):
        defects.append(
            Defect(
                kind="surface_policy_defect",
                detail=(
                    f"Citation count mismatch: {len(citations)} citations vs "
                    f"{len(outcome.verdicts)} product verdicts"
                ),
            )
        )
    else:
        for cit, ref_v, prod_v in zip(citations, ref_verdicts, outcome.verdicts):
            defect = _compare_citation_verdict(cit, ref_v, prod_v)
            if defect is not None:
                defects.append(defect)

    # Step 3: Recording defects (delivery log vs product minted records)
    if check_records:
        defects.extend(compare_records(delivery_log, outcome.records))

    # Surface-level healthy checks:
    # (a) Exempt speech in empty intent window (S01 / V07)
    if outcome.evidence_optional:
        citable = has_citable_observation(delivery_log, scope)
        if citable is None:
            defects.append(
                Defect(
                    kind="false_accept",
                    detail="Exempt speech granted while evidence ledger is unreadable (V07/S01)",
                )
            )
        elif citable is True:
            defects.append(
                Defect(
                    kind="false_accept",
                    detail="Exempt speech granted despite citable observations in intent window (S01)",
                )
            )

    # (b) Host-assembled closeout (S02): every attached handle must exist in DeliveryLog
    if outcome.host_assembled:
        logged_handles = {
            r.handle
            for r in delivery_log.records
            if r.session_id == scope.session_id and r.model_visible
        }
        for attached in outcome.attached_handles:
            if attached not in logged_handles:
                defects.append(
                    Defect(
                        kind="false_accept",
                        handle=attached,
                        detail=f"Host-assembled closeout attached handle {attached!r} not in delivery log (S02)",
                    )
                )

    # (c) Withheld mid-turn note (S06): must carry a typed MID_TURN_NOTE_* code
    if outcome.withheld and outcome.surface == "mid_turn_note":
        if outcome.code not in VALID_MID_TURN_NOTE_CODES:
            defects.append(
                Defect(
                    kind="surface_policy_defect",
                    detail=f"Withheld mid-turn note missing valid typed code, got {outcome.code!r}",
                )
            )

    defects.sort(key=lambda d: d.priority)
    return CheckResult(
        passed=not defects,
        status="pass" if not defects else "fail",
        defects=tuple(defects),
        reference_verdicts=tuple(ref_verdicts),
        friction_delta=friction_delta,
    )


def _outcome_signature(outcome: SurfaceOutcome) -> tuple[object, ...]:
    """Canonical D5 comparison signature across wording variants."""
    return (
        tuple((v.citation_id, v.is_grounded, v.verdict) for v in outcome.verdicts),
        outcome.code,
        outcome.traced,
        outcome.evidence_optional,
        outcome.host_assembled,
        outcome.withheld,
    )


def check_wording_triad(
    delivery_log: DeliveryLog,
    citations: Sequence[Citation],
    scope: PermittedScope,
    outcomes: Sequence[SurfaceOutcome],
    *,
    require_friction: bool = False,
    check_records: bool = True,
) -> CheckResult:
    """Validate all 3 D5 wording variants (confident, hedged, completion_asserting)."""
    wordings_seen = tuple(o.wording for o in outcomes)
    if set(wordings_seen) != set(WORDINGS) or len(outcomes) != 3:
        return CheckResult(
            passed=False,
            status="fail",
            defects=(
                Defect(
                    kind="wording_defect",
                    detail=f"Expected all 3 D5 wordings {WORDINGS}, got {wordings_seen}",
                ),
            ),
        )

    all_defects: list[Defect] = []
    first_refs: tuple[ReferenceVerdict, ...] = ()
    first_friction: FrictionCounters | None = None

    for idx, outcome in enumerate(outcomes):
        res = check_slot(
            delivery_log,
            citations,
            scope,
            outcome,
            require_friction=require_friction,
            check_records=check_records,
        )
        if res.status == "unknown":
            return res
        if idx == 0:
            first_refs = res.reference_verdicts
            first_friction = res.friction_delta
        all_defects.extend(res.defects)

    # D4 Step 4: Compare outcomes across the 3 wordings (D5)
    signatures = {o.wording: _outcome_signature(o) for o in outcomes}
    baseline_sig = signatures["confident"]
    for wording in ("hedged", "completion_asserting"):
        if signatures[wording] != baseline_sig:
            all_defects.append(
                Defect(
                    kind="wording_defect",
                    detail=(
                        f"Outcome differed between 'confident' and {wording!r}: "
                        f"confident={baseline_sig!r} vs {wording}={signatures[wording]!r}"
                    ),
                )
            )

    all_defects.sort(key=lambda d: d.priority)
    return CheckResult(
        passed=not all_defects,
        status="pass" if not all_defects else "fail",
        defects=tuple(all_defects),
        reference_verdicts=first_refs,
        friction_delta=first_friction,
    )
