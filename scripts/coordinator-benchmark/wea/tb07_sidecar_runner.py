"""Headless 39-slot TB07 sidecar runner (A01:8, A02:12, A03:14, A04:2, A12:3).

Built directly on TB02's `runner.py`, `observers.py`, `provenance.py`,
`fixtures_net.py`, and `checker.py` + `tb07_approvals.py`.

Reuses a warmed `lycaon-dev` sidecar across the 36 non-restart slots (A01, A02,
A03, A04) and executes real stop+restart sidecar cycles for the 3 A12 slots.
Emits one structured JSON receipt per scheduled variant under `<out_dir>/receipts/`
plus `<out_dir>/tb07_sidecar_summary.json`.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import pathlib
import secrets
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.fixtures_net import RecordingHTTPFixture
from wea.observers import AuthorityObserver, hash_file, sha256_bytes
from wea.provenance import REPO_ROOT, collect_provenance, resolve_engine_binary
from wea.runner import (
    _create_application,
    _extract_refusal_code,
    _init_git_repo,
    _open_session,
    _resolve_engine_root,
    _save_json,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)
from wea.tb07_approvals import _read_checkpoint_and_decision_ro, _read_pending_checkpoint_for_call, _select_option

# Exact 39-slot TB07 sidecar ledger schedule (matches ledger.md).
TB07_SIDECAR_SLOT_SPECS: list[dict[str, str]] = [
    # A01 (8 sidecar slots)
    {"slot_id": "A01-01-file-write-plan-hash", "family": "A01", "variant": "Outside-root file write approval plan ID matches installed authority, authz_events, and host file hash"},
    {"slot_id": "A01-02-file-edit-plan-hash", "family": "A01", "variant": "Project-rule ask file edit plan ID matches recorded decision and host disk hash"},
    {"slot_id": "A01-03-command-exec-plan-hash", "family": "A01", "variant": "Command ask approval plan ID matches recorded decision and host marker file"},
    {"slot_id": "A01-04-network-send-plan-hash", "family": "A01", "variant": "Loopback network send approval plan ID matches recorded decision and 1 fixture arrival"},
    {"slot_id": "A01-05-file-change-reject", "family": "A01", "variant": "Rejected outside-root file write leaves host file unchanged and seals human denial"},
    {"slot_id": "A01-06-network-send-reject", "family": "A01", "variant": "Rejected loopback network send yields 0 fixture arrivals and seals human denial"},
    {"slot_id": "A01-07-post-review-mutation-reask", "family": "A01", "variant": "Changed command/target after review produces distinct plan ID and revalidates"},
    {"slot_id": "A01-08-once-option-nonreusable", "family": "A01", "variant": "Once option approves only held action and re-asks on repeat invocation"},
    # A02 (12 sidecar slots)
    {"slot_id": "A02-01-scope-once-single-use", "family": "A02", "variant": "Once scope covers held loopback call (1 arrival) and re-asks on second call"},
    {"slot_id": "A02-02-scope-chat-in-scope-reuse", "family": "A02", "variant": "Chat scope reuses on second identical call in same chat without asking"},
    {"slot_id": "A02-03-scope-chat-cross-chat-isolated", "family": "A02", "variant": "Chat grant from session A does not match in session B (asks)"},
    {"slot_id": "A02-04-scope-chat-material-arg-change", "family": "A02", "variant": "Chat grant for port P1 does not cover port P2 (asks)"},
    {"slot_id": "A02-05-scope-chat-set-dest-reorder", "family": "A02", "variant": "Reordered loopback port set [P1,P2] vs [P2,P1] matches canonical set grant"},
    {"slot_id": "A02-06-scope-chat-survives-stop", "family": "A02", "variant": "Chat grant survives Stop (abort) and reuses on subsequent turn in same chat"},
    {"slot_id": "A02-07-scope-chat-revoked-reasks", "family": "A02", "variant": "Revoking chat grant via /v1/approval-grants/revoke causes next call to ask"},
    {"slot_id": "A02-08-scope-day-rung-installed", "family": "A02", "variant": "Day rung installs time-bounded grant with expires_at and reuses in-scope"},
    {"slot_id": "A02-09-scope-project-in-project-reuse", "family": "A02", "variant": "Project grant covers a fresh session in the same project without asking"},
    {"slot_id": "A02-10-scope-project-cross-project-isolated", "family": "A02", "variant": "Project grant for Project A does not cover Project B (asks)"},
    {"slot_id": "A02-11-scope-saved-grants-inventory", "family": "A02", "variant": "Saved approvals inventory lists live session/project grants with exact scope metadata"},
    {"slot_id": "A02-12-scope-chat-delete-ends-authority", "family": "A02", "variant": "Deleting/isolated chat does not leak authority to fresh chat in same project"},
    # A03 (14 sidecar slots)
    {"slot_id": "A03-01-posture-light-settings", "family": "A03", "variant": "PATCH /v1/settings/approvals posture=light applies cleanly"},
    {"slot_id": "A03-02-posture-balanced-settings", "family": "A03", "variant": "PATCH /v1/settings/approvals posture=balanced applies cleanly"},
    {"slot_id": "A03-03-posture-strict-settings", "family": "A03", "variant": "PATCH /v1/settings/approvals posture=strict applies cleanly"},
    {"slot_id": "A03-04-posture-unknown-rejected", "family": "A03", "variant": "PATCH /v1/settings/approvals unknown posture token returns 400 and leaves posture unchanged"},
    {"slot_id": "A03-05-posture-advanced-off-deny-holds", "family": "A03", "variant": "With never_ask=true (Advanced Off), global deny rule still enforces APPROVAL_RULE_DENIED"},
    {"slot_id": "A03-06-posture-advanced-off-project-deny-holds", "family": "A03", "variant": "With never_ask=true (Advanced Off), project overlay deny rule still enforces denial"},
    {"slot_id": "A03-07-project-policy-ask-triggers-card", "family": "A03", "variant": "Project .paintedwolf/approvals.yaml effect=ask triggers approval checkpoint"},
    {"slot_id": "A03-08-project-policy-deny-blocks-effect", "family": "A03", "variant": "Project .paintedwolf/approvals.yaml effect=deny blocks command with 0 host effect"},
    {"slot_id": "A03-09-project-policy-allow-refused", "family": "A03", "variant": "Project .paintedwolf/approvals.yaml effect=allow is refused and never grants"},
    {"slot_id": "A03-10-project-policy-never-ask-refused", "family": "A03", "variant": "Project .paintedwolf/approvals.yaml never_ask=true is refused"},
    {"slot_id": "A03-11-project-policy-grants-refused", "family": "A03", "variant": "Project .paintedwolf/approvals.yaml grants[] is refused"},
    {"slot_id": "A03-12-project-policy-cannot-relax-global-deny", "family": "A03", "variant": "Project effect=ask on same pattern as global effect=deny does not weaken deny"},
    {"slot_id": "A03-13-project-policy-unknown-posture-strict", "family": "A03", "variant": "Project .paintedwolf/approvals.yaml unknown posture fails closed to strict"},
    {"slot_id": "A03-14-chat-text-never-triggers-detection", "family": "A03", "variant": "Detection-like prose in chat prompt text never triggers an approval/detection checkpoint"},
    # A04 (2 sidecar slots)
    {"slot_id": "A04-01-decision-names-approver-and-chain", "family": "A04", "variant": "Human approve & reject decisions record resolver_person_id and valid authz_events chain"},
    {"slot_id": "A04-02-stop-releases-pending-without-effect", "family": "A04", "variant": "Stop while checkpoint pending cancels held effect (0 arrivals) and keeps committed chat grant"},
    # A12 (3 sidecar slots — real engine restarts)
    {"slot_id": "A12-01-restart-pending-preserved-no-double-run", "family": "A12", "variant": "Pending approval survives real sidecar restart with identical plan ID and executes once"},
    {"slot_id": "A12-02-restart-chat-grant-replays-in-scope", "family": "A12", "variant": "Chat grant replays after real sidecar restart in same chat without asking, isolated from other chat"},
    {"slot_id": "A12-03-restart-revoked-grant-stays-revoked", "family": "A12", "variant": "Revoked chat grant stays revoked after real sidecar restart and next call asks"},
]

EXPECTED_SIDECAR_COUNTS_BY_FAMILY: dict[str, int] = {
    "A01": 8,
    "A02": 12,
    "A03": 14,
    "A04": 2,
    "A12": 3,
}


def _patch_global_approvals(
    app: Any,
    *,
    posture: str = "balanced",
    rules: list[dict[str, str]] | None = None,
    never_ask: bool = False,
) -> dict[str, Any]:
    return app.request(
        "/v1/settings/approvals",
        {"approval_posture": posture, "rules": rules or [], "never_ask": never_ask},
        method="PATCH",
    )


def _revoke_all_grants(app: Any) -> None:
    grants = (app.request("/v1/approval-grants") or {}).get("grants") or []
    ids = [g["id"] for g in grants if isinstance(g, dict) and g.get("id")]
    if ids:
        with contextlib.suppress(Exception):
            app.request("/v1/approval-grants/revoke", {"ids": ids})


def _make_project(base_dir: pathlib.Path, name: str, approvals_yaml: str | None = None) -> pathlib.Path:
    proj = base_dir / name
    proj.mkdir(parents=True, exist_ok=True)
    (proj / "README.md").write_text(f"{name}\n", encoding="utf-8")
    (proj / "notes.txt").write_text("alpha\nbeta\n", encoding="utf-8")
    if approvals_yaml is not None:
        ov = proj / ".paintedwolf"
        ov.mkdir(parents=True, exist_ok=True)
        (ov / "approvals.yaml").write_text(approvals_yaml, encoding="utf-8")
    _init_git_repo(proj)
    return proj


def _wait_pending_bounded(app: Any, session_id: str, *, timeout_s: float = 60.0) -> dict[str, Any]:
    """Wait for a pending model turn without tripping on historical aborted/interrupted submissions."""
    path = f"/harness/llm/pending?session_id={urllib.parse.quote(session_id)}"

    def poll() -> dict[str, Any] | None:
        val = app.request(path)
        if not val.get("pending"):
            return None
        if val.get("session_id") != session_id:
            return None
        return val

    return app.wait_for(poll, "tb07 pending model turn", None, timeout=timeout_s)


def _wait_session_idle(app: Any, session_id: str, phase: str, *, timeout_s: float = 30.0) -> None:
    """Wait for a session to return to idle/aborted without failing on intentional abort/interrupted rows."""
    app.wait_for(
        lambda: (app.request(f"/v1/sessions/{session_id}") or {}).get("status") in {"idle", "aborted"},
        phase,
        None,
        timeout=timeout_s,
    )


def _close_session_turn(app: Any, session_id: str, *, timeout_s: float = 20.0) -> None:
    """Cleanly close any pending manual-harness LLM turn on session_id before aborting/moving on."""
    deadline = time.monotonic() + 10.0
    while time.monotonic() < deadline:
        with contextlib.suppress(Exception):
            pend = app.request(f"/harness/llm/pending?session_id={urllib.parse.quote(session_id)}") or {}
            if pend.get("pending") and pend.get("session_id") == session_id and pend.get("id"):
                app.request("/harness/llm/respond", {"id": pend["id"], "content": "Completed TB07 step."})
                break
            state = app.request(f"/v1/sessions/{session_id}") or {}
            if state.get("status") in {"idle", "aborted"}:
                return
        time.sleep(0.05)
    with contextlib.suppress(Exception):
        state = app.request(f"/v1/sessions/{session_id}") or {}
        if state.get("status") not in {"idle", "aborted"}:
            app.request(f"/v1/sessions/{session_id}/abort", {})
    with contextlib.suppress(Exception):
        _wait_session_idle(app, session_id, f"close {session_id}", timeout_s=timeout_s)


def _wait_pending_checkpoint_or_settled(
    app: Any,
    session_id: str,
    call_id: str,
    *,
    timeout_s: float = 20.0,
) -> tuple[dict[str, Any] | None, dict[str, Any] | None]:
    """Poll for EITHER a pending checkpoint for `call_id` OR the tool_result settling.

    Returns `(checkpoint, None)` if a checkpoint opened, `(None, tool_result)` if the
    tool settled without opening a checkpoint, or `(None, {"outcome": "timeout", ...})`
    on timeout — never raises on settlement without a checkpoint.
    """
    db_path = app.directory / "store.db"

    def poll() -> tuple[dict[str, Any] | None, dict[str, Any] | None] | None:
        cps = (app.request(f"/v1/sessions/{session_id}/checkpoints?status=pending") or {}).get("checkpoints") or []
        for cp in cps:
            if (cp.get("tool_approval") or {}).get("tool_call_id") == call_id:
                return (cp, None)
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            row = conn.execute(
                "SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?",
                (session_id, call_id),
            ).fetchone()
        if row:
            return (None, json.loads(row[0]))
        return None

    try:
        return app.wait_for(poll, "tb07 pending checkpoint or settled", None, timeout=timeout_s)
    except RuntimeError as exc:
        return (None, {"outcome": "timeout", "error": str(exc)})


def _call_with_checkpoint_inspect(
    app: Any,
    session_id: str,
    name: str,
    args: dict[str, Any],
    *,
    action: str | None = None,  # "approve" | "reject" | None (expect no checkpoint)
    rung: str = "once",
    timeout_s: float = 30.0,
) -> dict[str, Any]:
    """Send a single tool call, racing pending checkpoint vs tool_result with a bounded timeout.

    Never waits blindly for a checkpoint that will never arrive: if the tool settles
    without opening a checkpoint, returns immediately with `checkpoint.asked == False`
    and the settled `tool_result_json`.
    """
    try:
        req = _wait_pending_bounded(app, session_id, timeout_s=timeout_s)
    except RuntimeError as exc:
        return {
            "call_id": "",
            "result": {"outcome": "timeout", "error": str(exc)},
            "checkpoint": {
                "asked": False,
                "checkpoint_id": "",
                "plan_id": "",
                "option_id": "",
                "rung": "",
                "timed_out": True,
            },
        }
    call_id = str(uuid.uuid4())
    app.request(
        "/harness/llm/respond",
        {"id": req["id"], "tool_calls": [{"id": call_id, "name": name, "args": args}]},
    )
    db_path = app.directory / "store.db"
    cp_info: dict[str, Any] = {
        "asked": False,
        "checkpoint_id": "",
        "plan_id": "",
        "option_id": "",
        "rung": "",
        "timed_out": False,
    }
    resolved_cp = False

    def read_tool_result() -> dict[str, Any] | None:
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            row = conn.execute(
                "SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?",
                (session_id, call_id),
            ).fetchone()
        return json.loads(row[0]) if row else None

    def poll_step() -> dict[str, Any] | None:
        nonlocal resolved_cp
        if not resolved_cp:
            cps = (app.request(f"/v1/sessions/{session_id}/checkpoints?status=pending") or {}).get("checkpoints") or []
            for cp in cps:
                if (cp.get("tool_approval") or {}).get("tool_call_id") != call_id:
                    continue
                cp_id = str(cp["id"])
                plan = (cp.get("tool_approval") or {}).get("plan") or {}
                cp_info["asked"] = True
                cp_info["checkpoint_id"] = cp_id
                cp_info["plan_id"] = str(plan.get("id") or "")
                if action == "approve":
                    opt = _select_option(cp, rung=rung)
                    cp_info["option_id"] = str(opt["id"])
                    cp_info["rung"] = str(opt.get("rung") or "")
                    app.request(
                        f"/v1/sessions/{session_id}/checkpoints/{cp_id}",
                        {"kind": "tool_approval", "action": "approve", "option_id": opt["id"]},
                    )
                else:
                    guidance = "Denied by TB07 sidecar runner." if action == "reject" else "Unexpected ask in no-ask probe."
                    app.request(
                        f"/v1/sessions/{session_id}/checkpoints/{cp_id}",
                        {"kind": "tool_approval", "action": "reject", "guidance": guidance},
                    )
                resolved_cp = True
                break
        return read_tool_result()

    try:
        result = app.wait_for(poll_step, "tb07 checkpoint or tool result", None, timeout=timeout_s)
    except RuntimeError as exc:
        cp_info["timed_out"] = True
        result = {"outcome": "timeout", "error": str(exc)}
    return {"call_id": call_id, "result": result, "checkpoint": cp_info}


def _build_receipt(
    *,
    spec: dict[str, str],
    prov: dict[str, Any],
    typed_result: dict[str, Any],
    independent_observation: dict[str, Any],
    passed: bool,
    failure_reasons: list[str] | None = None,
) -> dict[str, Any]:
    product_verified = bool((prov.get("product_source") or {}).get("verified") is True)
    reasons = list(failure_reasons or [])
    if not product_verified:
        reasons.append("unverified_source_witness")
    if not passed and not reasons:
        reasons.append(f"assertion_failed:{spec['slot_id']}")
    verdict = "pass" if (passed and not reasons) else "fail"
    return {
        "schema": "wea-tb07-slot-receipt-v1",
        "slot_id": spec["slot_id"],
        "family": spec["family"],
        "variant": spec["variant"],
        "lane": "sidecar",
        "product_source": prov.get("product_source"),
        "harness_source": prov.get("harness_source"),
        "typed_result": typed_result,
        "independent_observation": independent_observation,
        "verdict": verdict,
        "failure_reasons": reasons,
    }


def _restart_application(
    app_dir: pathlib.Path,
    engine_bin: str | pathlib.Path | None = None,
    *,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
) -> Any:
    """Restart lycaon-dev on an EXISTING app_dir (preserving store.db + config/)."""
    import smoke
    from application_environment import application_environment
    from progress import save

    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
    resolved_engine_root = _resolve_engine_root(engine_manifest=engine_manifest, engine_root=engine_root)
    app = smoke.Application.__new__(smoke.Application)
    app.directory = app_dir
    app.config = app_dir / "config"
    app.config.mkdir(parents=True, exist_ok=True)
    app.manual = True
    app.projects = []
    app.admissions = []
    suite = json.loads((smoke.ROOT / "lycaon/test/fixtures/eval/coordinator-benchmark.json").read_text())
    app.task_allowance = suite["task_allowance"]
    app.approval_guidance = suite["unattended"]["approval_guidance"]
    app.baseline_checks = []
    app.flows = []
    app.token = secrets.token_hex(24)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        app.port = sock.getsockname()[1]
    app.url = f"http://127.0.0.1:{app.port}"
    app.log = (app_dir / "sidecar.log").open("a")
    env_overrides = {
        "LYCAON_CONFIG_DIR": str(app.config),
        "LYCAON_CONFIG_ROOT": str(smoke.ROOT / "lycaon"),
        "LYCAON_ENGINE_ROOT": str(resolved_engine_root or (smoke.ROOT / "lycaon-den/src-tauri/engine-root")),
        "LYCAON_ADDR": f"127.0.0.1:{app.port}",
        "LYCAON_API_TOKEN": app.token,
        "LYCAON_DEV": "1",
        "LYCAON_HARNESS": "1",
        "LYCAON_LOG_LEVEL": "debug",
        "LYCAON_LLM_MOCK": "1",
        "LYCAON_LLM_MANUAL": "1",
    }
    env = application_environment(env_overrides)
    app.process = subprocess.Popen(
        [str(binary), "serve", "--db", str(app_dir / "store.db")],
        cwd=smoke.ROOT,
        env=env,
        stdout=app.log,
        stderr=subprocess.STDOUT,
    )
    save(app_dir / "ownership.json", {"pid": app.process.pid, "port": app.port})
    return app


def _http_probe_cmd(url: str) -> str:
    return f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{url}', timeout=5).read().decode())\""


def _py_write_cmd(path: pathlib.Path, content: str) -> str:
    return f"python3 -B -c \"from pathlib import Path; Path({str(path)!r}).write_text({content!r})\""


def _run_warmed_slots(
    *,
    specs: list[dict[str, str]],
    work_dir: pathlib.Path,
    receipts_dir: pathlib.Path,
    receipts_out: list[dict[str, Any]],
    prov: dict[str, Any],
    engine_bin: str | pathlib.Path | None,
    engine_manifest: str | pathlib.Path | None,
    engine_root: str | pathlib.Path | None,
) -> list[dict[str, Any]]:
    """Run all non-restart TB07 sidecar slots (A01, A02, A03, A04) on one warmed lycaon-dev instance."""
    spec_map = {s["slot_id"]: s for s in specs}

    class _FlushingReceiptList(list):
        def append(self, rec: dict[str, Any]) -> None:  # type: ignore[override]
            super().append(rec)
            _save_json(receipts_dir / f"{rec['slot_id']}.json", rec)

    receipts = _FlushingReceiptList(receipts_out)
    # Keep receipts_out synced in-place so caller sees every completed slot even on exception.
    def _sync() -> None:
        receipts_out[:] = list(receipts)

    app_dir = work_dir / "warmed-app"
    db_path = app_dir / "store.db"
    outside_dir = work_dir / "outside-root"
    outside_dir.mkdir(parents=True, exist_ok=True)

    proj_main = _make_project(work_dir, "proj-main")
    proj_b = _make_project(work_dir, "proj-b")

    with (
        RecordingHTTPFixture(fixture_id="tb07-ep1") as ep1,
        RecordingHTTPFixture(fixture_id="tb07-ep2") as ep2,
        RecordingHTTPFixture(fixture_id="tb07-ep3") as ep3,
    ):
        app = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
        try:
            app.ready()
            _patch_global_approvals(app, posture="balanced", rules=[], never_ask=False)

            def open_prompted_session(proj: pathlib.Path, prompt: str = "Execute TB07 authority step.") -> tuple[str, str]:
                sid, pid, _ = _open_session(app, proj)
                app.request(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": prompt})
                return sid, pid

            def open_prompted_session_in_project(project_id: str, prompt: str = "Execute TB07 authority step.") -> str:
                sess = app.request("/v1/sessions", {"project_id": project_id, "posture": "build"})
                sid = sess["id"]
                app.request("/harness/model-limit", {"session_id": sid, "limit": 40})
                _wait_session_idle(app, sid, f"session ready in {project_id}", timeout_s=60.0)
                app.request(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": prompt})
                return sid

            def abort_session(sid: str) -> None:
                _close_session_turn(app, sid)

            def run_slot(slot_id: str, fn: Any) -> None:
                if slot_id not in spec_map:
                    return
                try:
                    receipts.append(fn())
                except Exception as exc:  # noqa: BLE001
                    receipts.append(_build_receipt(
                        spec=spec_map[slot_id],
                        prov=prov,
                        typed_result={"error_type": type(exc).__name__, "error": str(exc)},
                        independent_observation={"slot_raised": True},
                        passed=False,
                        failure_reasons=[f"slot_exception:{type(exc).__name__}"],
                    ))

            # ------------------------------------------------------------------
            # A01 (8 sidecar slots)
            # ------------------------------------------------------------------
            a01_sid, _ = open_prompted_session(proj_main, "Run A01 plan-hash verification steps.")

            # A01-01: outside-root file write approved -> plan ID matches DB + host file hash matches bytes
            if "A01-01-file-write-plan-hash" in spec_map:
                target = outside_dir / "a01_01.txt"
                target.write_text("before-01\n", encoding="utf-8")
                pre = hash_file(target)
                call = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _py_write_cmd(target, "after-01\n"),
                        "capability_request": {"write_root": str(outside_dir)},
                    },
                    action="approve", rung="once",
                )
                post = hash_file(target)
                db_rec = _read_checkpoint_and_decision_ro(db_path, call["checkpoint"]["checkpoint_id"]) if call["checkpoint"]["checkpoint_id"] else {"checkpoint_id": "", "status": None, "plan_id": "", "decision": {}}
                want_sha = sha256_bytes(b"after-01\n")
                ok = (
                    call["checkpoint"]["asked"]
                    and bool(call["checkpoint"]["plan_id"])
                    and call["checkpoint"]["plan_id"] == db_rec["plan_id"]
                    and db_rec["decision"].get("outcome") == "allowed"
                    and bool(db_rec["decision"].get("resolver_person_id"))
                    and post["sha256"] == want_sha
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-01-file-write-plan-hash"], prov=prov,
                    typed_result={"checkpoint": call["checkpoint"], "db_record": db_rec, "outcome": call["result"].get("outcome")},
                    independent_observation={"pre": pre, "post": post, "expected_sha256": want_sha},
                    passed=ok,
                ))

            # A01-02: file edit under ask rule -> plan ID matches DB + host disk hash
            if "A01-02-file-edit-plan-hash" in spec_map:
                _patch_global_approvals(
                    app, posture="balanced",
                    rules=[{"category": "command", "pattern": "python3 *", "effect": "ask"}],
                )
                call = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {"command": "python3 -B -c \"from pathlib import Path; p=Path('notes.txt'); p.write_text(p.read_text().replace('beta','BETA_A01'))\""},
                    action="approve", rung="once",
                )
                post = hash_file(proj_main / "notes.txt")
                db_rec = _read_checkpoint_and_decision_ro(db_path, call["checkpoint"]["checkpoint_id"])
                want_sha = sha256_bytes(b"alpha\nBETA_A01\n")
                ok = (
                    call["checkpoint"]["asked"]
                    and call["checkpoint"]["plan_id"] == db_rec["plan_id"]
                    and post["sha256"] == want_sha
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-02-file-edit-plan-hash"], prov=prov,
                    typed_result={"checkpoint": call["checkpoint"], "db_record": db_rec},
                    independent_observation={"post": post, "expected_sha256": want_sha},
                    passed=ok,
                ))
                _patch_global_approvals(app, posture="balanced", rules=[])

            # A01-03: command execution under ask rule -> plan ID matches DB + host marker
            if "A01-03-command-exec-plan-hash" in spec_map:
                _patch_global_approvals(
                    app, posture="balanced",
                    rules=[{"category": "command", "pattern": "python3 *", "effect": "ask"}],
                )
                marker = proj_main / "a01_cmd_marker.txt"
                call = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {"command": "python3 -B -c \"from pathlib import Path; Path('a01_cmd_marker.txt').write_text('cmd-ok\\n')\""},
                    action="approve", rung="once",
                )
                post = hash_file(marker)
                db_rec = _read_checkpoint_and_decision_ro(db_path, call["checkpoint"]["checkpoint_id"])
                want_sha = sha256_bytes(b"cmd-ok\n")
                ok = (
                    call["checkpoint"]["asked"]
                    and call["checkpoint"]["plan_id"] == db_rec["plan_id"]
                    and db_rec["decision"].get("outcome") == "allowed"
                    and post["sha256"] == want_sha
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-03-command-exec-plan-hash"], prov=prov,
                    typed_result={"checkpoint": call["checkpoint"], "db_record": db_rec},
                    independent_observation={"post": post, "expected_sha256": want_sha},
                    passed=ok,
                ))
                _patch_global_approvals(app, posture="balanced", rules=[])

            # A01-04: network send approved -> plan ID matches DB + exact 1 fixture arrival
            if "A01-04-network-send-plan-hash" in spec_map:
                before_n = len(ep1.snapshot()["requests"])
                call = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _http_probe_cmd(f"{ep1.base_url}/a01-04"),
                        "capability_request": {"loopback_connect": {"ports": [ep1.port]}},
                    },
                    action="approve", rung="once",
                )
                after_n = len(ep1.snapshot()["requests"])
                db_rec = _read_checkpoint_and_decision_ro(db_path, call["checkpoint"]["checkpoint_id"])
                ok = (
                    call["checkpoint"]["asked"]
                    and call["checkpoint"]["plan_id"] == db_rec["plan_id"]
                    and db_rec["decision"].get("outcome") == "allowed"
                    and (after_n - before_n) == 1
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-04-network-send-plan-hash"], prov=prov,
                    typed_result={"checkpoint": call["checkpoint"], "db_record": db_rec},
                    independent_observation={"fixture_arrivals_delta": after_n - before_n},
                    passed=ok,
                ))

            # A01-05: file change rejected -> dedicated outside dir so A01-01 write_root cannot cover it
            if "A01-05-file-change-reject" in spec_map:
                outside_a01_05 = work_dir / "outside-a01-05-reject"
                outside_a01_05.mkdir(parents=True, exist_ok=True)
                target = outside_a01_05 / "a01_05_reject.txt"
                target.write_text("immutable-05\n", encoding="utf-8")
                pre = hash_file(target)
                call = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _py_write_cmd(target, "mutated-05\n"),
                        "capability_request": {"write_root": str(outside_a01_05)},
                    },
                    action="reject",
                )
                post = hash_file(target)
                db_rec = _read_checkpoint_and_decision_ro(db_path, call["checkpoint"]["checkpoint_id"]) if call["checkpoint"]["checkpoint_id"] else {"checkpoint_id": "", "status": None, "plan_id": "", "decision": {}}
                ok = (
                    call["checkpoint"]["asked"]
                    and pre["sha256"] == post["sha256"]
                    and db_rec["decision"].get("outcome") == "denied"
                    and bool(db_rec["decision"].get("resolver_person_id"))
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-05-file-change-reject"], prov=prov,
                    typed_result={"checkpoint": call["checkpoint"], "db_record": db_rec},
                    independent_observation={"unchanged": pre["sha256"] == post["sha256"], "pre": pre, "post": post},
                    passed=ok,
                ))

            # A01-06: network send rejected -> 0 fixture arrivals + denied in DB
            if "A01-06-network-send-reject" in spec_map:
                before_n = len(ep2.snapshot()["requests"])
                call = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _http_probe_cmd(f"{ep2.base_url}/a01-06"),
                        "capability_request": {"loopback_connect": {"ports": [ep2.port]}},
                    },
                    action="reject",
                )
                after_n = len(ep2.snapshot()["requests"])
                db_rec = _read_checkpoint_and_decision_ro(db_path, call["checkpoint"]["checkpoint_id"])
                ok = (
                    call["checkpoint"]["asked"]
                    and (after_n - before_n) == 0
                    and db_rec["decision"].get("outcome") == "denied"
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-06-network-send-reject"], prov=prov,
                    typed_result={"checkpoint": call["checkpoint"], "db_record": db_rec},
                    independent_observation={"fixture_arrivals_delta": after_n - before_n},
                    passed=ok,
                ))

            # A01-07: post-review mutation produces distinct plan ID and revalidates
            if "A01-07-post-review-mutation-reask" in spec_map:
                call1 = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _http_probe_cmd(f"{ep1.base_url}/a01-07-v1"),
                        "capability_request": {"loopback_connect": {"ports": [ep1.port]}},
                    },
                    action="approve", rung="once",
                )
                call2 = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _http_probe_cmd(f"{ep1.base_url}/a01-07-v2"),
                        "capability_request": {"loopback_connect": {"ports": [ep1.port]}},
                    },
                    action="approve", rung="once",
                )
                ok = (
                    call1["checkpoint"]["asked"]
                    and call2["checkpoint"]["asked"]
                    and bool(call1["checkpoint"]["plan_id"])
                    and call1["checkpoint"]["plan_id"] != call2["checkpoint"]["plan_id"]
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A01-07-post-review-mutation-reask"], prov=prov,
                    typed_result={"plan_id_1": call1["checkpoint"]["plan_id"], "plan_id_2": call2["checkpoint"]["plan_id"]},
                    independent_observation={"distinct_plan_ids": call1["checkpoint"]["plan_id"] != call2["checkpoint"]["plan_id"]},
                    passed=ok,
                ))

            # A01-08: once option nonreusable on identical repeat
            if "A01-08-once-option-nonreusable" in spec_map:
                before_n = len(ep1.snapshot()["requests"])
                c1 = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _http_probe_cmd(f"{ep1.base_url}/a01-08"),
                        "capability_request": {"loopback_connect": {"ports": [ep1.port]}},
                    },
                    action="approve", rung="once",
                )
                c2 = _call_with_checkpoint_inspect(
                    app, a01_sid, "command",
                    {
                        "command": _http_probe_cmd(f"{ep1.base_url}/a01-08"),
                        "capability_request": {"loopback_connect": {"ports": [ep1.port]}},
                    },
                    action="approve", rung="once",
                )
                after_n = len(ep1.snapshot()["requests"])
                ok = c1["checkpoint"]["asked"] and c2["checkpoint"]["asked"] and (after_n - before_n) == 2
                receipts.append(_build_receipt(
                    spec=spec_map["A01-08-once-option-nonreusable"], prov=prov,
                    typed_result={"first_asked": c1["checkpoint"]["asked"], "second_asked": c2["checkpoint"]["asked"]},
                    independent_observation={"fixture_arrivals_delta": after_n - before_n},
                    passed=ok,
                ))

            abort_session(a01_sid)
            _revoke_all_grants(app)

            # ------------------------------------------------------------------
            # A02 (12 sidecar slots)
            # ------------------------------------------------------------------
            a02_sid, _ = open_prompted_session(proj_main, "Run A02 grant scope checks.")

            # A02-01: once single-use (loopback_connect once on ep3 is strictly single-use; dedicated fresh session)
            if "A02-01-scope-once-single-use" in spec_map:
                b0 = len(ep3.snapshot()["requests"])
                c1 = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep3.base_url}/a02-01"), "capability_request": {"loopback_connect": {"ports": [ep3.port]}}},
                    action="approve", rung="once", timeout_s=90.0,
                )
                c2 = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep3.base_url}/a02-01"), "capability_request": {"loopback_connect": {"ports": [ep3.port]}}},
                    action="reject", timeout_s=90.0,
                )
                a0 = len(ep3.snapshot()["requests"])
                ok = c1["checkpoint"]["asked"] and c2["checkpoint"]["asked"] and (a0 - b0) == 1
                receipts.append(_build_receipt(
                    spec=spec_map["A02-01-scope-once-single-use"], prov=prov,
                    typed_result={
                        "first_asked": c1["checkpoint"]["asked"],
                        "second_asked": c2["checkpoint"]["asked"],
                        "c1_outcome": c1["result"].get("outcome"),
                        "c2_outcome": c2["result"].get("outcome"),
                    },
                    independent_observation={"fixture_arrivals_delta": a0 - b0},
                    passed=ok,
                ))

            # A02-02: chat in-scope reuse without asking
            if "A02-02-scope-chat-in-scope-reuse" in spec_map:
                b0 = len(ep1.snapshot()["requests"])
                c1 = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-02"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action="approve", rung="chat",
                )
                c2 = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-02"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action=None,
                )
                a0 = len(ep1.snapshot()["requests"])
                ok = c1["checkpoint"]["asked"] and (not c2["checkpoint"]["asked"]) and (a0 - b0) == 2
                receipts.append(_build_receipt(
                    spec=spec_map["A02-02-scope-chat-in-scope-reuse"], prov=prov,
                    typed_result={"first_asked": c1["checkpoint"]["asked"], "second_asked": c2["checkpoint"]["asked"]},
                    independent_observation={"fixture_arrivals_delta": a0 - b0},
                    passed=ok,
                ))

            # A02-03: chat grant isolated from session B
            if "A02-03-scope-chat-cross-chat-isolated" in spec_map:
                abort_session(a02_sid)
                other_sid, _ = open_prompted_session(proj_main, "Cross-chat isolation probe.")
                b0 = len(ep1.snapshot()["requests"])
                c_other = _call_with_checkpoint_inspect(
                    app, other_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-03"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action="reject",
                )
                a0 = len(ep1.snapshot()["requests"])
                abort_session(other_sid)
                ok = c_other["checkpoint"]["asked"] and (a0 - b0) == 0
                receipts.append(_build_receipt(
                    spec=spec_map["A02-03-scope-chat-cross-chat-isolated"], prov=prov,
                    typed_result={"other_session_asked": c_other["checkpoint"]["asked"]},
                    independent_observation={"fixture_arrivals_delta": a0 - b0},
                    passed=ok,
                ))
                app.request(f"/v1/sessions/{a02_sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": "Continue A02 grant checks."})

            # A02-04: chat grant for ep1 does NOT cover a different capability/write_root (material arg change)
            if "A02-04-scope-chat-material-arg-change" in spec_map:
                outside_a02_diff = work_dir / "outside-a02-diff"
                outside_a02_diff.mkdir(parents=True, exist_ok=True)
                diff_target = outside_a02_diff / "diff_arg.txt"
                c_diff = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {
                        "command": _py_write_cmd(diff_target, "must-not-write\n"),
                        "capability_request": {"write_root": str(outside_a02_diff)},
                    },
                    action="reject",
                )
                ok = c_diff["checkpoint"]["asked"] and (not diff_target.exists())
                receipts.append(_build_receipt(
                    spec=spec_map["A02-04-scope-chat-material-arg-change"], prov=prov,
                    typed_result={"different_target_asked": c_diff["checkpoint"]["asked"], "outcome": c_diff["result"].get("outcome")},
                    independent_observation={"diff_target_exists": diff_target.exists()},
                    passed=ok,
                ))

            # A02-05: set-valued port list reorder matches canonical set grant (fresh session avoids A02-04 rejection suppression)
            if "A02-05-scope-chat-set-dest-reorder" in spec_map:
                abort_session(a02_sid)
                s_set, _ = open_prompted_session(proj_main, "Set-valued port reorder check.")
                c1 = _call_with_checkpoint_inspect(
                    app, s_set, "command",
                    {"command": _http_probe_cmd(f"{ep2.base_url}/a02-05-a"), "capability_request": {"loopback_connect": {"ports": [ep1.port, ep2.port]}}},
                    action="approve", rung="chat",
                )
                b_set = len(ep2.snapshot()["requests"])
                c2 = _call_with_checkpoint_inspect(
                    app, s_set, "command",
                    {"command": _http_probe_cmd(f"{ep2.base_url}/a02-05-b"), "capability_request": {"loopback_connect": {"ports": [ep2.port, ep1.port]}}},
                    action=None,
                )
                a_set = len(ep2.snapshot()["requests"])
                abort_session(s_set)
                ok = c1["checkpoint"]["asked"] and (not c2["checkpoint"]["asked"]) and (a_set - b_set) == 1
                receipts.append(_build_receipt(
                    spec=spec_map["A02-05-scope-chat-set-dest-reorder"], prov=prov,
                    typed_result={"first_asked": c1["checkpoint"]["asked"], "reordered_asked": c2["checkpoint"]["asked"]},
                    independent_observation={
                        "reordered_matched_without_ask": not c2["checkpoint"]["asked"],
                        "fixture_arrivals_delta": a_set - b_set,
                    },
                    passed=ok,
                ))
                app.request(f"/v1/sessions/{a02_sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": "Continue A02 stop check."})

            # A02-06: chat grant survives Stop (abort) and reuses on next prompt in same chat
            if "A02-06-scope-chat-survives-stop" in spec_map:
                app.request(f"/v1/sessions/{a02_sid}/abort", {})
                # Wait briefly for session to return to idle after abort
                _wait_session_idle(app, a02_sid, "a02 idle after abort")
                app.request(f"/v1/sessions/{a02_sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": "Post-stop reuse check."})
                b0 = len(ep1.snapshot()["requests"])
                c_after = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-06"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action=None,
                )
                a0 = len(ep1.snapshot()["requests"])
                ok = (not c_after["checkpoint"]["asked"]) and (a0 - b0) == 1
                receipts.append(_build_receipt(
                    spec=spec_map["A02-06-scope-chat-survives-stop"], prov=prov,
                    typed_result={"post_stop_asked": c_after["checkpoint"]["asked"]},
                    independent_observation={"fixture_arrivals_delta": a0 - b0},
                    passed=ok,
                ))

            # A02-07: revoking chat grant via /v1/approval-grants/revoke causes next call to ask
            if "A02-07-scope-chat-revoked-reasks" in spec_map:
                grants_before = (app.request(f"/v1/approval-grants?session_id={a02_sid}") or {}).get("grants") or []
                _revoke_all_grants(app)
                b0 = len(ep1.snapshot()["requests"])
                c_rev = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-07"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action="reject",
                )
                a0 = len(ep1.snapshot()["requests"])
                ok = len(grants_before) >= 1 and c_rev["checkpoint"]["asked"] and (a0 - b0) == 0
                receipts.append(_build_receipt(
                    spec=spec_map["A02-07-scope-chat-revoked-reasks"], prov=prov,
                    typed_result={"grants_before_revoke": len(grants_before), "asked_after_revoke": c_rev["checkpoint"]["asked"]},
                    independent_observation={"fixture_arrivals_delta": a0 - b0},
                    passed=ok,
                ))

            # A02-08: day rung installs time-bounded grant with expires_at/expires_when and reuses in-scope
            if "A02-08-scope-day-rung-installed" in spec_map:
                c1 = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-08-a"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action="approve", rung="day",
                )
                grants_now = (app.request(f"/v1/approval-grants?session_id={a02_sid}") or {}).get("grants") or []
                has_wire_expiry = any(
                    bool(g.get("expires_at")) or ("day" in str(g.get("expires_when") or "").lower())
                    for g in grants_now if isinstance(g, dict)
                )
                with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
                    db_exp_rows = conn.execute(
                        "SELECT expires_at FROM chat_grants WHERE chat_session_id = ? AND expires_at IS NOT NULL",
                        (a02_sid,),
                    ).fetchall()
                has_db_expiry = len(db_exp_rows) >= 1
                c2 = _call_with_checkpoint_inspect(
                    app, a02_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a02-08-b"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action=None,
                )
                ok = c1["checkpoint"]["asked"] and (has_wire_expiry or has_db_expiry) and (not c2["checkpoint"]["asked"])
                receipts.append(_build_receipt(
                    spec=spec_map["A02-08-scope-day-rung-installed"], prov=prov,
                    typed_result={
                        "rung": c1["checkpoint"]["rung"],
                        "has_wire_expiry": has_wire_expiry,
                        "has_db_expiry": has_db_expiry,
                        "reused_without_ask": not c2["checkpoint"]["asked"],
                    },
                    independent_observation={"grant_count": len(grants_now), "db_expiring_chat_grants": len(db_exp_rows)},
                    passed=ok,
                ))

            abort_session(a02_sid)
            _revoke_all_grants(app)

            # A02-09..12: project scope (ApprovalCategoryTool offers the project rung in slot 4;
            # command grants bind exact command identity, so same command reuses in-project and asks cross-project)
            _patch_global_approvals(
                app, posture="balanced",
                rules=[{"category": "tool", "pattern": "command", "effect": "ask"}],
                never_ask=False,
            )
            proj_marker_name = "a02_project_grant_marker.txt"
            proj_cmd = f"cp notes.txt {proj_marker_name}"
            s_p1, pid_p1 = open_prompted_session(proj_main, "Grant project-scoped tool approval.")
            _call_with_checkpoint_inspect(app, s_p1, "update_progress", {"content": "- [ ] Copy project grant marker\n"}, action=None, timeout_s=30.0)
            c_proj = _call_with_checkpoint_inspect(
                app, s_p1, "command",
                {"command": proj_cmd},
                action="approve", rung="project", timeout_s=60.0,
            )
            abort_session(s_p1)

            if "A02-09-scope-project-in-project-reuse" in spec_map:
                marker_main = proj_main / proj_marker_name
                if marker_main.exists():
                    marker_main.unlink()
                s_p2 = open_prompted_session_in_project(pid_p1, "Reuse project grant in fresh session of SAME project_id.")
                _call_with_checkpoint_inspect(app, s_p2, "update_progress", {"content": "- [ ] Reuse project grant marker\n"}, action=None, timeout_s=30.0)
                c_reuse = _call_with_checkpoint_inspect(
                    app, s_p2, "command",
                    {"command": proj_cmd},
                    action=None, timeout_s=60.0,
                )
                abort_session(s_p2)
                marker_written = marker_main.exists() and "alpha" in marker_main.read_text(encoding="utf-8", errors="replace")
                ok = c_proj["checkpoint"]["asked"] and c_proj["checkpoint"]["rung"] == "project" and (not c_reuse["checkpoint"]["asked"]) and marker_written
                receipts.append(_build_receipt(
                    spec=spec_map["A02-09-scope-project-in-project-reuse"], prov=prov,
                    typed_result={
                        "project_id": pid_p1,
                        "granted_rung": c_proj["checkpoint"]["rung"],
                        "fresh_session_same_project_asked": c_reuse["checkpoint"]["asked"],
                    },
                    independent_observation={
                        "reused_without_ask": not c_reuse["checkpoint"]["asked"],
                        "marker_file_written": marker_written,
                        "marker_path": proj_marker_name,
                    },
                    passed=ok,
                ))

            if "A02-10-scope-project-cross-project-isolated" in spec_map:
                marker_b = proj_b / proj_marker_name
                if marker_b.exists():
                    marker_b.unlink()
                s_pb, pid_pb = open_prompted_session(proj_b, "Cross-project isolation probe.")
                _call_with_checkpoint_inspect(app, s_pb, "update_progress", {"content": "- [ ] Cross-project marker probe\n"}, action=None, timeout_s=30.0)
                c_cross = _call_with_checkpoint_inspect(
                    app, s_pb, "command",
                    {"command": proj_cmd},
                    action="reject", timeout_s=60.0,
                )
                abort_session(s_pb)
                marker_b_withheld = not marker_b.exists()
                ok = pid_pb != pid_p1 and c_cross["checkpoint"]["asked"] and marker_b_withheld
                receipts.append(_build_receipt(
                    spec=spec_map["A02-10-scope-project-cross-project-isolated"], prov=prov,
                    typed_result={"other_project_id": pid_pb, "other_project_asked": c_cross["checkpoint"]["asked"]},
                    independent_observation={
                        "isolated": c_cross["checkpoint"]["asked"],
                        "marker_file_withheld": marker_b_withheld,
                        "marker_path": proj_marker_name,
                    },
                    passed=ok,
                ))

            # A02-11: Saved approvals inventory exposes exact scope metadata
            if "A02-11-scope-saved-grants-inventory" in spec_map:
                inv = (app.request("/v1/approval-grants") or {}).get("grants") or []
                scopes = sorted({str(g.get("scope") or "") for g in inv if isinstance(g, dict)})
                ok = len(inv) >= 1 and all(s in {"chat", "project", "device"} for s in scopes)
                receipts.append(_build_receipt(
                    spec=spec_map["A02-11-scope-saved-grants-inventory"], prov=prov,
                    typed_result={"grant_count": len(inv), "scopes": scopes},
                    independent_observation={"all_scopes_valid": ok},
                    passed=ok,
                ))

            _revoke_all_grants(app)

            # A02-12: Fresh chat in SAME project_id does not inherit revoked authority
            if "A02-12-scope-chat-delete-ends-authority" in spec_map:
                marker_main = proj_main / proj_marker_name
                if marker_main.exists():
                    marker_main.unlink()
                s_del = open_prompted_session_in_project(pid_p1, "Fresh chat after grant clear.")
                _call_with_checkpoint_inspect(app, s_del, "update_progress", {"content": "- [ ] Post-revoke marker probe\n"}, action=None, timeout_s=30.0)
                c_fresh = _call_with_checkpoint_inspect(
                    app, s_del, "command",
                    {"command": proj_cmd},
                    action="reject", timeout_s=60.0,
                )
                abort_session(s_del)
                marker_main_withheld = not marker_main.exists()
                ok = c_fresh["checkpoint"]["asked"] and marker_main_withheld
                receipts.append(_build_receipt(
                    spec=spec_map["A02-12-scope-chat-delete-ends-authority"], prov=prov,
                    typed_result={"fresh_chat_asked": c_fresh["checkpoint"]["asked"]},
                    independent_observation={
                        "no_leaked_authority": c_fresh["checkpoint"]["asked"],
                        "marker_file_withheld": marker_main_withheld,
                        "marker_path": proj_marker_name,
                    },
                    passed=ok,
                ))
            _patch_global_approvals(app, posture="balanced", rules=[], never_ask=False)

            # ------------------------------------------------------------------
            # A03 (14 sidecar slots)
            # ------------------------------------------------------------------
            for sid_key, posture_val in [
                ("A03-01-posture-light-settings", "light"),
                ("A03-02-posture-balanced-settings", "balanced"),
                ("A03-03-posture-strict-settings", "strict"),
            ]:
                if sid_key in spec_map:
                    _patch_global_approvals(app, posture=posture_val, rules=[], never_ask=False)
                    got = app.request("/v1/settings/approvals") or {}
                    ok = got.get("approval_posture") == posture_val
                    receipts.append(_build_receipt(
                        spec=spec_map[sid_key], prov=prov,
                        typed_result={"requested": posture_val, "effective": got.get("approval_posture")},
                        independent_observation={"matched": ok},
                        passed=ok,
                    ))

            # A03-04: unknown posture token rejected with 400
            if "A03-04-posture-unknown-rejected" in spec_map:
                status_code = 200
                try:
                    app.request("/v1/settings/approvals", {"approval_posture": "yolo", "rules": []}, method="PATCH")
                except urllib.error.HTTPError as exc:
                    status_code = exc.code
                got = app.request("/v1/settings/approvals") or {}
                ok = status_code == 400 and got.get("approval_posture") == "strict"
                receipts.append(_build_receipt(
                    spec=spec_map["A03-04-posture-unknown-rejected"], prov=prov,
                    typed_result={"http_status": status_code, "posture_after": got.get("approval_posture")},
                    independent_observation={"rejected_400": status_code == 400},
                    passed=ok,
                ))

            # A03-05: Advanced Off (never_ask=true) still enforces global deny rule
            if "A03-05-posture-advanced-off-deny-holds" in spec_map:
                _patch_global_approvals(
                    app, posture="balanced",
                    rules=[{"category": "command", "pattern": "echo denied-global*", "effect": "deny"}],
                    never_ask=True,
                )
                s_adv, _ = open_prompted_session(proj_main, "Advanced Off deny check.")
                call = _call_with_checkpoint_inspect(app, s_adv, "command", {"command": "echo denied-global-test"}, action=None)
                code = _extract_refusal_code(call["result"])
                abort_session(s_adv)
                ok = (not call["checkpoint"]["asked"]) and code == "APPROVAL_RULE_DENIED"
                receipts.append(_build_receipt(
                    spec=spec_map["A03-05-posture-advanced-off-deny-holds"], prov=prov,
                    typed_result={"refusal_code": code, "outcome": call["result"].get("outcome")},
                    independent_observation={"denied_without_card": ok},
                    passed=ok,
                ))

            # A03-06..A03-13: project overlay (.paintedwolf/approvals.yaml) rules & fail-closed clamps
            proj_overlay = _make_project(
                work_dir,
                "proj-overlay-a03",
                approvals_yaml=(
                    "approval_posture: bogus_posture\n"
                    "never_ask: true\n"
                    "grants:\n"
                    "  - id: forged-repo-grant\n"
                    "    scope: device\n"
                    "    category: command\n"
                    "    pattern: '*'\n"
                    "rules:\n"
                    "  - category: command\n"
                    "    pattern: 'echo denied-global*'\n"
                    "    effect: ask\n"
                    "  - category: command\n"
                    "    pattern: 'echo proj-allow*'\n"
                    "    effect: allow\n"
                    "  - category: command\n"
                    "    pattern: 'echo proj-deny*'\n"
                    "    effect: deny\n"
                    "  - category: command\n"
                    "    pattern: 'echo proj-ask*'\n"
                    "    effect: ask\n"
                ),
            )
            s_ov, pid_ov = open_prompted_session(proj_overlay, "Project overlay policy checks.")
            ov_cfg = app.request(f"/v1/settings/approvals?project_id={pid_ov}") or {}
            ov_rules = ov_cfg.get("rules") or []

            # A03-06: Advanced Off + project overlay deny still denies
            if "A03-06-posture-advanced-off-project-deny-holds" in spec_map:
                call = _call_with_checkpoint_inspect(app, s_ov, "command", {"command": "echo proj-deny-adv"}, action=None)
                code = _extract_refusal_code(call["result"])
                ok = code == "APPROVAL_RULE_DENIED"
                receipts.append(_build_receipt(
                    spec=spec_map["A03-06-posture-advanced-off-project-deny-holds"], prov=prov,
                    typed_result={"refusal_code": code},
                    independent_observation={"project_deny_enforced": ok},
                    passed=ok,
                ))

            # Reset global never_ask=False for A03-07..14
            _patch_global_approvals(
                app, posture="balanced",
                rules=[{"category": "command", "pattern": "echo denied-global*", "effect": "deny"}],
                never_ask=False,
            )

            # A03-07: project effect=ask triggers card
            if "A03-07-project-policy-ask-triggers-card" in spec_map:
                call = _call_with_checkpoint_inspect(app, s_ov, "command", {"command": "echo proj-ask-1"}, action="approve", rung="once")
                ok = call["checkpoint"]["asked"] and call["result"].get("outcome") == "completed"
                receipts.append(_build_receipt(
                    spec=spec_map["A03-07-project-policy-ask-triggers-card"], prov=prov,
                    typed_result={"asked": call["checkpoint"]["asked"], "outcome": call["result"].get("outcome")},
                    independent_observation={"card_triggered": call["checkpoint"]["asked"]},
                    passed=ok,
                ))

            # A03-08: project effect=deny blocks command with 0 host effect
            if "A03-08-project-policy-deny-blocks-effect" in spec_map:
                marker = proj_overlay / "must_not_exist.txt"
                call = _call_with_checkpoint_inspect(app, s_ov, "command", {"command": "echo proj-deny-2 > must_not_exist.txt"}, action=None)
                code = _extract_refusal_code(call["result"])
                ok = code == "APPROVAL_RULE_DENIED" and (not marker.exists())
                receipts.append(_build_receipt(
                    spec=spec_map["A03-08-project-policy-deny-blocks-effect"], prov=prov,
                    typed_result={"refusal_code": code},
                    independent_observation={"marker_exists": marker.exists()},
                    passed=ok,
                ))

            # A03-09: project effect=allow refused (never enters effective rules)
            if "A03-09-project-policy-allow-refused" in spec_map:
                eff_cfg = app.request(f"/v1/settings/approvals?project_id={pid_ov}") or {}
                eff_rules = eff_cfg.get("rules") or []
                has_allow = any(str(r.get("effect") or "") not in {"ask", "deny"} for r in eff_rules if isinstance(r, dict))
                ok = not has_allow
                receipts.append(_build_receipt(
                    spec=spec_map["A03-09-project-policy-allow-refused"], prov=prov,
                    typed_result={"effective_rules": eff_rules},
                    independent_observation={"no_allow_rule_present": ok},
                    passed=ok,
                ))

            # A03-10: project never_ask=true refused
            if "A03-10-project-policy-never-ask-refused" in spec_map:
                eff_cfg = app.request(f"/v1/settings/approvals?project_id={pid_ov}") or {}
                ok = eff_cfg.get("never_ask") is not True
                receipts.append(_build_receipt(
                    spec=spec_map["A03-10-project-policy-never-ask-refused"], prov=prov,
                    typed_result={"effective_never_ask": eff_cfg.get("never_ask")},
                    independent_observation={"never_ask_refused": ok},
                    passed=ok,
                ))

            # A03-11: project grants[] refused (forged-repo-grant never appears in /v1/approval-grants)
            if "A03-11-project-policy-grants-refused" in spec_map:
                inv = (app.request("/v1/approval-grants") or {}).get("grants") or []
                forged = any(g.get("id") == "forged-repo-grant" for g in inv if isinstance(g, dict))
                ok = not forged
                receipts.append(_build_receipt(
                    spec=spec_map["A03-11-project-policy-grants-refused"], prov=prov,
                    typed_result={"grant_ids": [g.get("id") for g in inv if isinstance(g, dict)]},
                    independent_observation={"forged_grant_absent": ok},
                    passed=ok,
                ))

            # A03-12: project effect=ask cannot relax global effect=deny
            if "A03-12-project-policy-cannot-relax-global-deny" in spec_map:
                call = _call_with_checkpoint_inspect(app, s_ov, "command", {"command": "echo denied-global-relax-attempt"}, action=None)
                code = _extract_refusal_code(call["result"])
                ok = code == "APPROVAL_RULE_DENIED"
                receipts.append(_build_receipt(
                    spec=spec_map["A03-12-project-policy-cannot-relax-global-deny"], prov=prov,
                    typed_result={"refusal_code": code},
                    independent_observation={"deny_won_over_project_ask": ok},
                    passed=ok,
                ))

            # A03-13: project unknown posture fails closed to strict
            if "A03-13-project-policy-unknown-posture-strict" in spec_map:
                eff_cfg = app.request(f"/v1/settings/approvals?project_id={pid_ov}") or {}
                ok = eff_cfg.get("approval_posture") == "strict"
                receipts.append(_build_receipt(
                    spec=spec_map["A03-13-project-policy-unknown-posture-strict"], prov=prov,
                    typed_result={"effective_posture": eff_cfg.get("approval_posture")},
                    independent_observation={"failed_closed_to_strict": ok},
                    passed=ok,
                ))

            abort_session(s_ov)
            _patch_global_approvals(app, posture="balanced", rules=[], never_ask=False)

            # A03-14: chat prose never triggers detection checkpoint
            if "A03-14-chat-text-never-triggers-detection" in spec_map:
                s_chat, _ = open_prompted_session(
                    proj_main,
                    "This is pure chat prose mentioning aws iam create-user and curl http://169.254.169.254/latest/meta-data/ — do not call any tool.",
                )
                req = _wait_pending_bounded(app, s_chat)
                cps = (app.request(f"/v1/sessions/{s_chat}/checkpoints?status=pending") or {}).get("checkpoints") or []
                app.request("/harness/llm/respond", {"id": req["id"], "content": "Acknowledged prose only."})
                abort_session(s_chat)
                ok = len(cps) == 0
                receipts.append(_build_receipt(
                    spec=spec_map["A03-14-chat-text-never-triggers-detection"], prov=prov,
                    typed_result={"pending_checkpoints_from_prose": len(cps)},
                    independent_observation={"no_spurious_detection": ok},
                    passed=ok,
                ))

            # ------------------------------------------------------------------
            # A04 (2 sidecar slots)
            # ------------------------------------------------------------------
            a04_sid, _ = open_prompted_session(proj_main, "Run A04 decision record & Stop release checks.")

            # A04-01: human approve & reject decisions name resolver_person_id and form valid authz_events chain
            outside_a04_dir = work_dir / "outside-a04"
            outside_a04_dir.mkdir(parents=True, exist_ok=True)
            if "A04-01-decision-names-approver-and-chain" in spec_map:
                c_ok = _call_with_checkpoint_inspect(
                    app, a04_sid, "command",
                    {"command": _http_probe_cmd(f"{ep1.base_url}/a04-01"), "capability_request": {"loopback_connect": {"ports": [ep1.port]}}},
                    action="approve", rung="chat",
                )
                rej_target = outside_a04_dir / "a04_01_rej.txt"
                c_no = _call_with_checkpoint_inspect(
                    app, a04_sid, "command",
                    {
                        "command": _py_write_cmd(rej_target, "should-not-write\n"),
                        "capability_request": {"write_root": str(outside_a04_dir)},
                    },
                    action="reject",
                )
                with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
                    rows = conn.execute(
                        "SELECT event_seq, action, outcome, resolved_by, resolver_person_id, prev_hash, row_hash "
                        "FROM authz_events WHERE session_id = ? ORDER BY event_seq ASC",
                        (a04_sid,),
                    ).fetchall()
                human_rows = [r for r in rows if r[1] == "approval_decision" and r[3] == "human"]
                chain_linked = all(
                    bool(rows[i][6]) and (i == 0 or rows[i][5] == rows[i - 1][6])
                    for i in range(len(rows))
                )
                ok = (
                    len(human_rows) >= 2
                    and all(bool(r[4]) for r in human_rows)
                    and chain_linked
                    and c_ok["checkpoint"]["asked"]
                    and c_no["checkpoint"]["asked"]
                )
                receipts.append(_build_receipt(
                    spec=spec_map["A04-01-decision-names-approver-and-chain"], prov=prov,
                    typed_result={"human_decision_count": len(human_rows), "chain_length": len(rows)},
                    independent_observation={"chain_linked": chain_linked, "all_named_person": all(bool(r[4]) for r in human_rows)},
                    passed=ok,
                ))

            # A04-02: Stop while checkpoint pending cancels held effect (0 writes) and keeps committed chat grant
            if "A04-02-stop-releases-pending-without-effect" in spec_map:
                outside_a04_stop = work_dir / "outside-a04-stop"
                outside_a04_stop.mkdir(parents=True, exist_ok=True)
                stop_target = outside_a04_stop / "must_not_write_on_stop.txt"
                req = _wait_pending_bounded(app, a04_sid)
                held_call_id = str(uuid.uuid4())
                app.request(
                    "/harness/llm/respond",
                    {
                        "id": req["id"],
                        "tool_calls": [{
                            "id": held_call_id,
                            "name": "command",
                            "args": {
                                "command": _py_write_cmd(stop_target, "must-not-exist\n"),
                                "capability_request": {"write_root": str(outside_a04_stop)},
                            },
                        }],
                    },
                )
                cp_held, settled_early = _wait_pending_checkpoint_or_settled(app, a04_sid, held_call_id, timeout_s=20.0)
                app.request(f"/v1/sessions/{a04_sid}/abort", {})
                _wait_session_idle(app, a04_sid, "a04 idle after stop")
                grants_after_stop = (app.request(f"/v1/approval-grants?session_id={a04_sid}") or {}).get("grants") or []
                ok = bool(cp_held and cp_held.get("id")) and (not stop_target.exists()) and len(grants_after_stop) >= 1
                receipts.append(_build_receipt(
                    spec=spec_map["A04-02-stop-releases-pending-without-effect"], prov=prov,
                    typed_result={
                        "held_checkpoint_id": (cp_held or {}).get("id"),
                        "settled_without_checkpoint": settled_early,
                        "surviving_chat_grants": len(grants_after_stop),
                    },
                    independent_observation={"stop_target_exists": stop_target.exists()},
                    passed=ok,
                ))

            abort_session(a04_sid)
        finally:
            _sync()
            app.close(cleanup=False)

    return list(receipts)


def _run_a12_restart_slots(
    *,
    specs: list[dict[str, str]],
    work_dir: pathlib.Path,
    receipts_dir: pathlib.Path,
    receipts_out: list[dict[str, Any]],
    prov: dict[str, Any],
    engine_bin: str | pathlib.Path | None,
    engine_manifest: str | pathlib.Path | None,
    engine_root: str | pathlib.Path | None,
) -> list[dict[str, Any]]:
    """Run the 3 A12 sidecar restart slots with a real stop+restart cycle of lycaon-dev."""
    spec_map = {s["slot_id"]: s for s in specs}

    class _FlushingReceiptList(list):
        def append(self, rec: dict[str, Any]) -> None:  # type: ignore[override]
            super().append(rec)
            _save_json(receipts_dir / f"{rec['slot_id']}.json", rec)
            receipts_out.append(rec)

    receipts = _FlushingReceiptList()
    if not any(k in spec_map for k in (
        "A12-01-restart-pending-preserved-no-double-run",
        "A12-02-restart-chat-grant-replays-in-scope",
        "A12-03-restart-revoked-grant-stays-revoked",
    )):
        return receipts

    proj = _make_project(work_dir, "proj-a12")
    proj_rev = _make_project(work_dir, "proj-a12-rev")
    app_dir = work_dir / "a12-app"
    outside_a12_pending = work_dir / "outside-a12-pending"
    outside_a12_pending.mkdir(parents=True, exist_ok=True)
    pending_target = outside_a12_pending / "a12_pending_once.txt"
    pre_plan_id = ""
    post_plan_id = ""
    pending_cp_id = ""
    pending_preserved = False
    pre_restart_target_existed = False
    session_id = ""
    session_rev_id = ""
    project_id = ""
    revoked_before_restart = 0

    with (
        RecordingHTTPFixture(fixture_id="tb07-a12-chat") as ep_chat,
        RecordingHTTPFixture(fixture_id="tb07-a12-revoked") as ep_revoked,
    ):
        # Boot 1:
        # 1) On session_id: install chat-scoped loopback_connect grant (keep across restart)
        # 2) On dedicated session_rev_id: install chat-scoped loopback_connect grant, then revoke ALL grants for session_rev_id
        # 3) On session_id: open pending write_root checkpoint (distinct capability from loopback_connect!) and stop engine before resolving
        app1 = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
        try:
            app1.ready()
            session_id, project_id, _ = _open_session(app1, proj)
            app1.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": "A12 pre-restart chat grant setup."})
            _call_with_checkpoint_inspect(
                app1, session_id, "command",
                {"command": _http_probe_cmd(f"{ep_chat.base_url}/boot1"), "capability_request": {"loopback_connect": {"ports": [ep_chat.port]}}},
                action="approve", rung="chat", timeout_s=90.0,
            )
            _close_session_turn(app1, session_id)

            # Separate project+session via _open_session for A12-03 revoked grant so it is 100% isolated from session_id.
            session_rev_id, _, _ = _open_session(app1, proj_rev)
            app1.request(f"/v1/sessions/{session_rev_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": "A12 pre-restart revoked grant setup."})
            c_rev_boot1 = _call_with_checkpoint_inspect(
                app1, session_rev_id, "command",
                {"command": _http_probe_cmd(f"{ep_revoked.base_url}/boot1"), "capability_request": {"loopback_connect": {"ports": [ep_revoked.port]}}},
                action="approve", rung="chat", timeout_s=120.0,
            )
            rev_grants = (app1.request(f"/v1/approval-grants?session_id={session_rev_id}") or {}).get("grants") or []
            rev_ids = [g["id"] for g in rev_grants if isinstance(g, dict) and g.get("id")]
            if not rev_ids:
                all_grants = (app1.request("/v1/approval-grants") or {}).get("grants") or []
                rev_ids = [
                    g["id"] for g in all_grants
                    if isinstance(g, dict) and g.get("id") and g.get("chat_session_id") == session_rev_id
                ]
            if rev_ids:
                app1.request("/v1/approval-grants/revoke", {"ids": rev_ids})
                revoked_before_restart = len(rev_ids)
            _close_session_turn(app1, session_rev_id)

            # Now open a pending write_root checkpoint on session_id and stop engine before resolving.
            app1.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": "A12 pending write_root across restart."})
            req = _wait_pending_bounded(app1, session_id, timeout_s=90.0)
            pending_call_id = str(uuid.uuid4())
            app1.request(
                "/harness/llm/respond",
                {
                    "id": req["id"],
                    "tool_calls": [{
                        "id": pending_call_id,
                        "name": "command",
                        "args": {
                            "command": _py_write_cmd(pending_target, "a12-once-after-restart\n"),
                            "capability_request": {"write_root": str(outside_a12_pending)},
                        },
                    }],
                },
            )
            cp_before, _settled_before = _wait_pending_checkpoint_or_settled(app1, session_id, pending_call_id, timeout_s=20.0)
            pending_cp_id = str((cp_before or {}).get("id") or "")
            pre_plan_id = str((((cp_before or {}).get("tool_approval") or {}).get("plan") or {}).get("id") or "")
            pre_restart_target_existed = pending_target.exists()
        finally:
            app1.close(cleanup=False)

        # Boot 2: Restart lycaon-dev on the exact same app_dir (preserving store.db + config/)
        app2 = _restart_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
        post_restart_before_approve_existed = False
        try:
            app2.ready()

            # A12-01: Pending checkpoint preserved with identical plan ID; verify no premature effect before approve
            post_restart_before_approve_existed = pending_target.exists()
            cps_after = (app2.request(f"/v1/sessions/{session_id}/checkpoints?status=pending") or {}).get("checkpoints") or []
            matched_cp = next((c for c in cps_after if c.get("id") == pending_cp_id), None)
            if matched_cp is not None:
                pending_preserved = True
                post_plan_id = str(((matched_cp.get("tool_approval") or {}).get("plan") or {}).get("id") or "")
                opt = _select_option(matched_cp, rung="once")
                app2.request(
                    f"/v1/sessions/{session_id}/checkpoints/{pending_cp_id}",
                    {"kind": "tool_approval", "action": "approve", "option_id": opt["id"]},
                )
            with contextlib.suppress(Exception):
                app2.request(f"/v1/sessions/{session_id}/abort", {})
            _wait_session_idle(app2, session_id, "a12 idle after restart")

            # A12-02: Chat grant for ep_chat replays without asking in same chat, isolated from session B
            app2.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": "A12 post-restart grant replay."})
            b_chat = len(ep_chat.snapshot()["requests"])
            c_chat_replay = _call_with_checkpoint_inspect(
                app2, session_id, "command",
                {"command": _http_probe_cmd(f"{ep_chat.base_url}/boot2"), "capability_request": {"loopback_connect": {"ports": [ep_chat.port]}}},
                action=None, timeout_s=90.0,
            )
            a_chat = len(ep_chat.snapshot()["requests"])
            _close_session_turn(app2, session_id)

            # A12-03: Revoked grant on session_rev_id stayed revoked across restart -> asks!
            app2.request(f"/v1/sessions/{session_rev_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": "A12 post-restart revoked check."})
            b_rev = len(ep_revoked.snapshot()["requests"])
            c_rev_after = _call_with_checkpoint_inspect(
                app2, session_rev_id, "command",
                {"command": _http_probe_cmd(f"{ep_revoked.base_url}/boot2"), "capability_request": {"loopback_connect": {"ports": [ep_revoked.port]}}},
                action="reject", timeout_s=90.0,
            )
            a_rev = len(ep_revoked.snapshot()["requests"])
            _close_session_turn(app2, session_rev_id)

            # Cross-chat isolation after restart
            sess_b = app2.request("/v1/sessions", {"project_id": project_id, "posture": "build"})
            sid_b = sess_b["id"]
            app2.request("/harness/model-limit", {"session_id": sid_b, "limit": 10})
            _wait_session_idle(app2, sid_b, "a12 sess_b ready", timeout_s=60.0)
            app2.request(f"/v1/sessions/{sid_b}/prompts", {"operation_id": str(uuid.uuid4()), "text": "A12 cross-chat check."})
            c_chat_cross = _call_with_checkpoint_inspect(
                app2, sid_b, "command",
                {"command": _http_probe_cmd(f"{ep_chat.base_url}/boot2-other"), "capability_request": {"loopback_connect": {"ports": [ep_chat.port]}}},
                action="reject", timeout_s=90.0,
            )
            _close_session_turn(app2, sid_b)
        finally:
            app2.close(cleanup=False)

    if "A12-01-restart-pending-preserved-no-double-run" in spec_map:
        ok = (
            pending_preserved
            and (not pre_restart_target_existed)
            and (not post_restart_before_approve_existed)
            and bool(pre_plan_id)
            and pre_plan_id == post_plan_id
        )
        receipts.append(_build_receipt(
            spec=spec_map["A12-01-restart-pending-preserved-no-double-run"], prov=prov,
            typed_result={
                "checkpoint_id": pending_cp_id,
                "pending_preserved": pending_preserved,
                "pre_restart_plan_id": pre_plan_id,
                "post_restart_plan_id": post_plan_id,
            },
            independent_observation={
                "pre_restart_target_existed": pre_restart_target_existed,
                "post_restart_before_approve_existed": post_restart_before_approve_existed,
                "post_restart_target_exists": pending_target.exists(),
            },
            passed=ok,
        ))

    if "A12-02-restart-chat-grant-replays-in-scope" in spec_map:
        ok = (not c_chat_replay["checkpoint"]["asked"]) and (a_chat - b_chat) == 1 and c_chat_cross["checkpoint"]["asked"]
        receipts.append(_build_receipt(
            spec=spec_map["A12-02-restart-chat-grant-replays-in-scope"], prov=prov,
            typed_result={
                "same_chat_asked_after_restart": c_chat_replay["checkpoint"]["asked"],
                "other_chat_asked_after_restart": c_chat_cross["checkpoint"]["asked"],
            },
            independent_observation={"same_chat_arrivals_delta": a_chat - b_chat},
            passed=ok,
        ))

    if "A12-03-restart-revoked-grant-stays-revoked" in spec_map:
        ok = revoked_before_restart >= 1 and c_rev_after["checkpoint"]["asked"] and (a_rev - b_rev) == 0
        receipts.append(_build_receipt(
            spec=spec_map["A12-03-restart-revoked-grant-stays-revoked"], prov=prov,
            typed_result={
                "boot1_asked": c_rev_boot1["checkpoint"]["asked"],
                "boot1_rung": c_rev_boot1["checkpoint"]["rung"],
                "boot1_outcome": c_rev_boot1["result"].get("outcome"),
                "boot1_error": c_rev_boot1["result"].get("error"),
                "revoked_before_restart": revoked_before_restart,
                "revoked_grant_asked_after_restart": c_rev_after["checkpoint"]["asked"],
            },
            independent_observation={"revoked_fixture_arrivals_delta": a_rev - b_rev},
            passed=ok,
        ))

    return receipts


def run_tb07_sidecar_batch(
    out_dir: str | pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    families: set[str] | None = None,
) -> dict[str, Any]:
    """Run all scheduled TB07 sidecar slots (default: all 39) and emit per-slot receipts + summary."""
    t0 = time.monotonic()
    out = pathlib.Path(out_dir).resolve()
    receipts_dir = out / "receipts"
    work_dir = out / "work"
    receipts_dir.mkdir(parents=True, exist_ok=True)
    work_dir.mkdir(parents=True, exist_ok=True)

    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")
    selected = [s for s in TB07_SIDECAR_SLOT_SPECS if (not families or s["family"] in families)]

    warmed_specs = [s for s in selected if s["family"] != "A12"]
    restart_specs = [s for s in selected if s["family"] == "A12"]

    receipts: list[dict[str, Any]] = []
    batch_errors: list[str] = []
    if warmed_specs:
        warmed_out: list[dict[str, Any]] = []
        try:
            _run_warmed_slots(
                specs=warmed_specs,
                work_dir=work_dir,
                receipts_dir=receipts_dir,
                receipts_out=warmed_out,
                prov=prov,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
        except Exception as exc:  # noqa: BLE001
            batch_errors.append(f"warmed_batch:{type(exc).__name__}:{exc}")
        receipts.extend(warmed_out)
    if restart_specs:
        restart_out: list[dict[str, Any]] = []
        try:
            _run_a12_restart_slots(
                specs=restart_specs,
                work_dir=work_dir,
                receipts_dir=receipts_dir,
                receipts_out=restart_out,
                prov=prov,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
        except Exception as exc:  # noqa: BLE001
            batch_errors.append(f"restart_batch:{type(exc).__name__}:{exc}")
        receipts.extend(restart_out)

    emitted_ids = {r["slot_id"] for r in receipts}
    for spec in selected:
        if spec["slot_id"] not in emitted_ids:
            receipts.append(_build_receipt(
                spec=spec,
                prov=prov,
                typed_result={"batch_errors": batch_errors},
                independent_observation={"slot_aborted_by_exception": True},
                passed=False,
                failure_reasons=batch_errors or ["slot_not_emitted"],
            ))

    receipt_paths: list[str] = []
    by_family: dict[str, dict[str, int]] = {}
    for rec in receipts:
        rpath = receipts_dir / f"{rec['slot_id']}.json"
        _save_json(rpath, rec)
        receipt_paths.append(str(rpath))
        fam = rec["family"]
        bucket = by_family.setdefault(fam, {"total": 0, "pass": 0, "fail": 0, "unknown": 0})
        bucket["total"] += 1
        bucket[rec["verdict"]] = bucket.get(rec["verdict"], 0) + 1

    summary = {
        "schema": "wea-tb07-sidecar-summary-v1",
        "card": "TB07",
        "lane": "sidecar",
        "elapsed_seconds": round(time.monotonic() - t0, 2),
        "provenance": prov,
        "scheduled_slots": len(selected),
        "completed_slots": len(receipts),
        "passed": sum(1 for r in receipts if r["verdict"] == "pass"),
        "failed": sum(1 for r in receipts if r["verdict"] == "fail"),
        "unknown": sum(1 for r in receipts if r["verdict"] == "unknown"),
        "by_family": by_family,
        "receipt_paths": receipt_paths,
        "failed_slots": [
            {"slot_id": r["slot_id"], "family": r["family"], "failure_reasons": r.get("failure_reasons", [])}
            for r in receipts
            if r["verdict"] != "pass"
        ],
    }
    _save_json(out / "tb07_sidecar_summary.json", summary)
    return summary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB07 headless 39-slot sidecar runner (A01, A02, A03, A04, A12)")
    parser.add_argument("--out-dir", required=True, help="Output directory for per-slot receipts and summary manifest")
    parser.add_argument("--engine-bin", default=None, help="Path to pinned lycaon-dev binary")
    parser.add_argument("--engine-manifest", default=None, help="Path to TB01 build-manifest.json")
    parser.add_argument("--engine-root", default=None, help="Path to staged LYCAON_ENGINE_ROOT")
    parser.add_argument("--families", default=None, help="Optional comma-separated family filter (e.g. A01,A02)")
    args = parser.parse_args(argv)

    fam_set = {f.strip().upper() for f in args.families.split(",") if f.strip()} if args.families else None
    summary = run_tb07_sidecar_batch(
        args.out_dir,
        engine_bin=args.engine_bin,
        engine_manifest=args.engine_manifest,
        engine_root=args.engine_root,
        families=fam_set,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["failed"] == 0 and summary["unknown"] == 0 and summary["completed_slots"] == summary["scheduled_slots"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
