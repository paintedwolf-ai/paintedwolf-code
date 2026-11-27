"""Model-free replay witness for M05 stale-source conflict/recovery and detached-root refusal."""
import argparse
import hashlib
import json
import pathlib
import sys
import urllib.error
import urllib.request
import uuid


def parse_args():
    p = argparse.ArgumentParser(
        description="Run M05 stale-source conflict/recovery and detached-root refusal replay witness."
    )
    p.add_argument("--engine-manifest", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--repo-root", default=None)
    return p.parse_args()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def resolve_root_id(attached, target_path):
    want = pathlib.Path(target_path).resolve()
    for r in attached.get("roots") or ():
        if isinstance(r, dict) and r.get("path") and pathlib.Path(r["path"]).resolve() == want:
            return r.get("id") or ""
    roots = attached.get("roots") or ()
    return roots[0].get("id") or "" if roots and isinstance(roots[0], dict) else ""


def main():
    args = parse_args()
    if args.repo_root:
        root = pathlib.Path(args.repo_root).resolve()
        for rel in ("scripts", "scripts/coordinator-benchmark"):
            cand = str(root / rel)
            if cand not in sys.path:
                sys.path.insert(0, cand)

    from bootstrap_policy import open_request
    from portable_command.harness import send_tool_calls
    from portable_command.observe import parse_tool_result
    from portable_command.provenance import make_app_factory, source_identity
    from portable_command.reporting import structured_rejection

    out_arg = pathlib.Path(args.out).resolve()
    work_dir, report_path = (
        (out_arg.parent / out_arg.stem, out_arg)
        if out_arg.suffix == ".json"
        else (out_arg, out_arg / "report.json")
    )
    manifest = pathlib.Path(args.engine_manifest).resolve()
    engine = pathlib.Path(json.loads(manifest.read_text())["executable_path"])
    identity = source_identity(engine_bin=engine, engine_manifest=manifest)
    if not identity["engine_provenance"]["provenance_verified"]:
        raise RuntimeError("engine manifest provenance_verified is false")

    work_dir.mkdir(parents=True, exist_ok=False)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    app = make_app_factory(engine)(work_dir / "app")
    report = {
        "suite": "m05-stale-source-and-detach",
        "identity": identity,
        "provenance_verified": True,
        "model_calls": 0,
        "cases": {},
        "passed": False,
    }

    def http_delete(path):
        req = urllib.request.Request(
            app.url + path,
            method="DELETE",
            headers={"Authorization": "Bearer " + app.token},
        )
        with open_request(req) as resp:
            raw = resp.read().decode("utf-8", errors="replace").strip()
            return {"http_status": resp.status, "body": json.loads(raw) if raw else None}

    def start_session(project_id, workspace_root_id, prompt_text):
        body = {"project_id": project_id, "posture": "build"}
        if workspace_root_id:
            body["workspace_root_id"] = workspace_root_id
        sid = app.request("/v1/sessions", body)["id"]
        app.request("/harness/model-limit", {"session_id": sid, "limit": 24})
        app.wait_for(
            lambda: app.request(f"/v1/sessions/{sid}")["status"] == "idle",
            "session ready",
            sid,
        )
        app.request(
            f"/v1/sessions/{sid}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": prompt_text},
        )
        return sid

    def run_cmd(sid, cmd_args, receipts_out):
        [(call_id, result)] = send_tool_calls(
            app, sid, [{"name": "command", "args": cmd_args}], surface={}
        )
        _, payload = parse_tool_result({"tool_result_json": json.dumps(result)})
        code, details = structured_rejection(result, payload)
        refusal = (payload or {}).get("refusal") or {}
        pid = (payload or {}).get("program_id")
        if pid and pid not in receipts_out:
            receipts_out[pid] = app.request(
                f"/v1/sessions/{sid}/program-receipts/{pid}?limit=50", missing=True
            )
        return {
            "args": cmd_args,
            "call_id": call_id,
            "outcome": result.get("outcome"),
            "code": code or refusal.get("code"),
            "feedback": refusal.get("feedback") or "",
            "details": details,
            "payload": payload,
        }

    try:
        app.ready()

        # 1. Stale-source conflict and clean recovery
        stale_dir = (work_dir / "stale_root").resolve()
        stale_dir.mkdir()
        target = stale_dir / "target.txt"
        target.write_bytes(b"alpha\n")

        proj1 = app.request("/v1/projects", {"roots": [{"path": str(stale_dir)}]})
        r1_id = resolve_root_id(proj1, stale_dir)
        s1 = start_session(proj1["id"], r1_id, "Exercise M05 stale-source conflict and recovery.")
        send_tool_calls(
            app, s1, [{"name": "update_progress", "args": {"content": "- [ ] Update target.txt safely"}}], surface={}
        )
        s1_receipts = {}
        read1 = run_cmd(s1, {"command": "cat ./target.txt"}, s1_receipts)

        # Mutate target.txt externally on disk so revision advances before edit.
        target.write_bytes(b"external-change\n")
        sha_external = sha256_bytes(target.read_bytes())

        stale_edit = run_cmd(
            s1,
            {"command": "edit --path target.txt --old-string 'alpha' --new-string 'beta'"},
            s1_receipts,
        )
        sha_after_conflict = sha256_bytes(target.read_bytes())

        # Re-read fresh bytes and re-apply cleanly.
        read2 = run_cmd(s1, {"command": "cat ./target.txt"}, s1_receipts)
        rebased_edit = run_cmd(
            s1,
            {"command": "edit --path target.txt --old-string 'external-change' --new-string 'rebased-change'"},
            s1_receipts,
        )
        sha_rebased = sha256_bytes(target.read_bytes())
        s1_invocations = app.request(f"/v1/sessions/{s1}/invocations")

        stale_ok = bool(
            (read1["payload"] or {}).get("stdout") == "alpha\n"
            and stale_edit["code"] in {"EDIT_TARGET_CHANGED_BY_OTHERS", "EDIT_OLD_STRING_NOT_FOUND"}
            and sha_after_conflict == sha_external
            and (read2["payload"] or {}).get("stdout") == "external-change\n"
            and (rebased_edit["payload"] or {}).get("state") == "completed"
            and (rebased_edit["payload"] or {}).get("status") == 0
            and target.read_bytes() == b"rebased-change\n"
            and bool(s1_receipts)
        )
        report["cases"]["stale-source-conflict-and-recovery"] = {
            "session_id": s1,
            "read_initial": read1,
            "stale_edit": stale_edit,
            "read_fresh": read2,
            "rebased_edit": rebased_edit,
            "external_sha256": sha_external,
            "after_conflict_sha256": sha_after_conflict,
            "rebased_sha256": sha_rebased,
            "invocations": s1_invocations,
            "program_receipts": s1_receipts,
            "passed": stale_ok,
        }

        app.request(f"/v1/sessions/{s1}/abort", {})
        app.wait_for(
            lambda: app.request("/harness/llm/pending").get("session_id") != s1,
            "stale-source session abort settle",
        )

        # 2. Detached root refusal
        root_a = (work_dir / "root_a").resolve()
        root_b = (work_dir / "root_b").resolve()
        root_a.mkdir()
        root_b.mkdir()
        file_a = root_a / "a.txt"
        file_b = root_b / "b.txt"
        file_a.write_bytes(b"root-a\n")
        file_b.write_bytes(b"root-b-baseline\n")
        sha_b_before = sha256_bytes(file_b.read_bytes())

        proj2 = app.request(
            "/v1/projects", {"roots": [{"path": str(root_a)}, {"path": str(root_b)}]}
        )
        root_a_id = resolve_root_id(proj2, root_a)
        root_b_id = resolve_root_id(proj2, root_b)
        if not root_b_id or root_b_id == root_a_id:
            raise RuntimeError(f"distinct root ids required: a={root_a_id!r} b={root_b_id!r}")

        s2 = start_session(proj2["id"], root_a_id, "Exercise M05 detached-root refusal.")
        send_tool_calls(
            app, s2, [{"name": "update_progress", "args": {"content": "- [ ] Verify detached root refusal"}}], surface={}
        )
        s2_receipts = {}
        detach_resp = http_delete(f"/v1/projects/{proj2['id']}/roots/{root_b_id}")

        detached_attempt = run_cmd(
            s2,
            {"command": "printf 'mutated\\n' > ./b.txt", "cwd": str(root_b)},
            s2_receipts,
        )
        sha_b_after = sha256_bytes(file_b.read_bytes())
        attached_write = run_cmd(
            s2, {"command": "printf 'still-attached\\n' > ./a.txt"}, s2_receipts
        )
        s2_invocations = app.request(f"/v1/sessions/{s2}/invocations")

        detach_ok = bool(
            detach_resp["http_status"] in {200, 204}
            and detached_attempt["code"] in {"CWD_OUT_OF_SCOPE", "ROOT_NOT_ATTACHED"}
            and file_b.read_bytes() == b"root-b-baseline\n"
            and sha_b_after == sha_b_before
            and (attached_write["payload"] or {}).get("state") == "completed"
            and (attached_write["payload"] or {}).get("status") == 0
            and file_a.read_bytes() == b"still-attached\n"
        )
        report["cases"]["detached-root-refusal"] = {
            "session_id": s2,
            "root_a_id": root_a_id,
            "root_b_id": root_b_id,
            "detach_response": detach_resp,
            "detached_attempt": detached_attempt,
            "attached_write": attached_write,
            "root_b_sha256_before": sha_b_before,
            "root_b_sha256_after": sha_b_after,
            "invocations": s2_invocations,
            "program_receipts": s2_receipts,
            "passed": detach_ok,
        }

        report["passed"] = stale_ok and detach_ok
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
