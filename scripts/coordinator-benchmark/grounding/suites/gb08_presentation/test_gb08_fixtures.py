"""Unit tests for GB08 (Den presentation scenarios, feedback readback, opt-in dev diagnostic)."""
from __future__ import annotations

import unittest

from grounding.delivery_log import (
    DeliveryLog,
    DeliveryRecord,
    LineRange,
    ProductVerdict,
    SurfaceOutcome,
)
from grounding.reference_resolver import Citation, PermittedScope
from grounding.suites.gb08_presentation import (
    DEN_PRESENTATION_SCENARIOS,
    audit_refusal_feedback,
    run_dev_diagnostic,
)


def _make_log() -> tuple[DeliveryLog, PermittedScope]:
    rec = DeliveryRecord(
        tool="read_file",
        arguments={"path": "internal/app.go"},
        raw_bytes=b"package app\n\nfunc Main() {}\n",
        delivered_ranges=(LineRange(1, 3),),
        delivery_tags=("[read#1 internal/app.go:1-3]",),
        line_numbered=True,
        printed_other_numbers=False,
        session_id="sess-gb08",
        worker_leg="",
        round_id="round-1",
        intent_window="intent-1",
        source_revision="rev-1",
        handle="read#1",
        kind="read",
        path="internal/app.go",
        url="",
        artifact_id="",
        call_id="call-read-1",
        fixture_provenance="host",
        shape="file_region",
        trust="trusted",
    )
    return DeliveryLog(records=(rec,)), PermittedScope(session_id="sess-gb08", round_id="round-1", intent_window="intent-1")


class TestGB08PresentationAndDiagnostic(unittest.TestCase):
    def test_all_five_den_presentation_states_defined(self) -> None:
        states = {s.state for s in DEN_PRESENTATION_SCENARIOS}
        self.assertEqual(states, {"grounded", "exempt", "host_added", "refused", "withheld_note"})

    def test_feedback_audit_accepts_valid_and_flags_inadmissible_or_misnamed(self) -> None:
        log, _ = _make_log()
        ok = audit_refusal_feedback(
            log,
            offered_handles=("read#1",),
            named_offenders=("[read#99:1]",),
            actual_failing_citations=("[read#99:1]",),
        )
        self.assertTrue(ok.passed)

        bad = audit_refusal_feedback(
            log,
            offered_handles=("read#1", "read#404"),
            named_offenders=("[read#1:3]",),
            actual_failing_citations=("[read#99:1]",),
        )
        self.assertFalse(bad.passed)
        self.assertEqual(bad.inadmissible_samples, ("read#404",))
        self.assertEqual(bad.misnamed_offenders, ("[read#1:3]",))

    def test_dev_diagnostic_silent_on_healthy_and_detects_all_three_contradictions(self) -> None:
        log, scope = _make_log()
        c_ok = Citation("handle", "[read#1:3]", handle="read#1", start_line=3, citation_id="c1")
        c_bad = Citation("handle", "[read#99:99]", handle="read#99", start_line=99, citation_id="c2")

        healthy = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict("c1", "matched", "read#1"),
                ProductVerdict("c2", "unverifiable", ""),
            ),
            records=(),
        )
        self.assertEqual(run_dev_diagnostic(log, (c_ok, c_bad), scope, healthy, enabled=True), ())
        self.assertEqual(run_dev_diagnostic(log, (c_ok, c_bad), scope, healthy, enabled=False), ())

        contradictory = SurfaceOutcome(
            surface="coordinator_closeout",
            verdicts=(
                ProductVerdict("c1", "unverifiable", ""),
                ProductVerdict("c2", "traced", "read#99"),
            ),
            records=(),
        )
        found = run_dev_diagnostic(
            log,
            (c_ok, c_bad),
            scope,
            contradictory,
            enabled=True,
            had_prior_refusal=True,
            recorded_accepted=True,
        )
        self.assertEqual(
            {f.kind for f in found},
            {"grounded_without_support", "refusal_with_unique_support", "accepted_after_refusal"},
        )
        # Disabled diagnostic emits nothing and never alters outcomes
        self.assertEqual(
            run_dev_diagnostic(
                log,
                (c_ok, c_bad),
                scope,
                contradictory,
                enabled=False,
                had_prior_refusal=True,
                recorded_accepted=True,
            ),
            (),
        )


if __name__ == "__main__":
    unittest.main()
