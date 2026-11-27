"""Unit tests for TB07 · Approvals, grants and decision records (A01, A02, A03, A04, A12)."""

from __future__ import annotations

import copy
import hashlib
import unittest
from typing import Any

from wea.controls import make_healthy_baseline
from wea.tb07_approvals import H1_PROGRAM_STATE_LIMIT_BYTES, TB07_SLOTS, check_tb07_evidence


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


if __name__ == "__main__":
    unittest.main()
