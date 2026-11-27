"""Unit and observer tests for TB06 · Attribution, restore and rewind (E04, E05, E06, E07, E09)."""

from __future__ import annotations

import json
import pathlib
import sqlite3
import sys
import tempfile
import unittest

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.observers import sha256_bytes
from wea.tb06_history import (
    evaluate_e04_attribution,
    evaluate_e05_restore,
    evaluate_e06_rewind,
    evaluate_e07_files_marks,
    evaluate_e09_worktrees_and_outside,
    observe_attribution_state,
    observe_files_marks_counts,
    parse_go_tb06_receipts,
    run_tb06_qualification_controls,
    snapshot_rewind_boundary,
)


def _seed_tb06_sqlite(db_path: pathlib.Path) -> None:
    conn = sqlite3.connect(db_path)
    conn.executescript(
        """
        CREATE TABLE messages (id TEXT PRIMARY KEY, content TEXT);
        CREATE TABLE rewind_operations (operation_id TEXT PRIMARY KEY, status TEXT);
        CREATE TABLE source_operations (
            id TEXT PRIMARY KEY,
            project_id TEXT,
            branch_id TEXT,
            origin TEXT,
            person_id TEXT,
            session_id TEXT,
            job_id TEXT,
            turn INTEGER,
            cause TEXT,
            command_window_id TEXT
        );
        CREATE TABLE source_versions (
            id TEXT PRIMARY KEY,
            project_id TEXT,
            file_id TEXT,
            root_id TEXT,
            path TEXT,
            state TEXT,
            content_sha256 TEXT,
            operation_id TEXT,
            seq INTEGER,
            landing TEXT
        );
        CREATE TABLE source_effects (
            id TEXT PRIMARY KEY,
            project_id TEXT,
            file_id TEXT,
            operation_id TEXT,
            ordinal INTEGER,
            walk_visible INTEGER
        );
        CREATE TABLE source_line_attr (
            project_id TEXT,
            branch_id TEXT,
            file_id TEXT,
            start_line INTEGER,
            end_line INTEGER,
            effect_id TEXT
        );
        CREATE TABLE source_agent_presentations (
            project_id TEXT,
            file_id TEXT,
            effect_id TEXT
        );
        CREATE TABLE source_presentation_watermarks (
            project_id TEXT,
            file_id TEXT,
            through_ordinal INTEGER
        );
        """
    )
    conn.execute("INSERT INTO messages VALUES ('m1', 'edit files')")
    conn.execute(
        "INSERT INTO source_operations VALUES ('op1', 'p1', '', 'agent', '', 's1', '', 1, '', NULL)"
    )
    conn.execute(
        "INSERT INTO source_operations VALUES ('op2', 'p1', 'job-w1', 'agent', '', 's1', 'job-w1', 1, '', NULL)"
    )
    conn.execute(
        "INSERT INTO source_versions VALUES ('v1', 'p1', 'f1', 'r1', 'a.go', 'content', 'aaa', 'op1', 1, 'working_file')"
    )
    conn.execute(
        "INSERT INTO source_effects VALUES ('e1', 'p1', 'f1', 'op1', 1, 1)"
    )
    conn.execute(
        "INSERT INTO source_effects VALUES ('e2', 'p1', 'f2', 'op2', 2, 1)"
    )
    conn.execute(
        "INSERT INTO source_line_attr VALUES ('p1', '', 'f1', 1, 3, 'e1')"
    )
    conn.execute(
        "INSERT INTO source_agent_presentations VALUES ('p1', 'f1', 'e1')"
    )
    conn.commit()
    conn.close()


class TestTB06ObserversAndEvaluators(unittest.TestCase):
    def test_qualification_controls_pass(self) -> None:
        res = run_tb06_qualification_controls()
        self.assertTrue(res["qualified"], msg=json.dumps(res, indent=2))

    def test_readonly_sqlite_observers_and_rewind_boundary(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            proj = root / "proj"
            proj.mkdir()
            (proj / "a.go").write_bytes(b"package a\n")
            db_path = root / "store.db"
            _seed_tb06_sqlite(db_path)

            attr = observe_attribution_state(db_path, "p1")
            self.assertTrue(attr["available"])
            self.assertEqual(len(attr["operations"]), 2)
            self.assertEqual(len(attr["versions"]), 1)
            self.assertEqual(len(attr["line_attr"]), 1)

            marks = observe_files_marks_counts(db_path, "p1")
            self.assertTrue(marks["available"])
            # Worker branch effect e2 on f2 MUST NOT be counted as trunk mark!
            self.assertEqual(marks["marked_file_ids"], ["f1"])
            self.assertEqual(marks["marked_file_count"], 1)
            self.assertEqual(marks["unpresented_agent_file_count"], 1)
            self.assertEqual(marks["unpresented_agent_effect_count"], 1)

            snap = snapshot_rewind_boundary(proj, db_path)
            self.assertTrue(snap["available"])
            self.assertEqual(snap["tree_hashes"]["a.go"], sha256_bytes(b"package a\n"))
            self.assertEqual(snap["table_counts"]["messages"], 1)
            self.assertEqual(snap["table_counts"]["source_versions"], 1)
            self.assertEqual(snap["table_counts"]["source_effects"], 2)

    def test_e05_e06_e07_e09_fail_closed_on_missing_instrumentation(self) -> None:
        self.assertEqual(evaluate_e04_attribution({})["verdict"], "unknown")
        self.assertEqual(evaluate_e05_restore({})["verdict"], "unknown")
        self.assertEqual(evaluate_e06_rewind({})["verdict"], "unknown")
        self.assertEqual(evaluate_e07_files_marks({})["verdict"], "unknown")
        self.assertEqual(evaluate_e09_worktrees_and_outside({})["verdict"], "unknown")

    def test_e09_detects_checkout_branch_mismatch_and_outside_mutation(self) -> None:
        sha_a = sha256_bytes(b"a\n")
        sha_b = sha256_bytes(b"b\n")
        res = evaluate_e09_worktrees_and_outside({
            "checkouts": [
                {
                    "label": "wt2",
                    "expected_sha256": sha_a,
                    "disk_sha256": sha_a,
                    "expected_branch": "worktree:wt2",
                    "recorded_branch": "",
                }
            ],
            "outside_refusals": [
                {"path": "/tmp/out.txt", "refused": False, "pre_sha256": sha_a, "post_sha256": sha_b},
            ],
        })
        self.assertEqual(res["verdict"], "fail")
        self.assertEqual(
            set(res["failed_rule_ids"]),
            {"e09_checkout_branch_mismatch", "e09_outside_not_refused", "e09_outside_mutated"},
        )

    def test_parse_go_tb06_receipts(self) -> None:
        sample = "\n".join([
            "=== RUN   TestWEATB06E04AttributionAndBranchIdentity",
            '    tb06_history_test.go:100: WEA_TB06_RECEIPT:{"family":"E04","head_sha256":"abc"}',
            "--- PASS: TestWEATB06E04AttributionAndBranchIdentity (0.12s)",
            '    tb06_history_test.go:200: WEA_TB06_RECEIPT:{"family":"E05","disk_conflict_refused":true}',
        ])
        parsed = parse_go_tb06_receipts(sample)
        self.assertEqual(sorted(parsed.keys()), ["E04", "E05"])
        self.assertEqual(parsed["E04"]["head_sha256"], "abc")
        self.assertTrue(parsed["E05"]["disk_conflict_refused"])


if __name__ == "__main__":
    unittest.main()
