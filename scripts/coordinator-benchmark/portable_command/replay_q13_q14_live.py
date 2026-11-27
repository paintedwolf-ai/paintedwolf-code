"""Model-free live-app replay witness for Q13 (stale-approval) and Q14 (publication-barrier)."""
import argparse
import hashlib
import json
import os
import pathlib
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid


def parse_args():
    p = argparse.ArgumentParser(description="Run Q13 stale-approval and Q14 publication-barrier live replay.")
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
        "suite": "bb04-q13-q14-live",
        "identity": identity,
        "provenance_verified": True,
        "model_calls": 0,
        "cases": {},
        "passed": False,
    }

    def http_json(method, path, payload=None):
        data = json.dumps(payload).encode("utf-8") if payload is not None else None
        headers = {"Authorization": "Bearer " + app.token}
        if data is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(app.url + path, data=data, method=method, headers=headers)
        try:
            with open_request(req) as resp:
                raw = resp.read().decode("utf-8", errors="replace").strip()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"HTTP {exc.code} {method} {path}: {body}") from exc

    def open_doc(pid, root_id, rel_path, client_id="replay-client"):
        return http_json("POST", f"/v1/projects/{pid}/editor-documents", {
            "root_id": root_id, "path": rel_path, "client_id": client_id,
        })

    def replace_doc(pid, doc_id, expected_rev, content, client_id="replay-client"):
        return http_json("PUT", f"/v1/projects/{pid}/editor-documents/{doc_id}", {
            "history_vector": None, "session_id": None, "operation_id": str(uuid.uuid4()),
            "client_id": client_id, "expected_revision": expected_rev,
            "content": content, "eol": "lf", "mixed_eol": False,
        })

    def start_session(project_id, workspace_root_id, prompt_text):
        body = {"project_id": project_id, "posture": "build"}
        if workspace_root_id:
            body["workspace_root_id"] = workspace_root_id
        sid = app.request("/v1/sessions", body)["id"]
        app.request("/harness/model-limit", {"session_id": sid, "limit": 24})
        app.wait_for(lambda: app.request(f"/v1/sessions/{sid}")["status"] == "idle", "session ready", sid)
        app.request(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": prompt_text})
        send_tool_calls(
            app, sid, [{"name": "update_progress", "args": {"content": "- [ ] Execute Q13/Q14 check"}}], surface={}
        )
        return sid

    def abort_session(sid):
        try:
            app.request(f"/v1/sessions/{sid}/abort", {})
        except Exception:
            pass
        app.wait_for(
            lambda: len(app.request("/harness/llm/pending").get("requests") or ()) == 0,
            "drain pending llm requests after abort",
            sid,
        )

    def pick_approve_option(cp):
        ta = cp.get("tool_approval") or {}
        plan = (ta.get("plan") if isinstance(ta, dict) else None) or cp.get("approval_plan") or {}
        if isinstance(plan, dict):
            for o in (plan.get("options") or ()):
                if isinstance(o, dict) and not o.get("disabled") and o.get("decision_action") == "approve" and o.get("id"):
                    return o["id"]
            if plan.get("recommended_option_id"):
                return plan["recommended_option_id"]
        return "allow_once"

    try:
        app.ready()

        # Case 1: stale-approval (Q13)
        # AGENTS.md triggers real FileChangeReview approval gate via AgentPolicyClassifier.
        ws1 = (work_dir / "ws_q13").resolve()
        ws1.mkdir()
        target_q13 = ws1 / "AGENTS.md"
        target_q13.write_text("alpha\n", encoding="utf-8")
        proj1 = app.request("/v1/projects", {"roots": [{"path": str(ws1)}]})
        pid1 = proj1["id"]
        rid1 = resolve_root_id(proj1, ws1)
        doc1 = open_doc(pid1, rid1, "AGENTS.md")
        sid1 = start_session(pid1, rid1, "Exercise Q13 stale-approval.")

        call_res = {}

        def run_sed():
            try:
                [(cid, res)] = send_tool_calls(
                    app, sid1, [{"name": "command", "args": {"command": "sed -i 's/alpha/beta/' ./AGENTS.md"}}], surface={}
                )
                call_res["call_id"], call_res["result"] = cid, res
            except Exception as exc:
                call_res["error"] = repr(exc)

        t = threading.Thread(target=run_sed, daemon=True)
        t.start()

        pending_cp = {}
        deadline = time.time() + 20.0
        while time.time() < deadline:
            cps = http_json("GET", f"/v1/sessions/{sid1}/checkpoints") or {}
            items = cps.get("checkpoints") if isinstance(cps, dict) else cps
            for cp in (items or ()):
                if isinstance(cp, dict) and cp.get("status") in ("pending", "open", None) and not cp.get("resolved_at"):
                    pending_cp = cp
                    break
            if pending_cp or not t.is_alive():
                break
            time.sleep(0.15)

        human_doc = None
        if pending_cp:
            human_doc = replace_doc(pid1, doc1["id"], doc1["revision"], "human\n")
            cp_id = pending_cp.get("checkpoint_id") or pending_cp.get("id")
            cp_kind = pending_cp.get("kind") or "tool_approval"
            body = (
                {"kind": "content_apply", "decision": "approve"}
                if cp_kind == "content_apply"
                else {"kind": cp_kind, "action": "approve", "option_id": pick_approve_option(pending_cp)}
            )
            http_json("POST", f"/v1/sessions/{sid1}/checkpoints/{cp_id}", body)

        t.join(timeout=25.0)
        res1 = call_res.get("result") or {}
        _, payload1 = parse_tool_result({"tool_result_json": json.dumps(res1)}) if res1 else (None, {})
        code1, details1 = structured_rejection(res1, payload1) if res1 else (None, None)
        refusal1 = (payload1 or {}).get("refusal") or {}
        stop_code1 = code1 or refusal1.get("code") or (payload1 or {}).get("stop_code") or ""
        after_doc1 = open_doc(pid1, rid1, "AGENTS.md", client_id="verify-client")
        disk1 = target_q13.read_text(encoding="utf-8")
        q13_passed = (
            bool(pending_cp)
            and call_res.get("error") is None
            and stop_code1 == "COMMAND_REVISION_CONFLICT"
            and human_doc is not None
            and after_doc1.get("revision") == human_doc.get("revision")
            and disk1 == "alpha\n"
        )
        report["cases"]["stale-approval"] = {
            "passed": q13_passed,
            "checkpoint_id": pending_cp.get("id") if pending_cp else None,
            "stop_code": stop_code1,
            "details": details1,
            "payload": payload1,
            "human_revision": human_doc.get("revision") if human_doc else None,
            "after_revision": after_doc1.get("revision"),
            "disk_after": disk1,
            "thread_error": call_res.get("error"),
        }
        abort_session(sid1)

        # Case 2: publication-barrier (Q14)
        ws2 = (work_dir / "ws_q14").resolve()
        sub = ws2 / "dir"
        sub.mkdir(parents=True)
        inp = sub / "input.txt"
        inp.write_text("old\n", encoding="utf-8")
        unrel = ws2 / "unrelated.txt"
        unrel.write_text("disk\n", encoding="utf-8")
        marker = ws2 / "marker.txt"

        proj2 = app.request("/v1/projects", {"roots": [{"path": str(ws2)}]})
        pid2 = proj2["id"]
        rid2 = resolve_root_id(proj2, ws2)
        doc_in = open_doc(pid2, rid2, "dir/input.txt")
        doc_unrel = open_doc(pid2, rid2, "unrelated.txt")
        doc_unrel_draft = replace_doc(pid2, doc_unrel["id"], doc_unrel["revision"], "human draft\n")

        os.chmod(sub, 0o555)
        try:
            sid2 = start_session(pid2, rid2, "Exercise Q14 publication-barrier.")
            [(cid2, res2)] = send_tool_calls(
                app, sid2,
                [{"name": "command", "args": {"command": "printf 'next\\n' > ./dir/input.txt && printf 'RAN\\n' > ./marker.txt"}}],
                surface={},
            )
            _, payload2 = parse_tool_result({"tool_result_json": json.dumps(res2)})
            code2, details2 = structured_rejection(res2, payload2)
            after_in = open_doc(pid2, rid2, "dir/input.txt", client_id="verify-client")
            after_unrel = open_doc(pid2, rid2, "unrelated.txt", client_id="verify-client")
            disk_in = inp.read_text(encoding="utf-8")
            disk_unrel = unrel.read_text(encoding="utf-8")
            marker_absent = not marker.exists()
            abort_session(sid2)
        finally:
            os.chmod(sub, 0o755)

        q14_passed = (
            disk_in == "old\n"
            and bool(after_in.get("dirty"))
            and bool(after_in.get("held_agent_version_id"))
            and (after_in.get("revision") or 0) > (doc_in.get("revision") or 0)
            and marker_absent
            and disk_unrel == "disk\n"
            and bool(after_unrel.get("dirty"))
            and after_unrel.get("revision") == doc_unrel_draft.get("revision")
        )
        report["cases"]["publication-barrier"] = {
            "passed": q14_passed,
            "code": code2,
            "details": details2,
            "payload": payload2,
            "input_disk_after": disk_in,
            "input_doc_before_rev": doc_in.get("revision"),
            "input_doc_after_rev": after_in.get("revision"),
            "input_doc_dirty": after_in.get("dirty"),
            "input_held_agent_version_id": after_in.get("held_agent_version_id"),
            "marker_absent": marker_absent,
            "unrelated_disk_after": disk_unrel,
            "unrelated_doc_dirty": after_unrel.get("dirty"),
            "unrelated_doc_rev_preserved": after_unrel.get("revision") == doc_unrel_draft.get("revision"),
        }

        report["passed"] = bool(q13_passed and q14_passed)
    finally:
        report_path.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        app.close()

    if not report["passed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
