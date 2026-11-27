"""Decision 040 worker-surface friction (`worker_report_refusals`) dual-host validation runner.

Proves on exact D1 `lycaon-dev`:
1. Charged worker case: worker submits `complete_leg` with an unobserved handle (`read#999999`),
   receives a persisted typed grounding refusal (`Code: WORKER_EVIDENCE_HANDLE_UNKNOWN`), repairs
   with observed `read#1`, and is accepted -> `worker_report_refusals == 1`, `finally_accepted == True`,
   `friction_units == 0`.
2. Zero-charge worker case: worker submits `complete_leg` with observed `read#1` on the first try ->
   `worker_report_refusals == 0`, `finally_accepted == True`, `friction_units == 0`.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys
import time
import uuid
from typing import Any

from grounding.friction_probe import probe_store_db, probe_worker_report_refusals
from grounding.sidecar_driver import IsolatedSidecarProcess


def _init_git_workspace(workspace: pathlib.Path) -> None:
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "app.go").write_text(
        'package app\n\nfunc Main() string { return "ready" }\n',
        encoding="utf-8",
    )
    for cmd in (
        ["git", "init", "-q"],
        ["git", "add", "."],
        ["git", "-c", "user.name=Grounding Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"],
    ):
        subprocess.run(cmd, cwd=workspace, check=True)


def _run_worker_job(
    client: Any,
    *,
    project_id: str,
    sidecar: IsolatedSidecarProcess,
    trace_log: pathlib.Path,
    inject_bad_first_report: bool,
    ignore_sessions: set[str],
) -> dict[str, Any]:
    root_sid = client.create_session(project_id, posture="build")["id"]
    ignore_sessions.add(root_sid)
    client.wait_session_ready(root_sid)
    client.send_prompt(root_sid, "Dispatch a repo researcher to survey app.go.")

    def _answer_tool(sess: str, tool_name: str, tool_args: dict[str, Any]) -> None:
        req = client.wait_pending(sess, timeout=180.0)
        if tool_name not in (req.get("tools") or []):
            client.respond(
                req["id"],
                tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": tool_name}}],
            )
            req = client.wait_pending(sess, timeout=180.0)
        client.respond(
            req["id"],
            tool_calls=[{"id": str(uuid.uuid4()), "name": tool_name, "args": tool_args}],
        )

    _answer_tool(root_sid, "update_progress", {"content": "## Progress\n- [ ] Survey app.go"})
    _answer_tool(
        root_sid,
        "task",
        {
            "agent_type": "repo-researcher",
            "brief": {"goal": "Survey app.go", "done_when": ["The declared scope is delivered."]},
            "scope": {"mode": "read", "paths": ["app.go"]},
        },
    )

    deadline = time.monotonic() + 180.0
    child_sid = ""
    child_req = None
    while time.monotonic() < deadline:
        pend = client.request("/harness/llm/pending")
        if isinstance(pend, dict) and pend.get("pending"):
            psid = str(pend.get("session_id") or "")
            if psid == root_sid:
                client.respond(
                    pend["id"],
                    tool_calls=[{"id": str(uuid.uuid4()), "name": "wait", "args": {"timeout_ms": 15000, "conditions": [{"kind": "next_worker_done"}]}}],
                )
            elif psid and psid not in ignore_sessions:
                child_sid = psid
                ignore_sessions.add(child_sid)
                child_req = pend
                break
        time.sleep(0.15)
    if not child_sid or child_req is None:
        raise RuntimeError("Worker child session never became pending")

    # Child reads app.go -> observes read#1
    client.respond(
        child_req["id"],
        tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n app.go"}}],
    )
    child_req2 = client.wait_pending(child_sid, timeout=180.0)

    if inject_bad_first_report:
        # Submit nonexistent path + unobserved read#999999 -> refused on D1 with typed grounding Code
        client.respond(
            child_req2["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "complete_leg",
                    "args": {
                        "leg_status": "complete",
                        "brief": "Surveyed nonexistent.go.",
                        "findings": [{"path": "nonexistent.go", "line": 90, "evidence": "read#999999", "note": "Unobserved file and handle."}],
                    },
                }
            ],
        )
        child_req3 = client.wait_pending(child_sid, timeout=180.0)
        client.respond(
            child_req3["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "complete_leg",
                    "args": {
                        "leg_status": "complete",
                        "brief": "Surveyed app.go.",
                        "findings": [{"path": "app.go", "line": 3, "evidence": "read#1", "note": "Verified Main()."}],
                    },
                }
            ],
        )
    else:
        client.respond(
            child_req2["id"],
            tool_calls=[
                {
                    "id": str(uuid.uuid4()),
                    "name": "complete_leg",
                    "args": {
                        "leg_status": "complete",
                        "brief": "Surveyed app.go.",
                        "findings": [{"path": "app.go", "line": 3, "evidence": "read#1", "note": "Verified Main()."}],
                    },
                }
            ],
        )

    # Wait for root to resume (meaning worker completed) and consume its pending turn
    root_resume = client.wait_pending(root_sid, timeout=180.0)
    client.respond(root_resume["id"], content="Worker completed.")
    refusals = probe_worker_report_refusals(sidecar.store_db_path, child_sid)
    d10_child = probe_store_db(sidecar.store_db_path, child_sid, trace_log_path=trace_log)
    return {
        "root_session_id": root_sid,
        "child_session_id": child_sid,
        "worker_report_refusals": refusals.to_dict(),
        "child_friction_units": d10_child.friction_units.value,
    }


def run_worker_refusals_validation(
    *,
    engine_bin: str,
    product_root: str,
    expected_engine_sha256: str,
    out_dir: pathlib.Path,
    run_id: str,
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)

    def _run_isolated(case_name: str, *, inject_bad_first_report: bool) -> tuple[dict[str, Any], dict[str, Any], str]:
        case_dir = out_dir / case_name
        workspace = case_dir / "workspace"
        config_dir = case_dir / "config"
        _init_git_workspace(workspace)
        sidecar = IsolatedSidecarProcess(
            engine_bin,
            product_root=product_root,
            config_dir=config_dir,
            workspace_dir=workspace,
            expected_sha256=expected_engine_sha256,
            trace_friction=True,
        )
        with sidecar as client:
            manifest = sidecar.build_run_manifest().to_dict()
            plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
            pid = client.create_project(workspace)["id"]
            seen: set[str] = set()
            result = _run_worker_job(
                client,
                project_id=pid,
                sidecar=sidecar,
                trace_log=sidecar.log_path,
                inject_bad_first_report=inject_bad_first_report,
                ignore_sessions=seen,
            )
            return result, manifest, plat

    charged, manifest, plat = _run_isolated("charged", inject_bad_first_report=True)
    zero, _zero_manifest, _zero_plat = _run_isolated("zero_charge", inject_bad_first_report=False)

    charged_ok = (
        charged["worker_report_refusals"]["worker_report_refusals"] == 1
        and charged["worker_report_refusals"]["finally_accepted"] is True
        and charged["child_friction_units"] == 0
    )
    zero_ok = (
        zero["worker_report_refusals"]["worker_report_refusals"] == 0
        and zero["worker_report_refusals"]["finally_accepted"] is True
        and zero["child_friction_units"] == 0
    )
    summary = {
        "card": "GB02",
        "kind": "worker_report_refusals_decision_040",
        "run_id": run_id,
        "platform": plat,
        "passed": charged_ok and zero_ok,
        "run_manifest": manifest,
        "charged_case": charged,
        "zero_charge_case": zero,
    }
    (out_dir / "worker-report-refusals-validation.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB02 Decision 040 worker_report_refusals validation")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb02-worker-refusals")
    args = ap.parse_args(argv)
    summary = run_worker_refusals_validation(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
