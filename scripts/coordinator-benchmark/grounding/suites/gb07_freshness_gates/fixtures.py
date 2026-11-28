"""Deterministic fixtures, D5 wordings, and typed expected outcomes for GB07."""
from __future__ import annotations

from dataclasses import dataclass

from grounding.delivery_log import (
    DeliveryLog,
    DeliveryRecord,
    LineRange,
    ProductRecord,
    ProductVerdict,
    SurfaceOutcome,
)
from grounding.reference_resolver import Citation, PermittedScope, resolve_citation
from grounding.selectors import WORDING_VARIANTS

from .catalog import ALL_GB07_SLOTS, SLOTS_BY_SELECTOR, GB07SlotSpec


_WORDING_PREFIX: dict[str, str] = {
    "confident": "Verified:",
    "hedged": "Appears verified:",
    "completion_asserting": "Complete and verified:",
}


@dataclass(frozen=True)
class GB07SlotFixture:
    spec: GB07SlotSpec
    wording: str
    rendered_text: str
    delivery_log: DeliveryLog
    scope: PermittedScope
    citations: tuple[Citation, ...]
    expected_code: str = ""
    expected_breaker_tripped: bool = False

    def build_healthy_outcome(self) -> SurfaceOutcome:
        verdicts: list[ProductVerdict] = []
        for cit in self.citations:
            ref = resolve_citation(self.delivery_log, cit, self.scope)
            verdicts.append(
                ProductVerdict(
                    citation_id=cit.citation_id,
                    verdict=ref.expected_exact_verdict,
                    resolved_handle=ref.supporting_handles[0] if ref.supporting_handles else "",
                    reanchored_line=ref.reanchored_line,
                )
            )
        records = tuple(
            ProductRecord(
                handle=r.handle,
                kind=r.kind,
                session_id=r.session_id,
                worker_leg=r.worker_leg,
                source_revision=r.source_revision,
                delivered_ranges=r.delivered_ranges,
                raw_bytes=r.raw_bytes,
                path=r.path,
                url=r.url,
                artifact_id=r.artifact_id,
                round_id=r.round_id,
                shape=r.shape,
                trust=r.trust,
                superseded_by=r.superseded_by,
            )
            for r in self.delivery_log.records
            if r.model_visible
        )
        return SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=tuple(verdicts),
            records=records,
        )


def _rec_read(handle: str = "read#1", path: str = "internal/gate.go") -> DeliveryRecord:
    body = "package gate\n\nfunc Verify() bool { return true }\n"
    return DeliveryRecord(
        tool="read_file",
        arguments={"path": path},
        raw_bytes=body.encode("utf-8"),
        delivered_ranges=(LineRange(1, 3),),
        delivery_tags=(f"[{handle} {path}:1-3]",),
        line_numbered=True,
        printed_other_numbers=False,
        session_id="sess-gb07",
        worker_leg="",
        round_id="round-1",
        intent_window="intent-1",
        source_revision="rev-1",
        handle=handle,
        kind="read",
        path=path,
        url="",
        artifact_id="",
        call_id=f"call-{handle}",
        fixture_provenance="host",
        shape="file_region",
        trust="trusted",
    )


def build_gb07_fixture(selector: str, wording: str = "confident") -> GB07SlotFixture:
    if selector not in SLOTS_BY_SELECTOR:
        raise KeyError(f"Unknown GB07 selector: {selector!r}")
    spec = SLOTS_BY_SELECTOR[selector]
    scope = PermittedScope(session_id="sess-gb07", round_id="round-1", intent_window="intent-1")
    r1 = _rec_read()
    prefix = _WORDING_PREFIX[wording]
    tripped = any(tok in selector for tok in ("trips-at-", "stays-tripped", "escalates", "escalation"))
    return GB07SlotFixture(
        spec=spec,
        wording=wording,
        rendered_text=f"{prefix} Gate check [read#1:3].",
        delivery_log=DeliveryLog(records=(r1,)),
        scope=scope,
        citations=(Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id=f"{selector}-c1"),),
        expected_code=spec.expected_code,
        expected_breaker_tripped=tripped,
    )


def build_all_gb07_fixtures() -> dict[tuple[str, str], GB07SlotFixture]:
    out: dict[tuple[str, str], GB07SlotFixture] = {}
    for spec in ALL_GB07_SLOTS:
        for wording in WORDING_VARIANTS:
            out[(spec.selector, wording)] = build_gb07_fixture(spec.selector, wording)
    return out
