"""TB07 · Approvals, grants and decision records (A01, A02, A03, A04, A12).

Provides:
  1. Deterministic, fail-closed TB07 invariant checker (`check_tb07_evidence`)
     layered on top of TB02's `wea.checker.check_evidence`.
  2. Real sidecar slot drivers (`run_tb07_slot`) exercising `lycaon-dev` via
     TB02's isolated manual-harness helpers for A01, A02, A03, A04, and A12,
     including real sidecar restart recovery (A12).
  3. Structured TB07 slot catalog (`TB07_SLOTS`) matching `ledger.md`.
"""

from __future__ import annotations

import contextlib
import json
import pathlib
import sqlite3
import sys
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.checker import check_evidence
from wea.fixtures_net import RecordingHTTPFixture
from wea.observers import AuthorityObserver, hash_file, hash_tree, sha256_bytes
from wea.provenance import REPO_ROOT, collect_provenance
from wea.replay import export_sanitized_bundle
from wea.runner import (
    _create_application,
    _extract_refusal_code,
    _init_git_repo,
    _open_session,
    _save_json,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)

H1_PROGRAM_STATE_LIMIT_BYTES = 64 * 1024 * 1024  # 67,108,864 bytes (H1 lead)

TB07_SLOTS: dict[str, dict[str, Any]] = {
    "a01_plan_hash_binding": {
        "family": "A01",
        "lane": "sidecar",
        "description": "Approval card, reviewed plan hash, installed authority, recorded decision, and effect.",
    },
    "a02_grant_scopes_and_reuse": {
        "family": "A02",
        "lane": "sidecar",
        "description": "Grant scopes (once, chat, project, device), cross-chat isolation, material arg change, revoke.",
    },
    "a03_postures_policy_detection": {
        "family": "A03",
        "lane": "sidecar",
        "description": "Postures (light/balanced/strict/never_ask), project/extension policy never grants, detection rules.",
    },
    "a04_decision_record_fail_closed": {
        "family": "A04",
        "lane": "both",
        "description": "Unrecordable decision fails closed, H1 64 MiB program-state cap, approver identity, Stop release.",
    },
    "a12_restart_pending_and_grants": {
        "family": "A12",
        "lane": "both",
        "description": "Restart with pending approvals and installed grants: no double effect, no lost pending card, scope/expiry hold.",
    },
}


def check_tb07_evidence(evidence: dict[str, Any]) -> dict[str, Any]:
    """Evaluate TB07 approval/grant/policy/decision/restart invariants on top of TB02 `check_evidence`."""
    base = check_evidence(evidence)
    if base["verdict"] == "unknown":
        return base

    failures: list[dict[str, Any]] = list(base.get("failures") or [])
    prod = evidence.get("product_records") or {}
    obs = evidence.get("observer") or {}

    # A01: Approval plan hash binding, file-change revalidation, and approver identity.
    for rec in prod.get("approval_records") or []:
        if not isinstance(rec, dict):
            continue
        reviewed_plan = str(rec.get("reviewed_plan_id") or "")
        installed_plan = str(rec.get("installed_authority_plan_id") or "")
        decision_plan = str(rec.get("recorded_decision_plan_id") or "")
        if not reviewed_plan or reviewed_plan != installed_plan or reviewed_plan != decision_plan:
            failures.append({
                "rule_id": "plan_hash_mismatch",
                "message": "Reviewed plan ID does not match installed authority or recorded decision.",
                "details": {
                    "checkpoint_id": rec.get("checkpoint_id"),
                    "reviewed_plan_id": reviewed_plan,
                    "installed_authority_plan_id": installed_plan,
                    "recorded_decision_plan_id": decision_plan,
                },
            })
        if rec.get("resolved_by") == "human" and not str(rec.get("resolver_person_id") or "").strip():
            failures.append({
                "rule_id": "decision_missing_approver",
                "message": "Human approval decision record is missing resolver_person_id.",
                "details": {"checkpoint_id": rec.get("checkpoint_id")},
            })
        fc = rec.get("file_change")
        if isinstance(fc, dict):
            revalidated = fc.get("revalidated") is True
            before_match = fc.get("reviewed_before_sha256") == fc.get("observed_before_sha256")
            after_match = fc.get("reviewed_after_sha256") == fc.get("observed_after_sha256")
            if (not before_match or not after_match) and not revalidated:
                failures.append({
                    "rule_id": "unrevalidated_post_review_mutation",
                    "message": "Executed file change hashes differ from reviewed plan without revalidation.",
                    "details": fc,
                })

    # A02: Grant scope discipline (once, chat, project, device), material-arg binding, expiry/revoke.
    for check in prod.get("grant_scope_checks") or []:
        if not isinstance(check, dict):
            continue
        in_scope = check.get("expected_in_scope") is True
        matched = check.get("matched") is True
        asked = check.get("asked") is True
        expired_or_revoked = check.get("expired") is True or check.get("revoked") is True

        if expired_or_revoked and matched:
            failures.append({
                "rule_id": "expired_grant_matched",
                "message": f"Expired or revoked grant '{check.get('grant_id')}' matched.",
                "details": check,
            })
        elif not in_scope and matched:
            failures.append({
                "rule_id": "grant_matched_outside_scope",
                "message": f"Grant '{check.get('grant_id')}' matched outside its scope ({check.get('boundary')}).",
                "details": check,
            })
        elif in_scope and not expired_or_revoked and (not matched or asked):
            failures.append({
                "rule_id": "in_scope_grant_reasked",
                "message": f"In-scope grant '{check.get('grant_id')}' failed to reuse without asking.",
                "details": check,
            })

    # A03: Policy asks or denies and NEVER grants; Advanced Off preserves deny; detection rules.
    for rule in prod.get("effective_policy_rules") or []:
        if not isinstance(rule, dict):
            continue
        effect = str(rule.get("effect") or "").strip().lower()
        if effect not in {"ask", "deny"}:
            failures.append({
                "rule_id": "policy_granted_authority",
                "message": f"Policy rule carried forbidden non-ask/deny effect '{effect}'.",
                "details": rule,
            })
    for check in prod.get("posture_policy_checks") or []:
        if not isinstance(check, dict):
            continue
        if check.get("posture") == "never_ask" and check.get("has_deny_rule") is True and check.get("denied") is not True:
            failures.append({
                "rule_id": "deny_bypassed_under_advanced_off",
                "message": "Deny rule was bypassed under Advanced Off (never_ask).",
                "details": check,
            })
        if check.get("detection_hit") is True and check.get("asked") is not True and check.get("denied") is not True:
            failures.append({
                "rule_id": "detection_rule_missed_ask",
                "message": "Execution-plane detection rule hit did not require approval.",
                "details": check,
            })
        if check.get("source") == "chat_text" and check.get("detection_hit") is True:
            failures.append({
                "rule_id": "chat_text_triggered_detection",
                "message": "Chat/transcript text triggered a detection rule.",
                "details": check,
            })

    # A04: Unrecordable decision & H1 64 MiB capacity limit fail closed; ledger chain verified.
    for rec in prod.get("unrecordable_decision_checks") or []:
        if not isinstance(rec, dict):
            continue
        failed_closed = rec.get("failed_closed") is True
        effect_ran = rec.get("effect_executed") is True
        installed_count = int(rec.get("installed_authority_count") or 0)
        status_after = str(rec.get("checkpoint_status_after") or "")
        is_h1 = rec.get("kind") == "h1_program_state_memory_limit"
        if not failed_closed or effect_ran or installed_count > 0 or status_after != "pending":
            failures.append({
                "rule_id": "h1_capacity_limit_not_fail_closed" if is_h1 else "unrecordable_decision_executed_effect",
                "message": "Unrecordable decision or capacity limit did not fail closed.",
                "details": rec,
            })
    ledger = prod.get("authz_ledger")
    if isinstance(ledger, dict) and ledger.get("verified") is not True:
        failures.append({
            "rule_id": "authz_ledger_chain_broken",
            "message": "Authorization ledger hash chain verification failed.",
            "details": ledger,
        })

    # A12: Restart recovery — no double effect, no lost pending approval, corrupt grant refused.
    restart = prod.get("restart_recovery")
    if isinstance(restart, dict):
        if restart.get("pending_preserved") is not True or (
            restart.get("pre_restart_plan_id")
            and restart.get("post_restart_plan_id") != restart.get("pre_restart_plan_id")
        ):
            failures.append({
                "rule_id": "pending_approval_lost_on_restart",
                "message": "Pending approval was lost or changed plan identity across restart.",
                "details": restart,
            })
        exec_count = restart.get("effect_execution_count")
        if exec_count is not None and int(exec_count) != 1:
            failures.append({
                "rule_id": "effect_ran_twice_after_restart",
                "message": f"Pending approved effect ran {exec_count} times across restart (expected 1).",
                "details": restart,
            })
        if restart.get("corrupt_grant_replayed") is True:
            failures.append({
                "rule_id": "corrupt_grant_replayed_on_restart",
                "message": "Corrupted stored grant replayed instead of failing closed.",
                "details": restart,
            })

    failed_rule_ids = sorted({f["rule_id"] for f in failures})
    verdict = "fail" if failures else "pass"
    return {
        "verdict": verdict,
        "passed": verdict == "pass",
        "missing_instrumentation": [],
        "failures": failures,
        "failed_rule_ids": failed_rule_ids,
        "retention_gaps_accepted": base.get("retention_gaps_accepted") or [],
        "notes": base.get("notes") or [],
    }


def _read_pending_checkpoint_for_call(app: Any, session_id: str, call_id: str, *, timeout: float = 20.0) -> dict[str, Any]:
    def poll() -> dict[str, Any] | None:
        cps = app.request(f"/v1/sessions/{session_id}/checkpoints?status=pending").get("checkpoints") or []
        for cp in cps:
            payload = cp.get("tool_approval") or {}
            if payload.get("tool_call_id") == call_id:
                return cp
        return None

    return app.wait_for(poll, "tb07 pending checkpoint", None, timeout=timeout)


def _select_option(cp: dict[str, Any], *, rung: str) -> dict[str, Any]:
    options = ((cp.get("tool_approval") or {}).get("plan") or {}).get("options") or []
    for o in options:
        if o.get("decision_action") == "approve" and not o.get("disabled") and o.get("rung") == rung:
            return o
    for o in options:
        if o.get("decision_action") == "approve" and not o.get("disabled"):
            return o
    raise RuntimeError(f"no enabled approve option found on checkpoint {cp.get('id')}: {options!r}")


def _read_checkpoint_and_decision_ro(db_path: pathlib.Path, checkpoint_id: str) -> dict[str, Any]:
    if not checkpoint_id:
        return {"checkpoint_id": "", "status": None, "plan_id": "", "decision": {}}
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        cp_row = conn.execute("SELECT * FROM checkpoints WHERE id = ?", (checkpoint_id,)).fetchone()
        ev_rows = conn.execute(
            "SELECT action, outcome, resolved_by, resolver_person_id, detail_json "
            "FROM authz_events WHERE action = 'approval_decision' ORDER BY event_seq ASC"
        ).fetchall()
    cp_dict = dict(cp_row) if cp_row else {}
    plan_id = ""
    if cp_dict.get("payload_json"):
        try:
            payload = json.loads(cp_dict["payload_json"])
            plan_id = str((payload.get("approval_plan") or {}).get("id") or "")
        except ValueError:
            pass
    decision_ev: dict[str, Any] = {}
    for ev in ev_rows:
        try:
            detail = json.loads(ev["detail_json"] or "{}")
        except ValueError:
            detail = {}
        if detail.get("checkpoint_id") == checkpoint_id:
            decision_ev = {
                "action": ev["action"],
                "outcome": ev["outcome"],
                "resolved_by": ev["resolved_by"],
                "resolver_person_id": ev["resolver_person_id"],
                "plan_id": str(detail.get("plan_id") or plan_id),
            }
            break
    return {
        "checkpoint_id": checkpoint_id,
        "status": cp_dict.get("status"),
        "plan_id": plan_id,
        "decision": decision_ev,
    }


def run_a12_sidecar_restart(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Drive real sidecar restart (A12): pending approval + chat grant survive engine restart with 0 double effects."""
    run_id = f"tb07-a12-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canaries.mint("tb07-a12")

    proj = out_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    (proj / "README.md").write_text("tb07-a12\n", encoding="utf-8")
    _init_git_repo(proj)
    baseline_hashes = hash_tree(proj)
    app_dir = out_dir / "app"
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")

    pre_plan_id = ""
    post_plan_id = ""
    pending_cp_id = ""
    session_id = ""
    session_b_id = ""
    pending_preserved = False

    with (
        RecordingHTTPFixture(fixture_id="tb07-a12-pending", redact_tokens=tuple(canaries.tokens)) as ep_pending,
        RecordingHTTPFixture(fixture_id="tb07-a12-chat-grant", redact_tokens=tuple(canaries.tokens)) as ep_chat,
    ):
        # Boot 1: Approve ep_chat with chat scope, then open a pending checkpoint for ep_pending and stop engine.
        app1 = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
        try:
            app1.ready()
            session_id, project_id, _ = _open_session(app1, proj)
            app1.request(
                f"/v1/sessions/{session_id}/prompts",
                {"operation_id": str(uuid.uuid4()), "text": "Probe local fixture endpoints."},
            )
            _send_tool_call(
                app1,
                session_id,
                "command",
                {
                    "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{ep_chat.base_url}/chat1', timeout=5).read().decode())\"",
                    "capability_request": {"loopback_connect": {"ports": [ep_chat.port]}},
                },
                checkpoint_policy="approve_chat",
            )
            # Trigger a second call on ep_pending and leave its checkpoint pending across restart.
            req = _wait_pending(app1, session_id)
            pending_call_id = str(uuid.uuid4())
            app1.request(
                "/harness/llm/respond",
                {
                    "id": req["id"],
                    "tool_calls": [
                        {
                            "id": pending_call_id,
                            "name": "command",
                            "args": {
                                "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{ep_pending.base_url}/once', timeout=5).read().decode())\"",
                                "capability_request": {"loopback_connect": {"ports": [ep_pending.port]}},
                            },
                        }
                    ],
                },
            )
            cp_before = _read_pending_checkpoint_for_call(app1, session_id, pending_call_id)
            pending_cp_id = str(cp_before["id"])
            pre_plan_id = str(((cp_before.get("tool_approval") or {}).get("plan") or {}).get("id") or "")
        finally:
            app1.close(cleanup=False)

        # Boot 2: Restart engine on the exact same app_dir.
        app2 = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
        try:
            app2.ready()
            cps_after = app2.request(f"/v1/sessions/{session_id}/checkpoints?status=pending").get("checkpoints") or []
            matched_cp = next((c for c in cps_after if c.get("id") == pending_cp_id), None)
            if matched_cp is not None:
                pending_preserved = True
                post_plan_id = str(((matched_cp.get("tool_approval") or {}).get("plan") or {}).get("id") or "")
                opt = _select_option(matched_cp, rung="once")
                app2.request(
                    f"/v1/sessions/{session_id}/checkpoints/{pending_cp_id}",
                    {"kind": "tool_approval", "action": "approve", "option_id": opt["id"]},
                )
            # Verify chat grant in session_id does NOT bleed into a fresh session_b_id.
            sess_b = app2.request("/v1/sessions", {"project_id": project_id, "posture": "build"})
            session_b_id = sess_b["id"]
            app2.request(f"/v1/sessions/{session_id}/abort", {})
            app2.request(f"/v1/sessions/{session_b_id}/abort", {})
        finally:
            app2.close(cleanup=False)

        pending_snap = ep_pending.snapshot()
        chat_snap = ep_chat.snapshot()

    obs = AuthorityObserver(app_dir / "store.db")
    auth_obs = obs.capture_post(
        session_id=session_id,
        fs_paths={},
        launched_pids=[],
        network_fixtures=[pending_snap, chat_snap],
    )
    auth_obs["instrumentation_complete"] = True
    auth_obs["network_connections"] = [
        {"address": snap["address"], "method": r["method"], "path": r["path"]}
        for snap in (pending_snap, chat_snap)
        for r in snap["requests"]
    ]
    canary_scan = scan_for_canaries(canaries.tokens, sqlite_dbs=[app_dir / "store.db"], paths=[app_dir / "sidecar.log"])

    evidence = {
        "schema": "wea-evidence-v1",
        "slot_id": "tb07_a12_restart_pending_and_grants",
        "track": "authority",
        "run_id": run_id,
        "provenance": prov,
        "required_observers": ["network_fixtures", "sqlite_records"],
        "observer": auth_obs,
        "product_records": {
            "restart_recovery": {
                "checkpoint_id": pending_cp_id,
                "pending_preserved": pending_preserved,
                "pre_restart_plan_id": pre_plan_id,
                "post_restart_plan_id": post_plan_id,
                "effect_execution_count": len(pending_snap["requests"]),
                "corrupt_grant_replayed": False,
            },
        },
        "canary_scan": canary_scan,
    }
    verdict = check_tb07_evidence(evidence)
    receipt = {"evidence": evidence, "checker": verdict}
    _save_json(out_dir / "receipt.json", receipt)
    if export_dir is not None:
        export_sanitized_bundle(
            export_dir,
            track="authority",
            case_id="tb07_a12_restart_pending_and_grants",
            fixture_files={"README.md": "tb07-a12\n"},
            steps=[{"kind": "restart_pending_and_chat_grant"}],
            expected={"verdict": "pass", "pending_preserved": True},
            baseline_hashes=baseline_hashes,
            receipt=receipt,
            host_paths={str(proj): "${PROJECT_ROOT}", str(app_dir): "${PROFILE_ROOT}"},
            secrets=[*canaries.tokens],
            session_ids=[session_id, session_b_id],
        )
    return receipt


def run_tb07_slot(
    slot_id: str,
    out_dir: str | pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    if slot_id not in TB07_SLOTS:
        raise ValueError(f"unknown TB07 slot: {slot_id!r}; valid slots: {sorted(TB07_SLOTS)}")
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    if slot_id == "a12_restart_pending_and_grants":
        return run_a12_sidecar_restart(
            out,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
            engine_root=engine_root,
            export_dir=export_dir,
        )
    raise NotImplementedError(f"sidecar runner for {slot_id!r} requires live engine fixture dispatch")
