"""All 12 WEA checker qualification controls + missing-instrumentation control (TB02).

7 injected-defect controls (MUST fail with their exact defect rule ID):
  1. wrong_author
  2. missing_landed_path
  3. landed_path_absent_from_result
  4. stale_read_basis_accepted
  5. grant_matched_across_chats
  6. unrecorded_connection
  7. canary_in_record

5 healthy/benign controls (MUST pass):
  1. healthy_lag
  2. late_card
  3. expected_refusal
  4. retention_gap
  5. clean_rebase

Plus:
  - missing_instrumentation (MUST yield verdict == "unknown", never "pass").
"""

from __future__ import annotations

import copy
from typing import Any

from wea.checker import check_evidence


def make_healthy_baseline() -> dict[str, Any]:
    """Construct a complete, healthy WEA evidence bundle suitable for defect injection."""
    return {
        "schema": "wea-evidence-v1",
        "slot_id": "ctrl-healthy-baseline",
        "provenance": {
            "lane": "sidecar",
            "product_source": {
                "executable_path": "/opt/wea/bin/lycaon-dev",
                "executable_sha256": "a" * 64,
                "go_build_id": "buildid-baseline-bbeb46e72c",
                "effective_git_head": "bbeb46e72ce3d1cfc052259cadcce29185bb40bd",
                "verified": True,
            },
            "harness_source": {
                "git_head": "029952e07b7a5e67e4eb0c2a0d3467feded3f113",
                "git_tree": "6e3e9f814c25bb4febb6e75983122068a9a9a2d6",
                "git_branch": "wea/tb02-driver",
                "dirty": False,
            },
        },
        "required_observers": ["primary_pre_hashes", "primary_post_hashes", "observed_writers"],
        "observer": {
            "instrumentation_complete": True,
            "primary_pre_hashes": {"app.py": "1" * 64, "README.md": "2" * 64},
            "primary_post_hashes": {"app.py": "3" * 64, "README.md": "2" * 64},
            "observed_writers": {"app.py": "agent"},
            "network_connections": [
                {"address": "127.0.0.1:41234", "method": "GET", "path": "/ping"},
            ],
        },
        "product_records": {
            "worker_landing": {
                "landed_paths": {"app.py": "3" * 64},
                "result_paths": ["app.py"],
                "clean_rebase_paths": [],
            },
            "attributions": [
                {"path": "app.py", "author": "agent"},
            ],
            "edit_operations": [
                {
                    "path": "app.py",
                    "read_basis_sha256": "1" * 64,
                    "disk_sha256_at_apply": "1" * 64,
                    "accepted": True,
                },
            ],
            "grant_uses": [
                {
                    "grant_id": "grant-1",
                    "grant_session_id": "sess-alpha",
                    "target_session_id": "sess-alpha",
                    "matched": True,
                },
            ],
            "recorded_connections": [
                {"address": "127.0.0.1:41234", "method": "GET", "path": "/ping", "allowed": True},
            ],
        },
        "canary_scan": {
            "clean": True,
            "canary_count": 1,
            "leak_count": 0,
            "findings": [],
        },
    }


def build_defect_controls() -> dict[str, tuple[dict[str, Any], str]]:
    """Return all 7 injected-defect controls mapping control_name -> (evidence, expected_rule_id)."""
    controls: dict[str, tuple[dict[str, Any], str]] = {}

    # 1. wrong_author
    e = copy.deepcopy(make_healthy_baseline())
    e["product_records"]["attributions"][0]["author"] = "human"
    controls["wrong_author"] = (e, "wrong_author")

    # 2. missing_landed_path
    e = copy.deepcopy(make_healthy_baseline())
    e["product_records"]["worker_landing"]["landed_paths"]["ghost.py"] = "9" * 64
    controls["missing_landed_path"] = (e, "missing_landed_path")

    # 3. landed_path_absent_from_result
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["primary_post_hashes"]["sneaky.py"] = "8" * 64
    controls["landed_path_absent_from_result"] = (e, "landed_path_absent_from_result")

    # 4. stale_read_basis_accepted
    e = copy.deepcopy(make_healthy_baseline())
    e["product_records"]["edit_operations"][0]["read_basis_sha256"] = "0" * 64
    e["product_records"]["edit_operations"][0]["disk_sha256_at_apply"] = "1" * 64
    e["product_records"]["edit_operations"][0]["accepted"] = True
    controls["stale_read_basis_accepted"] = (e, "stale_read_basis_accepted")

    # 5. grant_matched_across_chats
    e = copy.deepcopy(make_healthy_baseline())
    e["product_records"]["grant_uses"][0]["grant_session_id"] = "sess-alpha"
    e["product_records"]["grant_uses"][0]["target_session_id"] = "sess-beta"
    e["product_records"]["grant_uses"][0]["matched"] = True
    controls["grant_matched_across_chats"] = (e, "grant_matched_across_chats")

    # 6. unrecorded_connection
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["network_connections"].append(
        {"address": "127.0.0.1:49999", "method": "POST", "path": "/exfil"}
    )
    controls["unrecorded_connection"] = (e, "unrecorded_connection")

    # 7. canary_in_record
    e = copy.deepcopy(make_healthy_baseline())
    e["canary_scan"] = {
        "clean": False,
        "canary_count": 1,
        "leak_count": 1,
        "findings": [
            {
                "canary_sha256": "deadbeef12345678",
                "locator": "sqlite://store.db/messages[4].content",
                "kind": "sqlite",
            }
        ],
    }
    controls["canary_in_record"] = (e, "canary_in_record")

    return controls


def build_healthy_controls() -> dict[str, dict[str, Any]]:
    """Return all 5 healthy/benign controls (each MUST pass)."""
    controls: dict[str, dict[str, Any]] = {}

    # 1. healthy_lag
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["lag_ms"] = 4750
    controls["healthy_lag"] = e

    # 2. late_card
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["late_card_arrived"] = True
    controls["late_card"] = e

    # 3. expected_refusal (typed refusal + unchanged host bytes + passing adjacent positive control)
    e = copy.deepcopy(make_healthy_baseline())
    e["expected_refusal"] = {
        "typed_refusal_code": "SANDBOX_TRY_WRITE_ROOT",
        "host_target_unchanged": True,
        "adjacent_positive_control_passed": True,
    }
    controls["expected_refusal"] = e

    # 4. retention_gap (pruned historical body explicitly marked as expected retention gap -> pass)
    e = copy.deepcopy(make_healthy_baseline())
    e["historical_bodies"] = {
        "old-window-001": {
            "status": "retention_gap",
            "retention_gap": True,
            "body": None,
            "reason": "evicted_by_history_retention_policy",
        }
    }
    controls["retention_gap"] = e

    # 5. clean_rebase (concurrent unrelated primary-tree change cleanly rebased)
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["primary_post_hashes"]["concurrent_other.txt"] = "7" * 64
    e["product_records"]["worker_landing"]["clean_rebase_paths"] = ["concurrent_other.txt"]
    controls["clean_rebase"] = e

    return controls


def build_missing_instrumentation_control() -> dict[str, Any]:
    """Build an evidence bundle missing required instrumentation (MUST yield verdict=unknown)."""
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"].pop("primary_post_hashes", None)
    return e


def run_qualification_controls() -> dict[str, Any]:
    """Execute all 12 qualification controls + missing-instrumentation control and verify every verdict."""
    defect_results: dict[str, Any] = {}
    for name, (ev, expected_rule) in build_defect_controls().items():
        res = check_evidence(ev)
        ok = res["verdict"] == "fail" and expected_rule in res.get("failed_rule_ids", [])
        defect_results[name] = {
            "expected_verdict": "fail",
            "expected_rule_id": expected_rule,
            "actual_verdict": res["verdict"],
            "actual_rule_ids": res.get("failed_rule_ids", []),
            "qualified": ok,
        }

    healthy_results: dict[str, Any] = {}
    for name, ev in build_healthy_controls().items():
        res = check_evidence(ev)
        ok = res["verdict"] == "pass"
        if name == "retention_gap":
            ok = ok and "old-window-001" in res.get("retention_gaps_accepted", [])
        healthy_results[name] = {
            "expected_verdict": "pass",
            "actual_verdict": res["verdict"],
            "retention_gaps_accepted": res.get("retention_gaps_accepted", []),
            "qualified": ok,
        }

    missing_res = check_evidence(build_missing_instrumentation_control())
    missing_control = {
        "expected_verdict": "unknown",
        "actual_verdict": missing_res["verdict"],
        "missing_instrumentation": missing_res.get("missing_instrumentation", []),
        "qualified": missing_res["verdict"] == "unknown",
    }

    all_passed = (
        all(r["qualified"] for r in defect_results.values())
        and all(r["qualified"] for r in healthy_results.values())
        and missing_control["qualified"]
    )
    return {
        "schema": "wea-checker-controls-v1",
        "qualified": all_passed,
        "defect_controls": defect_results,
        "healthy_controls": healthy_results,
        "missing_instrumentation_control": missing_control,
    }
