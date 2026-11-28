"""Unit tests for GB08 (Den presentation scenarios, feedback readback, opt-in dev diagnostic, full runner)."""
from __future__ import annotations

import contextlib
import io
import json
import pathlib
import tempfile
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
    ALL_GB08_SLOTS,
    DEN_PRESENTATION_SCENARIOS,
    GB08_FAMILY_IDS,
    GB08_HEADLESS_FAMILY_IDS,
    GB08_NATIVE_FAMILY_IDS,
    HEADLESS_GB08_SLOTS,
    NATIVE_GB08_SLOTS,
    audit_refusal_feedback,
    run_dev_diagnostic,
    validate_gb08_catalog,
)
from grounding.suites.gb08_presentation.full_runner import (
    GB08_HEADLESS_EXECUTION_SPECS,
    build_native_den_plan,
    main as full_runner_main,
    select_gb08_runs,
    validate_gb08_execution_map,
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


class TestGB08CatalogAndFullRunner(unittest.TestCase):
    def test_catalog_and_execution_map_one_to_one_headless_and_native(self) -> None:
        cat_counts = validate_gb08_catalog()
        exec_counts = validate_gb08_execution_map()
        self.assertEqual(cat_counts, exec_counts)
        self.assertEqual(cat_counts["headless_slots"], len(HEADLESS_GB08_SLOTS))
        self.assertEqual(cat_counts["native_slots"], len(NATIVE_GB08_SLOTS))
        self.assertEqual(cat_counts["total_slots"], len(ALL_GB08_SLOTS))
        self.assertEqual(cat_counts["headless_slots"], len(GB08_HEADLESS_EXECUTION_SPECS))
        self.assertEqual(cat_counts["native_slots"], len(DEN_PRESENTATION_SCENARIOS))
        self.assertEqual(cat_counts["headless_slots"] + cat_counts["native_slots"], cat_counts["total_slots"])

        headless_selectors = {s.selector for s in HEADLESS_GB08_SLOTS}
        native_selectors = {s.selector for s in NATIVE_GB08_SLOTS}
        self.assertEqual(headless_selectors & native_selectors, set())
        self.assertEqual(GB08_HEADLESS_FAMILY_IDS + GB08_NATIVE_FAMILY_IDS, GB08_FAMILY_IDS)

    def test_sharding_is_disjoint_and_complete_for_headless_slots(self) -> None:
        all_headless = [u.spec.selector for u in select_gb08_runs()]
        self.assertEqual(all_headless, [s.selector for s in HEADLESS_GB08_SLOTS])

        for total_shards in (2, 3, 4, 5):
            seen: list[str] = []
            for k in range(total_shards):
                shard_units = select_gb08_runs(shard=k, shards=total_shards)
                shard_selectors = [u.spec.selector for u in shard_units]
                self.assertEqual(len(shard_selectors), len(set(shard_selectors)))
                seen.extend(shard_selectors)
            self.assertEqual(len(seen), len(all_headless))
            self.assertEqual(set(seen), set(all_headless))

    def test_family_filtering_and_native_separation(self) -> None:
        p01_only = select_gb08_runs(families=("P01",))
        self.assertTrue(len(p01_only) > 0)
        self.assertTrue(all(u.spec.family_id == "P01" and u.spec.lane == "sidecar" for u in p01_only))

        # Selecting DEN in headless selector returns 0 headless slots (never mixes native UI into sidecar loop)
        den_headless = select_gb08_runs(families=("DEN",))
        self.assertEqual(den_headless, [])

        native_plan = build_native_den_plan(families=("DEN",), run_id="test-run")
        self.assertEqual(native_plan["native_slot_count"], len(NATIVE_GB08_SLOTS))
        self.assertEqual(
            [s["selector"] for s in native_plan["scenarios"]],
            [s.selector for s in DEN_PRESENTATION_SCENARIOS],
        )

    def test_select_gb08_runs_validates_arguments(self) -> None:
        with self.assertRaises(ValueError):
            select_gb08_runs(shard=0, shards=0)
        with self.assertRaises(ValueError):
            select_gb08_runs(shard=-1, shards=2)
        with self.assertRaises(ValueError):
            select_gb08_runs(shard=2, shards=2)
        with self.assertRaises(ValueError):
            select_gb08_runs(families=("UNKNOWN",))

    def test_cli_list_native_slots_and_dry_run_and_validation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out_dir = pathlib.Path(tmp) / "out"

            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = full_runner_main(["--list-native-slots", "--out-dir", str(out_dir), "--run-id", "test-native"])
            self.assertEqual(rc, 0)
            payload = json.loads(buf.getvalue())
            self.assertEqual(payload["lane"], "native_den")
            self.assertEqual(payload["native_slot_count"], len(NATIVE_GB08_SLOTS))
            self.assertTrue((out_dir / "gb08-native-plan.json").is_file())

            buf2 = io.StringIO()
            with contextlib.redirect_stdout(buf2):
                rc2 = full_runner_main(
                    [
                        "--dry-run",
                        "--out-dir",
                        str(out_dir),
                        "--families",
                        "P01,P03",
                        "--shard",
                        "0",
                        "--shards",
                        "2",
                        "--run-id",
                        "test-dry",
                    ]
                )
            self.assertEqual(rc2, 0)
            dry = json.loads(buf2.getvalue())
            self.assertTrue(dry["dry_run"])
            self.assertTrue(dry["passed"])
            self.assertEqual(dry["native_slot_count"], 0)
            self.assertTrue((out_dir / "gb08-sidecar-summary.json").is_file())

            # Live mode without required flags exits via argparse error
            err_buf = io.StringIO()
            with contextlib.redirect_stderr(err_buf):
                with self.assertRaises(SystemExit) as cm:
                    full_runner_main(["--run-id", "missing-required"])
            self.assertEqual(cm.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
