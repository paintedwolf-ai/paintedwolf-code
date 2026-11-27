"""Deterministic WEA checker (TB02).

Verdict semantics:
  - "unknown": required instrumentation or observer fields are missing (never returns "pass").
  - "fail": one or more invariant/defect rules fired, each reported with its exact rule_id.
  - "pass": all required instrumentation is present (or an explicit expected `retention_gap`
    is declared for pruned historical bodies) and all applicable rules pass.
"""

from __future__ import annotations

from typing import Any

REQUIRED_INSTRUMENTATION_KEYS = ("provenance", "observer", "product_records")


def _is_retention_gap(obj: Any) -> bool:
    return isinstance(obj, dict) and (
        obj.get("retention_gap") is True or obj.get("status") == "retention_gap"
    )


def _check_instrumentation(evidence: dict[str, Any]) -> list[str]:
    missing: list[str] = []
    for key in REQUIRED_INSTRUMENTATION_KEYS:
        val = evidence.get(key)
        if val is None or (isinstance(val, dict) and not val):
            missing.append(f"missing_instrumentation:{key}")

    prov = evidence.get("provenance")
    if isinstance(prov, dict) and prov:
        if not isinstance(prov.get("product_source"), dict) or not prov["product_source"]:
            missing.append("missing_instrumentation:provenance.product_source")
        if not isinstance(prov.get("harness_source"), dict) or not prov["harness_source"]:
            missing.append("missing_instrumentation:provenance.harness_source")

    obs = evidence.get("observer")
    if isinstance(obs, dict) and obs:
        if obs.get("instrumentation_complete") is False:
            missing.append("missing_instrumentation:observer.incomplete")
        required_obs = evidence.get("required_observers") or ()
        for field in required_obs:
            val = obs.get(field)
            if val is None:
                missing.append(f"missing_instrumentation:observer.{field}")
            elif isinstance(val, dict) and not val and not _is_retention_gap(val):
                missing.append(f"missing_instrumentation:observer.{field}")

    # Distinguish expected retention_gap on historical bodies from missing required evidence.
    hist = evidence.get("historical_bodies")
    if isinstance(hist, dict):
        for body_id, entry in hist.items():
            if entry is None:
                missing.append(f"missing_instrumentation:historical_bodies.{body_id}")
            elif isinstance(entry, dict) and entry.get("body") is None and not _is_retention_gap(entry):
                missing.append(f"missing_instrumentation:historical_bodies.{body_id}")

    return missing


def check_evidence(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate a WEA evidence bundle and return a deterministic verdict + rule findings."""
    missing = _check_instrumentation(evidence)
    if missing:
        return {
            "verdict": "unknown",
            "passed": False,
            "missing_instrumentation": missing,
            "failures": [],
            "retention_gaps_accepted": [],
            "notes": ["Missing required instrumentation yields verdict=unknown (never pass)."],
        }

    failures: list[dict[str, Any]] = []
    retention_gaps_accepted: list[str] = []
    notes: list[str] = []

    obs = evidence["observer"]
    prod = evidence["product_records"]

    # Track explicit retention gaps (accepted, not treated as missing evidence).
    hist = evidence.get("historical_bodies") or {}
    if isinstance(hist, dict):
        for body_id, entry in hist.items():
            if _is_retention_gap(entry):
                retention_gaps_accepted.append(body_id)

    # 1. Canary leak check (`canary_in_record`)
    canary_scan = evidence.get("canary_scan")
    if isinstance(canary_scan, dict):
        if canary_scan.get("clean") is False or int(canary_scan.get("leak_count", 0)) > 0:
            failures.append({
                "rule_id": "canary_in_record",
                "message": "Run-scoped D6 canary token leaked into persisted record or evidence.",
                "details": {"leak_count": canary_scan.get("leak_count", 0), "findings": canary_scan.get("findings", [])},
            })

    # 2. Workers landing checks (`missing_landed_path`, `landed_path_absent_from_result`)
    worker_landing = prod.get("worker_landing")
    if isinstance(worker_landing, dict):
        primary_post = obs.get("primary_post_hashes") or {}
        primary_pre = obs.get("primary_pre_hashes") or {}
        landed_paths = worker_landing.get("landed_paths") or {}
        result_paths = set(worker_landing.get("result_paths") or ())
        rebased_paths = set(worker_landing.get("clean_rebase_paths") or ())

        # Every path claimed as landed must exist in post primary tree with matching SHA-256.
        for rel, expected_sha in landed_paths.items():
            actual_sha = primary_post.get(rel)
            if actual_sha is None or (expected_sha and actual_sha != expected_sha):
                failures.append({
                    "rule_id": "missing_landed_path",
                    "message": f"Claimed landed path '{rel}' is missing or hash-mismatched in post primary tree.",
                    "details": {"path": rel, "expected_sha256": expected_sha, "actual_sha256": actual_sha},
                })

        # Every path modified in primary tree (excluding clean-rebase concurrent unrelated edits)
        # must be present in the worker's result_paths.
        changed_in_primary = {
            rel for rel, sha in primary_post.items()
            if primary_pre.get(rel) != sha
        } | {
            rel for rel in primary_pre
            if rel not in primary_post
        }
        for rel in sorted(changed_in_primary - rebased_paths):
            if result_paths and rel not in result_paths:
                failures.append({
                    "rule_id": "landed_path_absent_from_result",
                    "message": f"Primary tree path '{rel}' changed on landing but is absent from worker result.",
                    "details": {"path": rel, "result_paths": sorted(result_paths)},
                })

    # 3. Edits author attribution check (`wrong_author`)
    attributions = prod.get("attributions") or []
    observed_writers = obs.get("observed_writers") or {}
    for attr in attributions:
        path = attr.get("path")
        recorded_author = attr.get("author")
        expected_author = observed_writers.get(path)
        if expected_author and recorded_author != expected_author:
            failures.append({
                "rule_id": "wrong_author",
                "message": f"Attribution for '{path}' recorded author '{recorded_author}', expected '{expected_author}'.",
                "details": {"path": path, "recorded_author": recorded_author, "expected_author": expected_author},
            })

    # 4. Stale read-basis acceptance check (`stale_read_basis_accepted`)
    edit_ops = prod.get("edit_operations") or []
    for op in edit_ops:
        read_basis = op.get("read_basis_sha256")
        current_disk = op.get("disk_sha256_at_apply")
        accepted = op.get("accepted") is True
        if read_basis and current_disk and read_basis != current_disk and accepted:
            failures.append({
                "rule_id": "stale_read_basis_accepted",
                "message": f"Edit to '{op.get('path')}' accepted a stale read basis without conflict refusal.",
                "details": {
                    "path": op.get("path"),
                    "read_basis_sha256": read_basis,
                    "disk_sha256_at_apply": current_disk,
                },
            })

    # 5. Cross-chat grant isolation check (`grant_matched_across_chats`)
    grant_uses = prod.get("grant_uses") or []
    for use in grant_uses:
        grant_sid = use.get("grant_session_id")
        target_sid = use.get("target_session_id")
        matched = use.get("matched") is True
        if matched and grant_sid and target_sid and grant_sid != target_sid:
            failures.append({
                "rule_id": "grant_matched_across_chats",
                "message": f"Chat grant '{use.get('grant_id')}' from session '{grant_sid}' matched in '{target_sid}'.",
                "details": {
                    "grant_id": use.get("grant_id"),
                    "grant_session_id": grant_sid,
                    "target_session_id": target_sid,
                },
            })

    # 6. Network fixture vs. product record check (`unrecorded_connection`)
    fixture_conns = obs.get("network_connections") or []
    recorded_conns = prod.get("recorded_connections") or []
    recorded_keys = {
        (c.get("address"), c.get("path", ""), c.get("method", ""))
        for c in recorded_conns
        if c.get("allowed", True) is True
    }
    for conn in fixture_conns:
        key = (conn.get("address"), conn.get("path", ""), conn.get("method", ""))
        if key not in recorded_keys:
            failures.append({
                "rule_id": "unrecorded_connection",
                "message": f"Fixture endpoint observed connection {key} with no matching allowed product record.",
                "details": {"observed_connection": conn},
            })

    # 7. Negative-claim / expected refusal check (typed refusal + unchanged host bytes + passing adjacent positive control)
    refusal = evidence.get("expected_refusal")
    if isinstance(refusal, dict):
        if not refusal.get("typed_refusal_code"):
            failures.append({
                "rule_id": "refusal_missing_code",
                "message": "Expected refusal is missing a typed refusal code.",
                "details": refusal,
            })
        if refusal.get("host_target_unchanged") is not True:
            failures.append({
                "rule_id": "refusal_mutated_target",
                "message": "Refused action still mutated the host target outside the boundary.",
                "details": refusal,
            })
        if refusal.get("adjacent_positive_control_passed") is not True:
            failures.append({
                "rule_id": "refusal_missing_positive_control",
                "message": "Negative claim lacks a passing adjacent positive control.",
                "details": refusal,
            })

    # Benign timing/presentation annotations (healthy_lag, late_card) are recorded as notes only, never failures.
    if obs.get("lag_ms"):
        notes.append(f"Benign lag observed ({obs['lag_ms']}ms); elapsed time is diagnostic only.")
    if obs.get("late_card_arrived"):
        notes.append("Late approval/worker card arrived cleanly after turn transition; accepted.")

    verdict = "pass" if not failures else "fail"
    return {
        "verdict": verdict,
        "passed": verdict == "pass",
        "missing_instrumentation": [],
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
        "retention_gaps_accepted": retention_gaps_accepted,
        "notes": notes,
    }
