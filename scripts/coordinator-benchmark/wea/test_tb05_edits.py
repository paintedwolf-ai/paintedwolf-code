"""Deterministic unit tests for TB05 · Agent edits and the read basis (E01, E02, E03, E08, E10)."""

from __future__ import annotations

import hashlib
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
    query_file_durability_ro,
    read_open_document_sha256_ro,
)
from wea.tb05_sidecar_runner import (
    _build_receipt,
    _extract_dispatcher_operation,
    _fixture_for_route,
    build_parser as build_sidecar_parser,
    select_slots,
    summarize_batch,
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
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "completed",
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
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_post_sha256": post,
            "outside_segment_pre_sha256": out_sha,
            "outside_segment_post_sha256": sha256_bytes(b"ALPHA\n"),
            "write_outcome": "completed",
        })
        self.assertEqual(res_mut["verdict"], "fail")
        self.assertIn("E01_OUTSIDE_BYTES_MUTATED", res_mut["failed_rule_ids"])

        # Healthy non-qualifying read refused with typed read-basis refusal + untouched disk -> pass
        res_ref = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": pre,
            "write_outcome": "rejected",
            "refusal_code": "EDITOR_DOCUMENT_READ_REQUIRED",
        })
        self.assertEqual(res_ref["verdict"], "pass")

        # Defect: schema/posture/unsupported refusal code on negative variant MUST fail (not pass!)
        res_wrong_refusal = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": pre,
            "write_outcome": "rejected",
            "refusal_code": "COMMAND_OPERAND_INVALID",
        })
        self.assertEqual(res_wrong_refusal["verdict"], "fail")
        self.assertIn("E01_WRONG_REFUSAL_CLASS", res_wrong_refusal["failed_rule_ids"])

        # Defect: missing refusal code on negative variant MUST fail closed
        res_no_code = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": pre,
            "write_outcome": "rejected",
            "refusal_code": "",
        })
        self.assertEqual(res_no_code["verdict"], "fail")
        self.assertIn("E01_MISSING_READ_BASIS_REFUSAL_CODE", res_no_code["failed_rule_ids"])

        # Defect: read form did not complete -> fail
        res_bad_read = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "rejected",
            "pre_disk_sha256": pre,
            "post_disk_sha256": pre,
            "write_outcome": "rejected",
            "refusal_code": "EDITOR_DOCUMENT_READ_REQUIRED",
        })
        self.assertEqual(res_bad_read["verdict"], "fail")
        self.assertIn("E01_READ_FORM_NOT_COMPLETED", res_bad_read["failed_rule_ids"])

        # Defect: wrong compiled dispatcher operation -> fail
        res_wrong_op = evaluate_e01_slot({
            "route_id": "write",
            "read_form": "full_read",
            "expected_authorized": True,
            "expected_operation": "write",
            "dispatched_operations": ["printf"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_post_sha256": post,
            "write_outcome": "completed",
        })
        self.assertEqual(res_wrong_op["verdict"], "fail")
        self.assertIn("E01_DISPATCH_OPERATION_MISMATCH", res_wrong_op["failed_rule_ids"])

        # Positive jq_edit uses independent parsed JSON expectation
        res_jq_ok = evaluate_e01_slot({
            "route_id": "jq_edit",
            "read_form": "full_read",
            "expected_authorized": True,
            "expected_operation": "jq_edit",
            "dispatched_operations": ["jq_edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_json_value": {"keep": "unchanged", "value": "BETA"},
            "post_json_value": {"keep": "unchanged", "value": "BETA"},
            "write_outcome": "completed",
        })
        self.assertEqual(res_jq_ok["verdict"], "pass")

        res_jq_bad = evaluate_e01_slot({
            "route_id": "jq_edit",
            "read_form": "full_read",
            "expected_authorized": True,
            "expected_operation": "jq_edit",
            "dispatched_operations": ["jq_edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "expected_json_value": {"keep": "unchanged", "value": "BETA"},
            "post_json_value": {"keep": "unchanged", "value": "beta"},
            "write_outcome": "completed",
        })
        self.assertEqual(res_jq_bad["verdict"], "fail")
        self.assertIn("E01_POST_JSON_MISMATCH", res_jq_bad["failed_rule_ids"])

        # Inline input & combined response independent verification
        res_inline_ok = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "inline_input",
            "expected_authorized": False,
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "inline_input_verified": True,
            "read_call_issued": False,
            "pre_disk_sha256": pre,
            "post_disk_sha256": pre,
            "write_outcome": "rejected",
            "refusal_code": "EDITOR_DOCUMENT_READ_REQUIRED",
        })
        self.assertEqual(res_inline_ok["verdict"], "pass")

        # Defect: non-qualifying read authorized write and mutated disk -> fail
        res_bad = evaluate_e01_slot({
            "route_id": "edit",
            "read_form": "outline",
            "expected_authorized": False,
            "expected_operation": "edit",
            "dispatched_operations": ["edit"],
            "read_outcome": "completed",
            "pre_disk_sha256": pre,
            "post_disk_sha256": post,
            "write_outcome": "completed",
        })
        self.assertEqual(res_bad["verdict"], "fail")
        self.assertIn("E01_NON_QUALIFYING_READ_AUTHORIZED", res_bad["failed_rule_ids"])
        self.assertIn("E01_REFUSED_WRITE_MUTATED_DISK", res_bad["failed_rule_ids"])

        # Missing/unobserved fields -> unknown (never pass)
        self.assertEqual(evaluate_e01_slot({"expected_authorized": True})["verdict"], "unknown")
        self.assertEqual(
            evaluate_e01_slot({
                "route_id": "jq_edit",
                "read_form": "full_read",
                "expected_authorized": True,
                "expected_operation": "jq_edit",
                "dispatched_operations": ["jq_edit"],
                "read_outcome": "completed",
                "pre_disk_sha256": pre,
                "post_disk_sha256": post,
                "write_outcome": "completed",
            })["verdict"],
            "unknown",
        )

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


class TestTB05InprocManifestAndSidecarRunner(unittest.TestCase):
    def test_inproc_manifest_maps_all_10_slots_and_matches_go_overlay(self) -> None:
        wea_dir = pathlib.Path(__file__).resolve().parent
        repo_root = wea_dir.parent.parent.parent
        manifest_path = wea_dir / "tb05_inproc_manifest.json"
        self.assertTrue(manifest_path.is_file())
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))

        self.assertEqual(manifest["schema"], "wea-tb05-inproc-manifest-v1")
        self.assertEqual(manifest["lane"], "in-process")
        owner = manifest["pinned_product_owner_source"]
        self.assertEqual(owner["git_head"], "bbeb46e72ce3d1cfc052259cadcce29185bb40bd")
        self.assertEqual(owner["git_tree"], "1830e4d60ed640d4ecf386653377498788fa9320")
        self.assertGreaterEqual(len(owner["go_owner_packages"]), 6)

        counts = manifest["scheduled_slot_counts"]
        self.assertEqual(counts, {"E02": 6, "E10": 4, "total": 10})
        slots = manifest["scheduled_slots"]
        self.assertEqual(len(slots), 10)
        self.assertEqual(sum(1 for s in slots if s["family"] == "E02"), 6)
        self.assertEqual(sum(1 for s in slots if s["family"] == "E10"), 4)

        # Verify overlay SHA-256 and that every Go subtest selector exists verbatim in tb05_edits_test.go.
        overlays = manifest["test_only_overlay_files"]
        self.assertEqual(len(overlays), 1)
        go_test_path = repo_root / overlays[0]["path"]
        self.assertTrue(go_test_path.is_file())
        raw_go = go_test_path.read_bytes()
        self.assertEqual(hashlib.sha256(raw_go).hexdigest(), overlays[0]["sha256"])
        go_text = raw_go.decode("utf-8")
        for s in slots:
            parent_fn, subtest = s["go_test_selector"].split("/", 1)
            self.assertIn(f"func {parent_fn}(", go_text)
            self.assertIn(f't.Run("{subtest}"', go_text)

    def test_sidecar_runner_selects_all_83_slots_and_filters(self) -> None:
        all_slots = select_slots()
        self.assertEqual(len(all_slots), 83)
        by_fam = {}
        for s in all_slots:
            by_fam[s["family"]] = by_fam.get(s["family"], 0) + 1
        self.assertEqual(by_fam, {"E01": 62, "E02": 4, "E03": 7, "E08": 9, "E10": 1})

        only_e10 = select_slots(families=["E10"])
        self.assertEqual(len(only_e10), 1)
        only_two = select_slots(slot_ids=["E01-edit-full_read", "E02-sidecar-non-overlapping-rebase"])
        self.assertEqual(len(only_two), 2)

        for route in D14_EDIT_ROUTES:
            rel, initial, after, outside, target_line, _patch = _fixture_for_route(route)
            self.assertEqual(rel, route.target_file)
            self.assertNotEqual(initial, after)
            self.assertIn(outside, initial)
            self.assertIn(outside, after)
            self.assertGreaterEqual(target_line, 50)
            self.assertGreaterEqual(len(initial.splitlines()), 55)
        self.assertEqual(build_read_call("outline", "target.txt"), ("command", {"command": "outline --path target.txt"}))
        self.assertEqual(build_read_call("partial_window", "target.txt"), ("command", {"command": "head -n 5 target.txt"}))

    def test_sidecar_runner_cli_receipt_and_summary_fail_closed(self) -> None:
        parser = build_sidecar_parser()
        ns = parser.parse_args(["--out", "/tmp/tb05-out", "--families", "E01,E02", "--fail-fast"])
        self.assertEqual(ns.families, "E01,E02")
        self.assertTrue(ns.fail_fast)

        prov = {
            "product_source": {"executable_sha256": "a" * 64, "verified": True},
            "harness_source": {"git_head": "b" * 40},
        }
        r_pass = _build_receipt(
            slot={"slot_id": "E01-edit-full_read", "family": "E01"},
            prov=prov,
            typed_result={"write_result": {"outcome": "completed"}},
            independent_observation={"post_disk_sha256": "c" * 64},
            verdict={"verdict": "pass", "passed": True, "failures": [], "failed_rule_ids": []},
            duration_ms=42,
        )
        r_fail = _build_receipt(
            slot={"slot_id": "E01-edit-outline", "family": "E01"},
            prov=prov,
            typed_result={"write_result": {"outcome": "completed"}},
            independent_observation={"post_disk_sha256": "d" * 64},
            verdict={"verdict": "fail", "passed": False, "failures": [{"rule_id": "E01_NON_QUALIFYING_READ_AUTHORIZED"}], "failed_rule_ids": ["E01_NON_QUALIFYING_READ_AUTHORIZED"]},
            duration_ms=19,
        )
        for key in ("slot_id", "family", "lane", "product_source", "harness_source", "typed_result", "independent_observation", "verdict"):
            self.assertIn(key, r_pass)
        self.assertEqual(r_pass["lane"], "sidecar")

        with tempfile.TemporaryDirectory() as td:
            summary = summarize_batch([r_pass, r_fail], prov=prov, out_dir=pathlib.Path(td), elapsed_s=1.25)
            self.assertFalse(summary["all_passed"])
            self.assertEqual(summary["total_slots"], 2)
            self.assertEqual(summary["counts"], {"pass": 1, "fail": 1, "unknown": 0})
            self.assertTrue((pathlib.Path(td) / "summary.json").is_file())

    def test_extract_dispatcher_operation_and_open_document_sha_against_real_schema(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            db_path = pathlib.Path(td) / "store.db"
            conn = sqlite3.connect(str(db_path))
            try:
                conn.executescript(
                    """
                    CREATE TABLE invocation_receipts (
                        id TEXT PRIMARY KEY,
                        tool_call_id TEXT NOT NULL,
                        tool_name TEXT NOT NULL
                    );
                    CREATE TABLE invocation_programs (
                        id TEXT PRIMARY KEY,
                        parent_receipt_id TEXT NOT NULL UNIQUE
                    );
                    CREATE TABLE invocation_program_executions (
                        id TEXT PRIMARY KEY,
                        program_id TEXT NOT NULL,
                        ordinal INTEGER NOT NULL,
                        operation_id TEXT NOT NULL
                    );
                    CREATE TABLE source_operations (
                        id TEXT PRIMARY KEY,
                        tool_call_id TEXT NOT NULL,
                        tool_name TEXT NOT NULL
                    );
                    CREATE TABLE editor_documents (
                        id TEXT PRIMARY KEY,
                        path TEXT NOT NULL,
                        draft TEXT NOT NULL,
                        eol TEXT NOT NULL,
                        base_sha256 TEXT NOT NULL,
                        dirty INTEGER NOT NULL
                    );
                    CREATE TABLE source_versions (
                        id TEXT PRIMARY KEY,
                        file_id TEXT NOT NULL,
                        branch_id TEXT NOT NULL DEFAULT '',
                        path TEXT NOT NULL,
                        seq INTEGER NOT NULL,
                        content_sha256 TEXT NOT NULL
                    );
                    CREATE TABLE source_effects (
                        id TEXT PRIMARY KEY,
                        file_id TEXT NOT NULL,
                        ordinal INTEGER NOT NULL
                    );
                    INSERT INTO invocation_receipts VALUES ('rec-1', 'call-write-1', 'command');
                    INSERT INTO invocation_programs VALUES ('prog-1', 'rec-1');
                    INSERT INTO invocation_program_executions VALUES ('exec-1', 'prog-1', 1, 'write');
                    INSERT INTO source_operations VALUES ('op-1', 'call-write-1', 'write');
                    INSERT INTO editor_documents VALUES ('doc-clean', 'live.txt', 'hello\n', 'lf', 'deadbeef', 0);
                    INSERT INTO editor_documents VALUES ('doc-dirty', 'crlf.txt', 'a\nb\n', 'crlf', 'oldsha', 1);
                    INSERT INTO source_versions VALUES ('v1', 'file-1', '', 'restart_target.txt', 1, 'sha_v1');
                    INSERT INTO source_versions VALUES ('v2', 'file-1', '', 'restart_target.txt', 2, 'sha_v2');
                    INSERT INTO source_effects VALUES ('e1', 'file-1', 1);
                    """
                )
                conn.commit()
            finally:
                conn.close()

            op = _extract_dispatcher_operation(
                db_path,
                call_id="call-write-1",
                dispatched_tool="command",
                expected_operation="write",
            )
            self.assertEqual(op["dispatched_operations"], ["write"])
            self.assertEqual(op["source_operation_tool_name"], "write")

            self.assertEqual(read_open_document_sha256_ro(db_path, doc_id="doc-clean"), "deadbeef")
            self.assertEqual(
                read_open_document_sha256_ro(db_path, doc_id="doc-dirty"),
                sha256_bytes(b"a\r\nb\r\n"),
            )
            self.assertEqual(query_file_durability_ro(db_path, "restart_target.txt"), (1, 2, "sha_v2"))
            # EDIT_TARGET_CHANGED_BY_OTHERS is accepted as a typed read-basis/stale-target refusal
            stale_ref = evaluate_e01_slot({
                "route_id": "edit",
                "read_form": "stale_disk",
                "expected_authorized": False,
                "expected_operation": "edit",
                "dispatched_operations": ["edit"],
                "read_outcome": "completed",
                "pre_disk_sha256": "a" * 64,
                "post_disk_sha256": "a" * 64,
                "write_outcome": "rejected",
                "refusal_code": "EDIT_TARGET_CHANGED_BY_OTHERS",
            })
            self.assertEqual(stale_ref["verdict"], "pass")
            # DTO fallback when DB absent
            self.assertEqual(
                read_open_document_sha256_ro(
                    pathlib.Path(td) / "missing.db",
                    doc_resp={"dirty": False, "base_sha256": "cafebabe"},
                ),
                "cafebabe",
            )


if __name__ == "__main__":
    unittest.main()
