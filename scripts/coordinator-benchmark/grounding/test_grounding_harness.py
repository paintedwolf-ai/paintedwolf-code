"""Comprehensive pytest/unittest suite for the GB02 independent grounding harness."""
from __future__ import annotations

import ast
import pathlib
import unittest

from grounding.checker import check_slot, check_wording_triad
from grounding.delivery_log import (
    DeliveryLog,
    DeliveryRecord,
    FrictionCounters,
    LineRange,
    ProductRecord,
    ProductVerdict,
    SurfaceOutcome,
)
from grounding.generators import (
    BASE_SEEDS,
    R06_VARIATIONS,
    R07_MUTATIONS,
    generate_r04,
    generate_r06,
    generate_r07,
    generate_v02,
)
from grounding.reference_resolver import (
    MEDIATED_CONTACT_KINDS,
    Citation,
    PermittedScope,
    has_citable_observation,
    is_recallable,
    resolve_citation,
)
from grounding.selectors import (
    DECISION_005_V04_ASSERTIONS,
    GB01_FINDINGS,
    PREFLIGHT_COVERAGE_ROWS,
    validate_manifest,
)
from grounding.shrinker import ShrinkableCase, ShrunkArtifact, shrink_case


GROUNDING_DIR = pathlib.Path(__file__).resolve().parent


def _make_read_record(
    *,
    handle: str = "read#1",
    path: str = "internal/app.go",
    raw_bytes: bytes = b"10: func Run() error {\n11:     return nil\n12: }\n",
    ranges: tuple[LineRange, ...] = (LineRange(10, 12),),
    tags: tuple[str, ...] | None = None,
    line_numbered: bool = True,
    printed_other_numbers: bool = False,
    session_id: str = "sess-1",
    worker_leg: str = "",
    round_id: str = "round-1",
    intent_window: str = "intent-1",
    source_revision: str = "rev-1",
    kind: str = "read",
    url: str = "",
    artifact_id: str = "",
    superseded_by: str = "",
    model_visible: bool = True,
) -> DeliveryRecord:
    if tags is None and ranges and path:
        tags = (f"[{handle} {path}:{ranges[0].start}-{ranges[0].end}]",)
    elif tags is None:
        tags = (f"[{handle}]",)
    return DeliveryRecord(
        tool="read_file",
        arguments={"path": path},
        raw_bytes=raw_bytes,
        delivered_ranges=ranges,
        delivery_tags=tags,
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
        url=url,
        artifact_id=artifact_id,
        call_id=f"call-{handle}",
        fixture_provenance="host",
        superseded_by=superseded_by,
        model_visible=model_visible,
    )


def _matching_product_record(rec: DeliveryRecord) -> ProductRecord:
    return ProductRecord(
        handle=rec.handle,
        kind=rec.kind,
        session_id=rec.session_id,
        worker_leg=rec.worker_leg,
        source_revision=rec.source_revision,
        delivered_ranges=rec.delivered_ranges,
        body_sha256=rec.body_sha256,
        path=rec.path,
        url=rec.url,
        artifact_id=rec.artifact_id,
        round_id=rec.round_id,
        superseded_by=rec.superseded_by,
    )


class TestIndependenceAndIsolation(unittest.TestCase):
    """D3: Reference resolver and harness import zero Go/product grounding packages."""

    FORBIDDEN_TOKENS = (
        "lycaon/internal/evidence",
        "lycaon/internal/citationmd",
        "lycaon/internal/grounding",
        "internal/evidence",
        "internal/citationmd",
        "internal/grounding",
    )

    def test_zero_product_grounding_imports_or_subprocesses(self) -> None:
        py_files = sorted(GROUNDING_DIR.glob("*.py"))
        self.assertGreaterEqual(len(py_files), 6)
        for path in py_files:
            if path.name == pathlib.Path(__file__).name:
                continue
            source = path.read_text(encoding="utf-8")
            tree = ast.parse(source, filename=str(path))
            for node in ast.walk(tree):
                if isinstance(node, ast.Import):
                    for alias in node.names:
                        self.assertNotIn("subprocess", alias.name)
                        for forbidden in self.FORBIDDEN_TOKENS:
                            self.assertNotIn(forbidden, alias.name)
                elif isinstance(node, ast.ImportFrom):
                    mod = node.module or ""
                    self.assertNotIn("subprocess", mod)
                    for forbidden in self.FORBIDDEN_TOKENS:
                        self.assertNotIn(forbidden, mod)

    def test_delivery_log_rejects_product_record(self) -> None:
        rec = _make_read_record()
        prod = _matching_product_record(rec)
        with self.assertRaises(TypeError):
            DeliveryLog(records=(prod,))  # type: ignore[arg-type]


class TestInjectedDefectsAndHealthyControls(unittest.TestCase):
    """Ledger Controls: all 5 injected defects flagged, all 4 healthy inputs pass."""

    def setUp(self) -> None:
        self.scope = PermittedScope(
            session_id="sess-1",
            worker_leg="",
            round_id="round-1",
            intent_window="intent-1",
        )

    def test_injected_defect_1_grounded_verdict_with_no_delivery(self) -> None:
        log = DeliveryLog(records=())
        cit = Citation(citation_type="handle", raw="[read#1:10]", handle="read#1", start_line=10, citation_id="c1")
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1"),),
            records=(),
        )
        res = check_slot(log, [cit], self.scope, outcome)
        self.assertFalse(res.passed)
        self.assertEqual(res.defects[0].kind, "false_accept")

    def test_injected_defect_2_refusal_with_one_delivery(self) -> None:
        rec = _make_read_record()
        log = DeliveryLog(records=(rec,))
        cit = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="c1")
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="unverifiable", code="SYNTH_CITATION_UNVERIFIABLE"),),
            records=(_matching_product_record(rec),),
        )
        res = check_slot(log, [cit], self.scope, outcome)
        self.assertFalse(res.passed)
        self.assertEqual(res.defects[0].kind, "false_reject")

    def test_injected_defect_3_record_missing_from_log(self) -> None:
        rec = _make_read_record()
        log = DeliveryLog(records=(rec,))
        extra_prod = ProductRecord(
            handle="read#99",
            kind="read",
            session_id="sess-1",
            worker_leg="",
            source_revision="rev-1",
            delivered_ranges=(LineRange(1, 1),),
            body_sha256=rec.body_sha256,
        )
        cit = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="c1")
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1"),),
            records=(_matching_product_record(rec), extra_prod),
        )
        res = check_slot(log, [cit], self.scope, outcome)
        self.assertFalse(res.passed)
        self.assertEqual(res.defects[0].kind, "recording_defect")

    def test_injected_defect_4_record_with_wrong_leg(self) -> None:
        rec = _make_read_record(worker_leg="leg-a")
        log = DeliveryLog(records=(rec,))
        scope = PermittedScope(
            session_id="sess-1",
            worker_leg="",
            permitted_legs=frozenset({"leg-a"}),
            round_id="round-1",
            intent_window="intent-1",
        )
        wrong_leg_prod = ProductRecord(
            handle=rec.handle,
            kind=rec.kind,
            session_id=rec.session_id,
            worker_leg="",  # Promoted worker read to parent first-hand read (defect!)
            source_revision=rec.source_revision,
            delivered_ranges=rec.delivered_ranges,
            body_sha256=rec.body_sha256,
        )
        cit = Citation(
            citation_type="handle",
            raw="[leg-a:read#1:11]",
            handle="leg-a:read#1",
            start_line=11,
            citation_id="c1",
        )
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="leg-a:read#1"),),
            records=(wrong_leg_prod,),
        )
        res = check_slot(log, [cit], scope, outcome)
        self.assertFalse(res.passed)
        self.assertEqual(res.defects[0].kind, "recording_defect")

    def test_injected_defect_5_outcome_changed_with_wording(self) -> None:
        rec = _make_read_record()
        log = DeliveryLog(records=(rec,))
        cit = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="c1")
        prod_rec = _matching_product_record(rec)
        outcomes = [
            SurfaceOutcome(
                surface="coordinator_closeout",
                wording="confident",
                verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1"),),
                records=(prod_rec,),
            ),
            SurfaceOutcome(
                surface="coordinator_closeout",
                wording="hedged",
                verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1"),),
                records=(prod_rec,),
            ),
            SurfaceOutcome(
                surface="coordinator_closeout",
                wording="completion_asserting",
                verdicts=(ProductVerdict(citation_id="c1", verdict="unverifiable", code="SYNTH_CITATION_UNVERIFIABLE"),),
                records=(prod_rec,),
                code="SYNTH_CITATION_UNVERIFIABLE",
                traced=False,
            ),
        ]
        res = check_wording_triad(log, [cit], self.scope, outcomes)
        self.assertFalse(res.passed)
        defect_kinds = [d.kind for d in res.defects]
        self.assertIn("wording_defect", defect_kinds)
        # D4 priority: false_reject (2) appears before wording_defect (4)
        self.assertEqual(defect_kinds, sorted(defect_kinds, key=lambda k: {"false_accept": 1, "false_reject": 2, "recording_defect": 3, "wording_defect": 4}[k]))

    def test_healthy_1_ambiguous_with_two_deliveries(self) -> None:
        rec1 = _make_read_record(handle="read#1")
        rec2 = _make_read_record(handle="read#2")
        log = DeliveryLog(records=(rec1, rec2))
        cit = Citation(citation_type="path_coord", raw="[internal/app.go:11]", path="internal/app.go", start_line=11, citation_id="c1")
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="ambiguous"),),
            records=(_matching_product_record(rec1), _matching_product_record(rec2)),
        )
        res = check_slot(log, [cit], self.scope, outcome)
        self.assertTrue(res.passed, res.defects)

    def test_healthy_2_exempt_speech_in_empty_window(self) -> None:
        log = DeliveryLog(records=(), readable=True)
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(),
            records=(),
            traced=True,
            evidence_optional=True,
        )
        res = check_slot(log, [], self.scope, outcome)
        self.assertTrue(res.passed, res.defects)

        # Unreadable ledger must NEVER earn exempt speech (V07 / S01)
        unreadable_res = check_slot(DeliveryLog(records=(), readable=False), [], self.scope, outcome)
        self.assertFalse(unreadable_res.passed)
        self.assertEqual(unreadable_res.defects[0].kind, "false_accept")

    def test_healthy_3_host_assembled_closeout(self) -> None:
        rec = _make_read_record()
        log = DeliveryLog(records=(rec,))
        outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(),
            records=(_matching_product_record(rec),),
            traced=False,
            host_assembled=True,
            attached_handles=("read#1",),
            friction_before=FrictionCounters(),
            friction_after=FrictionCounters(host_assembled_closeouts=1),
        )
        res = check_slot(log, [], self.scope, outcome, require_friction=True)
        self.assertTrue(res.passed, res.defects)
        self.assertEqual(res.friction_delta, FrictionCounters(host_assembled_closeouts=1))

    def test_healthy_4_withheld_note(self) -> None:
        rec = _make_read_record()
        log = DeliveryLog(records=(rec,))
        outcome = SurfaceOutcome(
            surface="mid_turn_note",
            verdicts=(),
            records=(_matching_product_record(rec),),
            code="MID_TURN_NOTE_UNGROUNDED",
            withheld=True,
            traced=False,
            friction_before=FrictionCounters(),
            friction_after=FrictionCounters(withheld_notes=1),
        )
        res = check_slot(log, [], self.scope, outcome, require_friction=True)
        self.assertTrue(res.passed, res.defects)
        self.assertEqual(res.friction_delta, FrictionCounters(withheld_notes=1))

    def test_missing_friction_instrumentation_yields_unknown(self) -> None:
        log = DeliveryLog(records=())
        outcome = SurfaceOutcome(surface="coordinator_closeout", traced=True, evidence_optional=True)
        res = check_slot(log, [], self.scope, outcome, require_friction=True)
        self.assertFalse(res.passed)
        self.assertEqual(res.status, "unknown")


class TestReferenceResolverContractRules(unittest.TestCase):
    """Test all 5 citation types, re-anchoring, F2 contact records, F3 & Decision 005."""

    def setUp(self) -> None:
        self.scope = PermittedScope(
            session_id="sess-1",
            worker_leg="",
            permitted_legs=frozenset({"leg-a"}),
            round_id="round-1",
            intent_window="intent-1",
        )

    def test_reanchoring_zero_one_and_multiple_matches(self) -> None:
        rec = _make_read_record(
            raw_bytes=b"10: dup_line\n11: unique_target\n12: dup_line\n",
            ranges=(LineRange(10, 12),),
        )
        log = DeliveryLog(records=(rec,))

        # 1 match -> re-anchors wrong line 10 to line 11
        one = resolve_citation(
            log,
            Citation(citation_type="handle", handle="read#1", start_line=10, excerpt="unique_target"),
            self.scope,
        )
        self.assertEqual(one.verdict_class, "grounded")
        self.assertEqual(one.reanchored_line, 11)
        self.assertEqual(one.reason, "reanchored_excerpt")

        # 0 matches -> ungrounded
        zero = resolve_citation(
            log,
            Citation(citation_type="handle", handle="read#1", start_line=10, excerpt="nonexistent"),
            self.scope,
        )
        self.assertEqual(zero.verdict_class, "ungrounded")
        self.assertEqual(zero.reason, "excerpt_mismatch")

        # >1 matches -> ungrounded (never picks arbitrarily)
        multi = resolve_citation(
            log,
            Citation(citation_type="handle", handle="read#1", start_line=11, excerpt="dup_line"),
            self.scope,
        )
        self.assertEqual(multi.verdict_class, "ungrounded")
        self.assertEqual(multi.reason, "excerpt_ambiguous")

    def test_stale_ordinal_binding_vs_wrong_leg_rejection(self) -> None:
        rec_parent = _make_read_record(handle="read#1", path="internal/app.go", worker_leg="")
        rec_other_leg = _make_read_record(handle="read#5", path="internal/worker.go", worker_leg="leg-b")
        log = DeliveryLog(records=(rec_parent, rec_other_leg))

        # Stale ordinal read#99 with matching path + line binds uniquely to read#1
        stale = resolve_citation(
            log,
            Citation(citation_type="handle_path", handle="read#99", path="internal/app.go", start_line=11),
            self.scope,
        )
        self.assertEqual(stale.verdict_class, "grounded")
        self.assertEqual(stale.expected_exact_verdict, "bound")
        self.assertEqual(stale.supporting_handles, ("read#1",))

        # Wrong-leg handle read#5 is rejected, never bound
        wrong_leg = resolve_citation(
            log,
            Citation(citation_type="handle_path", handle="read#5", path="internal/worker.go", start_line=11),
            self.scope,
        )
        self.assertEqual(wrong_leg.verdict_class, "ungrounded")
        self.assertEqual(wrong_leg.reason, "wrong_leg")

    def test_source_change_brief_only_rules(self) -> None:
        sc = _make_read_record(
            handle="source_change#1",
            kind="source_change",
            path="internal/edited.go",
            ranges=(),
            raw_bytes=b"edited internal/edited.go",
        )
        log = DeliveryLog(records=(sc,))

        # Bare handle proves the brief row
        bare = resolve_citation(log, Citation(citation_type="handle", handle="source_change#1"), self.scope)
        self.assertEqual(bare.verdict_class, "grounded")

        # Line/excerpt on source_change# is refused
        with_line = resolve_citation(
            log,
            Citation(citation_type="handle", handle="source_change#1", start_line=1),
            self.scope,
        )
        self.assertEqual(with_line.verdict_class, "ungrounded")
        self.assertEqual(with_line.reason, "source_change_brief_only")

        # Creates no citation duty and is unavailable to recall
        self.assertFalse(has_citable_observation(log, self.scope))
        self.assertFalse(is_recallable(sc))

    def test_f2_mediated_contact_records_never_grant_url_support(self) -> None:
        target_url = "https://example.test/api/v1"
        contact_records = tuple(
            _make_read_record(
                handle=f"{kind}#1",
                kind=kind,
                path="",
                ranges=(),
                url=target_url,
                raw_bytes=b"contact metadata",
                model_visible=True,  # Even if visible, contact records never grant URL support
            )
            for kind in sorted(MEDIATED_CONTACT_KINDS)
        )
        log = DeliveryLog(records=contact_records)
        cit = Citation(citation_type="url", raw=f"<{target_url}>", url=target_url)

        res = resolve_citation(log, cit, self.scope)
        self.assertEqual(res.verdict_class, "ungrounded")
        self.assertEqual(res.reason, "mediated_contact_only")
        self.assertFalse(has_citable_observation(log, self.scope))

        # Adding an admitted web# observation DOES ground the URL
        web_rec = _make_read_record(
            handle="web#1",
            kind="web",
            path="",
            ranges=(),
            url=target_url,
            raw_bytes=b"HTTP 200 body",
        )
        res_ok = resolve_citation(log.append(web_rec), cit, self.scope)
        self.assertEqual(res_ok.verdict_class, "grounded")
        self.assertEqual(res_ok.supporting_handles, ("web#1",))

    def test_f3_contrast_cases(self) -> None:
        # 1. Correctly numbered lines -> matched, not disputed
        r_num = _make_read_record(
            handle="read#1",
            raw_bytes=b"10: alpha\n11: beta\n",
            ranges=(LineRange(10, 11),),
            line_numbered=True,
            printed_other_numbers=False,
        )
        v1 = resolve_citation(
            DeliveryLog(records=(r_num,)),
            Citation(citation_type="handle", handle="read#1", start_line=10),
            self.scope,
        )
        self.assertEqual((v1.verdict_class, v1.expected_exact_verdict, v1.disputed_expectation), ("grounded", "matched", False))

        # 2. Unnumbered delivery -> traced, not disputed
        r_unnum = _make_read_record(
            handle="read#2",
            raw_bytes=b"alpha\nbeta\n",
            ranges=(LineRange(10, 11),),
            line_numbered=False,
            printed_other_numbers=False,
        )
        v2 = resolve_citation(
            DeliveryLog(records=(r_unnum,)),
            Citation(citation_type="handle", handle="read#2", start_line=10),
            self.scope,
        )
        self.assertEqual((v2.verdict_class, v2.expected_exact_verdict, v2.disputed_expectation), ("grounded", "traced", False))

        # 3. Mismatched printed numbers WITHOUT excerpt -> product unverifiable, contract traced, disputed=True
        r_mis = _make_read_record(
            handle="read#3",
            raw_bytes=b"100: alpha\n101: beta\n",
            ranges=(LineRange(10, 11),),
            line_numbered=False,
            printed_other_numbers=True,
        )
        v3 = resolve_citation(
            DeliveryLog(records=(r_mis,)),
            Citation(citation_type="handle", handle="read#3", start_line=10),
            self.scope,
        )
        self.assertEqual(v3.verdict_class, "ungrounded")
        self.assertEqual(v3.expected_exact_verdict, "unverifiable")
        self.assertEqual(v3.contract_reading_verdict, "traced")
        self.assertTrue(v3.disputed_expectation)
        self.assertEqual(v3.observed_product_rule, "printed_other_numbers_rejects_unexcerpted_coord")

        # 4. Mismatched printed numbers WITH verified excerpt -> grounded, matched, not disputed
        v4 = resolve_citation(
            DeliveryLog(records=(r_mis,)),
            Citation(citation_type="handle", handle="read#3", start_line=10, excerpt="alpha"),
            self.scope,
        )
        self.assertEqual((v4.verdict_class, v4.expected_exact_verdict, v4.disputed_expectation), ("grounded", "matched", False))

    def test_decision_005_copied_source_tag_second_file_contrast_set(self) -> None:
        """Decision 005: `cat -n a.go b.go` second-file copied tag vs continuous numbers vs excerpt."""
        # Synthetic multi-file `cat -n a.go b.go`:
        # a.go has lines 1-3 (printed 1-3), b.go has lines 1-2 (printed continuously as 4-5).
        # Host issues delivery tag `[read#2 b.go:1-2]` formatting true coverage for b.go.
        rec_a = _make_read_record(
            handle="read#1",
            path="a.go",
            raw_bytes=b"     1\tpackage main\n     2\t// a2\n     3\t// a3\n",
            ranges=(LineRange(1, 3),),
            tags=("[read#1 a.go:1-3]",),
            line_numbered=True,
            printed_other_numbers=False,
        )
        rec_b = _make_read_record(
            handle="read#2",
            path="b.go",
            raw_bytes=b"     4\tpackage main\n     5\tfunc SecondFile() {}\n",
            ranges=(LineRange(1, 2),),
            tags=("[read#2 b.go:1-2]",),
            line_numbered=False,
            printed_other_numbers=True,
        )
        log = DeliveryLog(records=(rec_a, rec_b))

        # (1) V04-copied-source-tag-second-file: exact copy of host-issued tag `[read#2 b.go:1-2]`
        copied_tag = resolve_citation(
            log,
            Citation(
                citation_type="handle_path",
                raw="[read#2 b.go:1-2]",
                handle="read#2",
                path="b.go",
                start_line=1,
                end_line=2,
            ),
            self.scope,
        )
        self.assertEqual(copied_tag.verdict_class, "grounded")
        self.assertEqual(copied_tag.reason, "copied_host_delivery_tag")
        self.assertEqual(copied_tag.contract_reading_verdict, "matched")
        self.assertEqual(copied_tag.observed_product_rule, "printed_other_numbers_rejects_unexcerpted_coord")
        self.assertTrue(copied_tag.disputed_expectation)

        # (2a) V04-displayed-continuous-numbers: un-excerpted counted coordinate (not copied tag)
        counted_coord = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw="[read#2:2]",
                handle="read#2",
                start_line=2,
            ),
            self.scope,
        )
        self.assertEqual(counted_coord.verdict_class, "ungrounded")
        self.assertEqual(counted_coord.reason, "mismatched_printed_numbers_no_excerpt")
        self.assertEqual(counted_coord.contract_reading_verdict, "traced")
        self.assertEqual(counted_coord.observed_product_rule, "printed_other_numbers_rejects_unexcerpted_coord")

        # (2b) V04-rejected-inferred-coordinate: citing printed continuous number 5 (outside true 1-2)
        inferred_coord = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw="[read#2:5]",
                handle="read#2",
                start_line=5,
            ),
            self.scope,
        )
        self.assertEqual(inferred_coord.verdict_class, "ungrounded")
        self.assertEqual(inferred_coord.reason, "uncovered_range")
        self.assertFalse(inferred_coord.disputed_expectation)

        # (2c) V04-uniquely-matching-excerpt: uniquely matching excerpt re-anchors even from printed line 5
        with_excerpt = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw='[read#2:5 "func SecondFile() {}"]',
                handle="read#2",
                start_line=5,
                excerpt="func SecondFile() {}",
            ),
            self.scope,
        )
        self.assertEqual(with_excerpt.verdict_class, "grounded")
        self.assertEqual(with_excerpt.reanchored_line, 2)
        self.assertFalse(with_excerpt.disputed_expectation)


class TestGeneratorsAndShrinker(unittest.TestCase):
    """D6: Deterministic seeded generators and delta-debugging shrinker + replay."""

    def test_v02_bounds_and_determinism(self) -> None:
        for base in BASE_SEEDS:
            for idx in range(10):
                a = generate_v02(base, idx)
                b = generate_v02(base, idx)
                self.assertEqual(a, b)
                self.assertGreaterEqual(len(a.payload), 1)
                self.assertLessEqual(len(a.payload), 4000)
                self.assertGreaterEqual(len(a.imitated_markers), 1)
                self.assertLessEqual(len(a.imitated_markers), 8)
                self.assertEqual(a.expected_minted_tags, ())

    def test_r04_bounds_and_oversized_node(self) -> None:
        oversized_seen = False
        for base in BASE_SEEDS:
            for idx in range(20):
                case = generate_r04(base, idx)
                self.assertEqual(case, generate_r04(base, idx))
                self.assertGreaterEqual(len(case.markdown.encode("utf-8")), 1)
                self.assertLessEqual(len(case.markdown.encode("utf-8")), 6000)
                self.assertGreaterEqual(case.total_spans, 1)
                self.assertLessEqual(case.total_spans, 40)
                if case.oversized_inline_nodes:
                    oversized_seen = True
                    self.assertGreater(len(case.oversized_inline_nodes[0].encode("utf-8")), 4096)
        self.assertTrue(oversized_seen)

    def test_r06_all_8_variations_grounded(self) -> None:
        for base in BASE_SEEDS:
            for idx, variation in enumerate(R06_VARIATIONS):
                case = generate_r06(base, idx, variation=variation)
                self.assertEqual(case, generate_r06(base, idx, variation=variation))
                verdict = resolve_citation(case.delivery_log, case.citation, case.scope)
                self.assertEqual(
                    verdict.verdict_class,
                    "grounded",
                    f"R06 variation {variation} ({base}:{idx}) failed: {verdict}",
                )

    def test_r07_all_6_mutations_ungrounded(self) -> None:
        for base in BASE_SEEDS:
            for idx, mutation in enumerate(R07_MUTATIONS):
                case = generate_r07(base, idx, mutation=mutation)
                self.assertEqual(case, generate_r07(base, idx, mutation=mutation))
                verdict = resolve_citation(case.delivery_log, case.citation, case.scope)
                self.assertEqual(
                    verdict.verdict_class,
                    "ungrounded",
                    f"R07 mutation {mutation} ({base}:{idx}) unexpectedly grounded: {verdict}",
                )

    def test_deliberately_wrong_expectation_caught_shrunk_and_replayed(self) -> None:
        # Generate an R06 case with multiple filler deliveries; deliberately inject a wrong
        # expectation claiming the citation must be UNGROUNDED (so it fails whenever grounded).
        gen = generate_r06("20261127", 12, variation="path_normalization")
        self.assertGreater(len(gen.delivery_log.records), 1)

        def wrong_expectation_fails(cand: ShrinkableCase) -> bool:
            assert cand.citation is not None
            verdict = resolve_citation(cand.delivery_log, cand.citation, cand.scope)
            # Wrong expectation: asserts grounded citations are a failure!
            return verdict.verdict_class == "grounded"

        initial = ShrinkableCase(
            delivery_log=gen.delivery_log,
            scope=gen.scope,
            citation=gen.citation,
            seed=gen.seed,
        )
        artifact = shrink_case(initial, wrong_expectation_fails)

        # Must shrink from >1 deliveries down to exactly 1 minimal delivery
        self.assertGreater(artifact.original_record_count, 1)
        self.assertEqual(artifact.shrunk_record_count, 1)
        self.assertLessEqual(artifact.shrunk_bytes, artifact.original_bytes)
        self.assertTrue(wrong_expectation_fails(artifact.minimized))

        # Serialize to JSON seed artifact, reload, and replay deterministically from seed
        serialized = artifact.to_json()
        reloaded = ShrunkArtifact.from_json(serialized)
        self.assertEqual(reloaded.sha256, artifact.sha256)
        self.assertTrue(wrong_expectation_fails(reloaded.minimized))

        assert reloaded.minimized.seed is not None
        replayed = generate_r06(
            reloaded.minimized.seed.base_seed,
            reloaded.minimized.seed.instance_index,
            variation="path_normalization",
        )
        self.assertEqual(replayed.delivery_log.sha256, gen.delivery_log.sha256)


class TestSelectorsManifest(unittest.TestCase):
    """Verify exact ledger counts, 7 preflight rows, GB01 F1/F2/F3, and Decision 005."""

    def test_manifest_counts_and_coverage(self) -> None:
        summary = validate_manifest()
        self.assertEqual(summary["total_families"], 30)
        self.assertEqual(summary["fixed_sidecar_slots"], 171)
        self.assertEqual(summary["fixed_in_process_slots"], 30)
        self.assertEqual(summary["fixed_base_slots"], 201)
        self.assertEqual(summary["fixed_tripled_per_platform"], 431)
        self.assertEqual(summary["fixed_both_platforms"], 862)
        self.assertEqual(summary["generated_per_platform_per_seed"], 23700)
        self.assertEqual(len(PREFLIGHT_COVERAGE_ROWS), 7)
        self.assertEqual({f.finding_id for f in GB01_FINDINGS}, {"F1", "F2", "F3"})
        f3 = next(f for f in GB01_FINDINGS if f.finding_id == "F3")
        for assertion in DECISION_005_V04_ASSERTIONS:
            self.assertIn(assertion, f3.assertions)


if __name__ == "__main__":
    unittest.main()
