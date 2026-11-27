"""Unit tests and negative/positive controls for CF07 · Agent discovery (Decision 045)."""

from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

from catalog_feed_search.families.cf07_g01 import evaluate_g01_observation
from catalog_feed_search.families.cf07_g02 import evaluate_g02_pair, parse_survey_json
from catalog_feed_search.families.cf07_g03 import evaluate_g03_refusal, inspect_model_facing_completeness
from catalog_feed_search.families.cf07_g04 import (
    evaluate_g04_agreement,
    extract_search_stage_path_lines,
    is_async_detached_handle,
    parse_rg_path_lines,
)
from catalog_feed_search.families.cf07_harness import CapturedToolCall, parse_captured_tool_call
from catalog_feed_search.families.cf07_runner import compute_gap_rate, write_sha256sums
from catalog_feed_search.references import WalkEntry


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


def _walk_entry(path: str, kind: str = "file", boundary: bool = False) -> WalkEntry:
    return WalkEntry(path=path, kind=kind, size=10 if kind == "file" else 0, sha256=None, hidden=False, boundary=boundary)


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


class TestCF07G01Controls(unittest.TestCase):
    def test_g01_cold_warming_flags_f_cf01_01_when_code_absent(self) -> None:
        call = _make_call(outcome="completed", stdout="pkg/a.txt\n")
        ev = evaluate_g01_observation("find", call, cold_no_complete_gen=True, tree_mutated_during_call=False)
        self.assertEqual(ev["verdict"], "DEFECT")
        self.assertIn("F-CF01-01", str(ev["reason"]))

        refused = _make_call(outcome="rejected", rejection_code="SURVEY_INVENTORY_WARMING")
        ev2 = evaluate_g01_observation("find", refused, cold_no_complete_gen=True, tree_mutated_during_call=False)
        self.assertEqual(ev2["verdict"], "PASS")

    def test_g01_flags_wrong_answer_and_wrong_freshness(self) -> None:
        # Wrong answer: hallucinated extra file not in reference window -> DEFECT
        hallucinated = _make_call(outcome="completed", stdout="a.txt\nghost.txt\n")
        ev_extra = evaluate_g01_observation(
            "find",
            hallucinated,
            cold_no_complete_gen=False,
            tree_mutated_during_call=False,
            ref_files_before={"a.txt"},
            ref_files_after={"a.txt"},
        )
        self.assertEqual(ev_extra["verdict"], "DEFECT")

        # Wrong freshness: tree mutated (new file added), answer served stale pre-mutation set without gap -> DEFECT
        stale_unmarked = _make_call(outcome="completed", stdout="a.txt\n")
        ev_stale = evaluate_g01_observation(
            "find",
            stale_unmarked,
            cold_no_complete_gen=False,
            tree_mutated_during_call=True,
            ref_files_before={"a.txt"},
            ref_files_after={"a.txt", "b.txt"},
        )
        self.assertEqual(ev_stale["verdict"], "DEFECT")

        # Same stale answer WITH declared freshness gap -> PASS_DECLARED_GAP
        stale_marked = _make_call(outcome="completed", stdout="a.txt\n", fresh=False, declared_gaps=("stale_inventory",))
        ev_ok = evaluate_g01_observation(
            "find",
            stale_marked,
            cold_no_complete_gen=False,
            tree_mutated_during_call=True,
            ref_files_before={"a.txt"},
            ref_files_after={"a.txt", "b.txt"},
        )
        self.assertEqual(ev_ok["verdict"], "PASS_DECLARED_GAP")

        # Wrong wc line count -> DEFECT
        bad_wc = _make_call(outcome="completed", stdout="99 a.txt\n")
        ev_wc = evaluate_g01_observation(
            "wc",
            bad_wc,
            cold_no_complete_gen=False,
            tree_mutated_during_call=False,
            expected_wc_lines={2},
        )
        self.assertEqual(ev_wc["verdict"], "DEFECT")


class TestCF07G02Controls(unittest.TestCase):
    def test_g02_rejects_equal_but_wrong_inventories_and_boundary_or_exclusion_leaks(self) -> None:
        ref = [
            _walk_entry("src", "dir"),
            _walk_entry("src/app.go", "file"),
            _walk_entry("src/util.go", "file"),
            _walk_entry("node_modules", "dir", boundary=True),
            _walk_entry("subrepo", "dir", boundary=True),
        ]
        # Healthy matching pair -> PASS
        good_payload = json.dumps(
            {
                "bundle": "layout_overview",
                "path": ".",
                "digest": "ok",
                "total": 2,
                "probes_run": 1,
                "inventory_entries": 4,
                "entries_examined": 4,
                "snapshot": [{"handle": "h1", "kind": "dir", "path": "src"}],
            }
        )
        s_call = _make_call(tool_name="survey_repo", stdout=good_payload)
        r_call = _make_call(tool_name="command", stdout=good_payload)
        self.assertIsNotNone(parse_survey_json(s_call))
        self.assertEqual(evaluate_g02_pair(s_call, r_call, ref_entries=ref)["verdict"], "PASS")

        # Equal-but-wrong: both claim non-existent path -> DEFECT
        wrong_payload = json.dumps(
            {
                "bundle": "layout_overview",
                "path": ".",
                "digest": "same",
                "total": 1,
                "probes_run": 1,
                "inventory_entries": 4,
                "entries_examined": 4,
                "snapshot": [{"handle": "h1", "kind": "file", "path": "phantom/missing.go"}],
            }
        )
        w_call = _make_call(stdout=wrong_payload)
        self.assertEqual(evaluate_g02_pair(w_call, w_call, ref_entries=ref)["verdict"], "DEFECT")

        # Boundary descent leak (node_modules/pkg/x.js) -> DEFECT
        bleak_payload = json.dumps(
            {
                "bundle": "layout_overview",
                "path": ".",
                "digest": "b",
                "total": 1,
                "probes_run": 1,
                "inventory_entries": 4,
                "entries_examined": 4,
                "snapshot": [{"handle": "h1", "kind": "file", "path": "node_modules/pkg/x.js"}],
            }
        )
        b_call = _make_call(stdout=bleak_payload)
        self.assertEqual(evaluate_g02_pair(b_call, b_call, ref_entries=ref)["verdict"], "DEFECT")

        # .paintedwolf path-component exclusion leak -> DEFECT
        pw_payload = json.dumps(
            {
                "bundle": "layout_overview",
                "path": ".",
                "digest": "p",
                "total": 1,
                "probes_run": 1,
                "inventory_entries": 4,
                "entries_examined": 4,
                "snapshot": [{"handle": "h1", "kind": "file", "path": "sub/.paintedwolf/policy.yaml"}],
            }
        )
        p_call = _make_call(stdout=pw_payload)
        self.assertEqual(evaluate_g02_pair(p_call, p_call, ref_entries=ref)["verdict"], "DEFECT")


class TestCF07G03AndG04Controls(unittest.TestCase):
    def test_g03_early_stop_visited_unvisited_and_inc02_visibility(self) -> None:
        ok_call = _make_call(
            outcome="rejected",
            rejection_code="GREP_MATCH_BUDGET",
            stdout="01_first.txt:aaab\n",
            model_msg="GREP_MATCH_BUDGET regex_work_steps exceeded",
        )
        ev = evaluate_g03_refusal(
            ok_call,
            expected_code="GREP_MATCH_BUDGET",
            allow_partial_stdout=True,
            visited_expected=("aaab",),
            unvisited_forbidden=("aaab_unvisited",),
            require_model_partial_marker=True,
        )
        self.assertEqual(ev["verdict"], "PASS")

        leaked = _make_call(
            outcome="rejected",
            rejection_code="GREP_MATCH_BUDGET",
            stdout="01_first.txt:aaab\n03_unvisited.txt:aaab_unvisited\n",
            model_msg="GREP_MATCH_BUDGET",
        )
        ev_leak = evaluate_g03_refusal(
            leaked,
            expected_code="GREP_MATCH_BUDGET",
            allow_partial_stdout=True,
            unvisited_forbidden=("aaab_unvisited",),
            require_model_partial_marker=True,
        )
        self.assertEqual(ev_leak["verdict"], "DEFECT")

        inc02 = _make_call(
            outcome="completed",
            status_code=2,
            stdout="01_first.txt:alpha first\n",
            stderr="rg: unreadable: Permission denied\n",
            model_msg='{"status":2} Command stderr:\nrg: unreadable\n01_first.txt:alpha first\n{"source_verdict":"failed"}',
        )
        vis = inspect_model_facing_completeness(inc02)
        self.assertTrue(vis["model_sees_status_2"])
        self.assertTrue(vis["model_sees_source_verdict_failed"])
        self.assertEqual(vis["disposition"], "model_notified_failed_verdict_despite_completed_transport")

    def test_g04_independent_route_controls_and_strict_gap_scoping(self) -> None:
        ref = {("src/a.txt", 2)}
        # Two empty responses against positive reference -> DEFECT
        empty_agent = _make_call(stdout="")
        empty_stage = {"hits": [], "issues": []}
        self.assertEqual(evaluate_g04_agreement(empty_agent, empty_stage, ref)["verdict"], "DEFECT")

        # Gap on stage (time_budget) does NOT excuse missing hit on agent -> DEFECT
        stage_with_gap = {"hits": [], "issues": [{"executor": "code", "reason": "time_budget"}]}
        self.assertEqual(evaluate_g04_agreement(empty_agent, stage_with_gap, ref)["verdict"], "DEFECT")

        # symbol_pending on stage does NOT excuse code-leg omission -> DEFECT
        good_agent = _make_call(stdout="src/a.txt:2:alpha\n")
        sym_gap_stage = {"hits": [], "issues": [{"executor": "symbol", "reason": "symbol_pending"}]}
        self.assertEqual(evaluate_g04_agreement(good_agent, sym_gap_stage, ref)["verdict"], "DEFECT")

        # Both match reference on common generation -> PASS
        good_stage = {"hits": [{"hit_kind": "code", "path": "src/a.txt", "line": 2}], "issues": []}
        self.assertEqual(parse_rg_path_lines(good_agent.stdout), ref)
        self.assertEqual(extract_search_stage_path_lines(good_stage), ref)
        ev_ok = evaluate_g04_agreement(good_agent, good_stage, ref, generation_before=5, generation_after=5)
        self.assertEqual(ev_ok["verdict"], "PASS")

        # Generation drift -> DEFECT
        ev_drift = evaluate_g04_agreement(good_agent, good_stage, ref, generation_before=5, generation_after=6)
        self.assertEqual(ev_drift["verdict"], "DEFECT")

        # Async detached handle detector
        detached = _make_call(model_msg='{"program_id":"p1","state":"running","handle":"h1"}')
        self.assertTrue(is_async_detached_handle(detached))

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
