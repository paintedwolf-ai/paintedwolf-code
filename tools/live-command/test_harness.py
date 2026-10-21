#!/usr/bin/env python3
"""Unit tests for the live command harness grading, receipts, isolation, and retention."""

from __future__ import annotations

import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

from cli import parse_models_arg
from engine import CREDENTIAL_FILES, is_dangerous_checkpoint, stage_credentials
from fixture import build_fixture_template, clone_fixture
from grading import analyze_store_db, extract_case_evidence, grade_live_task, scrub_text
from runner import aggregate_model_attempts, record_attempt, render_markdown_report, replace_loopback_port


def write_case_dump(case_dir: Path, command_str: str, stdout: str, reply: str, stop_code: str = ""):
    case_dir.mkdir(parents=True, exist_ok=True)
    pid = "11111111-2222-3333-4444-555555555555"
    state = "rejected" if stop_code else "completed"
    cmd_payload = {
        "program_id": pid,
        "state": state,
        "status": 1 if stop_code else 0,
        "stop_code": stop_code,
        "stdout": stdout,
        "stderr": "",
        "executed_children": 1,
        "completed_effects": 0,
        "uncertain_effects": 0,
    }
    messages = {
        "messages": [
            {
                "role": "assistant",
                "content": "",
                "tool_calls": [{"name": "command", "id": "call_1", "args": {"command": command_str}}],
            },
            {
                "role": "tool",
                "content": "",
                "tool_result": {"content": f"[command#1]\n{json.dumps(cmd_payload)}"},
            },
            {"role": "assistant", "content": reply},
        ]
    }
    receipts = {
        pid: {
            "program": {"id": pid, "state": state, "status_code": 0, "stop_code": stop_code},
            "executions": [
                {
                    "id": "ex_1",
                    "program_id": pid,
                    "command": command_str,
                    "state": "refused" if stop_code else "completed",
                    "stop_code": stop_code,
                }
            ],
        }
    }
    (case_dir / "messages.json").write_text(json.dumps(messages))
    (case_dir / "invocations.json").write_text(json.dumps({"invocations": []}))
    (case_dir / "receipts.json").write_text(json.dumps(receipts))


class HarnessUnitTest(unittest.TestCase):
    def test_fixture_generation_and_isolation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            tpl = build_fixture_template(root / "tpl")
            self.assertTrue((tpl / "go.mod").exists())
            self.assertEqual((tpl / "data" / "big.bin").stat().st_size, 300000)
            self.assertTrue((tpl / "README.txt").read_text().endswith("edited\n"))

            c1 = clone_fixture(tpl, root / "c1")
            (c1 / "config.json").write_text('{"mutated": true}')
            (c1 / "notes.txt").write_text("dirty")

            c2 = clone_fixture(tpl, root / "c2")
            self.assertFalse((c2 / "notes.txt").exists())
            self.assertEqual(json.loads((c2 / "config.json").read_text())["x"], 42)

    def test_grading_tasks_and_q20_false_completion(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            tpl = build_fixture_template(root / "tpl")

            p1 = clone_fixture(tpl, root / "p1")
            d1 = root / "d1"
            write_case_dump(d1, "wc -l", "28 cmd/tool/tool.go", "There are 6 Go files; cmd/tool/tool.go has 28 lines.")
            self.assertTrue(grade_live_task("L01-go-files", p1, d1)["passed"])

            p2 = clone_fixture(tpl, root / "p2")
            d2 = root / "d2"
            write_case_dump(d2, "go test ./...", "FAIL", "TestDouble failed: got 9, want 8 because of n*2 + 1.")
            self.assertTrue(grade_live_task("L02-tests", p2, d2)["passed"])
            (p2 / "internal" / "broken" / "broken.go").write_text("package broken\nfunc Double(n int) int { return n*2 }\n")
            self.assertFalse(grade_live_task("L02-tests", p2, d2)["passed"])

            p3 = clone_fixture(tpl, root / "p3")
            d3 = root / "d3"
            write_case_dump(d3, "grep -rn TODO .", "", "Created notes.txt with all items.")
            g3_false = grade_live_task("L03-todo-notes", p3, d3)
            self.assertFalse(g3_false["passed"])
            self.assertFalse(g3_false["artifact_ok"])
            (p3 / "notes.txt").write_text("tool.go\nlegacy.js\nbroken.go\ncalc.go\nmain.go\n")
            self.assertTrue(grade_live_task("L03-todo-notes", p3, d3)["passed"])

            p4 = clone_fixture(tpl, root / "p4")
            d4 = root / "d4"
            write_case_dump(d4, "jq '.y=.x|del(.x)' config.json", "", "Renamed x to y.")
            self.assertFalse(grade_live_task("L04-rename-key", p4, d4)["passed"])
            (p4 / "config.json").write_text('{"name":"sanity","timeout":30,"verbose":false,"y":42}')
            self.assertTrue(grade_live_task("L04-rename-key", p4, d4)["passed"])

            p8 = clone_fixture(tpl, root / "p8")
            d8 = root / "d8"
            write_case_dump(d8, "sed -i", "", "Updated list.txt")
            self.assertFalse(grade_live_task("L08-sed-edit", p8, d8)["passed"])
            (p8 / "docs" / "nested" / "list.txt").write_text("ALPHA\nbeta\nend\n")
            self.assertTrue(grade_live_task("L08-sed-edit", p8, d8)["passed"])

    def test_analyze_store_db_native_gaps_and_refusals(self):
        with tempfile.TemporaryDirectory() as tmp:
            db_path = Path(tmp) / "store.db"
            conn = sqlite3.connect(db_path)
            conn.executescript(
                """
                CREATE TABLE invocation_receipts (id TEXT PRIMARY KEY, session_id TEXT);
                CREATE TABLE invocation_programs (id TEXT PRIMARY KEY, parent_receipt_id TEXT, state TEXT, stop_code TEXT);
                CREATE TABLE invocation_program_executions (
                    id TEXT PRIMARY KEY, program_id TEXT, ordinal INT, canonical TEXT,
                    state TEXT, owner TEXT, stop_code TEXT, host_stop_code TEXT,
                    native_gap_json TEXT, status_code INT
                );
                INSERT INTO invocation_receipts VALUES ('r1', 'sess-1');
                INSERT INTO invocation_programs VALUES ('p1', 'r1', 'completed', '');
                INSERT INTO invocation_programs VALUES ('p2', 'r1', 'rejected', 'SANDBOX_LOOPBACK_CONNECT_DENIED');
                INSERT INTO invocation_program_executions VALUES (
                    'e1', 'p1', 1, 'jq . config.json', 'completed', 'processes', '', '',
                    '{"command":"jq","code":"COMMAND_NATIVE_FORM_UNSUPPORTED","form":""}', 0
                );
                INSERT INTO invocation_program_executions VALUES (
                    'e2', 'p2', 1, 'curl http://127.0.0.1:55217/', 'refused', 'processes',
                    'SANDBOX_LOOPBACK_CONNECT_DENIED', '', NULL, 1
                );
                """
            )
            conn.commit()
            conn.close()

            res = analyze_store_db(db_path, ["sess-1"])
            self.assertEqual(res["per_session"]["sess-1"]["children"], 2)
            self.assertEqual(res["per_session"]["sess-1"]["gaps"], 1)
            self.assertEqual(res["per_session"]["sess-1"]["refused"], 1)
            self.assertEqual(res["native_gaps"][0]["command"], "jq")
            self.assertEqual(res["refused_children"][0]["code"], "SANDBOX_LOOPBACK_CONNECT_DENIED")

    def test_attempt_retention_and_aggregation(self):
        attempts = []
        record_attempt(
            attempts,
            {
                "task": "L01-go-files",
                "attempt": 1,
                "status": "error",
                "elapsed_s": 1.2,
                "grade": {"passed": False, "reasons": ["timeout"], "native_gaps": [], "refusal_codes": ["TIMEOUT"]},
            },
        )
        record_attempt(
            attempts,
            {
                "task": "L01-go-files",
                "attempt": 2,
                "status": "idle",
                "elapsed_s": 5.4,
                "grade": {"passed": True, "reasons": [], "native_gaps": [], "refusal_codes": []},
            },
        )
        self.assertEqual([a["chronological_index"] for a in attempts], [1, 2])
        agg = aggregate_model_attempts(
            "together-ai-1",
            "zai-org/GLM-5.3-Flash",
            attempts,
            {"native_gaps": [{"command": "git", "code": "COMMAND_NATIVE_FORM_UNSUPPORTED", "form": "", "count": 2}], "refused_children": []},
        )
        self.assertEqual(agg["total_attempts"], 2)
        self.assertEqual(agg["passed_attempts"], 1)
        self.assertEqual(agg["failed_attempts"], 1)
        self.assertAlmostEqual(agg["pass_rate"], 0.5)
        self.assertEqual(agg["native_gaps"][0]["command"], "git")

    def test_safety_guards_and_path_scrubbing(self):
        self.assertTrue(is_dangerous_checkpoint({"tool_approval": {"tool": "process_signal"}}))
        self.assertTrue(is_dangerous_checkpoint({"tool_approval": {"tool": "process_list"}}))
        self.assertFalse(is_dangerous_checkpoint({"tool_approval": {"tool": "command"}}))
        with self.assertRaises(ValueError):
            parse_models_arg("anthropic-1:claude-sonnet-4-5")

        raw_path = "Path /" + "Users/alice/work/project and Bearer secret123token"
        scrubbed = scrub_text(raw_path)
        self.assertNotIn("alice", scrubbed)
        self.assertNotIn("secret123token", scrubbed)
        self.assertIn("$HOME/work/project", scrubbed)

        with tempfile.TemporaryDirectory() as tmp:
            dev = Path(tmp) / "dev"
            cfg = Path(tmp) / "cfg"
            dev.mkdir()
            for f in CREDENTIAL_FILES:
                (dev / f).write_text("encrypted-placeholder")
            stage_credentials(dev, cfg, "together-ai-1", "zai-org/GLM-5.3-Flash")
            for f in CREDENTIAL_FILES:
                mode = (cfg / f).stat().st_mode & 0o777
                self.assertEqual(mode, 0o600)

            rep = render_markdown_report(
                {
                    "source_commit": "abc1234",
                    "generated_at": "2026-10-21T15:00:00Z",
                    "task_count": 1,
                    "attempts_per_task": 1,
                    "models": [
                        {
                            "provider_id": "together-ai-1",
                            "model": "zai-org/GLM-5.3-Flash",
                            "passed_attempts": 1,
                            "total_attempts": 1,
                            "pass_rate": 1.0,
                            "attempts": [
                                {
                                    "chronological_index": 1,
                                    "task": "L01-go-files",
                                    "attempt": 1,
                                    "status": "idle",
                                    "elapsed_s": 3.2,
                                    "grade": {"passed": True, "programs_count": 1, "executions_count": 2, "reasons": []},
                                }
                            ],
                            "native_gaps": [],
                            "refusals": [],
                        }
                    ],
                }
            )
            self.assertIn("100.0%", rep)

        self.assertEqual(
            replace_loopback_port("GET http://127.0.0.1:PORT/health and http://127.0.0.1:18080/status.json", 54321),
            "GET http://127.0.0.1:54321/health and http://127.0.0.1:54321/status.json",
        )
        with tempfile.TemporaryDirectory() as tmp:
            cdir = Path(tmp) / "refusal-case"
            write_case_dump(cdir, "curl -s http://127.0.0.1:54321/status.json", "", "Refused", stop_code="SANDBOX_LOOPBACK_CONNECT_DENIED")
            ev = extract_case_evidence(cdir)
            self.assertEqual(ev["refusal_codes"], ["SANDBOX_LOOPBACK_CONNECT_DENIED"])
            self.assertEqual(ev["envelope_refusal_codes"], [])
            agg = aggregate_model_attempts(
                "together-ai-1",
                "Qwen/Qwen3.5-9B",
                [{"task": "L10-curl", "attempt": 1, "status": "idle", "elapsed_s": 2.0, "grade": grade_live_task("L10-curl", Path(tmp), cdir)}],
                {"native_gaps": [], "refused_children": [{"command": "curl -s http://127.0.0.1:54321/status.json", "code": "SANDBOX_LOOPBACK_CONNECT_DENIED", "count": 1}]},
            )
            self.assertEqual(sum(r["count"] for r in agg["refusals"]), 1)


if __name__ == "__main__":
    unittest.main()
