"""Deterministic fixture projects, D5 wording variants, and typed expected outcomes for GB05."""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any

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

from .catalog import ALL_GB05_SLOTS, SLOTS_BY_SELECTOR, GB05SlotSpec


_WORDING_PREFIX: dict[str, str] = {
    "confident": "Confirmed:",
    "hedged": "It appears likely that",
    "completion_asserting": "All checks are complete and verified:",
}


def render_wording(base_claim: str, wording: str) -> str:
    """Render a deterministic D5 wording variant without altering citation semantics."""
    if wording not in _WORDING_PREFIX:
        raise ValueError(f"Unsupported D5 wording {wording!r}; expected one of {WORDING_VARIANTS}")
    return f"{_WORDING_PREFIX[wording]} {base_claim}"


@dataclass(frozen=True)
class GB05SlotFixture:
    spec: GB05SlotSpec
    wording: str
    rendered_text: str
    delivery_log: DeliveryLog
    scope: PermittedScope
    citations: tuple[Citation, ...]
    expected_code: str = ""
    expected_evidence_optional: bool = False
    expected_host_assembled: bool = False
    expected_friction_charged: bool = False
    expected_tools_executed: bool = True
    workspace_files: dict[str, str] | None = None

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
            surface="coordinator_closeout" if self.spec.family_id != "S06" else "mid_turn_note",
            verdicts=tuple(verdicts),
            records=records,
            evidence_optional=self.expected_evidence_optional,
            host_assembled=self.expected_host_assembled,
        )


def _sample_record(
    *,
    handle: str = "read#1",
    kind: str = "read",
    path: str = "internal/service.go",
    session_id: str = "sess-gb05",
    worker_leg: str = "",
    intent_window: str = "intent-1",
) -> DeliveryRecord:
    body = "package service\n\nfunc Run() bool { return true }\n"
    return DeliveryRecord(
        tool="read_file",
        arguments={"path": path},
        raw_bytes=body.encode("utf-8"),
        delivered_ranges=(LineRange(1, 3),),
        delivery_tags=(f"[{handle} {path}:1-3]",),
        line_numbered=True,
        printed_other_numbers=False,
        session_id=session_id,
        worker_leg=worker_leg,
        round_id="round-1",
        intent_window=intent_window,
        source_revision="rev-1",
        handle=handle,
        kind=kind,
        path=path,
        url="",
        artifact_id="",
        call_id=f"call-{session_id}-{worker_leg or 'root'}-{handle}",
        fixture_provenance="host",
        shape="file_region",
        trust="trusted",
    )


def build_gb05_fixture(selector: str, wording: str = "confident") -> GB05SlotFixture:
    if selector not in SLOTS_BY_SELECTOR:
        raise KeyError(f"Unknown GB05 selector: {selector!r}")
    spec = SLOTS_BY_SELECTOR[selector]
    scope = PermittedScope(session_id="sess-gb05", round_id="round-1", intent_window="intent-1")
    rec_read = _sample_record()
    ws = {"internal/service.go": "package service\n\nfunc Run() bool { return true }\n"}

    if selector == "S01-01-exempt-no-citable-observations":
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording("No files were inspected.", wording),
            delivery_log=DeliveryLog(records=()),
            scope=scope,
            citations=(),
            expected_evidence_optional=True,
            workspace_files=ws,
        )
    if selector == "S01-05-unreadable-ledger-denies-exemption":
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording("Ledger unreadable [read#1:3].", wording),
            delivery_log=DeliveryLog(records=(rec_read,), readable=False),
            scope=scope,
            citations=(Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id="s01-05"),),
            expected_evidence_optional=False,
            workspace_files=ws,
        )
    if selector == "S02-02-empty-ledger-attaches-nothing":
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording("Fallback with empty ledger.", wording),
            delivery_log=DeliveryLog(records=()),
            scope=scope,
            citations=(),
            expected_host_assembled=True,
            workspace_files=ws,
        )
    if selector in {
        "S06-02-midturn-unresolved-handle-withheld-charges-friction",
        "S06-03-midturn-unresolved-url-withheld-charges-friction",
    }:
        cit = (
            Citation("handle", "[read#99:1]", handle="read#99", start_line=1, citation_id="s06-bad-h")
            if "handle" in selector
            else Citation("url", "https://unobserved.example.test/x", url="https://unobserved.example.test/x", citation_id="s06-bad-u")
        )
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording(f"Checking {cit.raw}.", wording),
            delivery_log=DeliveryLog(records=(rec_read,)),
            scope=scope,
            citations=(cit,),
            expected_friction_charged=True,
            expected_tools_executed=True,
            workspace_files=ws,
        )
    if selector == "S06-13-historical-unknown-intent-boundary":
        pre_batch_rec = _sample_record(handle="read#1", intent_window="intent-0")
        post_batch_rec = _sample_record(handle="read#2", intent_window="intent-1")
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording("Intent boundary check [read#2:3].", wording),
            delivery_log=DeliveryLog(records=(pre_batch_rec, post_batch_rec)),
            scope=scope,
            citations=(
                Citation("handle", "[read#2:3]", handle="read#2", start_line=3, citation_id="s06-13-post"),
            ),
            expected_friction_charged=False,
            expected_tools_executed=True,
            workspace_files=ws,
        )
    if selector == "S07-02-closeout-cites-completed-worker-leg":
        leg_rec = _sample_record(handle="read#1", worker_leg="leg-a")
        leg_scope = PermittedScope(
            session_id="sess-gb05",
            round_id="round-1",
            intent_window="intent-1",
            permitted_legs=("leg-a",),
        )
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording("Worker verified [leg-a:read#1:3].", wording),
            delivery_log=DeliveryLog(records=(leg_rec,)),
            scope=leg_scope,
            citations=(
                Citation("handle", "[leg-a:read#1:3]", handle="read#1", worker_leg="leg-a", start_line=3, citation_id="s07-02"),
            ),
            workspace_files=ws,
        )
    if selector in {
        "S07-03-closeout-refuses-cross-chat-handle",
        "S07-04-closeout-refuses-unobserved-working-tree",
    }:
        cit = (
            Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id="s07-bad-chat")
            if "cross-chat" in selector
            else Citation("path_coord", "[internal/unread.go:1]", path="internal/unread.go", start_line=1, citation_id="s07-unread")
        )
        log = (
            DeliveryLog(records=(_sample_record(session_id="sess-other"),))
            if "cross-chat" in selector
            else DeliveryLog(records=(rec_read,))
        )
        return GB05SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=render_wording(f"Claiming {cit.raw}.", wording),
            delivery_log=log,
            scope=scope,
            citations=(cit,),
            workspace_files=ws,
        )

    # Default grounded/typed slot fixture across all 3 D5 wordings
    return GB05SlotFixture(
        spec=spec,
        wording=wording,
        rendered_text=render_wording("Verified Run() at [read#1:3].", wording),
        delivery_log=DeliveryLog(records=(rec_read,)),
        scope=scope,
        citations=(Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id=f"{selector}-c1"),),
        expected_code=spec.expected_code,
        expected_host_assembled=(spec.family_id == "S02" and "host-attached" in selector),
        workspace_files=ws,
    )


def build_all_gb05_fixtures() -> dict[tuple[str, str], GB05SlotFixture]:
    """Build all 51 base slots x 3 D5 wordings = 153 deterministic GB05 slot fixtures."""
    out: dict[tuple[str, str], GB05SlotFixture] = {}
    for spec in ALL_GB05_SLOTS:
        for wording in WORDING_VARIANTS:
            out[(spec.selector, wording)] = build_gb05_fixture(spec.selector, wording)
    return out
