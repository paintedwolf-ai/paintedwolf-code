"""Unit tests for TB07 · Approvals, grants and decision records (A01, A02, A03, A04, A12)."""

from __future__ import annotations

import copy
import hashlib
import json
import pathlib
import unittest
from collections import Counter
from typing import Any

from wea.controls import make_healthy_baseline
from wea.tb07_approvals import H1_PROGRAM_STATE_LIMIT_BYTES, TB07_SLOTS, check_tb07_evidence
from wea.tb07_sidecar_runner import (
    EXPECTED_SIDECAR_COUNTS_BY_FAMILY,
    TB07_SIDECAR_SLOT_SPECS,
    _build_receipt,
    _call_with_checkpoint_inspect,
)


def _sha(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _make_tb07_healthy_evidence() -> dict[str, Any]:
    ev = copy.deepcopy(make_healthy_baseline())
    ev["slot_id"] = "tb07-healthy"
    before_sha = _sha("before\n")
    after_sha = _sha("after\n")
    ev["product_records"].update({
        "approval_records": [
            {
                "checkpoint_id": "cp-a01-1",
                "reviewed_plan_id": "plan_sha256_abc123",
                "installed_authority_plan_id": "plan_sha256_abc123",
                "recorded_decision_plan_id": "plan_sha256_abc123",
                "resolved_by": "human",
                "resolver_person_id": "person-owner-1",
                "file_change": {
                    "path": "notes.txt",
                    "reviewed_before_sha256": before_sha,
                    "observed_before_sha256": before_sha,
                    "reviewed_after_sha256": after_sha,
                    "observed_after_sha256": after_sha,
                    "revalidated": False,
                },
            }
        ],
        "grant_scope_checks": [
            {
                "grant_id": "grant-chat-1",
                "boundary": "same_chat",
                "expected_in_scope": True,
                "matched": True,
                "asked": False,
                "expired": False,
                "revoked": False,
            },
            {
                "grant_id": "grant-chat-1",
                "boundary": "cross_chat",
                "expected_in_scope": False,
                "matched": False,
                "asked": True,
                "expired": False,
                "revoked": False,
            },
            {
                "grant_id": "grant-proj-1",
                "boundary": "cross_project",
                "expected_in_scope": False,
                "matched": False,
                "asked": True,
                "expired": False,
                "revoked": False,
            },
            {
                "grant_id": "grant-day-1",
                "boundary": "after_expiry",
                "expected_in_scope": True,
                "matched": False,
                "asked": True,
                "expired": True,
                "revoked": False,
            },
        ],
        "effective_policy_rules": [
            {"category": "command", "pattern": "rm -rf *", "effect": "deny"},
            {"category": "command", "pattern": "deploy *", "effect": "ask"},
        ],
        "posture_policy_checks": [
            {"posture": "never_ask", "has_deny_rule": True, "denied": True, "asked": False},
            {"posture": "light", "source": "command_field", "detection_hit": True, "asked": True, "denied": False},
            {"posture": "balanced", "source": "chat_text", "detection_hit": False, "asked": False, "denied": False},
        ],
        "unrecordable_decision_checks": [
            {
                "kind": "ledger_seal_failure",
                "failed_closed": True,
                "effect_executed": False,
                "installed_authority_count": 0,
                "checkpoint_status_after": "pending",
            },
            {
                "kind": "h1_program_state_memory_limit",
                "limit_bytes": H1_PROGRAM_STATE_LIMIT_BYTES,
                "failed_closed": True,
                "effect_executed": False,
                "installed_authority_count": 0,
                "checkpoint_status_after": "pending",
            },
        ],
        "authz_ledger": {"verified": True, "chain_breaks": 0},
        "restart_recovery": {
            "checkpoint_id": "cp-a12-1",
            "pending_preserved": True,
            "pre_restart_plan_id": "plan_sha256_restart",
            "post_restart_plan_id": "plan_sha256_restart",
            "effect_execution_count": 1,
            "corrupt_grant_replayed": False,
        },
    })
    return ev


class TestTB07ApprovalsChecker(unittest.TestCase):
    def test_slot_catalog_covers_all_five_families(self) -> None:
        families = {meta["family"] for meta in TB07_SLOTS.values()}
        self.assertEqual(families, {"A01", "A02", "A03", "A04", "A12"})
        self.assertEqual(H1_PROGRAM_STATE_LIMIT_BYTES, 67108864)

    def test_healthy_tb07_evidence_passes(self) -> None:
        res = check_tb07_evidence(_make_tb07_healthy_evidence())
        self.assertEqual(res["verdict"], "pass", msg=res)
        self.assertEqual(res["failed_rule_ids"], [])

    def test_a01_plan_hash_mismatch_and_unrevalidated_mutation_fail(self) -> None:
        ev = _make_tb07_healthy_evidence()
        ev["product_records"]["approval_records"][0]["installed_authority_plan_id"] = "plan_tampered"
        res = check_tb07_evidence(ev)
        self.assertEqual(res["verdict"], "fail")
        self.assertIn("plan_hash_mismatch", res["failed_rule_ids"])

        ev2 = _make_tb07_healthy_evidence()
        ev2["product_records"]["approval_records"][0]["file_change"]["observed_after_sha256"] = _sha("mutated\n")
        res2 = check_tb07_evidence(ev2)
        self.assertEqual(res2["verdict"], "fail")
        self.assertIn("unrevalidated_post_review_mutation", res2["failed_rule_ids"])

        # Revalidation accepts changed bytes when explicitly revalidated.
        ev2["product_records"]["approval_records"][0]["file_change"]["revalidated"] = True
        self.assertEqual(check_tb07_evidence(ev2)["verdict"], "pass")

        ev3 = _make_tb07_healthy_evidence()
        ev3["product_records"]["approval_records"][0]["resolver_person_id"] = ""
        res3 = check_tb07_evidence(ev3)
        self.assertEqual(res3["verdict"], "fail")
        self.assertIn("decision_missing_approver", res3["failed_rule_ids"])

    def test_a02_grant_scope_reuse_and_expiry_defects_fail(self) -> None:
        # Cross-chat / out-of-scope match MUST fail.
        ev = _make_tb07_healthy_evidence()
        ev["product_records"]["grant_scope_checks"][1]["matched"] = True
        res = check_tb07_evidence(ev)
        self.assertEqual(res["verdict"], "fail")
        self.assertIn("grant_matched_outside_scope", res["failed_rule_ids"])

        # In-scope reuse that re-asked MUST fail.
        ev2 = _make_tb07_healthy_evidence()
        ev2["product_records"]["grant_scope_checks"][0]["asked"] = True
        res2 = check_tb07_evidence(ev2)
        self.assertEqual(res2["verdict"], "fail")
        self.assertIn("in_scope_grant_reasked", res2["failed_rule_ids"])

        # Expired grant that still matched MUST fail.
        ev3 = _make_tb07_healthy_evidence()
        ev3["product_records"]["grant_scope_checks"][3]["matched"] = True
        res3 = check_tb07_evidence(ev3)
        self.assertEqual(res3["verdict"], "fail")
        self.assertIn("expired_grant_matched", res3["failed_rule_ids"])

    def test_a03_policy_never_grants_advanced_off_and_detection_defects_fail(self) -> None:
        ev = _make_tb07_healthy_evidence()
        ev["product_records"]["effective_policy_rules"].append(
            {"category": "command", "pattern": "curl *", "effect": "allow"}
        )
        res = check_tb07_evidence(ev)
        self.assertEqual(res["verdict"], "fail")
        self.assertIn("policy_granted_authority", res["failed_rule_ids"])

        ev2 = _make_tb07_healthy_evidence()
        ev2["product_records"]["posture_policy_checks"][0]["denied"] = False
        res2 = check_tb07_evidence(ev2)
        self.assertEqual(res2["verdict"], "fail")
        self.assertIn("deny_bypassed_under_advanced_off", res2["failed_rule_ids"])

        ev3 = _make_tb07_healthy_evidence()
        ev3["product_records"]["posture_policy_checks"][1]["asked"] = False
        res3 = check_tb07_evidence(ev3)
        self.assertEqual(res3["verdict"], "fail")
        self.assertIn("detection_rule_missed_ask", res3["failed_rule_ids"])

        ev4 = _make_tb07_healthy_evidence()
        ev4["product_records"]["posture_policy_checks"][2]["detection_hit"] = True
        res4 = check_tb07_evidence(ev4)
        self.assertEqual(res4["verdict"], "fail")
        self.assertIn("chat_text_triggered_detection", res4["failed_rule_ids"])

    def test_a04_unrecordable_decision_and_h1_capacity_limit_fail_closed(self) -> None:
        ev = _make_tb07_healthy_evidence()
        ev["product_records"]["unrecordable_decision_checks"][0]["effect_executed"] = True
        res = check_tb07_evidence(ev)
        self.assertEqual(res["verdict"], "fail")
        self.assertIn("unrecordable_decision_executed_effect", res["failed_rule_ids"])

        ev2 = _make_tb07_healthy_evidence()
        ev2["product_records"]["unrecordable_decision_checks"][1]["installed_authority_count"] = 1
        res2 = check_tb07_evidence(ev2)
        self.assertEqual(res2["verdict"], "fail")
        self.assertIn("h1_capacity_limit_not_fail_closed", res2["failed_rule_ids"])

        ev3 = _make_tb07_healthy_evidence()
        ev3["product_records"]["authz_ledger"]["verified"] = False
        res3 = check_tb07_evidence(ev3)
        self.assertEqual(res3["verdict"], "fail")
        self.assertIn("authz_ledger_chain_broken", res3["failed_rule_ids"])

    def test_a12_restart_recovery_defects_fail(self) -> None:
        ev = _make_tb07_healthy_evidence()
        ev["product_records"]["restart_recovery"]["effect_execution_count"] = 2
        res = check_tb07_evidence(ev)
        self.assertEqual(res["verdict"], "fail")
        self.assertIn("effect_ran_twice_after_restart", res["failed_rule_ids"])

        ev2 = _make_tb07_healthy_evidence()
        ev2["product_records"]["restart_recovery"]["pending_preserved"] = False
        res2 = check_tb07_evidence(ev2)
        self.assertEqual(res2["verdict"], "fail")
        self.assertIn("pending_approval_lost_on_restart", res2["failed_rule_ids"])

        ev3 = _make_tb07_healthy_evidence()
        ev3["product_records"]["restart_recovery"]["corrupt_grant_replayed"] = True
        res3 = check_tb07_evidence(ev3)
        self.assertEqual(res3["verdict"], "fail")
        self.assertIn("corrupt_grant_replayed_on_restart", res3["failed_rule_ids"])


class TestTB07InProcManifestAndSidecarRunner(unittest.TestCase):
    def test_inproc_manifest_covers_all_9_ledger_slots_and_real_go_selectors(self) -> None:
        wea_dir = pathlib.Path(__file__).resolve().parent
        repo_root = wea_dir.parent.parent.parent
        manifest_path = wea_dir / "tb07_inproc_manifest.json"
        self.assertTrue(manifest_path.is_file(), "tb07_inproc_manifest.json must exist")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))

        self.assertEqual(manifest["card"], "TB07")
        self.assertEqual(manifest["lane"], "in-process")
        self.assertEqual(manifest["product_baseline"]["git_head"], "bbeb46e72ce3d1cfc052259cadcce29185bb40bd")
        self.assertEqual(manifest["total_inproc_slots"], 9)
        self.assertEqual(len(manifest["slots"]), 9)

        counts = Counter(slot["family"] for slot in manifest["slots"])
        self.assertEqual(dict(counts), {"A02": 2, "A04": 4, "A12": 3})

        overlay_sources = ""
        for rel in manifest["test_only_overlays"]:
            p = repo_root / rel
            self.assertTrue(p.is_file(), f"overlay file missing: {rel}")
            overlay_sources += p.read_text(encoding="utf-8") + "\n"

        slot_ids = set()
        for slot in manifest["slots"]:
            self.assertNotIn(slot["slot_id"], slot_ids)
            slot_ids.add(slot["slot_id"])
            top_fn = slot["go_test_selector"].lstrip("^").rstrip("$").split("/")[0]
            self.assertIn(f"func {top_fn}(", overlay_sources, f"missing Go test {top_fn}")

    def test_sidecar_slot_schedule_matches_39_ledger_slots(self) -> None:
        self.assertEqual(len(TB07_SIDECAR_SLOT_SPECS), 39)
        counts = Counter(s["family"] for s in TB07_SIDECAR_SLOT_SPECS)
        self.assertEqual(dict(counts), EXPECTED_SIDECAR_COUNTS_BY_FAMILY)
        self.assertEqual(len({s["slot_id"] for s in TB07_SIDECAR_SLOT_SPECS}), 39)

    def test_sidecar_receipt_schema_and_fail_closed_unverified_provenance(self) -> None:
        spec = TB07_SIDECAR_SLOT_SPECS[0]
        prov_ok = {
            "product_source": {"verified": True, "effective_git_head": "bbeb46e72ce3d1cfc052259cadcce29185bb40bd"},
            "harness_source": {"git_head": "7a61cae5dbbdb0ec4023189617fa8cd6e8eb4924"},
        }
        rec = _build_receipt(
            spec=spec,
            prov=prov_ok,
            typed_result={"outcome": "completed"},
            independent_observation={"hash_match": True},
            passed=True,
        )
        for key in ("slot_id", "family", "lane", "product_source", "harness_source", "typed_result", "independent_observation", "verdict"):
            self.assertIn(key, rec)
        self.assertEqual(rec["lane"], "sidecar")
        self.assertEqual(rec["verdict"], "pass")

        prov_unverified = copy.deepcopy(prov_ok)
        prov_unverified["product_source"]["verified"] = False
        rec_fail = _build_receipt(
            spec=spec,
            prov=prov_unverified,
            typed_result={"outcome": "completed"},
            independent_observation={"hash_match": True},
            passed=True,
        )
        self.assertEqual(rec_fail["verdict"], "fail")
        self.assertIn("unverified_source_witness", rec_fail["failure_reasons"])

    def test_call_with_checkpoint_inspect_returns_immediately_when_tool_settles_without_checkpoint(self) -> None:
        import sqlite3
        import tempfile

        with tempfile.TemporaryDirectory() as tmp:
            app_dir = pathlib.Path(tmp)
            db_path = app_dir / "store.db"
            conn = sqlite3.connect(db_path)
            conn.execute("CREATE TABLE messages (session_id TEXT, tool_result_call_id TEXT, tool_result_json TEXT)")
            conn.commit()
            conn.close()

            class FakeApp:
                directory = app_dir

                def request(self, path: str, body: Any = None, **_kw: Any) -> Any:
                    if path.startswith("/harness/llm/pending"):
                        return {"pending": True, "id": "turn-1", "session_id": "sess-1"}
                    if path == "/harness/llm/respond":
                        cid = body["tool_calls"][0]["id"]
                        c = sqlite3.connect(db_path)
                        c.execute(
                            "INSERT INTO messages VALUES (?, ?, ?)",
                            ("sess-1", cid, json.dumps({"outcome": "rejected", "rejection": {"code": "WRITE_SCOPE_DENIED"}})),
                        )
                        c.commit()
                        c.close()
                        return {"ok": True}
                    if "/checkpoints" in path:
                        return {"checkpoints": []}
                    return {}

                def wait_for(self, fn: Any, _phase: str, _sid: Any = None, timeout: float = 20.0) -> Any:
                    self.last_timeout = timeout
                    val = fn()
                    if val is None:
                        raise RuntimeError("no progress")
                    return val

            fake = FakeApp()
            out = _call_with_checkpoint_inspect(
                fake, "sess-1", "command", {"command": "printf x > /outside"}, action="approve", rung="once", timeout_s=20.0
            )
            self.assertFalse(out["checkpoint"]["asked"])
            self.assertFalse(out["checkpoint"]["timed_out"])
            self.assertEqual(out["result"]["outcome"], "rejected")
            self.assertEqual(fake.last_timeout, 20.0)


if __name__ == "__main__":
    unittest.main()
