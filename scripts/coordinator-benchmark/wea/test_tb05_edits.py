"""Deterministic unit tests for TB05 · Agent edits and the read basis (E01, E02, E03, E08, E10)."""

from __future__ import annotations

import pathlib
import sys
import unittest

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.observers import sha256_bytes
from wea.tb05_edits import (
    D14_EDIT_ROUTES,
    D17_MUTATION_BOUND_BYTES,
    D17_READ_BOUND_BYTES,
    D17_REFUSAL_SIZE_BYTES,
    E01_READ_FORMS,
    build_read_call,
    build_route_write_call,
    build_tb05_slot_catalog,
    evaluate_e01_slot,
    evaluate_e02_slot,
    evaluate_e03_slot,
    evaluate_e08_slot,
    evaluate_e10_slot,
)


class TestTB05CatalogAndD14Routes(unittest.TestCase):
    def test_d14_ten_routes_and_native_vs_callable_classification(self) -> None:
        self.assertEqual(len(D14_EDIT_ROUTES), 10)
        by_id = {r.route_id: r for r in D14_EDIT_ROUTES}
        # D14 verified rule: write, edit, replace_lines, jq_edit are native `command` operations,
        # NOT top-level callable tools.
        for op in ("write", "edit", "replace_lines", "jq_edit"):
            self.assertEqual(by_id[op].kind, "command_native")
            self.assertEqual(by_id[op].tool_name, "command")
        # code_rewrite and restore_version are callable tools.
        for tool in ("code_rewrite", "restore_version"):
            self.assertEqual(by_id[tool].kind, "callable_tool")
            self.assertEqual(by_id[tool].tool_name, tool)
        # Four shell routes run through `command`.
        for sh in ("shell_sed", "shell_patch", "shell_redirect", "shell_program"):
            self.assertEqual(by_id[sh].kind, "command_shell")
            self.assertEqual(by_id[sh].tool_name, "command")

        # Every route builds a valid (tool_name, args) call.
        for r in D14_EDIT_ROUTES:
            tname, args = build_route_write_call(r, restore_version_id="ver-1")
            self.assertEqual(tname, r.tool_name)
            self.assertIsInstance(args, dict)
            self.assertTrue(args)

    def test_fixed_slot_ledger_counts_match_ledger_md(self) -> None:
        cat = build_tb05_slot_catalog()
        # ledger.md sidecar slot counts for TB05:
        self.assertEqual(len(cat["E01"]), 62)
        self.assertEqual(len(cat["E02"]), 4)
        self.assertEqual(len(cat["E03"]), 7)
        self.assertEqual(len(cat["E08"]), 9)
        self.assertEqual(len(cat["E10"]), 1)

        # Exactly 2 of the 6 E01 read forms qualify (`full_read`, `full_summarize`).
        qualifying = [rf["id"] for rf in E01_READ_FORMS if rf["qualifies"]]
        self.assertEqual(qualifying, ["full_read", "full_summarize"])

        for rf in E01_READ_FORMS:
            call = build_read_call(rf["id"], "target.txt")
            if rf["id"] == "inline_input":
                self.assertIsNone(call)
            else:
                self.assertIsNotNone(call)

    def test_d17_size_constants(self) -> None:
        self.assertEqual(D17_MUTATION_BOUND_BYTES, 4 * 1024 * 1024)
        self.assertEqual(D17_READ_BOUND_BYTES, 8 * 1024 * 1024)
        self.assertEqual(D17_REFUSAL_SIZE_BYTES, 64 * 1024 * 1024)


class TestTB05FailClosedEvaluators(unittest.TestCase):
    def test_e01_qualifying_and_non_qualifying_fail_closed(self) -> None:
        pre = sha256_bytes(b"alpha\nbeta\ngamma\n")
        post = sha256_bytes(b"alpha\nBETA\ngamma\n")
        out_sha = sha256_bytes(b"alpha\n")

        # Healthy qualifying read -> pass
        res = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "full_read",
            "expected_authorized": True,
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_post_sha256": post,
            "outside_segment_pre_sha256": out_sha,
            "outside_segment_post_sha256": out_sha,
            "write_outcome": "completed",
        })
        self.assertEqual(res["verdict"], "pass")

        # Defect: outside bytes mutated -> fail
        res_mut = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "full_read",
            "expected_authorized": True,
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_post_sha256": post,
            "outside_segment_pre_sha256": out_sha,
            "outside_segment_post_sha256": sha256_bytes(b"ALPHA\n"),
            "write_outcome": "completed",
        })
        self.assertEqual(res_mut["verdict"], "fail")
        self.assertIn("E01_OUTSIDE_BYTES_MUTATED", res_mut["failed_rule_ids"])

        # Healthy non-qualifying read refused with untouched disk -> pass
        res_ref = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "pre_disk_sha256": pre,
            "post_disk_sha256": pre,
            "write_outcome": "rejected",
            "refusal_code": "EDITOR_DOCUMENT_READ_REQUIRED",
        })
        self.assertEqual(res_ref["verdict"], "pass")

        # Defect: non-qualifying read authorized write and mutated disk -> fail
        res_bad = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "write_outcome": "completed",
        })
        self.assertEqual(res_bad["verdict"], "fail")
        self.assertIn("E01_NON_QUALIFYING_READ_AUTHORIZED", res_bad["failed_rule_ids"])
        self.assertIn("E01_REFUSED_WRITE_MUTATED_DISK", res_bad["failed_rule_ids"])

        # Missing fields -> unknown (never pass)
        self.assertEqual(evaluate_e01_slot({"expected_authorized": True})["verdict"], "unknown")

    def test_e02_rebase_and_overlap_evaluators(self) -> None:
        pre = sha256_bytes(b"L1\nL2\nL3\n")
        combined = sha256_bytes(b"L1_H\nL2\nL3_A\n")
        mid = sha256_bytes(b"L2\n")

        self.assertEqual(
            evaluate_e02_slot({
                "scenario": "non_overlapping_rebase",
                "pre_disk_sha256": pre,
                "post_disk_sha256": combined,
                "expected_combined_sha256": combined,
                "outside_segment_pre_sha256": mid,
                "outside_segment_post_sha256": mid,
            })["verdict"],
            "pass",
        )
        # Overlap that mutates disk fails closed
        bad_overlap = evaluate_e02_slot({
            "scenario": "overlapping_reread",
            "pre_disk_sha256": pre,
            "post_disk_sha256": combined,
            "write_outcome": "completed",
        })
        self.assertEqual(bad_overlap["verdict"], "fail")
        self.assertIn("E02_OVERLAP_SILENTLY_ACCEPTED", bad_overlap["failed_rule_ids"])
        self.assertIn("E02_OVERLAP_MUTATED_DISK", bad_overlap["failed_rule_ids"])

    def test_e03_document_vs_disk_parity(self) -> None:
        sha_a = sha256_bytes(b"same\n")
        sha_b = sha256_bytes(b"drift\n")
        self.assertEqual(evaluate_e03_slot({"post_disk_sha256": sha_a, "open_document_sha256": sha_a})["verdict"], "pass")
        drift = evaluate_e03_slot({"post_disk_sha256": sha_a, "open_document_sha256": sha_b})
        self.assertEqual(drift["verdict"], "fail")
        self.assertIn("E03_DOCUMENT_DISK_DRIFT", drift["failed_rule_ids"])

    def test_e08_encoding_and_canary_screening(self) -> None:
        pre = sha256_bytes(b"\xef\xbb\xbfa\r\nb\r\n")
        post = sha256_bytes(b"\xef\xbb\xbfa\r\nB\r\n")
        self.assertEqual(
            evaluate_e08_slot({
                "scenario": "utf8_bom_crlf",
                "pre_disk_sha256": pre,
                "post_disk_sha256": post,
                "expected_post_sha256": post,
                "outside_segment_pre_sha256": sha256_bytes(b"\xef\xbb\xbfa\r\n"),
                "outside_segment_post_sha256": sha256_bytes(b"\xef\xbb\xbfa\r\n"),
            })["verdict"],
            "pass",
        )
        leaked = evaluate_e08_slot({
            "scenario": "secret_canary_span",
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_post_sha256": post,
            "outside_segment_pre_sha256": pre,
            "outside_segment_post_sha256": pre,
            "canary_scan": {"clean": False, "leak_count": 1},
        })
        self.assertEqual(leaked["verdict"], "fail")
        self.assertIn("E08_CANARY_LEAKED_TO_RECORDS", leaked["failed_rule_ids"])

    def test_e10_restart_durability_evaluator(self) -> None:
        sha = sha256_bytes(b"committed\n")
        ok = evaluate_e10_slot({
            "expected_post_sha256": sha,
            "post_restart_disk_sha256": sha,
            "ledger_tip_sha256": sha,
            "open_document_sha256": sha,
            "effect_count": 1,
            "version_count": 2,
            "expected_effect_count": 1,
        })
        self.assertEqual(ok["verdict"], "pass")

        dup = evaluate_e10_slot({
            "expected_post_sha256": sha,
            "post_restart_disk_sha256": sha,
            "ledger_tip_sha256": sha,
            "effect_count": 2,
            "version_count": 3,
            "expected_effect_count": 1,
        })
        self.assertEqual(dup["verdict"], "fail")
        self.assertIn("E10_EFFECT_COUNT_MISMATCH", dup["failed_rule_ids"])


if __name__ == "__main__":
    unittest.main()
