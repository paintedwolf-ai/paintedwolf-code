"""Deterministic synthetic fixture builders for GB04 (Citation resolution).

Imports read-only from `grounding.*` and constructs:
- Independent `DeliveryLog` observations, `PermittedScope` boundaries, and `Citation` inputs
  for all 34 fixed GB04 slots (R01=10, R02=8, R03=10, R05=6).
- Independent expected `ReferenceVerdict`s via `grounding.reference_resolver.resolve_all`,
  strictly separating syntax, scope, and observed support.
- R05 replacement-handle oracle (suggests a replacement ONLY when exactly 1 current-job
  record verifies the supplied path, line, and excerpt; never rewrites).
- Seeded R04, R06, and R07 fixture adapters with explicit COV-02 syntax-vs-observation
  cases, darwin/linux path-case contrasts, and re-anchoring/binding annotations.
"""
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
from grounding.generators import (
    GeneratedR04Case,
    GeneratedR06Case,
    GeneratedR07Case,
    R04Span,
    generate_path_case_contrast,
    generate_r04,
    generate_r06,
    generate_r07,
)
from grounding.reference_resolver import (
    Citation,
    PermittedScope,
    ReferenceVerdict,
    resolve_all,
    resolve_citation,
)

from .catalog import (
    ALL_GB04_FIXED_SLOTS,
    GB04SlotSpec,
    SLOT_BY_SELECTOR,
)


@dataclass(frozen=True)
class GB04FixedFixture:
    """Complete deterministic synthetic fixture for one fixed GB04 slot."""

    spec: GB04SlotSpec
    delivery_log: DeliveryLog
    scope: PermittedScope
    citations: tuple[Citation, ...]
    expected_verdicts: tuple[ReferenceVerdict, ...]
    working_tree_only_lines: tuple[str, ...] = ()
    expected_refusal_reason: str | None = None
    expected_suggested_replacement: str | None = None
    host_rewrites_citation: bool = False

    def build_healthy_product_outcome(self, *, surface: str = "closeout") -> SurfaceOutcome:
        """Build a matching healthy ProductOutcome for harness dry-run verification."""
        prod_records = tuple(
            ProductRecord(
                handle=rec.handle,
                kind=rec.kind,
                session_id=rec.session_id,
                worker_leg=rec.worker_leg,
                source_revision=rec.source_revision,
                delivered_ranges=rec.delivered_ranges,
                raw_bytes=rec.raw_bytes,
                path=rec.path,
                url=rec.url,
                artifact_id=rec.artifact_id,
                round_id=rec.round_id,
                shape=rec.shape,
                trust=rec.trust,
                superseded_by=rec.superseded_by,
            )
            for rec in self.delivery_log.records
            if rec.model_visible
        )
        prod_verdicts = tuple(
            ProductVerdict(
                citation_id=cit.citation_id,
                verdict=ref_v.expected_exact_verdict,
                resolved_handle=(
                    ref_v.supporting_handles[0]
                    if ref_v.verdict_class == "grounded" and ref_v.supporting_handles
                    else ""
                ),
                code=ref_v.reason if ref_v.verdict_class != "grounded" else "",
                reanchored_line=ref_v.reanchored_line,
            )
            for cit, ref_v in zip(self.citations, self.expected_verdicts)
        )
        return SurfaceOutcome(
            surface=surface,
            wording="confident",
            verdicts=prod_verdicts,
            records=prod_records,
        )


@dataclass(frozen=True)
class R04SyntaxObservationCase:
    """Explicit R04 case separating syntax classification, scope, and observed support."""

    case_id: str
    markdown: str
    spans: tuple[R04Span, ...]
    delivery_log: DeliveryLog
    scope: PermittedScope
    citation_under_test: Citation | None
    expected_syntax_attempt: bool
    expected_syntax_valid: bool
    expected_observed_support: bool | None
    notes: str


@dataclass(frozen=True)
class R06PreparedCase:
    """R06 generated fixture enriched with explicit platform expectations."""

    generated: GeneratedR06Case
    expected_verdict: ReferenceVerdict
    case_insensitive_paths: bool


@dataclass(frozen=True)
class R07PreparedCase:
    """R07 generated fixture enriched with explicit re-anchoring/binding distinction."""

    generated: GeneratedR07Case
    expected_verdict: ReferenceVerdict
    reanchor_possible: bool
    binding_possible: bool
    case_insensitive_paths: bool


# ---------------------------------------------------------------------------
# Record helpers
# ---------------------------------------------------------------------------


def _file_record(
    *,
    session_id: str,
    handle: str,
    path: str,
    raw_text: str,
    delivered_ranges: tuple[LineRange, ...],
    line_numbered: bool = True,
    printed_other_numbers: bool = False,
    worker_leg: str = "",
    round_id: str = "round-1",
    intent_window: str = "intent-1",
    source_revision: str = "rev-1",
    kind: str = "read",
    tool: str = "read_file",
    delivery_tags: tuple[str, ...] | None = None,
    superseded_by: str = "",
    model_visible: bool = True,
    call_id: str = "call-1",
) -> DeliveryRecord:
    if delivery_tags is None:
        if delivered_ranges:
            rng = delivered_ranges[0]
            delivery_tags = (f"[{handle} {path}:{rng.start}-{rng.end}]",)
        else:
            delivery_tags = (f"[{handle}]",)
    return DeliveryRecord(
        tool=tool,
        arguments={"path": path},
        raw_bytes=raw_text.encode("utf-8"),
        delivered_ranges=delivered_ranges,
        delivery_tags=delivery_tags,
        line_numbered=line_numbered,
        printed_other_numbers=printed_other_numbers,
        session_id=session_id,
        worker_leg=worker_leg,
        round_id=round_id,
        intent_window=intent_window,
        source_revision=source_revision,
        handle=handle,
        kind=kind,
        path=path,
        url="",
        artifact_id="",
        call_id=call_id,
        fixture_provenance="host",
        superseded_by=superseded_by,
        model_visible=model_visible,
    )


def _url_record(
    *,
    session_id: str,
    handle: str,
    url: str,
    kind: str = "web",
    tool: str = "web_fetch",
    raw_text: str = "HTTP 200 Observed documentation body",
    model_visible: bool = True,
    call_id: str = "call-web",
) -> DeliveryRecord:
    return DeliveryRecord(
        tool=tool,
        arguments={"url": url},
        raw_bytes=raw_text.encode("utf-8"),
        delivered_ranges=(),
        delivery_tags=(f"[{handle}]",),
        line_numbered=False,
        printed_other_numbers=False,
        session_id=session_id,
        worker_leg="",
        round_id="round-1",
        intent_window="intent-1",
        source_revision="rev-1",
        handle=handle,
        kind=kind,
        path="",
        url=url,
        artifact_id="",
        call_id=call_id,
        fixture_provenance="host",
        model_visible=model_visible,
    )


def _artifact_record(
    *,
    session_id: str,
    handle: str,
    artifact_id: str,
    raw_text: str = "PNG-synthetic-artifact",
    call_id: str = "call-artifact",
) -> DeliveryRecord:
    return DeliveryRecord(
        tool="capture_surface",
        arguments={"artifact_id": artifact_id},
        raw_bytes=raw_text.encode("utf-8"),
        delivered_ranges=(),
        delivery_tags=(f"[{handle}]",),
        line_numbered=False,
        printed_other_numbers=False,
        session_id=session_id,
        worker_leg="",
        round_id="round-1",
        intent_window="intent-1",
        source_revision="rev-1",
        handle=handle,
        kind="artifact",
        path="",
        url="",
        artifact_id=artifact_id,
        call_id=call_id,
        fixture_provenance="host",
    )


# ---------------------------------------------------------------------------
# R05 replacement-handle oracle
# ---------------------------------------------------------------------------


def suggest_replacement_handle(
    log: DeliveryLog,
    citation: Citation,
    scope: PermittedScope,
) -> str | None:
    """Return a replacement handle ONLY when exactly 1 current-job record verifies.

    Follows R05:
    - Evaluates the supplied path, line, and excerpt against every current-job
      non-superseded record in the viewer's scope (excluding the refused handle).
    - Returns the single verifying handle iff `len(verifying) == 1`.
    - Returns `None` when 0 or >1 records verify.
    """
    if not log.readable:
        return None
    if not citation.path and not citation.has_coordinate and not citation.excerpt:
        return None

    verifying: list[str] = []
    for rec in log.records:
        if not rec.model_visible or rec.is_superseded:
            continue
        if rec.session_id != scope.session_id or rec.worker_leg != scope.worker_leg:
            continue
        if scope.active_round_id and rec.round_id and rec.round_id != scope.active_round_id:
            continue
        if citation.handle and rec.handle == citation.handle:
            continue
        probe = Citation(
            citation_type="handle_path" if citation.path else "handle",
            raw=f"[{rec.handle}]",
            handle=rec.handle,
            worker_leg=rec.worker_leg,
            path=citation.path,
            start_line=citation.start_line,
            end_line=citation.end_line,
            excerpt=citation.excerpt,
            citation_id="probe",
        )
        verdict = resolve_citation(log, probe, scope)
        if verdict.verdict_class == "grounded":
            verifying.append(rec.handle)

    if len(verifying) == 1:
        return verifying[0]
    return None


# ---------------------------------------------------------------------------
# Fixed slot fixture builders (34 slots: R01=10, R02=8, R03=10, R05=6)
# ---------------------------------------------------------------------------


def _finalize_slot(
    selector: str,
    log: DeliveryLog,
    scope: PermittedScope,
    citations: tuple[Citation, ...],
    *,
    working_tree_only_lines: tuple[str, ...] = (),
) -> GB04FixedFixture:
    spec = SLOT_BY_SELECTOR[selector]
    verdicts = tuple(resolve_all(log, citations, scope))
    actual_exact = tuple(v.expected_exact_verdict for v in verdicts)
    if actual_exact != spec.expected_verdicts:
        raise AssertionError(
            f"Fixture {selector} verdicts {actual_exact!r} != manifest {spec.expected_verdicts!r}"
        )

    refusal_reason: str | None = None
    suggested: str | None = None
    if spec.family_id == "R05":
        refusal_reason = verdicts[0].reason
        suggested = suggest_replacement_handle(log, citations[0], scope)
        if refusal_reason != spec.expected_refusal_reason:
            raise AssertionError(
                f"R05 fixture {selector} reason {refusal_reason!r} != {spec.expected_refusal_reason!r}"
            )
        if suggested != spec.expected_suggested_replacement:
            raise AssertionError(
                f"R05 fixture {selector} replacement {suggested!r} != {spec.expected_suggested_replacement!r}"
            )

    return GB04FixedFixture(
        spec=spec,
        delivery_log=log,
        scope=scope,
        citations=citations,
        expected_verdicts=verdicts,
        working_tree_only_lines=working_tree_only_lines,
        expected_refusal_reason=refusal_reason,
        expected_suggested_replacement=suggested,
        host_rewrites_citation=spec.host_rewrites_citation,
    )


def build_fixed_fixture(selector: str) -> GB04FixedFixture:
    """Build the deterministic synthetic fixture for any of the 34 GB04 fixed slots."""
    if selector not in SLOT_BY_SELECTOR:
        raise KeyError(f"Unknown GB04 selector: {selector!r}")

    sess = f"sess-{selector.lower()}"
    base_scope = PermittedScope(
        session_id=sess,
        worker_leg="",
        round_id="round-1",
        intent_window="intent-1",
        platform="linux",
    )

    # ------------------------------ R01 (10) ------------------------------
    if selector == "R01-01-handle-supported":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth.go",
            raw_text="10: func Check() bool {\n11:     return true\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (Citation(citation_type="handle", raw="[read#1]", handle="read#1", citation_id="c1"),),
        )

    if selector == "R01-02-handle-unsupported":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth.go",
            raw_text="10: func Check() bool {\n11:     return true\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (Citation(citation_type="handle", raw="[read#99]", handle="read#99", citation_id="c1"),),
        )

    if selector == "R01-03-path-coord-supported":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth.go",
            raw_text="10: func Check() bool {\n11:     return true\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="path_coord",
                    raw='[internal/auth.go:11 "return true"]',
                    path="internal/auth.go",
                    start_line=11,
                    excerpt="return true",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R01-04-path-coord-unsupported":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth.go",
            raw_text="10: func Check() bool {\n11:     return true\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="path_coord",
                    raw="[internal/missing.go:10]",
                    path="internal/missing.go",
                    start_line=10,
                    citation_id="c1",
                ),
                Citation(
                    citation_type="path_coord",
                    raw="[internal/auth.go:50]",
                    path="internal/auth.go",
                    start_line=50,
                    citation_id="c2",
                ),
            ),
        )

    if selector == "R01-05-handle-path-supported":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth.go",
            raw_text="10: func Check() bool {\n11:     return true\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw="[read#1 internal/auth.go:11]",
                    handle="read#1",
                    path="internal/auth.go",
                    start_line=11,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R01-06-handle-path-contradictory":
        rec1 = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth.go",
            raw_text="10: func Check() bool {\n11:     return true\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            call_id="call-1",
        )
        rec2 = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/other.go",
            raw_text="10: func Other() bool {\n11:     return false\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            call_id="call-2",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec1, rec2)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw="[read#1 internal/other.go:10]",
                    handle="read#1",
                    path="internal/other.go",
                    start_line=10,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R01-07-url-observed-admitted":
        rec = _url_record(
            session_id=sess,
            handle="web#1",
            url="https://docs.example.test/grounding",
            kind="web",
            tool="web_fetch",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="url",
                    raw="<https://docs.example.test/grounding>",
                    url="https://docs.example.test/grounding",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R01-08-url-unobserved-contact-only":
        contact_rec = _url_record(
            session_id=sess,
            handle="dialed_host#1",
            url="https://contact-only.example.test/api",
            kind="dialed_host",
            tool="network_proxy",
            model_visible=False,
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(contact_rec,)),
            base_scope,
            (
                Citation(
                    citation_type="url",
                    raw="<https://contact-only.example.test/api>",
                    url="https://contact-only.example.test/api",
                    citation_id="c1",
                ),
                Citation(
                    citation_type="url",
                    raw="<https://never-observed.example.test/page>",
                    url="https://never-observed.example.test/page",
                    citation_id="c2",
                ),
            ),
        )

    if selector == "R01-09-artifact-present-permitted":
        art_id = "123e4567-e89b-12d3-a456-426614174000"
        rec = _artifact_record(session_id=sess, handle="artifact#1", artifact_id=art_id)
        scope = PermittedScope(
            session_id=sess,
            round_id="round-1",
            intent_window="intent-1",
            permitted_artifacts=frozenset({art_id}),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            scope,
            (
                Citation(
                    citation_type="artifact",
                    raw=f"[artifact:{art_id}]",
                    artifact_id=art_id,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R01-10-artifact-denied-or-absent":
        present_id = "123e4567-e89b-12d3-a456-426614174000"
        absent_id = "99999999-9999-9999-9999-999999999999"
        rec = _artifact_record(session_id=sess, handle="artifact#1", artifact_id=present_id)
        scope = PermittedScope(
            session_id=sess,
            round_id="round-1",
            intent_window="intent-1",
            permitted_artifacts=frozenset(),  # GB02-R4: explicit empty set denies all
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            scope,
            (
                Citation(
                    citation_type="artifact",
                    raw=f"[artifact:{present_id}]",
                    artifact_id=present_id,
                    citation_id="c1",
                ),
                Citation(
                    citation_type="artifact",
                    raw=f"[artifact:{absent_id}]",
                    artifact_id=absent_id,
                    citation_id="c2",
                ),
            ),
        )

    # ------------------------------ R02 (8) ------------------------------
    if selector == "R02-01-verdict-matched":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/engine.go",
            raw_text="10: func Run() error {\n11:     return nil\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            line_numbered=True,
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[read#1:11]",
                    handle="read#1",
                    start_line=11,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R02-02-verdict-traced":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/engine.go",
            raw_text="func Run() error {\n    return nil\n}\n",
            delivered_ranges=(LineRange(10, 12),),
            line_numbered=False,
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[read#1:11]",
                    handle="read#1",
                    start_line=11,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R02-03-verdict-bound-unique":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/engine.go",
            raw_text="10: func Run() error {\n11:     return nil\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            line_numbered=True,
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="path_coord",
                    raw="[./internal/../internal/engine.go:11]",
                    path="./internal/../internal/engine.go",
                    start_line=11,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R02-04-verdict-ambiguous-two":
        rec1 = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/dup.go",
            raw_text="10: func Dup() int {\n11:     return 1\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            call_id="call-1",
        )
        rec2 = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/dup.go",
            raw_text="10: func Dup() int {\n11:     return 1\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            call_id="call-2",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec1, rec2)),
            base_scope,
            (
                Citation(
                    citation_type="path_coord",
                    raw="[internal/dup.go:11]",
                    path="internal/dup.go",
                    start_line=11,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R02-05-verdict-unverifiable":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/engine.go",
            raw_text="10: func Run() error {\n11:     return nil\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[read#42:99]",
                    handle="read#42",
                    start_line=99,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R02-06-stale-ordinal-binds-uniquely":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/auth/token.go",
            raw_text="20: func VerifyToken() bool {\n21:     return true\n22: }\n",
            delivered_ranges=(LineRange(20, 22),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw='[read#99 internal/auth/token.go:21 "return true"]',
                    handle="read#99",
                    path="internal/auth/token.go",
                    start_line=21,
                    excerpt="return true",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R02-07-wrong-leg-rejected":
        rec_other_leg = _file_record(
            session_id=sess,
            worker_leg="worker-b",
            handle="read#1",
            path="internal/secret.go",
            raw_text="10: func Secret() string {\n11:     return \"ok\"\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec_other_leg,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[worker-b:read#1:11]",
                    handle="worker-b:read#1",
                    start_line=11,
                    citation_id="c1",
                ),
                Citation(
                    citation_type="handle",
                    raw="[read#1:11]",
                    handle="read#1",
                    start_line=11,
                    citation_id="c2",
                ),
            ),
        )

    if selector == "R02-08-wrong-observation-rejected":
        rec1 = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="1: alpha_line_one\n2: alpha_line_two\n",
            delivered_ranges=(LineRange(1, 2),),
            call_id="call-1",
        )
        rec2 = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/b.go",
            raw_text="1: beta_line_one\n2: beta_line_two\n",
            delivered_ranges=(LineRange(1, 2),),
            call_id="call-2",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec1, rec2)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw="[read#1 internal/b.go:1]",
                    handle="read#1",
                    path="internal/b.go",
                    start_line=1,
                    citation_id="c1",
                ),
                Citation(
                    citation_type="handle",
                    raw='[read#1:1 "beta_line_one"]',
                    handle="read#1",
                    start_line=1,
                    excerpt="beta_line_one",
                    citation_id="c2",
                ),
            ),
        )

    # ------------------------------ R03 (10) ------------------------------
    if selector == "R03-01-multiline-contiguous-blank-lines-clipping":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/multi.go",
            raw_text=(
                "10: func Build(x int) int {\n"
                "11:     y := x + 1\n"
                "12: \n"
                "13:     return y * 2\n"
                "14: }\n"
            ),
            delivered_ranges=(LineRange(10, 14),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:10-13 "Build(x int) int {\\n    y := x + 1\\n\\n    return y"]',
                    handle="read#1",
                    start_line=10,
                    end_line=13,
                    excerpt="Build(x int) int {\n    y := x + 1\n\n    return y",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-02-cross-observation-bytes-refused":
        rec1 = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/split.go",
            raw_text="1: first_half_line\n2: second_half_line\n",
            delivered_ranges=(LineRange(1, 2),),
            call_id="call-1",
        )
        rec2 = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/split.go",
            raw_text="3: third_obs_line\n4: fourth_obs_line\n",
            delivered_ranges=(LineRange(3, 4),),
            call_id="call-2",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec1, rec2)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:2-3 "second_half_line\\nthird_obs_line"]',
                    handle="read#1",
                    start_line=2,
                    end_line=3,
                    excerpt="second_half_line\nthird_obs_line",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-03-working-tree-bytes-refused":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/live.go",
            raw_text="1: delivered_line_one\n2: delivered_line_two\n",
            delivered_ranges=(LineRange(1, 2),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="path_coord",
                    raw='[internal/live.go:3 "working_tree_only_token"]',
                    path="internal/live.go",
                    start_line=3,
                    excerpt="working_tree_only_token",
                    citation_id="c1",
                ),
            ),
            working_tree_only_lines=("3: working_tree_only_token",),
        )

    if selector == "R03-04-printed-line-number-and-verified-excerpt-matched":
        rec_num = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/numbered.go",
            raw_text="10: func Alpha() int {\n11:     return 7\n12: }\n",
            delivered_ranges=(LineRange(10, 12),),
            line_numbered=True,
            call_id="call-1",
        )
        rec_unnum = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/unnumbered.go",
            raw_text="func Beta() int {\n    return 9\n}\n",
            delivered_ranges=(LineRange(20, 22),),
            line_numbered=False,
            call_id="call-2",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec_num, rec_unnum)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[read#1:11]",
                    handle="read#1",
                    start_line=11,
                    citation_id="c1",
                ),
                Citation(
                    citation_type="handle",
                    raw='[read#2:21 "return 9"]',
                    handle="read#2",
                    start_line=21,
                    excerpt="return 9",
                    citation_id="c2",
                ),
            ),
        )

    if selector == "R03-05-unnumbered-delivered-range-traced":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/unnumbered.go",
            raw_text="func Beta() int {\n    return 9\n}\n",
            delivered_ranges=(LineRange(20, 22),),
            line_numbered=False,
            printed_other_numbers=False,
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[read#1:21]",
                    handle="read#1",
                    start_line=21,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-06-reanchor-unique-one-match":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/reanchor.go",
            raw_text="10: line_ten\n11: unique_anchor_target\n12: line_twelve\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:10 "unique_anchor_target"]',
                    handle="read#1",
                    start_line=10,
                    excerpt="unique_anchor_target",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-07-reanchor-zero-matches-rejected":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/reanchor.go",
            raw_text="10: line_ten\n11: line_eleven\n12: line_twelve\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:10 "nonexistent_excerpt"]',
                    handle="read#1",
                    start_line=10,
                    excerpt="nonexistent_excerpt",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-08-reanchor-multiple-matches-rejected":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/reanchor.go",
            raw_text="10: return nil\n11: repeated_token\n12: repeated_token\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:10 "repeated_token"]',
                    handle="read#1",
                    start_line=10,
                    excerpt="repeated_token",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-09-inclusive-multiline-range-uncovered-end-rejected":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/range.go",
            raw_text="10: alpha_target\n11: beta_target\n12: gamma_target\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:10-15 "alpha_target"]',
                    handle="read#1",
                    start_line=10,
                    end_line=15,
                    excerpt="alpha_target",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R03-10-f3-printed-other-numbers-contrast":
        # Continuous numbering from `cat -n a.go b.go` where b.go lines 1-2 printed as 51-52
        copied_tag = "[command#1 internal/b.go:1-2]"
        rec = _file_record(
            session_id=sess,
            handle="command#1",
            kind="command",
            tool="run_command",
            path="internal/b.go",
            raw_text="51: func SecondFile() int {\n52:     return 42\n",
            delivered_ranges=(LineRange(1, 2),),
            delivery_tags=(copied_tag,),
            line_numbered=False,
            printed_other_numbers=True,
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                # c1: unexcerpted coordinate on printed_other_numbers -> product unverifiable vs contract traced
                Citation(
                    citation_type="handle",
                    raw="[command#1:1]",
                    handle="command#1",
                    start_line=1,
                    citation_id="c1",
                ),
                # c2: verified excerpt on printed_other_numbers -> matched
                Citation(
                    citation_type="handle",
                    raw='[command#1:2 "return 42"]',
                    handle="command#1",
                    start_line=2,
                    excerpt="return 42",
                    citation_id="c2",
                ),
                # c3: exact copied host delivery tag -> matched (disputed under D005)
                Citation(
                    citation_type="handle",
                    raw=copied_tag,
                    handle="command#1",
                    start_line=1,
                    end_line=2,
                    citation_id="c3",
                ),
            ),
        )

    # ------------------------------ R05 (6) ------------------------------
    if selector == "R05-01-refusal-unknown-handle":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="1: alpha\n2: beta\n",
            delivered_ranges=(LineRange(1, 2),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (Citation(citation_type="handle", raw="[read#99]", handle="read#99", citation_id="c1"),),
        )

    if selector == "R05-02-refusal-wrong-path":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="1: alpha\n2: beta\n",
            delivered_ranges=(LineRange(1, 2),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw="[read#1 internal/unseen.go:1]",
                    handle="read#1",
                    path="internal/unseen.go",
                    start_line=1,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R05-03-refusal-uncovered-range":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="10: line_ten\n11: line_eleven\n12: line_twelve\n",
            delivered_ranges=(LineRange(10, 12),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw="[read#1:25]",
                    handle="read#1",
                    start_line=25,
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R05-04-refusal-excerpt-mismatch":
        rec = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="10: line_ten\n11: line_eleven\n",
            delivered_ranges=(LineRange(10, 11),),
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec,)),
            base_scope,
            (
                Citation(
                    citation_type="handle",
                    raw='[read#1:10 "does_not_match_any_line"]',
                    handle="read#1",
                    start_line=10,
                    excerpt="does_not_match_any_line",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R05-05-replacement-suggested-unique-one":
        rec1 = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="1: alpha_only\n",
            delivered_ranges=(LineRange(1, 1),),
            call_id="call-1",
        )
        rec2 = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/b.go",
            raw_text="5: unique_replacement_token\n",
            delivered_ranges=(LineRange(5, 5),),
            call_id="call-2",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec1, rec2)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw='[read#1 internal/b.go:5 "unique_replacement_token"]',
                    handle="read#1",
                    path="internal/b.go",
                    start_line=5,
                    excerpt="unique_replacement_token",
                    citation_id="c1",
                ),
            ),
        )

    if selector == "R05-06-replacement-omitted-when-ambiguous-multiple":
        rec1 = _file_record(
            session_id=sess,
            handle="read#1",
            path="internal/a.go",
            raw_text="1: alpha_only\n",
            delivered_ranges=(LineRange(1, 1),),
            call_id="call-1",
        )
        rec2 = _file_record(
            session_id=sess,
            handle="read#2",
            path="internal/b.go",
            raw_text="5: shared_replacement_token\n",
            delivered_ranges=(LineRange(5, 5),),
            call_id="call-2",
        )
        rec3 = _file_record(
            session_id=sess,
            handle="read#3",
            path="internal/b.go",
            raw_text="5: shared_replacement_token\n",
            delivered_ranges=(LineRange(5, 5),),
            call_id="call-3",
        )
        return _finalize_slot(
            selector,
            DeliveryLog(records=(rec1, rec2, rec3)),
            base_scope,
            (
                Citation(
                    citation_type="handle_path",
                    raw='[read#1 internal/b.go:5 "shared_replacement_token"]',
                    handle="read#1",
                    path="internal/b.go",
                    start_line=5,
                    excerpt="shared_replacement_token",
                    citation_id="c1",
                ),
            ),
        )

    raise KeyError(f"Unhandled GB04 selector: {selector!r}")


def build_all_fixed_fixtures() -> dict[str, GB04FixedFixture]:
    """Build all 34 fixed GB04 fixtures keyed by selector."""
    return {slot.selector: build_fixed_fixture(slot.selector) for slot in ALL_GB04_FIXED_SLOTS}


# ---------------------------------------------------------------------------
# Generated family fixture builders (R04, R06, R07)
# ---------------------------------------------------------------------------


def build_r04_instance(base_seed: str, instance_index: int) -> GeneratedR04Case:
    """Build one deterministic seeded R04 Markdown instance via `grounding.generators`."""
    return generate_r04(base_seed, instance_index)


def build_r04_syntax_vs_observation_cases() -> tuple[R04SyntaxObservationCase, ...]:
    """Build explicit COV-02 + D018 cases separating syntax, scope, and observed support."""
    sess = "sess-r04-cov02"
    scope = PermittedScope(session_id=sess, round_id="round-1", intent_window="intent-1")
    read_rec = _file_record(
        session_id=sess,
        handle="read#3",
        path="internal/known.go",
        raw_text="1: package internal\n2: func Known() {}\n",
        delivered_ranges=(LineRange(1, 2),),
        call_id="call-known",
    )
    web_rec = _url_record(
        session_id=sess,
        handle="web#1",
        url="https://example.test/observed",
        kind="web",
        tool="web_fetch",
    )
    log = DeliveryLog(records=(read_rec, web_rec))

    indented_md = "    [read#3:1]  # indented code block stays literal"
    indented_span = R04Span(
        start=0,
        end=len(indented_md),
        text=indented_md,
        node_kind="literal_indented_code",
        is_citation_attempt=False,
        syntax_valid=True,
        destination_observed=None,
    )

    known_bare_md = "See [internal/known.go] in the summary."
    known_bare_text = "[internal/known.go]"
    known_bare_start = known_bare_md.index(known_bare_text)
    known_bare_span = R04Span(
        start=known_bare_start,
        end=known_bare_start + len(known_bare_text),
        text=known_bare_text,
        node_kind="known_bare_path",
        is_citation_attempt=True,
        syntax_valid=True,
        destination_observed=True,
    )

    unknown_bare_md = "See [internal/unknown.go] in the summary."
    unknown_bare_text = "[internal/unknown.go]"
    unknown_bare_start = unknown_bare_md.index(unknown_bare_text)
    unknown_bare_span = R04Span(
        start=unknown_bare_start,
        end=unknown_bare_start + len(unknown_bare_text),
        text=unknown_bare_text,
        node_kind="unknown_bare_path",
        is_citation_attempt=True,
        syntax_valid=True,
        destination_observed=False,
    )

    group_md = "Verified by [read#3:1, read#3:2]."
    group_text = "[read#3:1, read#3:2]"
    group_start = group_md.index(group_text)
    group_span = R04Span(
        start=group_start,
        end=group_start + len(group_text),
        text=group_text,
        node_kind="citation_group",
        is_citation_attempt=True,
        syntax_valid=True,
        destination_observed=True,
    )

    http_unobs_md = "Read [Unobserved Spec](https://example.test/unobserved)."
    http_unobs_text = "[Unobserved Spec](https://example.test/unobserved)"
    http_unobs_start = http_unobs_md.index(http_unobs_text)
    http_unobs_span = R04Span(
        start=http_unobs_start,
        end=http_unobs_start + len(http_unobs_text),
        text=http_unobs_text,
        node_kind="citation_http_link",
        is_citation_attempt=True,
        syntax_valid=True,
        destination_observed=False,
    )

    oversized_text = f'[read#3:1 "{"x" * 4120}"]'
    oversized_span = R04Span(
        start=0,
        end=len(oversized_text),
        text=oversized_text,
        node_kind="oversized_inline_node",
        is_citation_attempt=True,
        syntax_valid=False,
        destination_observed=None,
    )

    return (
        R04SyntaxObservationCase(
            case_id="R04-COV02-indented-code-literal",
            markdown=indented_md,
            spans=(indented_span,),
            delivery_log=log,
            scope=scope,
            citation_under_test=None,
            expected_syntax_attempt=False,
            expected_syntax_valid=True,
            expected_observed_support=None,
            notes="4-space indented code is literal Markdown and never a citation attempt.",
        ),
        R04SyntaxObservationCase(
            case_id="R04-COV02-known-bare-path",
            markdown=known_bare_md,
            spans=(known_bare_span,),
            delivery_log=log,
            scope=scope,
            citation_under_test=Citation(
                citation_type="path_coord",
                raw=known_bare_text,
                path="internal/known.go",
                citation_id="c1",
            ),
            expected_syntax_attempt=True,
            expected_syntax_valid=True,
            expected_observed_support=False,  # bare path is not line/excerpt evidence
            notes="Known bare path is syntactically recognized separately from line-evidence support.",
        ),
        R04SyntaxObservationCase(
            case_id="R04-COV02-unknown-bare-path",
            markdown=unknown_bare_md,
            spans=(unknown_bare_span,),
            delivery_log=log,
            scope=scope,
            citation_under_test=Citation(
                citation_type="path_coord",
                raw=unknown_bare_text,
                path="internal/unknown.go",
                citation_id="c1",
            ),
            expected_syntax_attempt=True,
            expected_syntax_valid=True,
            expected_observed_support=False,
            notes="Unknown bare path has no observation and no line/excerpt evidence.",
        ),
        R04SyntaxObservationCase(
            case_id="R04-COV02-group-disposition",
            markdown=group_md,
            spans=(group_span,),
            delivery_log=log,
            scope=scope,
            citation_under_test=Citation(
                citation_type="handle",
                raw="[read#3:1]",
                handle="read#3",
                start_line=1,
                citation_id="c1",
            ),
            expected_syntax_attempt=True,
            expected_syntax_valid=True,
            expected_observed_support=True,
            notes="Group citation syntax is parsed as a citation_group and members resolved independently.",
        ),
        R04SyntaxObservationCase(
            case_id="R04-COV02-http-link-valid-syntax-unobserved-dest",
            markdown=http_unobs_md,
            spans=(http_unobs_span,),
            delivery_log=log,
            scope=scope,
            citation_under_test=Citation(
                citation_type="url",
                raw=http_unobs_text,
                url="https://example.test/unobserved",
                citation_id="c1",
            ),
            expected_syntax_attempt=True,
            expected_syntax_valid=True,
            expected_observed_support=False,
            notes="Ordinary HTTP(S) link has valid syntax (`syntax_valid=True`) even when destination URL is unobserved.",
        ),
        R04SyntaxObservationCase(
            case_id="R04-COV02-oversized-inline-node-gt-4096",
            markdown=oversized_text,
            spans=(oversized_span,),
            delivery_log=log,
            scope=scope,
            citation_under_test=None,
            expected_syntax_attempt=True,
            expected_syntax_valid=False,
            expected_observed_support=None,
            notes="Inline node >4,096 bytes becomes a structured issue and is never truncated into acceptance.",
        ),
    )


def build_r06_instance(
    base_seed: str,
    instance_index: int,
    *,
    variation: str | None = None,
    platform: str = "linux",
) -> R06PreparedCase:
    """Build one R06 must-pass instance with independent reference verdict."""
    gen = generate_r06(base_seed, instance_index, variation=variation, platform=platform)
    verdict = resolve_citation(gen.delivery_log, gen.citation, gen.scope)
    return R06PreparedCase(
        generated=gen,
        expected_verdict=verdict,
        case_insensitive_paths=gen.scope.case_insensitive_paths,
    )


def build_r07_instance(
    base_seed: str,
    instance_index: int,
    *,
    mutation: str | None = None,
    platform: str = "linux",
) -> R07PreparedCase:
    """Build one R07 near-truth instance with explicit re-anchoring/binding distinction."""
    gen = generate_r07(base_seed, instance_index, mutation=mutation, platform=platform)
    verdict = resolve_citation(gen.delivery_log, gen.citation, gen.scope)
    return R07PreparedCase(
        generated=gen,
        expected_verdict=verdict,
        reanchor_possible=verdict.reanchored_line is not None,
        binding_possible=verdict.expected_exact_verdict == "bound",
        case_insensitive_paths=gen.scope.case_insensitive_paths,
    )


def build_platform_path_case_contrast(
    base_seed: str,
    instance_index: int,
) -> tuple[R06PreparedCase, R07PreparedCase]:
    """Return paired (darwin_case, linux_case) for `Src/Main.go` vs `src/main.go`.

    - `darwin` (APFS case-insensitive): expected `grounded` (R06).
    - `linux` (ext4 case-sensitive): expected `ungrounded` (R07).
    """
    darwin_gen = generate_path_case_contrast(base_seed, instance_index, platform="darwin")
    linux_gen = generate_path_case_contrast(base_seed, instance_index, platform="linux")
    assert isinstance(darwin_gen, GeneratedR06Case)
    assert isinstance(linux_gen, GeneratedR07Case)

    darwin_verdict = resolve_citation(darwin_gen.delivery_log, darwin_gen.citation, darwin_gen.scope)
    linux_verdict = resolve_citation(linux_gen.delivery_log, linux_gen.citation, linux_gen.scope)

    return (
        R06PreparedCase(
            generated=darwin_gen,
            expected_verdict=darwin_verdict,
            case_insensitive_paths=True,
        ),
        R07PreparedCase(
            generated=linux_gen,
            expected_verdict=linux_verdict,
            reanchor_possible=False,
            binding_possible=False,
            case_insensitive_paths=False,
        ),
    )
