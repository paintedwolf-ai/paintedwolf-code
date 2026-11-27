"""Comprehensive pytest/unittest suite for the GB02 independent grounding harness."""
from __future__ import annotations

import ast
import contextlib
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import pathlib
import sqlite3
import tempfile
import threading
import unittest
import urllib.parse

from grounding.checker import check_slot, check_wording_triad
from grounding.cli import main as cli_main
from grounding.delivery_log import (
    DeliveryLog,
    DeliveryRecord,
    FrictionCounters,
    LineRange,
    ProductRecord,
    ProductVerdict,
    SurfaceOutcome,
)
from grounding.friction_probe import (
    friction_observability_report,
    probe_store_db,
)
from grounding.inprocess_driver import (
    InProcessPlan,
    validate_test_only_overlay,
    write_test_overlay,
)
from grounding.generators import (
    BASE_SEEDS,
    R06_VARIATIONS,
    R07_MUTATIONS,
    generate_path_case_contrast,
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
    DECISION_008_S06_ASSERTIONS,
    GB01_FINDINGS,
    PREFLIGHT_COVERAGE_ROWS,
    validate_manifest,
)
from grounding.shrinker import ShrinkableCase, ShrunkArtifact, shrink_case
from grounding.sidecar_driver import (
    HARNESS_ROOT,
    IsolatedSidecarProcess,
    ScriptedTurn,
    SidecarSessionClient,
    evaluate_three_ledger_controls,
    extract_tool_messages_from_pending,
    inspect_engine_binary,
    run_scripted_session,
)


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

    PURE_ORACLE_MODULES = frozenset(
        {
            "delivery_log.py",
            "reference_resolver.py",
            "checker.py",
            "generators.py",
            "shrinker.py",
            "selectors.py",
            "friction_probe.py",
        }
    )

    def test_zero_product_grounding_imports_or_subprocesses(self) -> None:
        py_files = sorted(GROUNDING_DIR.glob("*.py"))
        self.assertGreaterEqual(len(py_files), 6)
        for path in py_files:
            if path.name == pathlib.Path(__file__).name:
                continue
            source = path.read_text(encoding="utf-8")
            tree = ast.parse(source, filename=str(path))
            is_pure = path.name in self.PURE_ORACLE_MODULES
            for node in ast.walk(tree):
                if isinstance(node, ast.Import):
                    for alias in node.names:
                        if is_pure:
                            self.assertNotIn("subprocess", alias.name)
                        for forbidden in self.FORBIDDEN_TOKENS:
                            self.assertNotIn(forbidden, alias.name)
                elif isinstance(node, ast.ImportFrom):
                    mod = node.module or ""
                    if is_pure:
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
        self.assertEqual(summary["fixed_sidecar_slots"], 173)
        self.assertEqual(summary["fixed_in_process_slots"], 30)
        self.assertEqual(summary["fixed_base_slots"], 203)
        self.assertEqual(summary["fixed_tripled_per_platform"], 435)
        self.assertEqual(summary["fixed_both_platforms"], 870)
        self.assertEqual(summary["generated_per_platform_per_seed"], 23700)
        self.assertEqual(len(PREFLIGHT_COVERAGE_ROWS), 7)
        self.assertEqual({f.finding_id for f in GB01_FINDINGS}, {"F1", "F2", "F3"})
        f3 = next(f for f in GB01_FINDINGS if f.finding_id == "F3")
        for assertion in DECISION_005_V04_ASSERTIONS:
            self.assertIn(assertion, f3.assertions)


class TestDecision007ReviewFixes(unittest.TestCase):
    """Comprehensive tests for GB02-R1 through GB02-R6 and generator enhancements."""

    def setUp(self) -> None:
        self.scope = PermittedScope(
            session_id="sess-root",
            worker_leg="",
            round_id="round-1",
            intent_window="intent-1",
        )

    # --- GB02-R1: Verdict join, support verification, exact typed verdicts ---

    def test_r1_misbound_resolved_handle_and_reanchored_line_fail(self) -> None:
        rec1 = _make_read_record(handle="read#1", session_id="sess-root")
        rec2 = _make_read_record(handle="read#2", path="internal/other.go", session_id="sess-root")
        log = DeliveryLog(records=(rec1, rec2))
        cit = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="c1")

        # Misbound resolved_handle: product claims read#2 for a read#1:11 citation -> MUST FAIL
        bad_handle = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#2"),),
            records=(_matching_product_record(rec1), _matching_product_record(rec2)),
        )
        res = check_slot(log, [cit], self.scope, bad_handle)
        self.assertFalse(res.passed)
        self.assertEqual(res.defects[0].kind, "false_accept")
        self.assertIn("Misbound resolved_handle", res.defects[0].detail)

        # Misbound reanchored_line -> MUST FAIL
        cit_reanchor = Citation(
            citation_type="handle",
            raw='[read#1:10 "return nil"]',
            handle="read#1",
            start_line=10,
            excerpt="return nil",
            citation_id="c1",
        )
        bad_reanchor = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1", reanchored_line=12),),
            records=(_matching_product_record(rec1), _matching_product_record(rec2)),
        )
        res_re = check_slot(log, [cit_reanchor], self.scope, bad_reanchor)
        self.assertFalse(res_re.passed)
        self.assertEqual(res_re.defects[0].kind, "false_accept")
        self.assertIn("Misbound reanchored_line", res_re.defects[0].detail)

    def test_r1_reordered_missing_duplicate_and_exact_typed_verdicts(self) -> None:
        rec1 = _make_read_record(handle="read#1", session_id="sess-root", line_numbered=True)
        rec2 = _make_read_record(
            handle="read#2",
            path="internal/unnumbered.go",
            raw_bytes=b"alpha\nbeta\ngamma\n",
            ranges=(LineRange(10, 12),),
            line_numbered=False,
            session_id="sess-root",
        )
        log = DeliveryLog(records=(rec1, rec2))
        c1 = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="occ-1")
        c2 = Citation(citation_type="handle", raw="[read#2:11]", handle="read#2", start_line=11, citation_id="occ-2")
        recs = (_matching_product_record(rec1), _matching_product_record(rec2))

        # Reordered verdicts join by occurrence_id and PASS (including exact typed verdicts!)
        reordered = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict(citation_id="occ-2", verdict="traced", resolved_handle="read#2"),
                ProductVerdict(citation_id="occ-1", verdict="matched", resolved_handle="read#1"),
            ),
            records=recs,
        )
        res_ok = check_slot(log, [c1, c2], self.scope, reordered, assert_exact_verdicts=True)
        self.assertTrue(res_ok.passed, res_ok.defects)

        # Wrong exact typed verdict in R02/R03 mode (matched instead of traced) fails
        wrong_exact = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict(citation_id="occ-1", verdict="matched", resolved_handle="read#1"),
                ProductVerdict(citation_id="occ-2", verdict="matched", resolved_handle="read#2"),
            ),
            records=recs,
        )
        res_exact_fail = check_slot(log, [c1, c2], self.scope, wrong_exact, assert_exact_verdicts=True)
        self.assertFalse(res_exact_fail.passed)
        self.assertIn("Exact typed verdict mismatch", res_exact_fail.defects[0].detail)

        # Missing verdict fails
        missing = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="occ-1", verdict="matched", resolved_handle="read#1"),),
            records=recs,
        )
        res_missing = check_slot(log, [c1, c2], self.scope, missing)
        self.assertFalse(res_missing.passed)
        self.assertTrue(any("Missing product verdict" in d.detail for d in res_missing.defects))

        # Duplicate verdict fails
        dup = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict(citation_id="occ-1", verdict="matched", resolved_handle="read#1"),
                ProductVerdict(citation_id="occ-1", verdict="matched", resolved_handle="read#1"),
                ProductVerdict(citation_id="occ-2", verdict="traced", resolved_handle="read#2"),
            ),
            records=recs,
        )
        res_dup = check_slot(log, [c1, c2], self.scope, dup)
        self.assertFalse(res_dup.passed)
        self.assertTrue(any("Duplicate product verdict" in d.detail for d in res_dup.defects))

    # --- GB02-R2: Evidence comparison, empty vs missing body, duplicates, defect preservation ---

    def test_r2_missing_body_vs_empty_body_and_duplicate_records(self) -> None:
        rec = _make_read_record(handle="read#1", session_id="sess-root")
        log = DeliveryLog(records=(rec,))
        cit = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="c1")

        # Clearing both raw_bytes and body_sha256 for a nonempty delivered body MUST flag recording_defect
        cleared_body = ProductRecord(
            handle=rec.handle,
            kind=rec.kind,
            session_id=rec.session_id,
            worker_leg=rec.worker_leg,
            source_revision=rec.source_revision,
            delivered_ranges=rec.delivered_ranges,
            body_sha256="",
            raw_bytes=None,
            path=rec.path,
        )
        outcome_cleared = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1"),),
            records=(cleared_body,),
        )
        res_cleared = check_slot(log, [cit], self.scope, outcome_cleared, require_friction=True)
        self.assertFalse(res_cleared.passed)
        # Known defect classification preserved even when friction instrumentation is missing!
        self.assertEqual(res_cleared.status, "fail")
        self.assertEqual(res_cleared.defects[0].kind, "recording_defect")
        self.assertIn("missing body bytes", res_cleared.defects[0].detail)
        self.assertTrue(res_cleared.missing_instrumentation)

        # Explicitly empty delivered body with raw_bytes=b"" passes
        empty_rec = _make_read_record(
            handle="command#1",
            kind="command",
            path="",
            ranges=(),
            raw_bytes=b"",
            line_numbered=False,
            session_id="sess-root",
        )
        empty_prod = ProductRecord(
            handle="command#1",
            kind="command",
            session_id="sess-root",
            worker_leg="",
            source_revision="rev-1",
            raw_bytes=b"",
        )
        res_empty = check_slot(
            DeliveryLog(records=(empty_rec,)),
            [],
            self.scope,
            SurfaceOutcome(surface="coordinator_closeout", records=(empty_prod,)),
        )
        self.assertTrue(res_empty.passed, res_empty.defects)

        # Duplicate identical product records MUST flag recording_defect
        prod = _matching_product_record(rec)
        dup_outcome = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1"),),
            records=(prod, prod),
        )
        res_dup = check_slot(log, [cit], self.scope, dup_outcome)
        self.assertFalse(res_dup.passed)
        self.assertTrue(any("Duplicate product record identity" in d.detail for d in res_dup.defects))

    # --- GB02-R3: Dispatch identity, round boundary, cited_results, refresh, URL isolation ---

    def test_r3_dispatch_legs_child_sessions_and_host_assembled_union(self) -> None:
        root_rec = _make_read_record(handle="read#1", path="root.go", session_id="sess-root", worker_leg="")
        leg_a_rec = _make_read_record(handle="read#1", path="leg_a.go", session_id="sess-child-a", worker_leg="")
        leg_b_rec = _make_read_record(handle="read#1", path="leg_b.go", session_id="sess-child-b", worker_leg="")
        unauth_rec = _make_read_record(handle="read#1", path="unauth.go", session_id="sess-child-c", worker_leg="")
        parent_url = _make_read_record(
            handle="web#1",
            kind="web",
            path="",
            ranges=(),
            url="https://parent.example.test/doc",
            raw_bytes=b"parent web",
            session_id="sess-root",
            worker_leg="",
        )
        log = DeliveryLog(records=(root_rec, leg_a_rec, leg_b_rec, unauth_rec, parent_url))

        parent_scope = PermittedScope(
            session_id="sess-root",
            worker_leg="",
            dispatched_legs={"leg-a": "sess-child-a", "leg-b": "sess-child-b"},
            round_id="round-1",
            intent_window="intent-1",
        )

        # Same ordinal read#1 in parent, leg-a, and leg-b each resolve unambiguously
        r_root = resolve_citation(log, Citation(citation_type="handle_path", handle="read#1", path="root.go", start_line=11), parent_scope)
        r_a = resolve_citation(log, Citation(citation_type="handle_path", handle="leg-a:read#1", path="leg_a.go", start_line=11), parent_scope)
        r_b = resolve_citation(log, Citation(citation_type="handle_path", handle="leg-b:read#1", path="leg_b.go", start_line=11), parent_scope)
        self.assertEqual((r_root.verdict_class, r_root.supporting_handles), ("grounded", ("read#1",)))
        self.assertEqual((r_a.verdict_class, r_a.supporting_handles), ("grounded", ("leg-a:read#1",)))
        self.assertEqual((r_b.verdict_class, r_b.supporting_handles), ("grounded", ("leg-b:read#1",)))

        # Unauthorized child leg-c is rejected
        r_unauth = resolve_citation(log, Citation(citation_type="handle", handle="leg-c:read#1", start_line=11), parent_scope)
        self.assertEqual(r_unauth.verdict_class, "ungrounded")
        self.assertEqual(r_unauth.reason, "wrong_leg")

        # Worker in sess-child-a CANNOT borrow parent's URL evidence
        worker_scope = PermittedScope(session_id="sess-child-a", worker_leg="leg-a", round_id="round-1")
        r_worker_url = resolve_citation(log, Citation(citation_type="url", url="https://parent.example.test/doc"), worker_scope)
        self.assertEqual(r_worker_url.verdict_class, "ungrounded")
        self.assertEqual(r_worker_url.reason, "url_not_observed")

        # Host-assembled closeout allows true scoped union (read#1, leg-a:read#1) and rejects leg-c:read#1
        ok_assembled = SurfaceOutcome(
            surface="coordinator_closeout",
            traced=False,
            host_assembled=True,
            attached_handles=("read#1", "leg-a:read#1"),
        )
        self.assertTrue(check_slot(log, [], parent_scope, ok_assembled, check_records=False).passed)

        bad_assembled = SurfaceOutcome(
            surface="coordinator_closeout",
            traced=False,
            host_assembled=True,
            attached_handles=("read#1", "leg-c:read#1"),
        )
        res_bad_asm = check_slot(log, [], parent_scope, bad_assembled, check_records=False)
        self.assertFalse(res_bad_asm.passed)
        self.assertEqual(res_bad_asm.defects[0].kind, "false_accept")

    def test_r3_prior_round_cited_results_and_refresh_invalidation(self) -> None:
        r1 = _make_read_record(handle="read#1", round_id="round-1", session_id="sess-w", worker_leg="leg-a")
        log = DeliveryLog(records=(r1,))
        cit = Citation(citation_type="handle", handle="read#1", start_line=11)

        # Active round-2 without cited_results -> prior-round file handle MUST NOT pass
        scope_r2 = PermittedScope(session_id="sess-w", worker_leg="leg-a", round_id="round-2")
        v_blocked = resolve_citation(log, cit, scope_r2)
        self.assertEqual(v_blocked.verdict_class, "ungrounded")
        self.assertEqual(v_blocked.reason, "prior_round_source_evidence")

        # Explicitly listed in cited_results -> allowed as historical provenance
        scope_cited = PermittedScope(
            session_id="sess-w",
            worker_leg="leg-a",
            round_id="round-2",
            cited_results_handles=frozenset({"read#1"}),
        )
        v_cited = resolve_citation(log, cit, scope_cited)
        self.assertEqual(v_cited.verdict_class, "grounded")
        self.assertEqual(v_cited.reason, "cited_results_historical")

        # Workspace refresh invalidates stale source observations even if in cited_results
        scope_refreshed = PermittedScope(
            session_id="sess-w",
            worker_leg="leg-a",
            round_id="round-2",
            cited_results_handles=frozenset({"read#1"}),
            workspace_refreshed=True,
        )
        v_invalid = resolve_citation(log, cit, scope_refreshed)
        self.assertEqual(v_invalid.verdict_class, "ungrounded")
        self.assertEqual(v_invalid.reason, "stale_round_invalidated")

    # --- GB02-R4: Artifact permissions (None vs empty set vs permitted vs absent) ---

    def test_r4_artifact_permissions_present_denied_empty_set_and_absent(self) -> None:
        art_id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
        art_rec = _make_read_record(
            handle="artifact#1",
            kind="artifact",
            path="",
            ranges=(),
            artifact_id=art_id,
            raw_bytes=b"PNG",
            line_numbered=False,
            session_id="sess-root",
        )
        log = DeliveryLog(records=(art_rec,))
        cit_present = Citation(citation_type="artifact", artifact_id=art_id)
        cit_absent = Citation(citation_type="artifact", artifact_id="bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

        # 1. Present and permitted (explicit permit set)
        s_permitted = PermittedScope(session_id="sess-root", permitted_artifacts=frozenset({art_id}))
        self.assertEqual(resolve_citation(log, cit_present, s_permitted).verdict_class, "grounded")

        # 2. Present and unrestricted (permitted_artifacts=None)
        s_none = PermittedScope(session_id="sess-root", permitted_artifacts=None)
        self.assertEqual(resolve_citation(log, cit_present, s_none).verdict_class, "grounded")

        # 3. Present and DENIED by explicitly empty permit set frozenset()
        s_empty = PermittedScope(session_id="sess-root", permitted_artifacts=frozenset())
        v_denied = resolve_citation(log, cit_present, s_empty)
        self.assertEqual(v_denied.verdict_class, "ungrounded")
        self.assertEqual(v_denied.reason, "artifact_not_permitted")

        # 4. Absent artifact
        v_absent = resolve_citation(log, cit_absent, s_none)
        self.assertEqual(v_absent.verdict_class, "ungrounded")
        self.assertEqual(v_absent.reason, "artifact_absent")

    # --- GB02-R5: Wording result attachment diff + unknown preserves defects ---

    def test_r5_wording_attachment_change_and_defect_preservation_with_unknown(self) -> None:
        r1 = _make_read_record(handle="read#1", session_id="sess-root")
        r2 = _make_read_record(handle="read#2", path="internal/other.go", session_id="sess-root")
        log = DeliveryLog(records=(r1, r2))
        recs = (_matching_product_record(r1), _matching_product_record(r2))
        f0 = FrictionCounters()
        f1 = FrictionCounters(host_assembled_closeouts=1)

        # Changing attached_handles from ("read#1",) to ("read#2",) between wordings MUST fail,
        # AND a later wording missing friction counters MUST NOT discard the established defect!
        outcomes = [
            SurfaceOutcome(
                surface="coordinator_closeout",
                wording="confident",
                records=recs,
                traced=False,
                host_assembled=True,
                attached_handles=("read#1",),
                friction_before=f0,
                friction_after=f1,
            ),
            SurfaceOutcome(
                surface="coordinator_closeout",
                wording="hedged",
                records=recs,
                traced=False,
                host_assembled=True,
                attached_handles=("read#2",),
                friction_before=f0,
                friction_after=f1,
            ),
            SurfaceOutcome(
                surface="coordinator_closeout",
                wording="completion_asserting",
                records=recs,
                traced=False,
                host_assembled=True,
                attached_handles=("read#1",),
                friction_before=None,  # Missing instrumentation on 3rd wording
                friction_after=None,
            ),
        ]
        res = check_wording_triad(log, [], self.scope, outcomes, require_friction=True)
        self.assertFalse(res.passed)
        self.assertEqual(res.status, "fail")
        self.assertTrue(any(d.kind == "wording_defect" for d in res.defects))
        self.assertIn("completion_asserting", res.missing_instrumentation)

        # Per-wording delivery log drift is also flagged
        drifted_logs = {
            "confident": log,
            "hedged": DeliveryLog(records=(r1,)),
            "completion_asserting": log,
        }
        res_drift = check_wording_triad(
            drifted_logs,
            [],
            self.scope,
            [
                SurfaceOutcome(surface="coordinator_closeout", wording="confident", records=recs, traced=False, host_assembled=True, attached_handles=("read#1",)),
                SurfaceOutcome(surface="coordinator_closeout", wording="hedged", records=(_matching_product_record(r1),), traced=False, host_assembled=True, attached_handles=("read#1",)),
                SurfaceOutcome(surface="coordinator_closeout", wording="completion_asserting", records=recs, traced=False, host_assembled=True, attached_handles=("read#1",)),
            ],
        )
        self.assertFalse(res_drift.passed)
        self.assertTrue(any(d.kind == "recording_defect" for d in res_drift.defects))

    # --- GB02-R6: Inclusive range vs point re-anchoring & meaningful-excerpt floor ---

    def test_r6_uncovered_range_with_unique_excerpt_rejected_and_floor_enforced(self) -> None:
        rec = _make_read_record(
            handle="read#1",
            raw_bytes=b"10: func Run() error {\n11:     return nil\n12: }\n",
            ranges=(LineRange(10, 12),),
            session_id="sess-root",
        )
        log = DeliveryLog(records=(rec,))

        # Range 11-100 with unique excerpt "return nil" MUST be rejected (13-100 undelivered!)
        uncovered_range = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw='[read#1:11-100 "return nil"]',
                handle="read#1",
                start_line=11,
                end_line=100,
                excerpt="return nil",
            ),
            self.scope,
        )
        self.assertEqual(uncovered_range.verdict_class, "ungrounded")
        self.assertEqual(uncovered_range.reason, "uncovered_range")

        # Valid inclusive range 10-12 with matching excerpt inside span -> grounded
        valid_range = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw='[read#1:10-12 "return nil"]',
                handle="read#1",
                start_line=10,
                end_line=12,
                excerpt="return nil",
            ),
            self.scope,
        )
        self.assertEqual(valid_range.verdict_class, "grounded")

        # Valid single-point correction (line 10 -> re-anchored to 11 via unique excerpt) -> grounded
        point_fix = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw='[read#1:10 "return nil"]',
                handle="read#1",
                start_line=10,
                excerpt="return nil",
            ),
            self.scope,
        )
        self.assertEqual(point_fix.verdict_class, "grounded")
        self.assertEqual(point_fix.reanchored_line, 11)

        # Meaningful-excerpt floor: tiny 1-2 char excerpt rejected
        below_floor = resolve_citation(
            log,
            Citation(
                citation_type="handle",
                raw='[read#1:12 "}"]',
                handle="read#1",
                start_line=12,
                excerpt="}",
            ),
            self.scope,
        )
        self.assertEqual(below_floor.verdict_class, "ungrounded")
        self.assertEqual(below_floor.reason, "excerpt_below_floor")

    # --- Platform path-case & R04 spans ---

    def test_platform_path_case_and_r04_exact_spans(self) -> None:
        darwin_case = generate_path_case_contrast("20261127", 0, platform="darwin")
        linux_case = generate_path_case_contrast("20261127", 0, platform="linux")
        self.assertEqual(
            resolve_citation(darwin_case.delivery_log, darwin_case.citation, darwin_case.scope).verdict_class,
            "grounded",
        )
        self.assertEqual(
            resolve_citation(linux_case.delivery_log, linux_case.citation, linux_case.scope).verdict_class,
            "ungrounded",
        )

        # R04 exact source spans and HTTP(S) link syntax vs destination_observed distinction
        saw_observed_link = False
        saw_unobserved_link = False
        for idx in range(30):
            r04 = generate_r04("20261127", idx)
            self.assertEqual(len(r04.spans), r04.total_spans)
            for span in r04.spans:
                self.assertEqual(r04.markdown[span.start : span.end], span.text)
                if span.node_kind == "citation_http_link":
                    self.assertTrue(span.is_citation_attempt)
                    self.assertTrue(span.syntax_valid)
                    if span.destination_observed is True:
                        saw_observed_link = True
                    elif span.destination_observed is False:
                        saw_unobserved_link = True
        self.assertTrue(saw_observed_link)
        self.assertTrue(saw_unobserved_link)


class TestDecision008And009RunnerAndProbes(unittest.TestCase):
    """Tests for sidecar_driver, friction_probe, cli, S06 boundary, and D009 readback."""

    def test_d009_missing_resolved_handle_and_reanchored_line_never_silently_pass(self) -> None:
        rec = _make_read_record(handle="read#1", session_id="sess-1")
        log = DeliveryLog(records=(rec,))
        scope = PermittedScope(session_id="sess-1", round_id="round-1", intent_window="intent-1")
        prod_rec = _matching_product_record(rec)

        # Missing resolved_handle on a grounded verdict with expected handle -> status="unknown", NOT pass!
        cit = Citation(citation_type="handle", raw="[read#1:11]", handle="read#1", start_line=11, citation_id="c1")
        missing_handle = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle=""),),
            records=(prod_rec,),
        )
        res = check_slot(log, [cit], scope, missing_handle)
        self.assertFalse(res.passed)
        self.assertEqual(res.status, "unknown")
        self.assertIn("missing resolved_handle readback", res.missing_instrumentation)

        # Missing reanchored_line on a re-anchored citation -> status="unknown", NOT pass!
        cit_re = Citation(
            citation_type="handle",
            raw='[read#1:10 "return nil"]',
            handle="read#1",
            start_line=10,
            excerpt="return nil",
            citation_id="c1",
        )
        missing_re = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(ProductVerdict(citation_id="c1", verdict="matched", resolved_handle="read#1", reanchored_line=None),),
            records=(prod_rec,),
        )
        res_re = check_slot(log, [cit_re], scope, missing_re)
        self.assertFalse(res_re.passed)
        self.assertEqual(res_re.status, "unknown")
        self.assertIn("missing reanchored_line readback", res_re.missing_instrumentation)

    def test_d008_s06_historical_unknown_intent_boundary_pre_vs_post_batch(self) -> None:
        # Verify selector manifest includes S06-historical-unknown-intent-boundary
        cov05 = next(r for r in PREFLIGHT_COVERAGE_ROWS if r.row_id == "COV-05")
        for assertion in DECISION_008_S06_ASSERTIONS:
            self.assertIn(assertion, cov05.assertions)

        scope = PermittedScope(session_id="sess-s06", worker_leg="", round_id="round-1", intent_window="intent-2")
        prior_window_rec = _make_read_record(
            handle="read#1",
            session_id="sess-s06",
            intent_window="intent-1",
        )
        pending_batch_rec = _make_read_record(
            handle="task#1",
            kind="task",
            path="",
            ranges=(),
            raw_bytes=b"dispatched worker leg-a",
            line_numbered=False,
            session_id="sess-s06",
            intent_window="intent-2",
        )
        cit_task = Citation(citation_type="handle", raw="[task#1]", handle="task#1", citation_id="n1")

        # BEFORE carrier's tool batch executes: task#1 is NOT yet in the delivery log -> ungrounded!
        pre_batch_log = DeliveryLog(records=(prior_window_rec,))
        self.assertFalse(has_citable_observation(pre_batch_log, scope))
        v_pre = resolve_citation(pre_batch_log, cit_task, scope)
        self.assertEqual(v_pre.verdict_class, "ungrounded")
        self.assertEqual(v_pre.reason, "unknown_handle")

        # AFTER carrier's tool batch executes: task#1 IS in intent-2 -> grounded!
        post_batch_log = pre_batch_log.append(pending_batch_rec)
        self.assertTrue(has_citable_observation(post_batch_log, scope))
        v_post = resolve_citation(post_batch_log, cit_task, scope)
        self.assertEqual(v_post.verdict_class, "grounded")
        self.assertEqual(v_post.supporting_handles, ("task#1",))

    def test_friction_probe_extracts_observable_counters_and_marks_friction_units_unknown(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            db_path = pathlib.Path(tmp) / "store.db"
            with contextlib.closing(sqlite3.connect(db_path)) as conn:
                conn.execute(
                    "CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, "
                    "grounding_json TEXT, workflow_feedback_json TEXT, host_signal_id TEXT, kind TEXT)"
                )
                conn.execute(
                    "INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "m1",
                        "sess-f1",
                        "assistant",
                        "Answer [read#1:1]",
                        json.dumps({"retry_count": 2, "host_assembled": True}),
                        "",
                        "",
                        "",
                    ),
                )
                conn.execute(
                    "INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "m2",
                        "sess-f1",
                        "system",
                        "withheld",
                        json.dumps({"hint_code": "MID_TURN_NOTE_UNGROUNDED"}),
                        "",
                        "",
                        "host_feedback",
                    ),
                )
                conn.commit()

            sse = [
                {"id": "ev-1", "session_id": "sess-f1", "code": "COORDINATOR_GROUNDING_ESCALATED"},
            ]
            snap = probe_store_db(db_path, "sess-f1", sse_events=sse)
            self.assertEqual(snap.repair_attempts.value, 2)
            self.assertEqual(snap.host_assembled_closeouts.value, 1)
            self.assertEqual(snap.withheld_notes.value, 1)
            self.assertEqual(snap.breaker_advisories.value, 1)
            self.assertEqual(snap.friction_units.status, "unknown")
            self.assertIsNone(snap.friction_units.value)
            self.assertEqual(snap.unknown_counters(), ("friction_units",))
            self.assertIsNone(snap.to_friction_counters())

        report = friction_observability_report()
        self.assertFalse(report["counters"]["friction_units"]["sidecar_observable"])
        self.assertTrue(report["counters"]["host_assembled_closeouts"]["sidecar_observable"])

    def test_sidecar_driver_mock_http_and_sqlite_capture(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            # Create a dummy engine binary file and test SHA-256 verification (Decision 009)
            fake_bin = tmp_path / "lycaon-dev"
            fake_bin.write_bytes(b"fake-d1-binary-bytes")
            expected_sha = hashlib.sha256(b"fake-d1-binary-bytes").hexdigest()
            prov = inspect_engine_binary(fake_bin, expected_sha256=expected_sha)
            self.assertEqual(prov.sha256, expected_sha)
            with self.assertRaises(ValueError):
                inspect_engine_binary(fake_bin, expected_sha256="0" * 64)

            # Seed a temporary SQLite store.db with product evidence + closeout grounding_json
            workspace = tmp_path / "workspace"
            workspace.mkdir()
            sample_source = "package sample\n\nfunc Hello() {}\n"
            (workspace / "sample.go").write_text(sample_source, encoding="utf-8")
            sample_source_sha = hashlib.sha256(sample_source.encode("utf-8")).hexdigest()
            delivered_body = "[read#1 sample.go:1-3]\n1: package sample\n2: \n3: func Hello() {}\n"
            body_sha = sample_source_sha

            db_path = tmp_path / "store.db"
            with contextlib.closing(sqlite3.connect(db_path)) as conn:
                conn.execute(
                    "CREATE TABLE evidence_records (session_id TEXT, handle TEXT, kind TEXT, shape TEXT, "
                    "path TEXT, url TEXT, line_ranges TEXT, content_blob_sha256 TEXT, marks_untrusted INTEGER, superseded_by TEXT)"
                )
                conn.execute(
                    "CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, "
                    "grounding_json TEXT, workflow_feedback_json TEXT, host_signal_id TEXT, kind TEXT)"
                )
                conn.execute(
                    "INSERT INTO evidence_records VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "sess-mock",
                        "read#1",
                        "read",
                        "file_region",
                        "sample.go",
                        "",
                        json.dumps(
                            {
                                "v": 1,
                                "ranges": [{"start": 1, "end": 3}],
                                "source_delivery": {"source": {"SHA256": sample_source_sha}},
                            }
                        ),
                        body_sha,
                        0,
                        None,
                    ),
                )
                conn.execute(
                    "INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "m-close",
                        "sess-mock",
                        "assistant",
                        "Verified [read#1:3].",
                        json.dumps({"traced": True, "retry_count": 0, "host_assembled": False}),
                        "",
                        "",
                        "",
                    ),
                )
                conn.execute(
                    "INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "m-unsup",
                        "sess-unsup",
                        "assistant",
                        "Claimed [read#1:99].",
                        json.dumps({"traced": False, "hint_code": "SYNTH_CITATION_UNVERIFIABLE", "retry_count": 1}),
                        "",
                        "",
                        "",
                    ),
                )
                conn.commit()

            # Spin up a local mock HTTP server simulating D1's exact nested tool_result shape
            state: dict[str, object] = {"turns": {}, "responses": []}

            class MockHandler(BaseHTTPRequestHandler):
                def log_message(self, _fmt: str, *_args: object) -> None:
                    return

                def do_GET(self) -> None:  # noqa: N802
                    parsed = urllib.parse.urlparse(self.path)
                    if parsed.path == "/health":
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(b'{"status":"ok"}')
                        return
                    if parsed.path == "/harness/llm/pending":
                        qs = urllib.parse.parse_qs(parsed.query)
                        sid = (qs.get("session_id") or [""])[0]
                        turns_map = state["turns"]  # type: ignore[assignment]
                        turn_num = int(turns_map.get(sid, 0))  # type: ignore[union-attr]
                        if turn_num == 0:
                            payload = {"id": f"req-{sid}-1", "messages": [{"role": "user", "content": "Read sample.go"}]}
                        else:
                            # Exact D1 nested Message.id, Message.tool_result.tool_call_id,
                            # Message.tool_result.content, and Message.evidence_handles shape
                            # (verified in coordinator-gb02-linux-manual-preflight.md)
                            payload = {
                                "id": f"req-{sid}-2",
                                "messages": [
                                    {"role": "user", "content": "Read sample.go"},
                                    {
                                        "id": f"msg-tool-{sid}-1",
                                        "role": "tool",
                                        "content": "",
                                        "evidence_handles": ["read#1"],
                                        "tool_result": {
                                            "tool_call_id": f"call-{sid}-1",
                                            "tool_name": "read_file",
                                            "content": delivered_body,
                                        },
                                    },
                                ],
                            }
                        body = json.dumps(payload).encode("utf-8")
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(body)
                        return
                    if parsed.path.startswith("/harness/execution/"):
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(b'{"settled":true}')
                        return
                    self.send_response(404)
                    self.end_headers()

                def do_POST(self) -> None:  # noqa: N802
                    length = int(self.headers.get("Content-Length", "0"))
                    raw = self.rfile.read(length).decode("utf-8")
                    if self.path == "/harness/llm/respond":
                        req_obj = json.loads(raw)
                        state["responses"].append(req_obj)  # type: ignore[union-attr]
                        # Increment turn for the session encoded in req-<sid>-<n>
                        rid = str(req_obj.get("id", ""))
                        parts = rid.split("-")
                        sid = "-".join(parts[1:-1]) if len(parts) >= 3 else ""
                        turns_map = state["turns"]  # type: ignore[assignment]
                        turns_map[sid] = int(turns_map.get(sid, 0)) + 1  # type: ignore[index,union-attr]
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(b'{"ok":true}')
                        return
                    self.send_response(404)
                    self.end_headers()

            server = ThreadingHTTPServer(("127.0.0.1", 0), MockHandler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                base_url = f"http://127.0.0.1:{server.server_address[1]}"
                client = SidecarSessionClient(base_url, token="test-token")
                # Intentionally request lines 1-999 in args to prove delivery range (1-3) comes
                # strictly from actual delivered text/tags + fixture bytes, NEVER requested args!
                sup_artifact = run_scripted_session(
                    client,
                    session_id="sess-mock",
                    turns=(
                        ScriptedTurn(
                            tool_calls=({"id": "call-sess-mock-1", "name": "read_file", "args": {"path": "sample.go", "start_line": 1, "end_line": 999}},),
                        ),
                        ScriptedTurn(content="Verified [read#1:3]."),
                    ),
                    db_path=db_path,
                    workspace_dir=workspace,
                    submission_id="sub-1",
                    wait_for_settlement=True,
                    source_revision="",
                    engine_provenance=prov,
                )
                unsup_artifact = run_scripted_session(
                    client,
                    session_id="sess-unsup",
                    turns=(
                        ScriptedTurn(
                            tool_calls=({"id": "call-sess-unsup-1", "name": "read_file", "args": {"path": "sample.go", "start_line": 1, "end_line": 999}},),
                        ),
                        ScriptedTurn(content="Claimed [read#1:99]."),
                    ),
                    db_path=db_path,
                    workspace_dir=workspace,
                    submission_id="sub-2",
                    wait_for_settlement=True,
                    source_revision="",
                    engine_provenance=prov,
                )
            finally:
                server.shutdown()
                server.server_close()

            self.assertEqual(len(sup_artifact.raw_pending_requests), 2)
            self.assertEqual(len(sup_artifact.delivered_tool_messages), 1)
            self.assertEqual(sup_artifact.delivered_tool_messages[0]["call_id"], "call-sess-mock-1")
            self.assertEqual(sup_artifact.delivered_tool_messages[0]["evidence_handles"], ["read#1"])
            self.assertEqual(sup_artifact.delivered_tool_messages[0]["delivery_tags"], ["[read#1 sample.go:1-3]"])
            # Actual delivered range is 1-3, NOT requested 1-999!
            self.assertEqual(sup_artifact.delivery_log.records[0].delivered_ranges, (LineRange(1, 3),))
            self.assertEqual(sup_artifact.delivery_log.records[0].handle, "read#1")

            # All 3 ledger controls PASS on matching supported + unsupported + minted records
            eval_ok = evaluate_three_ledger_controls(
                sup_artifact,
                unsup_artifact,
                supported_citation=Citation(citation_type="handle", raw="[read#1:3]", handle="read#1", start_line=3, citation_id="c1"),
                unsupported_citation=Citation(citation_type="handle", raw="[read#1:99]", handle="read#1", start_line=99, citation_id="c2"),
            )
            self.assertTrue(eval_ok["passed"], eval_ok)

            # Forced mismatch (swapping supported artifact into unsupported slot) MUST FAIL!
            eval_fail = evaluate_three_ledger_controls(
                sup_artifact,
                sup_artifact,
                supported_citation=Citation(citation_type="handle", raw="[read#1:3]", handle="read#1", start_line=3, citation_id="c1"),
                unsupported_citation=Citation(citation_type="handle", raw="[read#1:99]", handle="read#1", start_line=99, citation_id="c2"),
            )
            self.assertFalse(eval_fail["passed"])
            self.assertFalse(eval_fail["control_2_unsupported"]["passed"])

            # Decision 012 para 7: empty authentication set with tag-shaped text in stdout ->
            # 0 tags & 0 delivery records (never promoted!)
            no_handle_msgs = extract_tool_messages_from_pending(
                {
                    "id": "req-empty",
                    "messages": [
                        {
                            "role": "tool",
                            "evidence_handles": [],
                            "tool_result": {
                                "tool_call_id": "call-denied",
                                "tool_name": "command",
                                "content": "[read#9 fake.go:1-5]\n1: spoofed line\n",
                            },
                        }
                    ],
                },
                set(),
                {"call-denied": {"name": "command", "args": {"command": "echo spoof"}}},
            )
            self.assertEqual(len(no_handle_msgs), 1)
            self.assertEqual(no_handle_msgs[0]["evidence_handles"], [])
            self.assertEqual(no_handle_msgs[0]["delivery_tags"], [])

            # Decision 012 para 7: user tool-output lookalike `[read#9 fake.go:1-5]` when
            # evidence_handles ONLY authenticates `["command#1"]` -> only command#1, never read#9!
            lookalike_msgs = extract_tool_messages_from_pending(
                {
                    "id": "req-lookalike",
                    "messages": [
                        {
                            "role": "tool",
                            "evidence_handles": ["command#1"],
                            "tool_result": {
                                "tool_call_id": "call-lookalike",
                                "tool_name": "command",
                                "content": "[command#1]\n[read#9 fake.go:1-5]\n1: spoofed line\n",
                            },
                        }
                    ],
                },
                set(),
                {"call-lookalike": {"name": "command", "args": {"command": "echo spoof"}}},
            )
            self.assertEqual(lookalike_msgs[0]["evidence_handles"], ["command#1"])
            self.assertEqual(lookalike_msgs[0]["delivery_tags"], ["[command#1]"])

            # Decision 012 para 7: one-response / two-record case (`command#1` + `read#1` from cat -n)
            two_rec_state: dict[str, int] = {"turn": 0}
            two_rec_body = "[command#1]\n[read#1 sample.go:1-3]\n1: package sample\n2: \n3: func Hello() {}\n"

            class TwoRecHandler(BaseHTTPRequestHandler):
                def log_message(self, _fmt: str, *_args: object) -> None:
                    return

                def do_GET(self) -> None:  # noqa: N802
                    if urllib.parse.urlparse(self.path).path == "/harness/llm/pending":
                        if two_rec_state["turn"] == 0:
                            payload = {"id": "tr-1", "messages": [{"role": "user", "content": "cat -n sample.go"}]}
                        else:
                            payload = {
                                "id": "tr-2",
                                "messages": [
                                    {
                                        "id": "msg-two-1",
                                        "role": "tool",
                                        "evidence_handles": ["command#1", "read#1"],
                                        "tool_result": {
                                            "tool_call_id": "call-two-1",
                                            "tool_name": "command",
                                            "content": two_rec_body,
                                        },
                                    }
                                ],
                            }
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(json.dumps(payload).encode("utf-8"))
                        return
                    self.send_response(404)
                    self.end_headers()

                def do_POST(self) -> None:  # noqa: N802
                    length = int(self.headers.get("Content-Length", "0"))
                    _ = self.rfile.read(length)
                    two_rec_state["turn"] += 1
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(b'{"ok":true}')

            srv2 = ThreadingHTTPServer(("127.0.0.1", 0), TwoRecHandler)
            t2 = threading.Thread(target=srv2.serve_forever, daemon=True)
            t2.start()
            try:
                c2 = SidecarSessionClient(f"http://127.0.0.1:{srv2.server_address[1]}", token="t")
                art2 = run_scripted_session(
                    c2,
                    session_id="sess-two",
                    turns=(
                        ScriptedTurn(tool_calls=({"id": "call-two-1", "name": "command", "args": {"command": "cat -n sample.go"}},)),
                        ScriptedTurn(content="Done [read#1:3]."),
                    ),
                    workspace_dir=workspace,
                )
            finally:
                srv2.shutdown()
                srv2.server_close()

            # Raw message preserved ONCE; two separately attributed DeliveryRecords derived
            self.assertEqual(len(art2.delivered_tool_messages), 1)
            self.assertEqual(len(art2.delivery_log.records), 2)
            by_h = {r.handle: r for r in art2.delivery_log.records}
            self.assertEqual(by_h["command#1"].kind, "command")
            self.assertEqual(by_h["command#1"].shape, "command")
            self.assertEqual(by_h["command#1"].path, "")
            self.assertEqual(by_h["command#1"].delivered_ranges, ())
            self.assertEqual(by_h["command#1"].delivery_tags, ("[command#1]",))
            self.assertEqual(by_h["read#1"].kind, "read")
            self.assertEqual(by_h["read#1"].shape, "file_region")
            self.assertEqual(by_h["read#1"].path, "sample.go")
            self.assertEqual(by_h["read#1"].delivered_ranges, (LineRange(1, 3),))
            self.assertEqual(by_h["read#1"].delivery_tags, ("[read#1 sample.go:1-3]",))
            self.assertEqual(by_h["read#1"].source_revision, sample_source_sha)

    def test_cli_selftest_replay_controls_dry_run_inprocess_and_friction_report(self) -> None:
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = cli_main(["selftest"])
        self.assertEqual(rc, 0)
        self.assertTrue(json.loads(buf.getvalue())["passed"])

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = cli_main(["friction-report"])
        self.assertEqual(rc, 0)
        self.assertIn("d7_compliant_proposal", json.loads(buf.getvalue()))

        # Decision 010 para 11: inprocess requires explicit verified --product-root
        # and constrains overlays strictly to non-shipped *_test.go files.
        with self.assertRaises(ValueError):
            validate_test_only_overlay({"lycaon/internal/evidence/resolve.go": "/tmp/fake.go"})
        with self.assertRaises(ValueError):
            validate_test_only_overlay({"lycaon/internal/evidence/gb02_test.go": "/tmp/not_a_test_file.go"})
        valid_overlay = validate_test_only_overlay(
            {"lycaon/internal/evidence/gb02_inprocess_test.go": "/tmp/gb02_inprocess_test.go"}
        )
        self.assertEqual(len(valid_overlay), 1)

        with tempfile.TemporaryDirectory() as tmp_inp:
            inp_root = pathlib.Path(tmp_inp) / "d1-checkout"
            (inp_root / "lycaon" / "internal" / "evidence").mkdir(parents=True)
            shipped_file = inp_root / "lycaon" / "internal" / "evidence" / "existing_test.go"
            shipped_file.write_text("package evidence\n", encoding="utf-8")

            # Reject replacing an existing file even if it ends with _test.go
            with self.assertRaises(ValueError):
                validate_test_only_overlay(
                    {"lycaon/internal/evidence/existing_test.go": "/tmp/override_test.go"},
                    product_root=inp_root,
                )

            # Reject harness checkout as inprocess --product-root
            with self.assertRaises(ValueError):
                InProcessPlan(product_root=HARNESS_ROOT).verify()

            # Initialize a temp git repo to verify `cli inprocess --product-root ... --dry-run`
            import subprocess as _sp
            _sp.run(["git", "init", "-q", str(inp_root)], check=True)
            _sp.run(["git", "-C", str(inp_root), "config", "user.email", "gb02@example.test"], check=True)
            _sp.run(["git", "-C", str(inp_root), "config", "user.name", "GB02"], check=True)
            _sp.run(["git", "-C", str(inp_root), "add", "."], check=True)
            _sp.run(["git", "-C", str(inp_root), "commit", "-q", "-m", "init"], check=True)
            commit = _sp.check_output(["git", "-C", str(inp_root), "rev-parse", "HEAD"], text=True).strip()
            tree = _sp.check_output(["git", "-C", str(inp_root), "rev-parse", "HEAD^{tree}"], text=True).strip()

            overlay_file = write_test_overlay(
                pathlib.Path(tmp_inp) / "overlay.json",
                {"lycaon/internal/evidence/gb02_added_test.go": "/tmp/gb02_added_test.go"},
                product_root=inp_root,
            )
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = cli_main(
                    [
                        "inprocess",
                        "--product-root",
                        str(inp_root),
                        "--expected-commit",
                        commit,
                        "--expected-tree",
                        tree,
                        "--overlay",
                        str(overlay_file),
                        "--dry-run",
                    ]
                )
            self.assertEqual(rc, 0)
            inp_json = json.loads(buf.getvalue())
            self.assertTrue(inp_json["dry_run"])
            self.assertEqual(inp_json["verified"]["commit"], commit)
            self.assertEqual(inp_json["verified"]["tree"], tree)

        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            # Replay a shrunk artifact JSON
            gen = generate_r06("20261127", 0, variation="path_normalization")
            art = shrink_case(
                ShrinkableCase(delivery_log=gen.delivery_log, scope=gen.scope, citation=gen.citation, seed=gen.seed),
                lambda c: resolve_citation(c.delivery_log, c.citation, c.scope).verdict_class == "grounded",  # type: ignore[arg-type]
            )
            art_file = tmp_path / "shrunk.json"
            art_file.write_text(art.to_json(), encoding="utf-8")
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = cli_main(["replay", str(art_file)])
            self.assertEqual(rc, 0)
            self.assertEqual(json.loads(buf.getvalue())["verdict_class"], "grounded")

            # Controls dry-run with explicit --product-root and expected SHA-256 verification
            product_root = tmp_path / "baseline-d1"
            (product_root / "lycaon" / "config").mkdir(parents=True)
            (product_root / "lycaon-den" / "src-tauri" / "engine-root").mkdir(parents=True)
            fake_bin = tmp_path / "lycaon-dev"
            fake_bin.write_bytes(b"cli-test-bin")
            sha = hashlib.sha256(b"cli-test-bin").hexdigest()
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = cli_main(
                    [
                        "controls",
                        "--engine-bin",
                        str(fake_bin),
                        "--product-root",
                        str(product_root),
                        "--expected-engine-sha256",
                        sha,
                        "--dry-run",
                    ]
                )
            self.assertEqual(rc, 0)
            out_json = json.loads(buf.getvalue())
            self.assertEqual(out_json["engine_provenance"]["sha256"], sha)
            self.assertEqual(out_json["run_manifest"]["product_root"], str(product_root.resolve()))
            self.assertEqual(
                out_json["run_manifest"]["resolved_config_root"],
                str((product_root / "lycaon").resolve()),
            )
            self.assertEqual(
                out_json["run_manifest"]["resolved_engine_root"],
                str((product_root / "lycaon-den" / "src-tauri" / "engine-root").resolve()),
            )

    def test_d009_product_root_binding_and_d2_harness_leak_rejection(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            product_root = tmp_path / "baseline-d1"
            (product_root / "lycaon" / "config").mkdir(parents=True)
            (product_root / "lycaon-den" / "src-tauri" / "engine-root").mkdir(parents=True)
            fake_bin = tmp_path / "lycaon-dev"
            fake_bin.write_bytes(b"d1-binary-payload")
            sha = hashlib.sha256(b"d1-binary-payload").hexdigest()

            proc = IsolatedSidecarProcess(
                fake_bin,
                product_root=product_root,
                config_dir=tmp_path / "cfg",
                workspace_dir=tmp_path / "ws",
                expected_sha256=sha,
            )
            # Decision 011: exact `serve --db <scratch_db>` invocation, NEVER `--addr`
            cmd = proc.build_command()
            self.assertEqual(cmd, [str(fake_bin.resolve()), "serve", "--db", str((tmp_path / "cfg" / "store.db").resolve())])
            self.assertNotIn("--addr", cmd)

            # Decision 010: exact shipped env keys, stripping ambient LYCAON_* vars, no token in manifest
            env = proc.build_process_env(port=49999)
            self.assertEqual(env["LYCAON_CONFIG_ROOT"], str((product_root / "lycaon").resolve()))
            self.assertEqual(
                env["LYCAON_ENGINE_ROOT"],
                str((product_root / "lycaon-den" / "src-tauri" / "engine-root").resolve()),
            )
            self.assertEqual(env["LYCAON_ADDR"], "127.0.0.1:49999")
            self.assertEqual(env["LYCAON_DEV"], "1")
            self.assertEqual(env["LYCAON_HARNESS"], "1")
            self.assertEqual(env["LYCAON_LLM_MOCK"], "1")
            self.assertEqual(env["LYCAON_LLM_MANUAL"], "1")
            self.assertEqual(env["LYCAON_API_TOKEN"], proc.token)
            self.assertNotIn("LYCAON_AUTH_TOKEN", env)
            self.assertNotIn("LYCAON_LLM_PROVIDER", env)
            self.assertNotIn("LYCAON_LISTEN_ADDR", env)
            self.assertNotIn(str(HARNESS_ROOT), env["LYCAON_CONFIG_ROOT"])
            self.assertNotIn(str(HARNESS_ROOT), env["LYCAON_ENGINE_ROOT"])

            manifest = proc.build_run_manifest().to_dict()
            self.assertNotIn(proc.token, json.dumps(manifest))
            self.assertEqual(manifest["product_root"], str(product_root.resolve()))
            self.assertEqual(manifest["resolved_config_root"], env["LYCAON_CONFIG_ROOT"])
            self.assertEqual(manifest["resolved_engine_root"], env["LYCAON_ENGINE_ROOT"])
            self.assertEqual(manifest["engine_bin"], str(fake_bin.resolve()))
            self.assertEqual(manifest["engine_sha256"], sha)
            self.assertIn("engine_buildid", manifest)
            self.assertIn("product_commit", manifest)
            self.assertNotEqual(manifest["harness_head"], "unavailable")

            # Reject D2 harness worktree as product_root
            with self.assertRaises(ValueError):
                IsolatedSidecarProcess(
                    fake_bin,
                    product_root=HARNESS_ROOT,
                    config_dir=tmp_path / "cfg",
                    workspace_dir=tmp_path / "ws",
                )

            # Reject D2 harness root leaked via config_root or engine_root override
            with self.assertRaises(ValueError):
                IsolatedSidecarProcess(
                    fake_bin,
                    product_root=product_root,
                    config_root=HARNESS_ROOT / "lycaon",
                    config_dir=tmp_path / "cfg",
                    workspace_dir=tmp_path / "ws",
                )
            with self.assertRaises(ValueError):
                IsolatedSidecarProcess(
                    fake_bin,
                    product_root=product_root,
                    engine_root=HARNESS_ROOT / "lycaon-den" / "src-tauri" / "engine-root",
                    config_dir=tmp_path / "cfg",
                    workspace_dir=tmp_path / "ws",
                )


    def test_d016_independent_delivered_bytes_and_clipped_blank_lines(self) -> None:
        """Decision 016 controls: (a) tampered delivered bytes fail parity; (b) blank+clipped preserved."""
        from grounding.sidecar_driver import _extract_delivered_read_bytes, _safe_fixture_file

        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            workspace = tmp_path / "workspace"
            workspace.mkdir()
            sample_source = "package sample\n\nfunc Hello() {}\n"
            (workspace / "sample.go").write_text(sample_source, encoding="utf-8")
            sample_source_sha = hashlib.sha256(sample_source.encode("utf-8")).hexdigest()

            # Path traversal outside workspace MUST be rejected
            self.assertIsNone(_safe_fixture_file(workspace, "../../etc/passwd"))

            db_path = tmp_path / "store.db"
            with contextlib.closing(sqlite3.connect(db_path)) as conn:
                conn.execute(
                    "CREATE TABLE evidence_records (session_id TEXT, handle TEXT, kind TEXT, shape TEXT, "
                    "path TEXT, url TEXT, line_ranges TEXT, content_blob_sha256 TEXT, marks_untrusted INTEGER, superseded_by TEXT)"
                )
                conn.execute(
                    "CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, "
                    "grounding_json TEXT, workflow_feedback_json TEXT, host_signal_id TEXT, kind TEXT)"
                )
                conn.execute(
                    "INSERT INTO evidence_records VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "sess-tm",
                        "read#1",
                        "read",
                        "file_region",
                        "sample.go",
                        "",
                        json.dumps(
                            {
                                "v": 1,
                                "ranges": [{"start": 1, "end": 3}],
                                "source_delivery": {"source": {"SHA256": sample_source_sha}},
                            }
                        ),
                        sample_source_sha,
                        0,
                        None,
                    ),
                )
                conn.execute(
                    "INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                    ("m1", "sess-tm", "assistant", "Done [read#1:3].", json.dumps({"traced": True}), "", "", ""),
                )
                conn.commit()

            # Control (a): Fixture file + durable product record both contain value A (`Hello`),
            # while actual delivered body in tool_result.content contains value B (`Tampered`) -> MUST FAIL!
            tampered_state: dict[str, int] = {"turn": 0}
            tampered_stdout = "     1\tpackage sample\n     2\t\n     3\tfunc Tampered() {}\n"
            tampered_body = (
                "[read#1 sample.go:1-3]\n"
                + json.dumps({"program_id": "p1", "state": "completed", "status_code": 0, "stdout": tampered_stdout})
            )

            class TamperedHandler(BaseHTTPRequestHandler):
                def log_message(self, _fmt: str, *_args: object) -> None:
                    return

                def do_GET(self) -> None:  # noqa: N802
                    if urllib.parse.urlparse(self.path).path == "/harness/llm/pending":
                        if tampered_state["turn"] == 0:
                            payload = {"id": "tm-1", "messages": [{"role": "user", "content": "cat -n sample.go"}]}
                        else:
                            payload = {
                                "id": "tm-2",
                                "messages": [
                                    {
                                        "id": "msg-tm-1",
                                        "role": "tool",
                                        "evidence_handles": ["read#1"],
                                        "tool_result": {
                                            "tool_call_id": "call-tm-1",
                                            "tool_name": "command",
                                            "content": tampered_body,
                                        },
                                    }
                                ],
                            }
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(json.dumps(payload).encode("utf-8"))
                        return
                    self.send_response(404)
                    self.end_headers()

                def do_POST(self) -> None:  # noqa: N802
                    length = int(self.headers.get("Content-Length", "0"))
                    _ = self.rfile.read(length)
                    tampered_state["turn"] += 1
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(b'{"ok":true}')

            srv_tm = ThreadingHTTPServer(("127.0.0.1", 0), TamperedHandler)
            t_tm = threading.Thread(target=srv_tm.serve_forever, daemon=True)
            t_tm.start()
            try:
                c_tm = SidecarSessionClient(f"http://127.0.0.1:{srv_tm.server_address[1]}", token="t")
                art_tm = run_scripted_session(
                    c_tm,
                    session_id="sess-tm",
                    turns=(
                        ScriptedTurn(tool_calls=({"id": "call-tm-1", "name": "command", "args": {"command": "cat -n sample.go"}},)),
                        ScriptedTurn(content="Done [read#1:3]."),
                    ),
                    db_path=db_path,
                    workspace_dir=workspace,
                )
            finally:
                srv_tm.shutdown()
                srv_tm.server_close()

            self.assertIn(b"Tampered", art_tm.delivery_log.records[0].raw_bytes)
            self.assertNotEqual(art_tm.delivery_log.records[0].body_sha256, sample_source_sha)
            eval_tm = evaluate_three_ledger_controls(
                art_tm,
                art_tm,
                supported_citation=Citation(citation_type="handle", raw="[read#1:3]", handle="read#1", start_line=3, citation_id="c1"),
                unsupported_citation=Citation(citation_type="handle", raw="[read#1:99]", handle="read#1", start_line=99, citation_id="c2"),
            )
            self.assertFalse(eval_tm["control_3_minting_parity"]["passed"])
            self.assertTrue(any("body digest mismatch" in d["detail"] for d in eval_tm["control_3_minting_parity"]["defects"]))

            # Control (b): Blank line + clipped end (no trailing newline) preserved verbatim
            clipped_env = "[read#1 sample.go:1-3]\n" + json.dumps(
                {"stdout": "     1\tpackage sample\n     2\t\n     3\tfunc Hel"}
            )
            self.assertEqual(
                _extract_delivered_read_bytes(clipped_env),
                b"package sample\n\nfunc Hel",
            )


if __name__ == "__main__":
    unittest.main()
