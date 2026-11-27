"""Deterministic fixture projects, D5 wordings, and typed expected outcomes for GB06."""
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

from .catalog import ALL_GB06_SLOTS, SLOTS_BY_SELECTOR, GB06SlotSpec


_WORDING_PREFIX: dict[str, str] = {
    "confident": "Verified:",
    "hedged": "Evidence suggests:",
    "completion_asserting": "Work complete and verified:",
}


@dataclass(frozen=True)
class GB06SlotFixture:
    spec: GB06SlotSpec
    wording: str
    rendered_text: str
    delivery_log: DeliveryLog
    scope: PermittedScope
    citations: tuple[Citation, ...]
    expected_code: str = ""
    expected_stored: bool = True
    deprecated_compat_code: str = ""

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
            surface="worker_result" if self.spec.family_id in {"W03", "W04"} else "finding_or_verdict",
            verdicts=tuple(verdicts),
            records=records,
        )


def _rec_read(handle: str = "read#1", path: str = "internal/core.go") -> DeliveryRecord:
    body = "package core\n\nfunc Check() bool { return true }\n"
    return DeliveryRecord(
        tool="read_file",
        arguments={"path": path},
        raw_bytes=body.encode("utf-8"),
        delivered_ranges=(LineRange(1, 3),),
        delivery_tags=(f"[{handle} {path}:1-3]",),
        line_numbered=True,
        printed_other_numbers=False,
        session_id="sess-gb06",
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


def build_gb06_fixture(selector: str, wording: str = "confident") -> GB06SlotFixture:
    if selector not in SLOTS_BY_SELECTOR:
        raise KeyError(f"Unknown GB06 selector: {selector!r}")
    spec = SLOTS_BY_SELECTOR[selector]
    scope = PermittedScope(session_id="sess-gb06", round_id="round-1", intent_window="intent-1")
    r1 = _rec_read()
    prefix = _WORDING_PREFIX[wording]

    if selector == "W01-05-finding-unsupported-handle-never-stored":
        return GB06SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=f"{prefix} Unobserved finding [read#88:1].",
            delivery_log=DeliveryLog(records=(r1,)),
            scope=scope,
            citations=(Citation("handle", "[read#88:1]", handle="read#88", start_line=1, citation_id="w01-05"),),
            expected_stored=False,
        )
    if selector == "W01-06-finding-unsupported-excerpt-never-stored":
        return GB06SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=f'{prefix} Fabricated excerpt [read#1:3 "panic()"].',
            delivery_log=DeliveryLog(records=(r1,)),
            scope=scope,
            citations=(Citation("handle", '[read#1:3 "panic()"]', handle="read#1", start_line=3, excerpt="panic()", citation_id="w01-06"),),
            expected_stored=False,
        )
    if selector == "W03-04-implementer-missing-artifact-active-code":
        return GB06SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=f"{prefix} Implementer summary without artifact.",
            delivery_log=DeliveryLog(records=(r1,)),
            scope=scope,
            citations=(Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id="w03-04"),),
            expected_code="WORKER_SUMMARY_NO_ARTIFACT",
            expected_stored=False,
            deprecated_compat_code="WORKER_IMPLEMENT_NO_ARTIFACT",
        )
    if selector == "W04-06-artifact-provenance-host-vs-fixture":
        host_uuid = "22222222-2222-4222-8222-222222222222"
        spoof_uuid = "33333333-3333-4333-8333-333333333333"
        art_rec = DeliveryRecord(
            tool="capture_surface",
            arguments={},
            raw_bytes=f"fixture payload claiming {spoof_uuid}".encode("utf-8"),
            delivered_ranges=(),
            delivery_tags=(f"[artifact:{host_uuid}]",),
            line_numbered=False,
            printed_other_numbers=False,
            session_id="sess-gb06",
            worker_leg="",
            round_id="round-1",
            intent_window="intent-1",
            source_revision="rev-1",
            handle="surface#1",
            kind="surface",
            path="",
            url="",
            artifact_id=host_uuid,
            call_id="call-surface-1",
            fixture_provenance="host",
            shape="artifact_ref",
            trust="trusted",
        )
        return GB06SlotFixture(
            spec=spec,
            wording=wording,
            rendered_text=f"{prefix} Host artifact [artifact:{host_uuid}] vs spoofed [artifact:{spoof_uuid}].",
            delivery_log=DeliveryLog(records=(art_rec,)),
            scope=scope,
            citations=(
                Citation("artifact", f"[artifact:{host_uuid}]", artifact_id=host_uuid, citation_id="w04-06-host"),
                Citation("artifact", f"[artifact:{spoof_uuid}]", artifact_id=spoof_uuid, citation_id="w04-06-spoof"),
            ),
        )

    return GB06SlotFixture(
        spec=spec,
        wording=wording,
        rendered_text=f"{prefix} Grounded reference [read#1:3].",
        delivery_log=DeliveryLog(records=(r1,)),
        scope=scope,
        citations=(Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id=f"{selector}-c1"),),
        expected_code=spec.expected_code,
    )


def build_all_gb06_fixtures() -> dict[tuple[str, str], GB06SlotFixture]:
    out: dict[tuple[str, str], GB06SlotFixture] = {}
    for spec in ALL_GB06_SLOTS:
        for wording in WORDING_VARIANTS:
            out[(spec.selector, wording)] = build_gb06_fixture(spec.selector, wording)
    return out
