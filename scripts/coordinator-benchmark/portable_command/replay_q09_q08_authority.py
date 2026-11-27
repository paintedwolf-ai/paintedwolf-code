"""Model-free replay witness for Q09 and Q08/Q20 authority refusals."""
import argparse
import hashlib
import json
import pathlib
import sys
import urllib.error
import urllib.request
import uuid

FORBIDDEN_MUTATION_TOOLS = (
    "write", "edit", "delete", "replace_lines",
    "code_rewrite", "jq_edit", "chmod", "chown",
)


def parse_args():
    p = argparse.ArgumentParser(description="Run Q09 and Q08/Q20 model-free authority replay witness.")
    p.add_argument("--engine-manifest", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--repo-root", default=None)
    return p.parse_args()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def workflow_yaml(wf_id, profile):
    return f"""id: {wf_id}
version: 1.0.0
extends: plan@1.0.0
surface_profile: implement
coordinator_profile: {profile}
name: {wf_id} authority witness
initial_posture: build
agents:
  - {{id: coordinator, tools: profile, spawn: false}}
phases:
  - id: research
    activity_label: Exercise {wf_id} under {profile}
    coordinator_surface: implement_investigate
    surface_template: agents/coordinator-surface-build.md
    complete_when: gates_satisfied
    gates: [evidence_passed:verify]
    advance:
      when_gate_met: coordinator
    next: build
  - id: build
    activity_label: Complete {wf_id} witness
    complete_when: gates_satisfied
    gates: [delegation_closeout_complete, evidence_passed:verify]
    advance:
      when_gate_met: coordinator
"""


def has_source_span(details):
    if not isinstance(details, dict):
        return False
    for key in ("source_line", "operation_source_line", "program_line"):
        if isinstance(details.get(key), int) and details[key] >= 1:
            return True
    span = details.get("span")
    return isinstance(span, dict) and isinstance(span.get("line"), int) and span["line"] >= 1


def has_no_mutating_remedy(feedback):
    text = (feedback or "").lower()
    return bool(text) and not any(bad in text for bad in ("use write", "use edit", "sed -i", "run rm "))


def has_zero_external_launches(payload, receipt):
    if (payload or {}).get("native_gaps", 0) != 0:
        return False
    if isinstance(receipt, dict):
        for bucket in ("activities", "children", "executions"):
            for item in receipt.get(bucket) or ():
                if isinstance(item, dict) and item.get("native_command") == "external":
                    return False
    return True


def main():
    args = parse_args()
    if args.repo_root:
        root = pathlib.Path(args.repo_root).resolve()
        for rel in ("scripts", "scripts/coordinator-benchmark"):
            cand = str(root / rel)
            if cand not in sys.path:
                sys.path.insert(0, cand)

    import smoke_workers
    from bootstrap_policy import open_request
    from portable_command.harness import create_isolated_session, send_tool_calls
    from portable_command.observe import parse_tool_result
    from portable_command.provenance import make_app_factory, source_identity
    from portable_command.reporting import structured_rejection

    out_arg = pathlib.Path(args.out).resolve()
    work_dir, report_path = (
        (out_arg.parent / out_arg.stem, out_arg) if out_arg.suffix == ".json" else (out_arg, out_arg / "report.json")
    )
    manifest = pathlib.Path(args.engine_manifest).resolve()
    engine = pathlib.Path(json.loads(manifest.read_text())["executable_path"])
    identity = source_identity(engine_bin=engine, engine_manifest=manifest)
    if not identity["engine_provenance"]["provenance_verified"]:
        raise RuntimeError("engine manifest provenance_verified is false")

    work_dir.mkdir(parents=True, exist_ok=False)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    app = make_app_factory(engine)(work_dir / "app")
    report = {"suite": "q09-q08-authority", "identity": identity, "provenance_verified": True, "model_calls": 0, "cases": {}, "passed": False}

    def start_profile_session(project_dir, wf_id, profile):
        author_sid, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement"})
        req = urllib.request.Request(
            app.url + f"/v1/sessions/{author_sid}/workflows/compose",
            method="POST",
            data=workflow_yaml(wf_id, profile).encode("utf-8"),
            headers={"Content-Type": "application/yaml", "Authorization": "Bearer " + app.token},
        )
        with open_request(req) as resp:
            compose = json.load(resp)
        publish = app.request(
            f"/v1/sessions/{author_sid}/workflows/{wf_id}/persist",
            {"version": "1.0.0", "confirm": True, "trigger": "/" + wf_id},
        )
        sid = app.request("/v1/sessions", {"project_id": project_id, "posture": "build"})["id"]
        app.request("/harness/model-limit", {"session_id": sid, "limit": 20})
        app.wait_for(lambda: app.request(f"/v1/sessions/{sid}")["status"] == "idle", f"{wf_id} ready", sid)
        run = app.request(f"/v1/sessions/{sid}/workflow-runs", {
            "operation_id": str(uuid.uuid4()), "workflow_id": wf_id, "workflow_version": "1.0.0",
            "request": f"Exercise {wf_id} under {profile}.",
        })
        pending = app.wait_for(lambda: smoke_workers.pending(app, sid), f"{wf_id} initial turn", sid)
        offered = sorted(set(pending.get("tools") or ()))
        state = app.request(f"/v1/sessions/{sid}")
        return {
            "session_id": sid, "posture": state.get("posture"), "workflow_run_id": run.get("id"),
            "offered_tools": offered,
            "mutation_tools_absent": "command" in offered and all(t not in offered for t in FORBIDDEN_MUTATION_TOOLS),
            "compose": compose, "publish": publish,
        }

    def run_cmd(sid, offered, cmd):
        [(call_id, result)] = send_tool_calls(
            app, sid, [{"name": "command", "args": {"command": cmd}}], surface={"offered_tools": list(offered)}
        )
        _, payload = parse_tool_result({"tool_result_json": json.dumps(result)})
        code, details = structured_rejection(result, payload)
        refusal = (payload or {}).get("refusal") or {}
        merged = dict(refusal.get("details") or {})
        if isinstance(details, dict):
            merged.update(details)
        pid = (payload or {}).get("program_id")
        receipt = app.request(f"/v1/sessions/{sid}/program-receipts/{pid}", missing=True) if pid else None
        return {
            "command": cmd, "call_id": call_id, "outcome": result.get("outcome"),
            "code": code or refusal.get("code"), "feedback": refusal.get("feedback") or "",
            "details": merged, "payload": payload, "program_receipt": receipt,
        }

    try:
        app.ready()

        # Session 1: worker_readonly (Q09 sed-write + Q09 awk-exec)
        q09_dir = work_dir / "fixture-q09"
        q09_dir.mkdir()
        q09_input = q09_dir / "input.txt"
        q09_input.write_bytes(b"keep\n")
        q09_sha = sha256_bytes(q09_input.read_bytes())
        s_q09 = start_profile_session(q09_dir, "q09-worker-readonly", "worker_readonly")

        sed_res = run_cmd(s_q09["session_id"], s_q09["offered_tools"], "sed 'w ./forbidden' ./input.txt")
        sed_sha = sha256_bytes(q09_input.read_bytes()) if q09_input.exists() else None
        sed_ok = bool(
            s_q09["posture"] == "build" and s_q09["mutation_tools_absent"]
            and sed_res["code"] == "COMMAND_EFFECT_NOT_ALLOWED"
            and has_source_span(sed_res["details"]) and has_no_mutating_remedy(sed_res["feedback"])
            and not (q09_dir / "forbidden").exists() and sed_sha == q09_sha
            and sed_res["program_receipt"] is not None
        )
        report["cases"]["embedded-sed-write-refusal"] = {
            **sed_res, "profile": "worker_readonly", "posture": s_q09["posture"],
            "offered_tools": s_q09["offered_tools"], "forbidden_absent": not (q09_dir / "forbidden").exists(),
            "input_sha256_before": q09_sha, "input_sha256_after": sed_sha, "passed": sed_ok,
        }

        awk_res = run_cmd(s_q09["session_id"], s_q09["offered_tools"], """awk 'BEGIN { system("fixture-tool") }'""")
        awk_zero_ext = has_zero_external_launches(awk_res["payload"], awk_res["program_receipt"])
        awk_ok = bool(
            awk_res["code"] == "COMMAND_NATIVE_FORM_UNSUPPORTED"
            and awk_res["details"].get("expected") == "no system()"
            and has_source_span(awk_res["details"]) and awk_zero_ext
            and awk_res["program_receipt"] is not None
        )
        report["cases"]["embedded-awk-exec-refusal"] = {
            **awk_res, "profile": "worker_readonly", "posture": s_q09["posture"],
            "zero_external_launches": awk_zero_ext, "passed": awk_ok,
        }

        # Session 2: plan_review_readonly (Q08/Q20 oar-unavailable-remedy)
        q20_dir = work_dir / "fixture-q20"
        q20_dir.mkdir()
        q20_input = q20_dir / "input.txt"
        q20_input.write_bytes(b"a\n")
        q20_sha = sha256_bytes(q20_input.read_bytes())
        s_q20 = start_profile_session(q20_dir, "q20-plan-review-readonly", "plan_review_readonly")

        oar_res = run_cmd(s_q20["session_id"], s_q20["offered_tools"], "sed -i 's/a/b/' ./input.txt")
        oar_sha = sha256_bytes(q20_input.read_bytes()) if q20_input.exists() else None
        oar_ok = bool(
            s_q20["posture"] == "build" and s_q20["mutation_tools_absent"]
            and oar_res["code"] == "COMMAND_EFFECT_NOT_ALLOWED"
            and "plan_review_readonly" in oar_res["feedback"]
            and has_no_mutating_remedy(oar_res["feedback"])
            and oar_sha == q20_sha and oar_res["program_receipt"] is not None
        )
        report["cases"]["oar-unavailable-remedy"] = {
            **oar_res, "profile": "plan_review_readonly", "posture": s_q20["posture"],
            "offered_tools": s_q20["offered_tools"], "names_profile": "plan_review_readonly" in oar_res["feedback"],
            "input_sha256_before": q20_sha, "input_sha256_after": oar_sha, "passed": oar_ok,
        }

        report["passed"] = sed_ok and awk_ok and oar_ok
    except urllib.error.HTTPError as error:
        report["error"] = {"http_status": error.code, "body": error.read().decode(errors="replace")}
        report["passed"] = False
    except Exception as error:
        report["error"] = f"{type(error).__name__}: {error}"
        report["passed"] = False
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n")
        app.close()

    sys.exit(0 if report["passed"] else 1)


if __name__ == "__main__":
    main()
