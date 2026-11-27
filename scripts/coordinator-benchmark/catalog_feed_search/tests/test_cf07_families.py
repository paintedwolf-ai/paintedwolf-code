"""Unit tests for CF07 · Agent discovery evaluators, parsers, and manifest helpers."""

from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

from catalog_feed_search.families.cf07_g01 import evaluate_g01_observation
from catalog_feed_search.families.cf07_g02 import evaluate_g02_pair, parse_survey_json
from catalog_feed_search.families.cf07_g03 import evaluate_g03_refusal
from catalog_feed_search.families.cf07_g04 import (
    evaluate_g04_agreement,
    extract_search_stage_path_lines,
    parse_rg_path_lines,
)
from catalog_feed_search.families.cf07_harness import CapturedToolCall, parse_captured_tool_call
from catalog_feed_search.families.cf07_runner import compute_gap_rate, write_sha256sums


def _make_call(
    *,
    tool_name: str = "command",
    outcome: str = "completed",
    rejection_code: str | None = None,
    stdout: str = "",
    stderr: str = "",
    status_code: int | None = 0,
    stop_code: str | None = None,
    declared_gaps: tuple[str, ...] = (),
    fresh: bool | None = None,
    model_msg: str = "",
) -> CapturedToolCall:
    return CapturedToolCall(
        tool_name=tool_name,
        args={},
        call_id="call-1",
        outcome=outcome,
        rejection_code=rejection_code,
        stdout=stdout,
        stderr=stderr,
        status_code=status_code,
        stop_code=stop_code,
        generation_id=None,
        fresh=fresh,
        declared_gaps=declared_gaps,
        tool_result_json={"outcome": outcome},
        model_facing_message=model_msg,
    )


class TestCF07HarnessParsing(unittest.TestCase):
    def test_parse_captured_tool_call_extracts_rejection_and_coverage_gaps(self) -> None:
        raw = {
            "outcome": "completed",
            "stdout": json.dumps(
                {
                    "coverage": {
                        "catalog_state": "warming",
                        "catalog_refreshing": True,
                        "catalog_revision": 42,
                        "truncated": True,
                    }
                }
            ),
            "status_code": 0,
        }
        parsed = parse_captured_tool_call("command", {"command": "summarize --path ."}, "c1", raw, raw["stdout"])
        self.assertEqual(parsed.generation_id, "42")
        self.assertIs(parsed.fresh, False)
        self.assertIn("catalog_warming", parsed.declared_gaps)
        self.assertIn("catalog_refreshing", parsed.declared_gaps)
        self.assertIn("catalog_bounded", parsed.declared_gaps)

    def test_parse_captured_tool_call_extracts_grep_refusal_code(self) -> None:
        raw = {"outcome": "rejected", "rejection": {"code": "GREP_MATCH_BUDGET"}, "stderr": "regex_work_steps exceeded"}
        parsed = parse_captured_tool_call("command", {"command": "rg -P '(a+)+b' f"}, "c2", raw, "GREP_MATCH_BUDGET")
        self.assertEqual(parsed.rejection_code, "GREP_MATCH_BUDGET")
        self.assertIn("GREP_MATCH_BUDGET", parsed.declared_gaps)

    def test_parse_captured_tool_call_unwraps_d1_command_envelope(self) -> None:
        inner = json.dumps(
            {
                "state": "completed",
                "status": 2,
                "stdout": "01_first.txt:alpha first\n",
                "stderr": "rg: unreadable.txt: Permission denied\n",
            }
        )
        raw = {"outcome": "completed", "content": f"[read#2 01_first.txt:2]\n[command#6]\n{inner}"}
        parsed = parse_captured_tool_call("command", {"command": "rg alpha 01_first.txt unreadable.txt"}, "c4", raw, "")
        self.assertEqual(parsed.status_code, 2)
        self.assertIn("alpha first", parsed.stdout)
        self.assertIn("Permission denied", parsed.stderr)
        self.assertIsNone(parsed.stop_code)

    def test_parse_captured_tool_call_extracts_inventory_fresh_false(self) -> None:
        msg = json.dumps({"inventory": {"fresh": False, "revision": 7}})
        parsed = parse_captured_tool_call("command", {}, "c3", {"outcome": "completed", "stdout": msg}, msg)
        self.assertIs(parsed.fresh, False)
        self.assertEqual(parsed.generation_id, "7")
        self.assertIn("stale_inventory", parsed.declared_gaps)


class TestCF07Evaluators(unittest.TestCase):
    def test_g01_cold_warming_flags_f_cf01_01_when_code_absent(self) -> None:
        call = _make_call(outcome="completed", stdout="pkg/a.txt\n")
        ev = evaluate_g01_observation("find", call, cold_no_complete_gen=True, tree_mutated_during_call=False)
        self.assertEqual(ev["verdict"], "DEFECT")
        self.assertIn("F-CF01-01", str(ev["reason"]))

        refused = _make_call(outcome="rejected", rejection_code="SURVEY_INVENTORY_WARMING")
        ev2 = evaluate_g01_observation("find", refused, cold_no_complete_gen=True, tree_mutated_during_call=False)
        self.assertEqual(ev2["verdict"], "PASS")

    def test_g01_moving_tree_passes_with_or_without_gap(self) -> None:
        clean = _make_call(outcome="completed", stdout="a.txt\n")
        self.assertEqual(
            evaluate_g01_observation("find", clean, cold_no_complete_gen=False, tree_mutated_during_call=True)["verdict"],
            "PASS",
        )
        gapped = _make_call(outcome="completed", declared_gaps=("catalog_refreshing",))
        self.assertEqual(
            evaluate_g01_observation("summarize", gapped, cold_no_complete_gen=False, tree_mutated_during_call=True)["verdict"],
            "PASS_DECLARED_GAP",
        )

    def test_g02_pair_equivalence_and_empty_repo_guard(self) -> None:
        payload = json.dumps(
            {
                "bundle": "layout_overview",
                "path": ".",
                "digest": "abc",
                "total": 4,
                "probes_run": 1,
                "inventory_entries": 10,
                "entries_examined": 10,
                "snapshot": [{"handle": "h1", "kind": "dir", "path": "src"}],
            }
        )
        s_call = _make_call(tool_name="survey_repo", stdout=payload)
        r_call = _make_call(tool_name="command", stdout=payload)
        self.assertIsNotNone(parse_survey_json(s_call))
        ev = evaluate_g02_pair(s_call, r_call, ref_file_count=5)
        self.assertEqual(ev["verdict"], "PASS")
        self.assertTrue(ev["schema_match"])
        self.assertTrue(ev["digest_match"])

        empty_payload = json.dumps(
            {
                "bundle": "layout_overview",
                "path": ".",
                "digest": "empty",
                "total": 0,
                "probes_run": 1,
                "inventory_entries": 0,
                "entries_examined": 0,
            }
        )
        e_call = _make_call(tool_name="command", stdout=empty_payload)
        ev_empty = evaluate_g02_pair(e_call, e_call, ref_file_count=5)
        self.assertEqual(ev_empty["verdict"], "DEFECT")
        self.assertTrue(ev_empty["empty_claim_violation"])

    def test_g03_refusal_evaluation(self) -> None:
        ok_call = _make_call(outcome="rejected", rejection_code="GREP_REGEX_UNSUPPORTED", stdout="")
        self.assertEqual(evaluate_g03_refusal(ok_call, expected_code="GREP_REGEX_UNSUPPORTED")["verdict"], "PASS")

        spurious = _make_call(outcome="rejected", rejection_code="GREP_REGEX_UNSUPPORTED", stdout="oops\n")
        self.assertEqual(evaluate_g03_refusal(spurious, expected_code="GREP_REGEX_UNSUPPORTED")["verdict"], "DEFECT")

        partial_budget = _make_call(outcome="rejected", rejection_code="GREP_MATCH_BUDGET", stdout="01:aaab\n")
        self.assertEqual(
            evaluate_g03_refusal(partial_budget, expected_code="GREP_MATCH_BUDGET", allow_partial_stdout=True)["verdict"],
            "PASS",
        )

    def test_g04_agreement_and_exclusions(self) -> None:
        agent_call = _make_call(stdout="./src/a.txt:2:alpha\n.paintedwolf/x.txt:1:alpha\n")
        search_resp = {
            "hits": [
                {"hit_kind": "code", "path": "src/a.txt", "line": 2},
                {"hit_kind": "symbol", "path": "src/sym.go", "line": 10},
            ],
            "issues": [],
        }
        self.assertEqual(parse_rg_path_lines(agent_call.stdout), {("src/a.txt", 2), (".paintedwolf/x.txt", 1)})
        self.assertEqual(extract_search_stage_path_lines(search_resp), {("src/a.txt", 2)})
        ev = evaluate_g04_agreement(agent_call, search_resp, {("src/a.txt", 2)})
        self.assertEqual(ev["verdict"], "PASS")

        # Disagreement without declared gap -> DEFECT
        disagree = evaluate_g04_agreement(_make_call(stdout="src/a.txt:2:alpha\n"), {"hits": [], "issues": []}, {("src/a.txt", 2)})
        self.assertEqual(disagree["verdict"], "DEFECT")

    def test_runner_gap_rate_and_sha256sums(self) -> None:
        self.assertEqual(compute_gap_rate({"executed_instances": 10, "verdict_counts": {"PASS_DECLARED_GAP": 3}}), 0.3)
        self.assertEqual(compute_gap_rate({"executed_instances": 0}), 0.0)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "a.json").write_text("{}\n", encoding="utf-8")
            sums = write_sha256sums(root)
            self.assertTrue(sums.is_file())
            self.assertIn("a.json", sums.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
