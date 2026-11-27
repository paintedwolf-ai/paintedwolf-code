"""WEA checker qualification controls (TB02).

10 injected-defect controls (MUST fail with their exact defect rule ID):
  1. wrong_author
  2. missing_landed_path
  3. landed_path_absent_from_result
  4. stale_read_basis_accepted
  5. grant_matched_across_chats
  6. unrecorded_connection
  7. canary_in_record
  8. empty_claimed_paths_with_real_change  (-> landed_path_absent_from_result)
  9. unverified_source_witness             (-> unverified_source_witness)
 10. surplus_duplicate_connection          (-> unrecorded_connection)

5 healthy/benign controls (MUST pass):
  1. healthy_lag
  2. late_card
  3. expected_refusal
  4. retention_gap
  5. clean_rebase (tied to exact combined expected bytes)

2 missing-instrumentation controls (MUST yield verdict == "unknown"):
  1. missing_instrumentation
  2. missing_network_witness
"""

from __future__ import annotations

import copy
import hashlib
from typing import Any

from wea.checker import check_evidence


def _sha(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def make_healthy_baseline() -> dict[str, Any]:
    """Construct a complete, healthy WEA evidence bundle suitable for defect injection."""
    pre_app = "line1\nline2\n"
    post_app = "line1\nline2-edited\n"
    pre_readme = "readme\n"
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
        "required_observers": ["primary_pre_hashes", "primary_post_hashes", "observed_writers", "network_connections"],
        "observer": {
            "instrumentation_complete": True,
            "primary_pre_hashes": {"app.py": _sha(pre_app), "README.md": _sha(pre_readme)},
            "primary_post_hashes": {"app.py": _sha(post_app), "README.md": _sha(pre_readme)},
            "observed_writers": {"app.py": "agent"},
            "network_witness": {"live_witness": True, "fixture_count": 1},
            "network_connections": [
                {"address": "127.0.0.1:41234", "method": "GET", "path": "/ping"},
            ],
        },
        "product_records": {
            "worker_landing": {
                "landed_paths": {"app.py": _sha(post_app)},
                "result_paths": ["app.py"],
                "expected_post_hashes": {"app.py": _sha(post_app)},
                "clean_rebase_paths": [],
            },
            "attributions": [
                {"path": "app.py", "author": "agent"},
            ],
            "edit_operations": [
                {
                    "path": "app.py",
                    "read_basis_sha256": _sha(pre_app),
                    "disk_sha256_at_apply": _sha(pre_app),
                    "recorded_after_sha256": _sha(post_app),
                    "expected_after_sha256": _sha(post_app),
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
    """Return all 10 injected-defect controls mapping control_name -> (evidence, expected_rule_id)."""
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

    # 8. empty_claimed_paths_with_real_change (fail-closed: empty result_paths MUST NOT skip check)
    e = copy.deepcopy(make_healthy_baseline())
    e["product_records"]["worker_landing"]["result_paths"] = []
    controls["empty_claimed_paths_with_real_change"] = (e, "landed_path_absent_from_result")

    # 9. unverified_source_witness (fail-closed: product_source.verified=False MUST fail)
    e = copy.deepcopy(make_healthy_baseline())
    e["provenance"]["product_source"]["verified"] = False
    controls["unverified_source_witness"] = (e, "unverified_source_witness")

    # 10. surplus_duplicate_connection (multiset cardinality: 2 observed vs 1 recorded MUST fail)
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["network_connections"].append(
        {"address": "127.0.0.1:41234", "method": "GET", "path": "/ping"}
    )
    controls["surplus_duplicate_connection"] = (e, "unrecorded_connection")

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

    # 3. expected_refusal (typed refusal + unchanged host bytes + passing adjacent positive control + live empty witness)
    e = copy.deepcopy(make_healthy_baseline())
    pos_sha = _sha("permitted-inside\n")
    e["observer"]["network_connections"] = []
    e["observer"]["network_witness"] = {"live_witness": True, "fixture_count": 1}
    e["product_records"]["recorded_connections"] = []
    e["expected_refusal"] = {
        "typed_refusal_code": "SANDBOX_TRY_WRITE_ROOT",
        "host_target_unchanged": True,
        "adjacent_positive_control_passed": True,
        "adjacent_positive_expected_sha256": pos_sha,
        "adjacent_positive_actual_sha256": pos_sha,
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

    # 5. clean_rebase (non-overlapping outside edit + agent edit -> exact combined expected bytes verified)
    e = copy.deepcopy(make_healthy_baseline())
    base_shared = "header\nsection_a: orig\nsection_b: orig\n"
    combined_shared = "header\nsection_a: outside-edit\nsection_b: agent-edit\n"
    combined_sha = _sha(combined_shared)
    e["observer"]["primary_pre_hashes"]["shared.txt"] = _sha(base_shared)
    e["observer"]["primary_post_hashes"]["shared.txt"] = combined_sha
    e["product_records"]["worker_landing"]["clean_rebase_paths"] = ["shared.txt"]
    e["product_records"]["worker_landing"]["clean_rebase_expected_sha256"] = {"shared.txt": combined_sha}
    controls["clean_rebase"] = e

    return controls


def build_missing_instrumentation_control() -> dict[str, Any]:
    """Build an evidence bundle missing required instrumentation (MUST yield verdict=unknown)."""
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"].pop("primary_post_hashes", None)
    return e


def build_missing_network_witness_control() -> dict[str, Any]:
    """Build an evidence bundle with empty network_connections but NO live_witness (MUST yield unknown)."""
    e = copy.deepcopy(make_healthy_baseline())
    e["observer"]["network_connections"] = []
    e["observer"].pop("network_witness", None)
    return e


def run_qualification_controls() -> dict[str, Any]:
    """Execute all qualification controls and verify every verdict and rule ID."""
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

    missing_nw_res = check_evidence(build_missing_network_witness_control())
    missing_network_witness_control = {
        "expected_verdict": "unknown",
        "actual_verdict": missing_nw_res["verdict"],
        "missing_instrumentation": missing_nw_res.get("missing_instrumentation", []),
        "qualified": missing_nw_res["verdict"] == "unknown",
    }

    all_passed = (
        all(r["qualified"] for r in defect_results.values())
        and all(r["qualified"] for r in healthy_results.values())
        and missing_control["qualified"]
        and missing_network_witness_control["qualified"]
    )
    return {
        "schema": "wea-checker-controls-v1",
        "qualified": all_passed,
        "defect_controls": defect_results,
        "healthy_controls": healthy_results,
        "missing_instrumentation_control": missing_control,
        "missing_network_witness_control": missing_network_witness_control,
    }
