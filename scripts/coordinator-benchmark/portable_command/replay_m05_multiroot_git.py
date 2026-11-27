"""Model-free replay witness for M05 multi-root isolation and linked Git worktree."""
import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import urllib.error
import uuid


def parse_args():
    p = argparse.ArgumentParser(description="Run M05 multi-root and linked-worktree Git replay witness.")
    p.add_argument("--engine-manifest", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--repo-root", default=None)
    return p.parse_args()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def run_git(cwd, *args):
    env = {
        **os.environ,
        "GIT_CONFIG_GLOBAL": "/dev/null",
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_AUTHOR_NAME": "Fixture Author",
        "GIT_AUTHOR_EMAIL": "fixture@example.test",
        "GIT_COMMITTER_NAME": "Fixture Author",
        "GIT_COMMITTER_EMAIL": "fixture@example.test",
    }
    res = subprocess.run(
        ["git", *args], cwd=cwd, env=env, check=True, capture_output=True, text=True
    )
    return res.stdout


def main():
    args = parse_args()
    if args.repo_root:
        root = pathlib.Path(args.repo_root).resolve()
        for rel in ("scripts", "scripts/coordinator-benchmark"):
            cand = str(root / rel)
            if cand not in sys.path:
                sys.path.insert(0, cand)

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
        "suite": "m05-multiroot-git",
        "identity": identity,
        "provenance_verified": True,
        "model_calls": 0,
        "cases": {},
        "passed": False,
    }

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

    def run_cmd(sid, cmd, receipts_out):
        [(call_id, result)] = send_tool_calls(
            app, sid, [{"name": "command", "args": {"command": cmd}}], surface={}
        )
        _, payload = parse_tool_result({"tool_result_json": json.dumps(result)})
        code, details = structured_rejection(result, payload)
        pid = (payload or {}).get("program_id")
        if pid and pid not in receipts_out:
            receipts_out[pid] = app.request(
                f"/v1/sessions/{sid}/program-receipts/{pid}?limit=50", missing=True
            )
        return {
            "command": cmd,
            "call_id": call_id,
            "outcome": result.get("outcome"),
            "rejection_code": code,
            "rejection_details": details,
            "payload": payload,
        }

    def resolve_root_id(attached, target_path):
        want = pathlib.Path(target_path).resolve()
        for r in attached.get("roots") or ():
            if isinstance(r, dict) and r.get("path") and pathlib.Path(r["path"]).resolve() == want:
                return r.get("id") or ""
        roots = attached.get("roots") or ()
        return roots[0].get("id") or "" if roots and isinstance(roots[0], dict) else ""

    try:
        app.ready()

        # 1. Multi-root isolation (root_a + root_b)
        root_a = (work_dir / "root_a").resolve()
        root_b = (work_dir / "root_b").resolve()
        root_a.mkdir()
        root_b.mkdir()
        shared_a = root_a / "shared.txt"
        shared_b = root_b / "shared.txt"
        shared_a.write_bytes(b"root-a-before\n")
        shared_b.write_bytes(b"root-b-before\n")
        sha_b_before = sha256_bytes(shared_b.read_bytes())

        proj_mr = app.request(
            "/v1/projects", {"roots": [{"path": str(root_a)}, {"path": str(root_b)}]}
        )
        root_a_id = resolve_root_id(proj_mr, root_a)
        s1 = start_session(proj_mr["id"], root_a_id, "Exercise M05 multi-root isolation on root_a.")
        send_tool_calls(
            app, s1, [{"name": "update_progress", "args": {"content": "- [ ] Update shared.txt in root_a"}}], surface={}
        )
        s1_receipts = {}
        read_a = run_cmd(s1, "cat ./shared.txt", s1_receipts)
        write_a = run_cmd(s1, "printf 'root-a-after\\n' > ./shared.txt", s1_receipts)
        s1_invocations = app.request(f"/v1/sessions/{s1}/invocations")

        sha_a_after = sha256_bytes(shared_a.read_bytes())
        sha_b_after = sha256_bytes(shared_b.read_bytes())
        mr_ok = bool(
            (read_a["payload"] or {}).get("state") == "completed"
            and (read_a["payload"] or {}).get("status") == 0
            and (read_a["payload"] or {}).get("stdout") == "root-a-before\n"
            and (write_a["payload"] or {}).get("state") == "completed"
            and (write_a["payload"] or {}).get("status") == 0
            and shared_a.read_bytes() == b"root-a-after\n"
            and shared_b.read_bytes() == b"root-b-before\n"
            and sha_b_after == sha_b_before
            and bool(s1_receipts)
        )
        report["cases"]["multi-root-isolation"] = {
            "session_id": s1,
            "root_a_id": root_a_id,
            "read": read_a,
            "write": write_a,
            "root_a_sha256_after": sha_a_after,
            "root_b_sha256_before": sha_b_before,
            "root_b_sha256_after": sha_b_after,
            "invocations": s1_invocations,
            "program_receipts": s1_receipts,
            "passed": mr_ok,
        }

        app.request(f"/v1/sessions/{s1}/abort", {})
        app.wait_for(
            lambda: app.request("/harness/llm/pending").get("session_id") != s1,
            "multi-root session abort settle",
        )

        # 2. Linked Git worktree (git worktree add) read & write
        main_repo = (work_dir / "main_repo").resolve()
        linked_wt = (work_dir / "linked_wt").resolve()
        main_repo.mkdir()
        run_git(main_repo, "init", "-q", "-b", "main")
        run_git(main_repo, "config", "user.name", "Fixture Author")
        run_git(main_repo, "config", "user.email", "fixture@example.test")
        run_git(main_repo, "config", "commit.gpgsign", "false")
        (main_repo / "base.txt").write_bytes(b"base\n")
        run_git(main_repo, "add", "base.txt")
        run_git(main_repo, "commit", "-q", "-m", "initial commit")
        main_head_before = run_git(main_repo, "rev-parse", "HEAD").strip()

        run_git(main_repo, "worktree", "add", "-q", str(linked_wt), "-b", "wt-branch")
        wt_head_before = run_git(linked_wt, "rev-parse", "HEAD").strip()

        proj_wt = app.request(
            "/v1/projects",
            {"roots": [{"path": str(main_repo)}, {"path": str(linked_wt)}]},
        )
        main_root_id = resolve_root_id(proj_wt, main_repo)
        wt_root_id = resolve_root_id(proj_wt, linked_wt)
        if not wt_root_id or wt_root_id == main_root_id:
            raise RuntimeError(f"linked worktree root id did not resolve distinctly: main={main_root_id!r} wt={wt_root_id!r}")
        s2 = start_session(proj_wt["id"], wt_root_id, "Exercise M05 linked Git worktree read and commit.")
        send_tool_calls(
            app, s2, [{"name": "update_progress", "args": {"content": "- [ ] Commit wt.txt in linked worktree"}}], surface={}
        )
        s2_receipts = {}
        git_status = run_cmd(s2, "git status --porcelain", s2_receipts)
        git_commit = run_cmd(
            s2, "printf 'wt-change\\n' > ./wt.txt && git add ./wt.txt && git commit -m 'wt commit'", s2_receipts
        )
        s2_invocations = app.request(f"/v1/sessions/{s2}/invocations")

        wt_head_after = run_git(linked_wt, "rev-parse", "HEAD").strip()
        wt_branch = run_git(linked_wt, "rev-parse", "--abbrev-ref", "HEAD").strip()
        wt_committed_content = run_git(linked_wt, "show", "HEAD:wt.txt")
        main_head_after = run_git(main_repo, "rev-parse", "HEAD").strip()
        main_status_after = run_git(main_repo, "status", "--porcelain")

        wt_ok = bool(
            (git_status["payload"] or {}).get("state") == "completed"
            and (git_status["payload"] or {}).get("status") == 0
            and (git_status["payload"] or {}).get("stdout") == ""
            and (git_commit["payload"] or {}).get("state") == "completed"
            and (git_commit["payload"] or {}).get("status") == 0
            and wt_head_after != wt_head_before
            and wt_branch == "wt-branch"
            and wt_committed_content == "wt-change\n"
            and main_head_after == main_head_before
            and main_status_after == ""
            and not (main_repo / "wt.txt").exists()
            and bool(s2_receipts)
        )
        report["cases"]["linked-git-worktree"] = {
            "session_id": s2,
            "main_root_id": main_root_id,
            "wt_root_id": wt_root_id,
            "git_status": git_status,
            "git_commit": git_commit,
            "wt_branch": wt_branch,
            "wt_head_before": wt_head_before,
            "wt_head_after": wt_head_after,
            "main_head_before": main_head_before,
            "main_head_after": main_head_after,
            "main_status_after": main_status_after,
            "main_wt_file_absent": not (main_repo / "wt.txt").exists(),
            "invocations": s2_invocations,
            "program_receipts": s2_receipts,
            "passed": wt_ok,
        }

        report["passed"] = mr_ok and wt_ok
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
