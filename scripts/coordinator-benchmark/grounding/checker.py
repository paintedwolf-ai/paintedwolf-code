"""Independent grounding checker implementing D4 comparison order and D10 friction.

Comparison order (D4):
1. false_accept: product grounds a reference that no delivery in scope supports,
   grounds an ambiguous non-handle citation, or misbinds support/re-anchoring.
2. false_reject: product refuses a reference that exactly one delivery in scope supports.
3. recording_defect: product's minted records differ from the delivery log in
   kind, shape, path/url/artifact_id, delivered_ranges, body/digest, worker_leg,
   source_revision, or trust, or are missing/extra/duplicated.
4. wording_defect: complete contracted outcome (including host-attached handles)
   differs across the 3 D5 wordings (confident, hedged, completion_asserting).
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping, Sequence

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
    permitted_union_handles,
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
    *,
    assert_exact_verdicts: bool = False,
) -> tuple[Defect | None, str]:
    """Compare one product verdict against the independent reference verdict (D4, GB02-R1, D009).

    Returns `(defect_or_none, missing_readback_note)`.
    Decision 009: When a grounded verdict has an expected support handle or
    re-anchored line and the product adapter omits that readback, never silently
    accept it — surface an explicit missing-readback note (`unknown`).
    """
    missing_readbacks: list[str] = []

    # Priority 1: False accept (including misbound support or re-anchoring)
    if prod_verdict.is_grounded:
        if ref_verdict.verdict_class == "ungrounded":
            return (
                Defect(
                    kind="false_accept",
                    citation_id=prod_verdict.citation_id,
                    handle=prod_verdict.resolved_handle,
                    detail=(
                        f"Product grounded citation ({prod_verdict.verdict}) where reference "
                        f"resolver says ungrounded ({ref_verdict.reason})"
                    ),
                ),
                "",
            )
        if ref_verdict.verdict_class == "ambiguous" and not citation.names_handle:
            return (
                Defect(
                    kind="false_accept",
                    citation_id=prod_verdict.citation_id,
                    handle=prod_verdict.resolved_handle,
                    detail=(
                        f"Product grounded non-handle citation ({prod_verdict.verdict}) where "
                        f"reference resolver found multiple deliveries ({ref_verdict.supporting_handles})"
                    ),
                ),
                "",
            )
        # GB02-R1 & Decision 009: Verify returned support handle against reference observations;
        # never silently accept a missing support handle when an expected handle exists.
        if ref_verdict.supporting_handles:
            if not prod_verdict.resolved_handle:
                missing_readbacks.append(
                    f"occurrence {prod_verdict.citation_id!r} missing resolved_handle readback "
                    f"(expected one of {sorted(ref_verdict.supporting_handles)})"
                )
            else:
                allowed = set(ref_verdict.supporting_handles)
                if prod_verdict.resolved_handle not in allowed:
                    return (
                        Defect(
                            kind="false_accept",
                            citation_id=prod_verdict.citation_id,
                            handle=prod_verdict.resolved_handle,
                            detail=(
                                f"Misbound resolved_handle {prod_verdict.resolved_handle!r}; "
                                f"expected one of {sorted(allowed)}"
                            ),
                        ),
                        "",
                    )
        # GB02-R1 & Decision 009: Verify re-anchored line against reference observation;
        # never silently accept a missing re-anchored coordinate readback.
        if ref_verdict.reanchored_line is not None:
            if prod_verdict.reanchored_line is None:
                missing_readbacks.append(
                    f"occurrence {prod_verdict.citation_id!r} missing reanchored_line readback "
                    f"(expected line {ref_verdict.reanchored_line})"
                )
            elif prod_verdict.reanchored_line != ref_verdict.reanchored_line:
                return (
                    Defect(
                        kind="false_accept",
                        citation_id=prod_verdict.citation_id,
                        handle=prod_verdict.resolved_handle,
                        detail=(
                            f"Misbound reanchored_line {prod_verdict.reanchored_line}; "
                            f"expected {ref_verdict.reanchored_line}"
                        ),
                    ),
                    "",
                )
        # R02/R03 exact typed verdict assertion (D4)
        if assert_exact_verdicts and prod_verdict.verdict != ref_verdict.expected_exact_verdict:
            return (
                Defect(
                    kind="surface_policy_defect",
                    citation_id=prod_verdict.citation_id,
                    handle=prod_verdict.resolved_handle,
                    detail=(
                        f"Exact typed verdict mismatch: product={prod_verdict.verdict!r}, "
                        f"expected={ref_verdict.expected_exact_verdict!r}"
                    ),
                ),
                "; ".join(missing_readbacks),
            )
        return (None, "; ".join(missing_readbacks))

    # Priority 2: False reject
    if ref_verdict.verdict_class == "grounded":
        return (
            Defect(
                kind="false_reject",
                citation_id=prod_verdict.citation_id,
                handle=prod_verdict.resolved_handle,
                detail=(
                    f"Product refused citation ({prod_verdict.verdict}) where reference "
                    f"resolver found supporting delivery {ref_verdict.supporting_handles} ({ref_verdict.reason})"
                ),
            ),
            "",
        )

    # Healthy ambiguous: reference says ambiguous (without handle) and product says ambiguous
    if ref_verdict.verdict_class == "ambiguous" and not citation.names_handle:
        if prod_verdict.verdict == "ambiguous":
            return (None, "")
        return (
            Defect(
                kind="false_reject",
                citation_id=prod_verdict.citation_id,
                detail=(
                    f"Expected ambiguous verdict for multi-delivery citation, got {prod_verdict.verdict}"
                ),
            ),
            "",
        )

    # R02/R03 exact typed verdict assertion on ungrounded class
    if assert_exact_verdicts and prod_verdict.verdict != ref_verdict.expected_exact_verdict:
        return (
            Defect(
                kind="surface_policy_defect",
                citation_id=prod_verdict.citation_id,
                detail=(
                    f"Exact typed verdict mismatch: product={prod_verdict.verdict!r}, "
                    f"expected={ref_verdict.expected_exact_verdict!r}"
                ),
            ),
            "",
        )

    return (None, "")


def compare_records(
    delivery_log: DeliveryLog,
    product_records: Sequence[ProductRecord],
) -> list[Defect]:
    """Compare product-minted records against the independent DeliveryLog (D4 step 3, GB02-R2)."""
    defects: list[Defect] = []
    if not delivery_log.readable:
        return defects

    # Index expected model-visible records by full typed identity (session_id, worker_leg, handle)
    expected_by_key: dict[tuple[str, str, str], DeliveryRecord] = {}
    expected_by_session_handle: dict[tuple[str, str], list[DeliveryRecord]] = {}
    for rec in delivery_log.records:
        if rec.model_visible:
            expected_by_key[rec.identity_key] = rec
            expected_by_session_handle.setdefault((rec.session_id, rec.handle), []).append(rec)

    seen_keys: set[tuple[str, str, str]] = set()
    matched_expected_keys: set[tuple[str, str, str]] = set()

    for prod in product_records:
        key = prod.identity_key
        # GB02-R2: Reject duplicate product record identities
        if key in seen_keys:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Duplicate product record identity {key!r}",
                )
            )
            continue
        seen_keys.add(key)

        expected = expected_by_key.get(key)
        if expected is None:
            # Check if the same (session_id, handle) exists under a different worker_leg
            same_sh = expected_by_session_handle.get((prod.session_id, prod.handle), [])
            if len(same_sh) == 1:
                exp_leg = same_sh[0]
                matched_expected_keys.add(exp_leg.identity_key)
                defects.append(
                    Defect(
                        kind="recording_defect",
                        handle=prod.handle,
                        detail=(
                            f"Record {prod.handle!r} worker_leg mismatch: "
                            f"product={prod.worker_leg!r}, log={exp_leg.worker_leg!r}"
                        ),
                    )
                )
                continue
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Product minted record {prod.handle!r} not present in independent delivery log",
                )
            )
            continue

        matched_expected_keys.add(key)

        if prod.kind != expected.kind:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} kind mismatch: product={prod.kind!r}, log={expected.kind!r}",
                )
            )
        if expected.shape and prod.shape != expected.shape:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} shape mismatch: product={prod.shape!r}, log={expected.shape!r}",
                )
            )
        if prod.path != expected.path:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} path mismatch: product={prod.path!r}, log={expected.path!r}",
                )
            )
        if prod.url != expected.url:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} url mismatch: product={prod.url!r}, log={expected.url!r}",
                )
            )
        if prod.artifact_id != expected.artifact_id:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=(
                        f"Record {prod.handle!r} artifact_id mismatch: "
                        f"product={prod.artifact_id!r}, log={expected.artifact_id!r}"
                    ),
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
        # GB02-R2: Distinguish empty body from missing body evidence; never pass silently
        if not prod.has_body_evidence():
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=(
                        f"Record {prod.handle!r} missing body bytes and body_sha256 digest "
                        f"(expected {len(expected.raw_bytes)} bytes)"
                    ),
                )
            )
        elif prod.effective_body_sha256() != expected.body_sha256:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} body digest mismatch",
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
        if prod.trust != expected.trust:
            defects.append(
                Defect(
                    kind="recording_defect",
                    handle=prod.handle,
                    detail=f"Record {prod.handle!r} trust mismatch: product={prod.trust!r}, log={expected.trust!r}",
                )
            )

    for key, expected in expected_by_key.items():
        if key not in matched_expected_keys:
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
    assert_exact_verdicts: bool = False,
) -> CheckResult:
    """Evaluate one slot outcome strictly in D4 priority order.

    GB02-R1: Joins verdicts by stable occurrence identity (`citation_id`).
    GB02-R2: Preserves any known defect classification even when friction
    instrumentation is unavailable.
    """
    friction_delta: FrictionCounters | None = None
    missing_instr = ""
    if outcome.friction_before is not None and outcome.friction_after is not None:
        friction_delta = outcome.friction_after.delta(outcome.friction_before)
    elif require_friction:
        missing_instr = "Missing D10 friction counters (friction_before / friction_after)"

    ref_verdicts: list[ReferenceVerdict] = [
        resolve_citation(delivery_log, c, scope) for c in citations
    ]
    defects: list[Defect] = []

    # GB02-R1: Join citations and product verdicts by stable occurrence_id (citation_id)
    cit_by_id: dict[str, tuple[Citation, ReferenceVerdict]] = {}
    for idx, (cit, ref_v) in enumerate(zip(citations, ref_verdicts)):
        if not cit.citation_id:
            defects.append(
                Defect(
                    kind="surface_policy_defect",
                    detail=f"Citation at index {idx} is missing a stable citation_id",
                )
            )
            continue
        if cit.citation_id in cit_by_id:
            defects.append(
                Defect(
                    kind="surface_policy_defect",
                    citation_id=cit.citation_id,
                    detail=f"Duplicate citation_id {cit.citation_id!r} in slot citations",
                )
            )
            continue
        cit_by_id[cit.citation_id] = (cit, ref_v)

    prod_by_id: dict[str, ProductVerdict] = {}
    for idx, prod_v in enumerate(outcome.verdicts):
        if not prod_v.citation_id:
            defects.append(
                Defect(
                    kind="surface_policy_defect",
                    detail=f"Product verdict at index {idx} is missing a stable citation_id",
                )
            )
            continue
        if prod_v.citation_id in prod_by_id:
            defects.append(
                Defect(
                    kind="surface_policy_defect",
                    citation_id=prod_v.citation_id,
                    detail=f"Duplicate product verdict for occurrence {prod_v.citation_id!r}",
                )
            )
            continue
        prod_by_id[prod_v.citation_id] = prod_v

    missing_notes: list[str] = []
    if missing_instr:
        missing_notes.append(missing_instr)

    for cid, (cit, ref_v) in cit_by_id.items():
        prod_v = prod_by_id.get(cid)
        if prod_v is None:
            defects.append(
                Defect(
                    kind="false_reject" if ref_v.verdict_class == "grounded" else "surface_policy_defect",
                    citation_id=cid,
                    detail=f"Missing product verdict for occurrence {cid!r}",
                )
            )
            continue
        defect, readback_missing = _compare_citation_verdict(
            cit, ref_v, prod_v, assert_exact_verdicts=assert_exact_verdicts
        )
        if defect is not None:
            defects.append(defect)
        if readback_missing:
            missing_notes.append(readback_missing)

    missing_instr = "; ".join(missing_notes)

    for cid in prod_by_id:
        if cid not in cit_by_id:
            defects.append(
                Defect(
                    kind="surface_policy_defect",
                    citation_id=cid,
                    detail=f"Unexpected product verdict for unknown occurrence {cid!r}",
                )
            )

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

    # (b) Host-assembled closeout (S02 / GB02-R3): every attached handle must belong
    # to the viewer's true scoped union (own session + dispatched child legs).
    if outcome.host_assembled:
        union_handles = permitted_union_handles(delivery_log, scope)
        for attached in outcome.attached_handles:
            if attached not in union_handles:
                defects.append(
                    Defect(
                        kind="false_accept",
                        handle=attached,
                        detail=(
                            f"Host-assembled closeout attached handle {attached!r} "
                            "outside viewer's permitted delivery union (S02/GB02-R3)"
                        ),
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
    # GB02-R2: Keep known defect classification even if friction measurement is unavailable
    if defects:
        return CheckResult(
            passed=False,
            status="fail",
            defects=tuple(defects),
            reference_verdicts=tuple(ref_verdicts),
            friction_delta=friction_delta,
            missing_instrumentation=missing_instr,
        )
    if missing_instr:
        return CheckResult(
            passed=False,
            status="unknown",
            defects=(),
            reference_verdicts=tuple(ref_verdicts),
            friction_delta=None,
            missing_instrumentation=missing_instr,
        )
    return CheckResult(
        passed=True,
        status="pass",
        defects=(),
        reference_verdicts=tuple(ref_verdicts),
        friction_delta=friction_delta,
    )


def _outcome_signature(outcome: SurfaceOutcome) -> tuple[object, ...]:
    """Canonical D5 comparison signature across wording variants (GB02-R5).

    Includes support identities (`resolved_handle`, `reanchored_line`) and
    host-attached references (`attached_handles`).
    """
    sorted_verdicts = tuple(
        sorted(
            (
                v.citation_id,
                v.is_grounded,
                v.verdict,
                v.resolved_handle,
                v.reanchored_line,
            )
            for v in outcome.verdicts
        )
    )
    return (
        sorted_verdicts,
        outcome.code,
        outcome.traced,
        outcome.evidence_optional,
        outcome.host_assembled,
        outcome.withheld,
        outcome.attached_handles,
    )


def check_wording_triad(
    delivery_log: DeliveryLog | Mapping[str, DeliveryLog],
    citations: Sequence[Citation],
    scope: PermittedScope,
    outcomes: Sequence[SurfaceOutcome],
    *,
    require_friction: bool = False,
    check_records: bool = True,
    assert_exact_verdicts: bool = False,
) -> CheckResult:
    """Validate all 3 D5 wording variants (confident, hedged, completion_asserting).

    GB02-R5:
    - Compares the complete contracted outcome including attached references.
    - Accepts either a single DeliveryLog or a per-wording mapping of actual
      DeliveryLogs (and verifies normalized delivery equivalence across wordings).
    - Never discards established defects when a later wording has unknown instrumentation.
    """
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

    per_wording_logs: dict[str, DeliveryLog]
    if isinstance(delivery_log, Mapping):
        if set(delivery_log.keys()) != set(WORDINGS):
            return CheckResult(
                passed=False,
                status="fail",
                defects=(
                    Defect(
                        kind="wording_defect",
                        detail=f"Per-wording delivery_logs must cover {WORDINGS}, got {sorted(delivery_log.keys())}",
                    ),
                ),
            )
        per_wording_logs = {k: delivery_log[k] for k in WORDINGS}
    else:
        per_wording_logs = {k: delivery_log for k in WORDINGS}

    all_defects: list[Defect] = []
    missing_notes: list[str] = []
    first_refs: tuple[ReferenceVerdict, ...] = ()
    first_friction: FrictionCounters | None = None

    # Verify normalized deliveries are equivalent across the 3 D5 wordings (Decision 002)
    base_fp = per_wording_logs["confident"].normalized_fingerprint()
    for wording in ("hedged", "completion_asserting"):
        if per_wording_logs[wording].normalized_fingerprint() != base_fp:
            all_defects.append(
                Defect(
                    kind="recording_defect",
                    detail=(
                        f"Delivery log for wording {wording!r} is not equivalent to "
                        "'confident' baseline delivery log"
                    ),
                )
            )

    for idx, outcome in enumerate(outcomes):
        res = check_slot(
            per_wording_logs[outcome.wording],
            citations,
            scope,
            outcome,
            require_friction=require_friction,
            check_records=check_records,
            assert_exact_verdicts=assert_exact_verdicts,
        )
        if idx == 0:
            first_refs = res.reference_verdicts
            first_friction = res.friction_delta
        all_defects.extend(res.defects)
        if res.missing_instrumentation:
            missing_notes.append(f"{outcome.wording}: {res.missing_instrumentation}")

    # D4 Step 4: Compare complete contracted outcomes across the 3 wordings (D5 / GB02-R5)
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
    missing_summary = "; ".join(missing_notes)
    if all_defects:
        return CheckResult(
            passed=False,
            status="fail",
            defects=tuple(all_defects),
            reference_verdicts=first_refs,
            friction_delta=first_friction,
            missing_instrumentation=missing_summary,
        )
    if missing_summary:
        return CheckResult(
            passed=False,
            status="unknown",
            defects=(),
            reference_verdicts=first_refs,
            friction_delta=None,
            missing_instrumentation=missing_summary,
        )
    return CheckResult(
        passed=True,
        status="pass",
        defects=(),
        reference_verdicts=first_refs,
        friction_delta=first_friction,
    )
