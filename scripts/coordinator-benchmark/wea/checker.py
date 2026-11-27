"""Deterministic, fail-closed WEA checker (TB02).

Verdict semantics:
  - "unknown": required instrumentation, observer fields, live witnesses, or required
    product-side records (worker result/version hashes, edit attribution/version hashes)
    are missing (never returns "pass"). Explicit empty observations (`[]` paired with
    `live_witness: true`) are valid; missing observations (`None` or missing
    `live_witness`) yield "unknown".
  - "fail": one or more invariant/defect rules fired (including unverified product source
    witness, empty claimed paths with real disk change, bidirectional connection
    cardinality mismatch, wrong author, stale read basis, cross-chat grant match,
    canary leak, or mismatched expected bytes), each reported with its exact `rule_id`.
  - "pass": all required instrumentation, independent observers, and product source
    witnesses are complete and verified (or an explicit expected `retention_gap` is
    declared for pruned historical bodies) and every applicable rule passes.
"""

from __future__ import annotations

from collections import Counter
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
        ps = prov.get("product_source")
        hs = prov.get("harness_source")
        if not isinstance(ps, dict) or not ps:
            missing.append("missing_instrumentation:provenance.product_source")
        else:
            if not ps.get("executable_sha256"):
                missing.append("missing_instrumentation:provenance.product_source.executable_sha256")
            if not ps.get("go_build_id"):
                missing.append("missing_instrumentation:provenance.product_source.go_build_id")
            if "verified" not in ps or ps.get("verified") is None:
                missing.append("missing_instrumentation:provenance.product_source.verified")
        if not isinstance(hs, dict) or not hs or not hs.get("git_head"):
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
            elif field == "network_connections":
                nw = obs.get("network_witness")
                if not isinstance(nw, dict) or nw.get("live_witness") is not True:
                    missing.append("missing_instrumentation:observer.network_witness")
            elif isinstance(val, dict) and not val and not _is_retention_gap(val):
                missing.append(f"missing_instrumentation:observer.{field}")
            elif isinstance(val, list) and not val:
                nw = obs.get("network_witness")
                if field == "network_fixtures" and (not isinstance(nw, dict) or nw.get("live_witness") is not True):
                    missing.append(f"missing_instrumentation:observer.{field}")

    # Product-side completeness checks: missing required product proofs MUST yield unknown.
    prod = evidence.get("product_records")
    if isinstance(prod, dict) and prod:
        wl = prod.get("worker_landing")
        if isinstance(wl, dict):
            if wl.get("result_paths") is None:
                missing.append("missing_instrumentation:product_records.worker_landing.result_paths")
            if wl.get("require_complete_proof", False):
                if not wl.get("job_id"):
                    missing.append("missing_instrumentation:product_records.worker_landing.job_id")
                if not wl.get("worker_result_ids"):
                    missing.append("missing_instrumentation:product_records.worker_landing.worker_result_ids")
                if not wl.get("disposition_states"):
                    missing.append("missing_instrumentation:product_records.worker_landing.disposition_states")
            landed_paths = wl.get("landed_paths")
            if isinstance(landed_paths, dict):
                for rel, sha in landed_paths.items():
                    if not sha:
                        missing.append(f"missing_instrumentation:product_records.worker_landing.version_hash:{rel}")

        edit_ops = prod.get("edit_operations")
        if isinstance(edit_ops, list) and edit_ops:
            attrs = prod.get("attributions")
            attr_paths = {a.get("path") for a in (attrs or []) if isinstance(a, dict) and a.get("author")}
            for idx, op in enumerate(edit_ops):
                if not isinstance(op, dict):
                    continue
                rel = op.get("path") or f"op[{idx}]"
                if not op.get("read_basis_sha256"):
                    missing.append(f"missing_instrumentation:product_records.edit_operations.read_basis:{rel}")
                if not op.get("disk_sha256_at_apply"):
                    missing.append(f"missing_instrumentation:product_records.edit_operations.disk_at_apply:{rel}")
                if op.get("accepted") is True and not op.get("recorded_after_sha256"):
                    missing.append(f"missing_instrumentation:product_records.edit_operations.recorded_after:{rel}")
                if op.get("path") and op.get("path") not in attr_paths:
                    missing.append(f"missing_instrumentation:product_records.attributions:{rel}")

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
            "failed_rule_ids": [],
            "retention_gaps_accepted": [],
            "notes": ["Missing required instrumentation yields verdict=unknown (never pass)."],
        }

    failures: list[dict[str, Any]] = []
    retention_gaps_accepted: list[str] = []
    notes: list[str] = []

    prov = evidence["provenance"]
    obs = evidence["observer"]
    prod = evidence["product_records"]

    # 0. Product source witness MUST be verified (fail-closed).
    product_source = prov["product_source"]
    if product_source.get("verified") is not True:
        failures.append({
            "rule_id": "unverified_source_witness",
            "message": "Product source provenance is not verified (verified != true).",
            "details": {
                "executable_sha256": product_source.get("executable_sha256"),
                "go_build_id": product_source.get("go_build_id"),
                "verified": product_source.get("verified"),
            },
        })

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

    # 2. Workers landing checks (`missing_landed_path`, `landed_path_absent_from_result`, `rebase_bytes_mismatch`)
    worker_landing = prod.get("worker_landing")
    if isinstance(worker_landing, dict):
        primary_post = obs.get("primary_post_hashes") or {}
        primary_pre = obs.get("primary_pre_hashes") or {}
        landed_paths = worker_landing.get("landed_paths") or {}
        result_paths = set(worker_landing.get("result_paths") or ())
        rebased_paths = set(worker_landing.get("clean_rebase_paths") or ())
        expected_post_hashes = worker_landing.get("expected_post_hashes") or {}
        clean_rebase_expected = worker_landing.get("clean_rebase_expected_sha256") or {}

        for rel, expected_sha in landed_paths.items():
            actual_sha = primary_post.get(rel)
            if actual_sha is None or actual_sha != expected_sha:
                failures.append({
                    "rule_id": "missing_landed_path",
                    "message": f"Claimed landed path '{rel}' is missing or hash-mismatched in post primary tree.",
                    "details": {"path": rel, "expected_sha256": expected_sha, "actual_sha256": actual_sha},
                })

        for rel, expected_sha in expected_post_hashes.items():
            actual_sha = primary_post.get(rel)
            if actual_sha != expected_sha:
                failures.append({
                    "rule_id": "missing_landed_path",
                    "message": f"Expected landed bytes for '{rel}' do not match post primary tree.",
                    "details": {"path": rel, "expected_sha256": expected_sha, "actual_sha256": actual_sha},
                })

        for rel, combined_sha in clean_rebase_expected.items():
            actual_sha = primary_post.get(rel)
            if actual_sha != combined_sha:
                failures.append({
                    "rule_id": "rebase_bytes_mismatch",
                    "message": f"Clean rebase combined bytes for '{rel}' do not match post primary tree.",
                    "details": {"path": rel, "expected_combined_sha256": combined_sha, "actual_sha256": actual_sha},
                })

        # Every path modified in primary tree (excluding clean-rebase concurrent unrelated edits)
        # MUST be present in the worker's independent result_paths — even when result_paths is empty!
        changed_in_primary = {
            rel for rel, sha in primary_post.items()
            if primary_pre.get(rel) != sha
        } | {
            rel for rel in primary_pre
            if rel not in primary_post
        }
        for rel in sorted(changed_in_primary - rebased_paths):
            if rel not in result_paths:
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

    # 4. Stale read-basis, tool completion & expected edit bytes check
    edit_ops = prod.get("edit_operations") or []
    primary_post = obs.get("primary_post_hashes") or {}
    for op in edit_ops:
        if op.get("read_outcome") is not None and op.get("read_outcome") != "completed":
            failures.append({
                "rule_id": "edit_tool_not_completed",
                "message": f"Read tool for '{op.get('path')}' did not complete: {op.get('read_outcome')}.",
                "details": op,
            })
        if op.get("write_outcome") is not None and op.get("write_outcome") != "completed":
            failures.append({
                "rule_id": "edit_tool_not_completed",
                "message": f"Write tool for '{op.get('path')}' did not complete: {op.get('write_outcome')}.",
                "details": op,
            })
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
        expected_after = op.get("expected_after_sha256")
        recorded_after = op.get("recorded_after_sha256")
        disk_after = primary_post.get(op.get("path"))
        if expected_after:
            if disk_after != expected_after or recorded_after != expected_after:
                failures.append({
                    "rule_id": "unexpected_post_bytes",
                    "message": f"Post-edit bytes for '{op.get('path')}' do not match expected content SHA-256.",
                    "details": {
                        "path": op.get("path"),
                        "expected_after_sha256": expected_after,
                        "disk_after_sha256": disk_after,
                        "recorded_after_sha256": recorded_after,
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

    # 6. Bidirectional multiset connection cardinality (`unrecorded_connection` & `unobserved_product_connection`)
    fixture_conns = obs.get("network_connections")
    recorded_conns = prod.get("recorded_connections") or []
    if isinstance(fixture_conns, list):
        obs_counts: Counter[tuple[Any, Any, Any]] = Counter(
            (c.get("address"), c.get("method", ""), c.get("path", ""))
            for c in fixture_conns
        )
        prod_counts: Counter[tuple[Any, Any, Any]] = Counter(
            (c.get("address"), c.get("method", ""), c.get("path", ""))
            for c in recorded_conns
            if c.get("allowed", True) is True
        )
        for key in sorted(set(obs_counts) | set(prod_counts)):
            obs_n = obs_counts.get(key, 0)
            prod_n = prod_counts.get(key, 0)
            if obs_n > prod_n:
                failures.append({
                    "rule_id": "unrecorded_connection",
                    "message": (
                        f"Fixture endpoint observed {obs_n} connection(s) for {key}, "
                        f"but product recorded only {prod_n} allowed connection(s)."
                    ),
                    "details": {
                        "address": key[0],
                        "method": key[1],
                        "path": key[2],
                        "observed_count": obs_n,
                        "recorded_count": prod_n,
                        "surplus_observed": obs_n - prod_n,
                    },
                })
            elif prod_n > obs_n:
                failures.append({
                    "rule_id": "unobserved_product_connection",
                    "message": (
                        f"Product recorded {prod_n} allowed connection(s) for {key}, "
                        f"but fixture endpoint observed only {obs_n}."
                    ),
                    "details": {
                        "address": key[0],
                        "method": key[1],
                        "path": key[2],
                        "observed_count": obs_n,
                        "recorded_count": prod_n,
                        "surplus_product": prod_n - obs_n,
                    },
                })

    # 7. Negative-claim / expected refusal check
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
        exp_pos_sha = refusal.get("adjacent_positive_expected_sha256")
        act_pos_sha = refusal.get("adjacent_positive_actual_sha256")
        if exp_pos_sha and act_pos_sha != exp_pos_sha:
            failures.append({
                "rule_id": "refusal_positive_bytes_mismatch",
                "message": "Adjacent positive control bytes do not match expected SHA-256.",
                "details": {"expected_sha256": exp_pos_sha, "actual_sha256": act_pos_sha},
            })

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
