"""Unit tests for GB07 (Freshness, closeout gates, friction: G01..G05, D5 tripled)."""
from __future__ import annotations

import json
import pathlib
import tempfile
import unittest

from grounding.checker import check_slot
from grounding.inprocess_driver import validate_test_only_overlay
from grounding.reference_resolver import resolve_citation
from grounding.selectors import WORDING_VARIANTS
from grounding.suites.gb07_freshness_gates import (
    ALL_GB07_SLOTS,
    SLOTS_BY_SELECTOR,
    build_all_gb07_fixtures,
    build_gb07_fixture,
    validate_gb07_catalog,
)
from grounding.suites.gb07_freshness_gates.full_runner import (
    GB07_EXECUTION_SPECS_BY_SELECTOR,
    INPROCESS_OVERLAYS,
    INPROCESS_SLOTS,
    _apply_g01_03_non_content_churn,
    _extract_source_checks_from_pending,
    _init_plain_workspace,
    _install_verify_gate_workflow_overlay,
    _parse_workflow_advance_result,
    _unwrap_closeout_grounding,
    evaluate_wording_invariance,
    main,
    parse_and_validate_args,
    select_gb07_runs,
    validate_gb07_execution_map,
)


class TestGB07CatalogAndFixtures(unittest.TestCase):
    def test_catalog_matches_ledger_and_d5_tripling(self) -> None:
        summary = validate_gb07_catalog()
        self.assertEqual(summary["sidecar_slots"], 28)
        self.assertEqual(summary["in_process_slots"], 6)
        self.assertEqual(summary["base_slots"], 34)
        self.assertEqual(summary["d5_tripled_per_platform"], 102)
        self.assertEqual(len(ALL_GB07_SLOTS), 34)
        self.assertEqual(len(SLOTS_BY_SELECTOR), 34)

    def test_all_102_wording_fixtures_pass_and_are_wording_invariant(self) -> None:
        fixtures = build_all_gb07_fixtures()
        self.assertEqual(len(fixtures), 102)
        for spec in ALL_GB07_SLOTS:
            verdicts = []
            for w in WORDING_VARIANTS:
                fx = fixtures[(spec.selector, w)]
                res = check_slot(fx.delivery_log, fx.citations, fx.scope, fx.build_healthy_outcome())
                self.assertTrue(res.passed, f"{spec.selector}/{w} failed: {res.defects}")
                verdicts.append(tuple(resolve_citation(fx.delivery_log, c, fx.scope).verdict_class for c in fx.citations))
            self.assertEqual(verdicts[0], verdicts[1])
            self.assertEqual(verdicts[0], verdicts[2])

    def test_d8_inprocess_block_mode_escalation_codes(self) -> None:
        fx3 = build_gb07_fixture("G04-06-inprocess-block-mode-3-total-escalates")
        fx2 = build_gb07_fixture("G04-07-inprocess-block-mode-2-consecutive-escalates")
        self.assertEqual(fx3.expected_code, "COORDINATOR_GROUNDING_ESCALATED")
        self.assertEqual(fx2.expected_code, "COORDINATOR_GROUNDING_ESCALATED")
        self.assertTrue(fx3.expected_breaker_tripped)
        self.assertTrue(fx2.expected_breaker_tripped)

    def test_full_runner_maps_all_34_base_slots_and_overlays(self) -> None:
        summary = validate_gb07_execution_map()
        self.assertEqual(summary["sidecar_slots"], 28)
        self.assertEqual(summary["in_process_slots"], 6)
        self.assertEqual(summary["base_slots"], 34)
        self.assertEqual(summary["sidecar_d5_runs"], 84)
        self.assertEqual(summary["in_process_d5_runs"], 18)
        self.assertEqual(summary["d5_runs_total"], 102)

        sidecar_catalog = {s.selector for s in ALL_GB07_SLOTS if s.lane == "sidecar"}
        inproc_catalog = {s.selector for s in ALL_GB07_SLOTS if s.lane == "in_process"}
        self.assertEqual(set(GB07_EXECUTION_SPECS_BY_SELECTOR.keys()), sidecar_catalog)
        self.assertEqual(set(INPROCESS_SLOTS), inproc_catalog)
        self.assertEqual(set(GB07_EXECUTION_SPECS_BY_SELECTOR.keys()) | set(INPROCESS_SLOTS), set(SLOTS_BY_SELECTOR.keys()))

        # Verify test-only Go overlays exist, pass Decision 010 validation, and reference all 6 in-process slots + 3 D5 wordings
        grounding_dir = pathlib.Path(__file__).resolve().parents[2]
        combined_go_src = ""
        replacements: dict[str, str] = {}
        for target_rel, source_rel in INPROCESS_OVERLAYS:
            src = grounding_dir / source_rel
            self.assertTrue(src.is_file(), f"Missing overlay file: {src}")
            replacements[target_rel] = str(src)
            combined_go_src += "\n" + src.read_text(encoding="utf-8")
        validated = validate_test_only_overlay(replacements)
        self.assertEqual(len(validated), len(INPROCESS_OVERLAYS))
        for sel in INPROCESS_SLOTS:
            self.assertIn(sel, combined_go_src)
        for w in WORDING_VARIANTS:
            self.assertIn(f'"{w}"', combined_go_src)

    def test_full_runner_sharding_partitions_84_sidecar_runs_disjointly(self) -> None:
        all_runs = select_gb07_runs()
        self.assertEqual(len(all_runs), 84)
        sidecar_slots = [s for s in ALL_GB07_SLOTS if s.lane == "sidecar"]
        expected_pairs = {(spec.selector, w) for spec in sidecar_slots for w in WORDING_VARIANTS}
        self.assertEqual({(u.spec.selector, u.wording) for u in all_runs}, expected_pairs)

        for total_shards in (1, 2, 3, 4, 7, 28):
            seen_pairs: set[tuple[str, str]] = set()
            selector_to_shard: dict[str, int] = {}
            for shard_idx in range(total_shards):
                shard_runs = select_gb07_runs(shard=shard_idx, shards=total_shards)
                for u in shard_runs:
                    pair = (u.spec.selector, u.wording)
                    self.assertNotIn(pair, seen_pairs, f"Duplicate pair {pair} in shards={total_shards}")
                    seen_pairs.add(pair)
                    if u.spec.selector in selector_to_shard:
                        self.assertEqual(
                            selector_to_shard[u.spec.selector],
                            shard_idx,
                            f"Slot {u.spec.selector} split across shards for shards={total_shards}",
                        )
                    else:
                        selector_to_shard[u.spec.selector] = shard_idx
            self.assertEqual(seen_pairs, expected_pairs)
            self.assertEqual(len(selector_to_shard), 28)

    def test_cli_validation_filters_wording_invariance_and_dry_run(self) -> None:
        g01_only = select_gb07_runs(families=("G01",))
        self.assertEqual(len(g01_only), 21)  # 7 G01 slots x 3 wordings
        g04_conf = select_gb07_runs(families=("G04",), wordings=("confident",))
        self.assertEqual(len(g04_conf), 5)  # 5 G04 sidecar slots x 1 wording

        with self.assertRaises(ValueError):
            select_gb07_runs(shard=2, shards=2)
        with self.assertRaises(ValueError):
            select_gb07_runs(shard=0, shards=0)
        with self.assertRaises(ValueError):
            select_gb07_runs(families=("NOPE",))
        with self.assertRaises(ValueError):
            select_gb07_runs(wordings=("invalid",))

        # CLI argument validation
        with self.assertRaises(SystemExit):
            parse_and_validate_args(["--product-root", "/tmp/d1", "--out-dir", "/tmp/out", "--inprocess-only", "--sidecar-only"])
        with self.assertRaises(SystemExit):
            parse_and_validate_args(["--product-root", "/tmp/d1", "--out-dir", "/tmp/out"])  # missing --engine-bin without --dry-run/--inprocess-only
        with self.assertRaises(SystemExit):
            parse_and_validate_args(["--product-root", "/tmp/d1", "--out-dir", "/tmp/out", "--dry-run", "--shard", "3", "--shards", "2"])
        with self.assertRaises(SystemExit):
            parse_and_validate_args(["--product-root", "/tmp/d1", "--out-dir", "/tmp/out", "--dry-run", "--families", "UNKNOWN"])

        parsed = parse_and_validate_args(
            [
                "--product-root",
                "/tmp/d1",
                "--out-dir",
                "/tmp/out",
                "--dry-run",
                "--sidecar-only",
                "--families",
                "G01,G02",
                "--wordings",
                "confident,hedged",
                "--shard",
                "1",
                "--shards",
                "2",
                "--run-id",
                "test-run",
            ]
        )
        self.assertEqual(parsed.parsed_families, ["G01", "G02"])
        self.assertEqual(parsed.parsed_wordings, ["confident", "hedged"])
        self.assertTrue(parsed.dry_run)
        self.assertTrue(parsed.sidecar_only)

        # Sidecar --dry-run writes gb07-sidecar-summary.json and gb07-sidecar-divergences.jsonl cleanly
        with tempfile.TemporaryDirectory(prefix="gb07-dryrun-") as td:
            rc = main(
                [
                    "--product-root",
                    "/tmp/d1",
                    "--out-dir",
                    td,
                    "--dry-run",
                    "--sidecar-only",
                    "--run-id",
                    "dry-run-check",
                ]
            )
            self.assertEqual(rc, 0)
            summary_path = pathlib.Path(td) / "gb07-sidecar-summary.json"
            div_path = pathlib.Path(td) / "gb07-sidecar-divergences.jsonl"
            self.assertTrue(summary_path.is_file())
            self.assertTrue(div_path.is_file())
            data = json.loads(summary_path.read_text(encoding="utf-8"))
            self.assertEqual(data["card"], "GB07")
            self.assertTrue(data["dry_run"])
            self.assertEqual(data["base_slot_count"], 28)
            self.assertEqual(data["run_count"], 84)

        # D5 wording invariance evaluator
        invariant_rows = [
            {
                "selector": "G01-01-fresh-receipt-unchanged-content",
                "wording": w,
                "passed": True,
                "observed_codes": [],
                "worker_report_refusals": None,
            }
            for w in WORDING_VARIANTS
        ]
        inv = evaluate_wording_invariance(invariant_rows)
        self.assertEqual(len(inv), 1)
        self.assertTrue(inv[0]["checked"])
        self.assertTrue(inv[0]["invariant"])

        divergent_rows = list(invariant_rows)
        divergent_rows[2] = dict(divergent_rows[2], passed=False)
        inv_div = evaluate_wording_invariance(divergent_rows)
        self.assertFalse(inv_div[0]["invariant"])

    def test_unwrap_closeout_grounding_and_plain_workspace_helpers(self) -> None:
        self.assertEqual(_unwrap_closeout_grounding([]), {})
        wrapped = [{"message_id": "m1", "role": "assistant", "grounding": {"traced": True, "hint_code": "X"}}]
        self.assertEqual(_unwrap_closeout_grounding(wrapped), {"traced": True, "hint_code": "X"})
        unwrapped = [{"traced": False, "hint_code": "Y"}]
        self.assertEqual(_unwrap_closeout_grounding(unwrapped), {"traced": False, "hint_code": "Y"})

        with tempfile.TemporaryDirectory(prefix="gb07-plain-") as td:
            ws = pathlib.Path(td) / "ws"
            _init_plain_workspace(ws)
            self.assertTrue((ws / "gate.go").is_file())
            self.assertTrue((ws / "unrelated.go").is_file())
            self.assertTrue((ws / "secret.go").is_file())
            self.assertFalse((ws / ".git").exists())

        g03_02 = GB07_EXECUTION_SPECS_BY_SELECTOR["G03-02-open-review-gate-blocks"]
        self.assertIn("SUBMIT_VERDICT_REVIEWER_MISSING", g03_02.expected_codes)

    def test_g01_verify_gate_overlay_and_observation_parsers(self) -> None:
        with tempfile.TemporaryDirectory(prefix="gb07-wf-") as td:
            ws = pathlib.Path(td) / "ws"
            wf_path = _install_verify_gate_workflow_overlay(ws)
            self.assertTrue(wf_path.is_file())
            self.assertEqual(
                wf_path.relative_to(ws).as_posix(),
                ".paintedwolf/workflows/gb07-verify-gate/workflow.yaml",
            )
            body = wf_path.read_text(encoding="utf-8")
            self.assertIn("evidence_passed:verify", body)
            self.assertIn("when_gate_met: coordinator", body)

        pending = {
            "messages": [
                {"role": "user", "content": "check"},
                {
                    "role": "system",
                    "content": (
                        "Execution observations.\n"
                        '{"source_checks":{"checks":[{"command":"echo VERIFY_OK gate.go",'
                        '"cwd":".","recorded_verdict":"passed","source_match":"different_source",'
                        '"prior_user_intent":false}],"older_checks_omitted":0,"required_by_workflow":false}}'
                    ),
                },
            ]
        }
        sc = _extract_source_checks_from_pending(pending)
        self.assertIsNotNone(sc)
        assert sc is not None
        self.assertEqual(sc["checks"][0]["source_match"], "different_source")
        self.assertIsNone(_extract_source_checks_from_pending({"messages": [{"role": "user", "content": "hi"}]}))

        adv = _parse_workflow_advance_result(
            [
                {
                    "tool_name": "workflow_advance",
                    "content": '{"error":"phase_gate_unmet","failed_leaves":["evidence_passed:verify"],"phase":"verify"}',
                }
            ]
        )
        self.assertEqual(adv["error"], "phase_gate_unmet")
        self.assertEqual(adv["failed_leaves"], ["evidence_passed:verify"])

        with tempfile.TemporaryDirectory(prefix="gb07-churn-") as td:
            ws = pathlib.Path(td) / "ws"
            _init_plain_workspace(ws)
            (ws / ".git").mkdir()
            gate_before = (ws / "gate.go").read_bytes()
            unrel_before = (ws / "unrelated.go").read_bytes()
            summary = _apply_g01_03_non_content_churn(ws)
            self.assertEqual((ws / "gate.go").read_bytes(), gate_before)
            self.assertEqual((ws / "unrelated.go").read_bytes(), unrel_before)
            self.assertEqual(summary["watcher_burst_iterations"], 10)
            self.assertFalse((ws / ".git" / "gb07-churn.tmp").exists())
            self.assertFalse((ws / "tmp" / "gb07-excluded-build.out").exists())
            self.assertFalse((ws / "node_modules" / "gb07-pkg" / "index.js").exists())


if __name__ == "__main__":
    unittest.main()
