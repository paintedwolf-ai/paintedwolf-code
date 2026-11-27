"""Model-free replay witness for Q07/Q08 under an authoritative worker_readonly workflow.

Persists a disposable workflow declaring `coordinator_profile: worker_readonly`
in an author session, then starts a second execution session under `posture: build`
bound to that workflow so read-only enforcement is proven to come from the
`worker_readonly` profile itself rather than `vet` posture.
"""
import argparse
import hashlib
import json
import pathlib
import sys
import urllib.error
import urllib.request
import uuid


WORKFLOW_ID = "q07-worker-readonly"
WORKFLOW_VERSION = "1.0.0"
WORKFLOW_YAML = """id: q07-worker-readonly
version: 1.0.0
extends: plan@1.0.0
surface_profile: implement
coordinator_profile: worker_readonly
name: Q07 worker_readonly authority witness
initial_posture: build
agents:
  - {id: coordinator, tools: profile, spawn: false}
phases:
  - id: research
    activity_label: Inspect fixture under worker_readonly
    coordinator_surface: implement_investigate
    surface_template: agents/coordinator-surface-build.md
    complete_when: gates_satisfied
    gates: [evidence_passed:verify]
    advance:
      when_gate_met: coordinator
    next: build
  - id: build
    activity_label: Complete worker_readonly witness
    complete_when: gates_satisfied
    gates: [delegation_closeout_complete, evidence_passed:verify]
    advance:
      when_gate_met: coordinator
"""

FORBIDDEN_MUTATION_TOOLS = (
    "write",
    "edit",
    "delete",
    "replace_lines",
    "code_rewrite",
    "jq_edit",
    "chmod",
    "chown",
)

BASELINE_BYTES = b"readonly-baseline\n"
SELECTOR_BYTES = b"rm\n"


def parse_args():
    parser = argparse.ArgumentParser(
        description="Run Q07/Q08 model-free worker_readonly replay witness."
    )
    parser.add_argument(
        "--engine-manifest",
        required=True,
        help="Path to the verified engine manifest JSON.",
    )
    parser.add_argument(
        "--out",
        required=True,
        help="Output directory (writes report.json inside) or .json report path.",
    )
    parser.add_argument(
        "--repo-root",
        default=None,
        help="Optional repository root to prepend scripts/ and scripts/coordinator-benchmark/ to sys.path.",
    )
    return parser.parse_args()


def configure_paths(repo_root):
    if repo_root:
        root = pathlib.Path(repo_root).resolve()
        for rel in ("scripts", "scripts/coordinator-benchmark"):
            candidate = str(root / rel)
            if candidate not in sys.path:
                sys.path.insert(0, candidate)


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def main():
    args = parse_args()
    configure_paths(args.repo_root)

    import smoke_workers
    from bootstrap_policy import open_request
    from portable_command.harness import create_isolated_session, send_tool_calls
    from portable_command.observe import parse_tool_result
    from portable_command.provenance import make_app_factory, source_identity
    from portable_command.reporting import structured_rejection

    out_arg = pathlib.Path(args.out).resolve()
    if out_arg.suffix == ".json":
        report_path = out_arg
        work_dir = out_arg.parent / out_arg.stem
    else:
        work_dir = out_arg
        report_path = work_dir / "report.json"

    manifest = pathlib.Path(args.engine_manifest).resolve()
    engine = pathlib.Path(json.loads(manifest.read_text())["executable_path"])
    identity = source_identity(engine_bin=engine, engine_manifest=manifest)
    if not identity["engine_provenance"]["provenance_verified"]:
        raise RuntimeError("engine manifest provenance_verified is false")

    work_dir.mkdir(parents=True, exist_ok=False)
    report_path.parent.mkdir(parents=True, exist_ok=True)

    project = work_dir / "fixture"
    project.mkdir()
    input_file = project / "input.txt"
    selector_file = project / "selector"
    input_file.write_bytes(BASELINE_BYTES)
    selector_file.write_bytes(SELECTOR_BYTES)
    sha_before = sha256_bytes(input_file.read_bytes())

    app = make_app_factory(engine)(work_dir / "app")
    report = {
        "case": "q07-q08-worker-readonly",
        "requested_profile": "worker_readonly",
        "identity": identity,
        "provenance_verified": identity["engine_provenance"]["provenance_verified"],
        "model_calls": 0,
        "input_sha256_before": sha_before,
        "input_sha256_after": None,
        "input_preserved": False,
        "offered_tools": [],
        "offered_tool_descriptors": None,
        "denied_mutation_tools_absent": False,
        "workflow_binding": {},
        "cases": {},
        "invocations": None,
        "program_receipts": {},
        "passed": False,
    }

    def invoke(session_id, surface, command_str):
        [(call_id, result)] = send_tool_calls(
            app,
            session_id,
            [{"name": "command", "args": {"command": command_str}}],
            surface=surface,
        )
        _, payload = parse_tool_result({"tool_result_json": json.dumps(result)})
        code, details = structured_rejection(result, payload)
        return {
            "command": command_str,
            "call_id": call_id,
            "outcome": result.get("outcome"),
            "payload": payload,
            "rejection_code": code,
            "rejection_details": details,
        }

    try:
        app.ready()
        author_session, project_id, _, _ = create_isolated_session(
            app, project, {"profile": "implement"}
        )

        # Persist disposable workflow declaring coordinator_profile: worker_readonly.
        compose_req = urllib.request.Request(
            app.url + f"/v1/sessions/{author_session}/workflows/compose",
            method="POST",
            data=WORKFLOW_YAML.encode("utf-8"),
            headers={
                "Content-Type": "application/yaml",
                "Authorization": "Bearer " + app.token,
            },
        )
        with open_request(compose_req) as resp:
            report["compose"] = json.load(resp)

        report["publish"] = app.request(
            f"/v1/sessions/{author_session}/workflows/{WORKFLOW_ID}/persist",
            {"version": WORKFLOW_VERSION, "confirm": True, "trigger": "/" + WORKFLOW_ID},
        )

        # Create second execution session under posture=build so read-only
        # enforcement is isolated to coordinator_profile: worker_readonly.
        session = app.request(
            "/v1/sessions", {"project_id": project_id, "posture": "build"}
        )
        session_id = session["id"]
        app.request("/harness/model-limit", {"session_id": session_id, "limit": 20})
        app.wait_for(
            lambda: app.request(f"/v1/sessions/{session_id}")["status"] == "idle",
            "worker_readonly session preparation",
            session_id,
        )

        run = app.request(
            f"/v1/sessions/{session_id}/workflow-runs",
            {
                "operation_id": str(uuid.uuid4()),
                "workflow_id": WORKFLOW_ID,
                "workflow_version": WORKFLOW_VERSION,
                "request": "Verify allowed read and typed mutation refusal under worker_readonly.",
            },
        )

        # Observe offered tools and descriptors on the first pending manual turn.
        pending = app.wait_for(
            lambda: smoke_workers.pending(app, session_id),
            "worker_readonly initial model turn",
            session_id,
        )
        offered_tools = sorted(set(pending.get("tools") or ()))
        report["offered_tools"] = offered_tools
        if "tool_definitions" in pending:
            report["offered_tool_descriptors"] = pending["tool_definitions"]
        elif "tool_descriptors" in pending:
            report["offered_tool_descriptors"] = pending["tool_descriptors"]
        else:
            report["offered_tool_descriptors"] = [
                {"name": name} for name in offered_tools
            ]

        session_state = app.request(f"/v1/sessions/{session_id}")
        active_run = app.request(
            f"/v1/sessions/{session_id}/workflow-runs/active", missing=True
        )
        report["workflow_binding"] = {
            "session_id": session_id,
            "session_posture": session_state.get("posture"),
            "session_mode": session_state.get("mode"),
            "session_agent_type": session_state.get("agent_type"),
            "workflow_run_id": run.get("id"),
            "workflow_id": WORKFLOW_ID,
            "workflow_version": WORKFLOW_VERSION,
            "declared_coordinator_profile": "worker_readonly",
            "active_workflow_run": active_run,
        }

        surface = {"offered_tools": offered_tools}

        # Case 1: Allowed read under worker_readonly.
        read_res = invoke(session_id, surface, "cat ./input.txt")
        report["cases"]["allowed_read"] = read_res

        # Case 2a (Q07): Direct mutation refusal (`rm ./input.txt`).
        rm_res = invoke(session_id, surface, "rm ./input.txt")
        report["cases"]["direct_mutation_rm"] = rm_res

        # Case 2b: Redirect mutation refusal (`printf 'mutated\n' > ./input.txt`).
        redir_res = invoke(session_id, surface, "printf 'mutated\\n' > ./input.txt")
        report["cases"]["redirect_mutation"] = redir_res

        # Case 2c (Q08): Dynamic command-name mutation refusal.
        dyn_res = invoke(session_id, surface, 'op=$(cat ./selector); "$op" ./input.txt')
        report["cases"]["dynamic_mutation_q08"] = dyn_res

        # Collect authoritative invocation and program receipts.
        report["invocations"] = app.request(f"/v1/sessions/{session_id}/invocations")
        program_ids = []
        for entry in report["cases"].values():
            pid = (entry.get("payload") or {}).get("program_id")
            if pid and pid not in program_ids:
                program_ids.append(pid)
        for pid in program_ids:
            report["program_receipts"][pid] = app.request(
                f"/v1/sessions/{session_id}/program-receipts/{pid}", missing=True
            )

        exists_after = input_file.exists()
        bytes_after = input_file.read_bytes() if exists_after else None
        sha_after = sha256_bytes(bytes_after) if bytes_after is not None else None
        report["input_sha256_after"] = sha_after
        report["input_preserved"] = (
            exists_after and bytes_after == BASELINE_BYTES and sha_after == sha_before
        )

        report["denied_mutation_tools_absent"] = (
            "command" in offered_tools
            and all(tool not in offered_tools for tool in FORBIDDEN_MUTATION_TOOLS)
        )

        def is_refusal(entry, expected_code):
            payload = entry.get("payload") or {}
            refusal_code = (payload.get("refusal") or {}).get("code")
            code = entry.get("rejection_code") or refusal_code
            return entry.get("outcome") == "rejected" and code == expected_code

        read_payload = read_res.get("payload") or {}
        read_ok = (
            read_res.get("outcome") == "completed"
            and read_payload.get("state") == "completed"
            and read_payload.get("status") == 0
            and read_payload.get("stdout") == BASELINE_BYTES.decode("utf-8")
            and read_payload.get("refusal") is None
        )

        report["passed"] = bool(
            report["provenance_verified"]
            and report["denied_mutation_tools_absent"]
            and report["workflow_binding"]["session_posture"] == "build"
            and read_ok
            and is_refusal(rm_res, "COMMAND_OPERATION_NOT_ALLOWED")
            and is_refusal(redir_res, "COMMAND_EFFECT_NOT_ALLOWED")
            and is_refusal(dyn_res, "COMMAND_OPERATION_NOT_ALLOWED")
            and report["input_preserved"]
        )
    except urllib.error.HTTPError as error:
        report["error"] = {
            "http_status": error.code,
            "body": error.read().decode(errors="replace"),
        }
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
