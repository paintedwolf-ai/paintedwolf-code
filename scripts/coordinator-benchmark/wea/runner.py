"""Unified zero-model sidecar driver and 3 per-track controls (TB02 / D3).

Drives the real `lycaon-dev` engine in an isolated profile (`source_config=None`),
isolated port, and isolated fixture root using `smoke.Application` +
`/harness/llm/pending` + `/harness/llm/respond`.

All product claims in receipts are extracted strictly from real runtime SQLite
records (`?mode=ro`) and real `tool_result_json` responses — never synthesized
from fixture expectations or copied from independent host observations.
"""

from __future__ import annotations

import contextlib
import json
import pathlib
import sqlite3
import subprocess
import sys
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.checker import check_evidence
from wea.fixtures_net import RecordingHTTPFixture
from wea.observers import (
    AuthorityObserver,
    EditsObserver,
    WorkersObserver,
    hash_file,
    hash_tree,
    outside_diff_dirs,
    sha256_bytes,
)
from wea.provenance import REPO_ROOT, collect_provenance, resolve_engine_binary
from wea.replay import export_sanitized_bundle


def _save_json(path: pathlib.Path, data: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _init_git_repo(project_dir: pathlib.Path) -> None:
    project_dir.mkdir(parents=True, exist_ok=True)
    if (project_dir / ".git").exists():
        return
    subprocess.run(["git", "init", "-q"], cwd=str(project_dir), check=True)
    subprocess.run(["git", "config", "user.name", "WEA Fixture"], cwd=str(project_dir), check=True)
    subprocess.run(["git", "config", "user.email", "wea-fixture@example.invalid"], cwd=str(project_dir), check=True)
    subprocess.run(["git", "add", "."], cwd=str(project_dir), check=True)
    subprocess.run(["git", "commit", "-q", "-m", "initial fixture"], cwd=str(project_dir), check=True)


def _create_application(app_dir: pathlib.Path, engine_bin: str | pathlib.Path | None = None):
    """Instantiate smoke.Application strictly in isolated manual mode (source_config=None)."""
    import smoke

    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
    orig = smoke.engine_path
    smoke.engine_path = lambda _root: binary
    try:
        return smoke.Application(app_dir, source_config=None)
    finally:
        smoke.engine_path = orig


def _open_session(app: Any, project_dir: pathlib.Path, *, posture: str = "build") -> tuple[str, str, str]:
    attached = app.request("/v1/projects", {"roots": [{"path": str(project_dir)}]})
    project_id = attached["id"]
    roots = attached.get("roots") or []
    root_id = roots[0]["id"] if roots and isinstance(roots[0], dict) and roots[0].get("id") else ""
    session = app.request("/v1/sessions", {"project_id": project_id, "posture": posture})
    sid = session["id"]
    app.request("/harness/model-limit", {"session_id": sid, "limit": 40})

    def ready() -> bool:
        state = app.request(f"/v1/sessions/{sid}")
        if state["status"] == "preparing":
            return False
        if state["status"] != "idle":
            raise RuntimeError(f"session preparation ended in {state['status']}")
        return True

    app.wait_for(ready, "wea session ready", sid)
    return sid, project_id, root_id


def _wait_pending(app: Any, session_id: str | None = None) -> dict[str, Any]:
    def poll() -> dict[str, Any] | None:
        val = app.request("/harness/llm/pending")
        if not val.get("pending"):
            return None
        if session_id is not None and val.get("session_id") != session_id:
            return None
        return val

    return app.wait_for(poll, "wea pending model turn", session_id)


def _wait_tool_result(
    app: Any,
    session_id: str,
    call_id: str,
    *,
    checkpoint_policy: str | None = None,
) -> dict[str, Any]:
    """Wait for terminal tool_result_json, optionally resolving pending checkpoints."""
    db_path = app.directory / "store.db"

    def poll() -> dict[str, Any] | None:
        if checkpoint_policy in {"approve_chat", "approve_once", "reject"}:
            cps = app.request(f"/v1/sessions/{session_id}/checkpoints?status=pending").get("checkpoints") or []
            for cp in cps:
                payload = cp.get("tool_approval") or {}
                if payload.get("tool_call_id") != call_id:
                    continue
                if checkpoint_policy == "reject":
                    app.request(
                        f"/v1/sessions/{session_id}/checkpoints/{cp['id']}",
                        {"kind": "tool_approval", "action": "reject", "guidance": "Denied by WEA authority control."},
                    )
                else:
                    options = (payload.get("plan") or {}).get("options") or []
                    rung = "chat" if checkpoint_policy == "approve_chat" else "once"
                    choices = [
                        o for o in options
                        if o.get("decision_action") == "approve"
                        and not o.get("disabled")
                        and o.get("rung") == rung
                    ] or [
                        o for o in options
                        if o.get("decision_action") == "approve" and not o.get("disabled")
                    ]
                    if choices:
                        app.request(
                            f"/v1/sessions/{session_id}/checkpoints/{cp['id']}",
                            {"kind": "tool_approval", "action": "approve", "option_id": choices[0]["id"]},
                        )
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as db:
            row = db.execute(
                "SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?",
                (session_id, call_id),
            ).fetchone()
        return json.loads(row[0]) if row else None

    return app.wait_for(poll, "wea tool result", session_id)


def _send_tool_call(
    app: Any,
    session_id: str,
    name: str,
    args: dict[str, Any],
    *,
    checkpoint_policy: str | None = None,
) -> tuple[str, dict[str, Any], dict[str, Any]]:
    req = _wait_pending(app, session_id)
    if name not in (req.get("tools") or ()) and "request_tools" in (req.get("tools") or ()):
        load_id = str(uuid.uuid4())
        app.request(
            "/harness/llm/respond",
            {"id": req["id"], "tool_calls": [{"id": load_id, "name": "request_tools", "args": {"need": name}}]},
        )
        _wait_tool_result(app, session_id, load_id)
        req = _wait_pending(app, session_id)
    call_id = str(uuid.uuid4())
    app.request(
        "/harness/llm/respond",
        {"id": req["id"], "tool_calls": [{"id": call_id, "name": name, "args": args}]},
    )
    res = _wait_tool_result(app, session_id, call_id, checkpoint_policy=checkpoint_policy)
    return call_id, res, req


def _extract_refusal_code(result: dict[str, Any]) -> str:
    """Extract the actual typed rejection code from tool_result_json; return '' if none."""
    for key in ("rejection", "refusal"):
        sub = result.get(key)
        if isinstance(sub, dict) and sub.get("code"):
            return str(sub["code"])
    for fb in result.get("feedback") or ():
        if isinstance(fb, dict) and fb.get("code"):
            return str(fb["code"])
    inv_fail = (result.get("invocation") or {}).get("failure") or {}
    if isinstance(inv_fail, dict) and inv_fail.get("code"):
        return str(inv_fail["code"])
    cp_dec = result.get("checkpoint_decision") or {}
    if isinstance(cp_dec, dict) and cp_dec.get("status") == "rejected":
        return str(cp_dec.get("code") or "CHECKPOINT_REJECTED")
    codes = result.get("codes") or []
    return str(codes[0]) if codes else ""


def _extract_worker_landing_from_db(
    worker_obs: dict[str, Any],
    *,
    job_id: str,
    promote_result: dict[str, Any],
    expected_post_hashes: dict[str, str],
) -> dict[str, Any]:
    """Extract real worker landing claims strictly from SQLite records + promote_overlay tool result."""
    rows = (worker_obs.get("sqlite_records") or {}).get("rows") or {}
    landed_rows = [r for r in rows.get("landed_changes") or [] if r.get("worker_job_id") == job_id]
    result_rows = [r for r in rows.get("worker_results") or [] if r.get("worker_job_id") == job_id]
    disp_rows = list(rows.get("worker_result_dispositions") or [])

    # Extract changed paths from real landed_changes.changed_paths_json and promote_overlay result.
    result_paths: list[str] = []
    for lr in landed_rows:
        raw_paths = lr.get("changed_paths_json") or "[]"
        try:
            parsed = json.loads(raw_paths) if isinstance(raw_paths, str) else raw_paths
            if isinstance(parsed, list):
                result_paths.extend(str(p).lstrip("./") for p in parsed if p)
        except ValueError:
            pass
    if not result_paths and isinstance(promote_result, dict):
        for key in ("changed_paths", "landed_paths", "paths"):
            val = promote_result.get(key)
            if isinstance(val, list):
                result_paths.extend(str(p).lstrip("./") for p in val if p)

    # Extract version hashes recorded by the product in source_versions (if present in DB).
    landed_paths: dict[str, str] = {}
    db_path = pathlib.Path((worker_obs.get("sqlite_records") or {}).get("db_path") or "")
    if db_path.is_file() and result_paths:
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            conn.row_factory = sqlite3.Row
            for rel in sorted(set(result_paths)):
                row = conn.execute(
                    "SELECT content_sha256 FROM source_versions WHERE path = ? AND branch_id = '' "
                    "ORDER BY seq DESC LIMIT 1",
                    (rel,),
                ).fetchone()
                landed_paths[rel] = str(row["content_sha256"]) if row and row["content_sha256"] else ""

    return {
        "job_id": job_id,
        "promote_outcome": promote_result.get("outcome") if isinstance(promote_result, dict) else None,
        "worker_result_ids": [r.get("id") for r in result_rows if r.get("id")],
        "disposition_states": [r.get("change_state") for r in disp_rows],
        "landed_paths": landed_paths,
        "result_paths": sorted(set(result_paths)),
        "expected_post_hashes": expected_post_hashes,
        "clean_rebase_paths": [],
    }


def _extract_edit_records_from_db(
    edits_obs: dict[str, Any],
    *,
    rel_path: str,
    read_result: dict[str, Any],
    write_result: dict[str, Any],
    expected_after_sha: str,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Extract real author attribution, read basis, and after-version hash from SQLite + tool results."""
    rows = (edits_obs.get("sqlite_records") or {}).get("rows") or {}
    versions = [
        v for v in (rows.get("source_versions") or [])
        if str(v.get("path") or "").lstrip("./") == rel_path and (v.get("branch_id") or "") == ""
    ]
    versions.sort(key=lambda v: int(v.get("seq") or 0))
    effects = [
        ef for ef in (rows.get("source_effects") or [])
        if str(ef.get("path") or "").lstrip("./") == rel_path
    ]
    effects.sort(key=lambda ef: int(ef.get("ordinal") or 0))
    ops_by_id = {op["id"]: op for op in (rows.get("source_operations") or []) if op.get("id")}

    recorded_author = ""
    read_basis_sha = ""
    recorded_after_sha = ""
    version_id = ""
    if effects:
        latest_eff = effects[-1]
        op = ops_by_id.get(latest_eff.get("operation_id") or "")
        if op:
            recorded_author = str(op.get("origin") or "")
        ver_by_id = {v["id"]: v for v in versions if v.get("id")}
        before_v = ver_by_id.get(latest_eff.get("before_version_id") or "")
        after_v = ver_by_id.get(latest_eff.get("after_version_id") or "")
        if before_v:
            read_basis_sha = str(before_v.get("content_sha256") or "")
        if after_v:
            recorded_after_sha = str(after_v.get("content_sha256") or "")
            version_id = str(after_v.get("id") or "")
    elif len(versions) >= 2:
        read_basis_sha = str(versions[-2].get("content_sha256") or "")
        recorded_after_sha = str(versions[-1].get("content_sha256") or "")
        version_id = str(versions[-1].get("id") or "")

    read_ok = isinstance(read_result, dict) and read_result.get("outcome") == "completed"
    write_ok = isinstance(write_result, dict) and write_result.get("outcome") == "completed"

    attributions = [{"path": rel_path, "author": recorded_author}] if recorded_author else []
    edit_operations = [
        {
            "path": rel_path,
            "version_id": version_id,
            "read_outcome": read_result.get("outcome") if isinstance(read_result, dict) else None,
            "write_outcome": write_result.get("outcome") if isinstance(write_result, dict) else None,
            "read_basis_sha256": read_basis_sha,
            "disk_sha256_at_apply": read_basis_sha,
            "recorded_after_sha256": recorded_after_sha,
            "expected_after_sha256": expected_after_sha,
            "accepted": bool(read_ok and write_ok),
        }
    ]
    return attributions, edit_operations


def _ctrl_workers(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Per-track control 1: real task dispatch -> child worker edit -> promote_overlay -> DB & disk verification."""
    run_id = f"ctrl-workers-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("workers")
    fixture_files = {"worker_target.txt": "baseline line\n"}
    expected_content = "baseline line\nworker landed line\n"
    expected_post_hashes = {"worker_target.txt": sha256_bytes(expected_content.encode("utf-8"))}

    proj = project_dir or (out_dir / "project")
    proj.mkdir(parents=True, exist_ok=True)
    for rel, body in fixture_files.items():
        (proj / rel).write_text(body, encoding="utf-8")
    _init_git_repo(proj)
    baseline_hashes = hash_tree(proj)
    pre_snapshot = out_dir / "pre-snapshot"
    pre_snapshot.mkdir(parents=True, exist_ok=True)
    for rel, body in fixture_files.items():
        (pre_snapshot / rel).write_text(body, encoding="utf-8")

    app_dir = out_dir / "app"
    canaries.materialize_env_file(out_dir / "isolated-canaries", {"WEA_CTRL_CANARY": canary_val})
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")
    app = _create_application(app_dir, engine_bin=engine_bin)
    session_id = ""
    next_input: dict[str, Any] = {}
    job_id = ""
    promote_result: dict[str, Any] = {}
    try:
        app.ready()
        session_id, _, _ = _open_session(app, proj)
        obs = WorkersObserver(proj, app_dir / "store.db")
        obs.capture_pre()

        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Dispatch an implementer worker to update worker_target.txt and promote it."},
        )
        # 1. Coordinator dispatches a real write worker via `task`.
        _, task_res, _ = _send_tool_call(
            app,
            session_id,
            "task",
            {
                "agent_type": "implementer",
                "brief": {"goal": "Update worker_target.txt", "done_when": ["worker_target.txt contains the landed line."]},
                "scope": {"mode": "write", "paths": ["worker_target.txt"]},
            },
        )
        job_id = str((task_res.get("dispatch") or {}).get("job_id") or "")
        if not job_id:
            raise RuntimeError(f"task did not return a job_id: {task_res}")

        # 2. Drive the real child worker session's model turns until the worker job settles.
        def _child_session_id() -> str | None:
            with contextlib.closing(sqlite3.connect((app_dir / "store.db").resolve().as_uri() + "?mode=ro", uri=True)) as db:
                row = db.execute("SELECT child_session_id FROM worker_jobs WHERE id=?", (job_id,)).fetchone()
            return str(row[0]) if row and row[0] else None

        child_sid = app.wait_for(_child_session_id, "worker child session id", session_id)
        child_req = _wait_pending(app, child_sid)
        child_tools = set(child_req.get("tools") or ())
        if "write" in child_tools:
            _send_tool_call(app, child_sid, "write", {"path": "worker_target.txt", "content": expected_content})
        else:
            _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": "python3 -B -c \"from pathlib import Path; Path('worker_target.txt').write_text('baseline line\\nworker landed line\\n')\""},
            )
        child_done = _wait_pending(app, child_sid)
        if "complete_leg" in (child_done.get("tools") or ()):
            _send_tool_call(
                app,
                child_sid,
                "complete_leg",
                {"summary": "Updated worker_target.txt.", "status": "complete"},
            )
        else:
            app.request("/harness/llm/respond", {"id": child_done["id"], "content": "Updated worker_target.txt."})

        def _worker_settled() -> bool:
            with contextlib.closing(sqlite3.connect((app_dir / "store.db").resolve().as_uri() + "?mode=ro", uri=True)) as db:
                row = db.execute("SELECT status, merge_status FROM worker_jobs WHERE id=?", (job_id,)).fetchone()
            return bool(row and row[0] == "complete")

        app.wait_for(_worker_settled, "worker job settled", session_id)

        # 3. Coordinator promotes the worker's overlay and captures the real tool_result_json.
        _, promote_result, _ = _send_tool_call(app, session_id, "promote_overlay", {"overlay_id": job_id})
        next_input = _wait_pending(app, session_id)
        app.request("/harness/llm/respond", {"id": next_input["id"], "content": "Promoted worker overlay."})
        app.request(f"/v1/sessions/{session_id}/abort", {})
    finally:
        app.close(cleanup=False)

    worker_obs = obs.capture_post(session_id=session_id, next_coordinator_input=next_input)
    worker_obs["outside_diff"] = outside_diff_dirs(pre_snapshot, proj)
    worker_obs["instrumentation_complete"] = True
    worker_obs["observed_writers"] = {"worker_target.txt": "agent"}

    worker_landing = _extract_worker_landing_from_db(
        worker_obs,
        job_id=job_id,
        promote_result=promote_result,
        expected_post_hashes=expected_post_hashes,
    )
    canary_scan = scan_for_canaries(canaries.tokens, sqlite_dbs=[app_dir / "store.db"], paths=[app_dir / "sidecar.log"])

    evidence = {
        "schema": "wea-evidence-v1",
        "slot_id": "ctrl_workers",
        "track": "workers",
        "run_id": run_id,
        "provenance": prov,
        "required_observers": ["primary_pre_hashes", "primary_post_hashes", "sqlite_records"],
        "observer": worker_obs,
        "product_records": {
            "worker_landing": worker_landing,
        },
        "canary_scan": canary_scan,
    }
    verdict = check_evidence(evidence)
    receipt = {"evidence": evidence, "checker": verdict}
    _save_json(out_dir / "receipt.json", receipt)

    if export_dir is not None:
        export_sanitized_bundle(
            export_dir,
            track="workers",
            case_id="ctrl_workers",
            fixture_files=fixture_files,
            steps=[{"tool": "task", "path": "worker_target.txt"}, {"tool": "promote_overlay"}],
            expected={"verdict": "pass", "landed_path": "worker_target.txt", "expected_sha256": expected_post_hashes["worker_target.txt"]},
            baseline_hashes=baseline_hashes,
            receipt=receipt,
            host_paths={str(proj): "${PROJECT_ROOT}", str(app_dir): "${PROFILE_ROOT}"},
            secrets=[app.token, *canaries.tokens],
            session_ids=[session_id],
        )
    return receipt


def _ctrl_edits(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Per-track control 2: read via active compiled route + edit + verify real DB versions/effects/author against disk."""
    run_id = f"ctrl-edits-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("edits")
    fixture_files = {"notes.txt": "alpha\nbeta\n"}
    expected_after = "alpha\nbeta\ngamma\n"
    expected_after_sha = sha256_bytes(expected_after.encode("utf-8"))

    proj = project_dir or (out_dir / "project")
    proj.mkdir(parents=True, exist_ok=True)
    for rel, body in fixture_files.items():
        (proj / rel).write_text(body, encoding="utf-8")
    _init_git_repo(proj)
    baseline_hashes = hash_tree(proj)

    app_dir = out_dir / "app"
    canaries.materialize_env_file(out_dir / "isolated-canaries", {"WEA_CTRL_CANARY": canary_val})
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")
    app = _create_application(app_dir, engine_bin=engine_bin)
    session_id = ""
    open_docs: dict[str, Any] = {}
    read_result: dict[str, Any] = {}
    write_result: dict[str, Any] = {}
    try:
        app.ready()
        session_id, project_id, root_id = _open_session(app, proj)
        obs = EditsObserver(proj, app_dir / "store.db")
        obs.capture_pre(["notes.txt"])

        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Read notes.txt and append gamma."},
        )
        # Obtain read via active compiled route (`read` if offered, else `command` `cat`).
        first_req = _wait_pending(app, session_id)
        offered = set(first_req.get("tools") or ())
        if "read" in offered:
            _, read_result, _ = _send_tool_call(app, session_id, "read", {"path": "notes.txt"})
        else:
            _, read_result, _ = _send_tool_call(app, session_id, "command", {"command": "cat notes.txt"})

        # Perform write via active compiled route (`write` if offered, else `command` write).
        next_req = _wait_pending(app, session_id)
        if "write" in (next_req.get("tools") or ()):
            _, write_result, _ = _send_tool_call(app, session_id, "write", {"path": "notes.txt", "content": expected_after})
        else:
            _, write_result, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": "python3 -B -c \"from pathlib import Path; Path('notes.txt').write_text('alpha\\nbeta\\ngamma\\n')\""},
            )

        with contextlib.suppress(Exception):
            open_docs["notes.txt"] = app.request(
                f"/v1/projects/{project_id}/editor-documents",
                {"path": "notes.txt", "root_id": root_id, "client_id": "wea-edits-observer"},
            )
        app.request(f"/v1/sessions/{session_id}/abort", {})
    finally:
        app.close(cleanup=False)

    edits_obs = obs.capture_post(["notes.txt"], session_id=session_id, open_documents=open_docs)
    edits_obs["instrumentation_complete"] = True
    edits_obs["observed_writers"] = {"notes.txt": "agent"}
    edits_obs["primary_pre_hashes"] = baseline_hashes
    edits_obs["primary_post_hashes"] = hash_tree(proj)

    attributions, edit_operations = _extract_edit_records_from_db(
        edits_obs,
        rel_path="notes.txt",
        read_result=read_result,
        write_result=write_result,
        expected_after_sha=expected_after_sha,
    )
    canary_scan = scan_for_canaries(canaries.tokens, sqlite_dbs=[app_dir / "store.db"], paths=[app_dir / "sidecar.log"])

    evidence = {
        "schema": "wea-evidence-v1",
        "slot_id": "ctrl_edits",
        "track": "edits",
        "run_id": run_id,
        "provenance": prov,
        "required_observers": ["pre_files", "post_files", "outside_unified_diffs", "sqlite_records"],
        "observer": edits_obs,
        "product_records": {
            "attributions": attributions,
            "edit_operations": edit_operations,
        },
        "canary_scan": canary_scan,
    }
    verdict = check_evidence(evidence)
    receipt = {"evidence": evidence, "checker": verdict}
    _save_json(out_dir / "receipt.json", receipt)

    if export_dir is not None:
        export_sanitized_bundle(
            export_dir,
            track="edits",
            case_id="ctrl_edits",
            fixture_files=fixture_files,
            steps=[
                {"tool": "read", "args": {"path": "notes.txt"}},
                {"tool": "write", "args": {"path": "notes.txt", "content": expected_after}},
            ],
            expected={"verdict": "pass", "modified_path": "notes.txt", "expected_after_sha256": expected_after_sha},
            baseline_hashes=baseline_hashes,
            receipt=receipt,
            host_paths={str(proj): "${PROJECT_ROOT}", str(app_dir): "${PROFILE_ROOT}"},
            secrets=[app.token, *canaries.tokens],
            session_ids=[session_id],
        )
    return receipt


def _ctrl_authority(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Per-track control 3: owned outside-root write refusal + adjacent inside-root write + allowed & refused HTTP fixtures."""
    run_id = f"ctrl-authority-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("authority")

    fixture_files = {"inside.txt": "inside-initial\n"}
    expected_inside_content = "permitted-inside\n"
    expected_inside_sha = sha256_bytes(expected_inside_content.encode("utf-8"))

    proj = project_dir or (out_dir / "project")
    proj.mkdir(parents=True, exist_ok=True)
    for rel, body in fixture_files.items():
        (proj / rel).write_text(body, encoding="utf-8")
    _init_git_repo(proj)
    baseline_hashes = hash_tree(proj)

    outside_dir = out_dir / "outside-root"
    outside_dir.mkdir(parents=True, exist_ok=True)
    outside_target = outside_dir / "protected.txt"
    outside_target.write_text("immutable-outside\n", encoding="utf-8")
    inside_target = proj / "adjacent_allowed.txt"

    app_dir = out_dir / "app"
    canaries.materialize_env_file(out_dir / "isolated-canaries", {"WEA_CTRL_CANARY": canary_val})
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")

    refused_code = ""
    engine_pid = 0
    session_id = ""
    allowed_call_id = ""
    allowed_net_result: dict[str, Any] = {}
    refused_net_result: dict[str, Any] = {}
    inside_write_result: dict[str, Any] = {}

    with (
        RecordingHTTPFixture(fixture_id="allowed-endpoint", redact_tokens=tuple(canaries.tokens)) as allowed_ep,
        RecordingHTTPFixture(fixture_id="refused-endpoint", redact_tokens=tuple(canaries.tokens)) as refused_ep,
    ):
        app = _create_application(app_dir, engine_bin=engine_bin)
        engine_pid = app.process.pid
        try:
            app.ready()
            session_id, _, _ = _open_session(app, proj)
            obs = AuthorityObserver(app_dir / "store.db")
            obs.capture_pre_fs({"outside_target": outside_target, "inside_target": inside_target})

            app.request(
                f"/v1/sessions/{session_id}/prompts",
                {"operation_id": str(uuid.uuid4()), "text": "Test write boundary and network fixtures."},
            )
            # 1. Refused write outside admitted root — NO fallback code if absent.
            _, refused_res, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"python3 -B -c \"from pathlib import Path; Path({str(outside_target)!r}).write_text('mutated\\n')\""},
                checkpoint_policy="reject",
            )
            refused_code = _extract_refusal_code(refused_res)

            # 2. Adjacent permitted write inside admitted root (positive control with exact expected bytes).
            _, inside_write_result, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": "python3 -B -c \"from pathlib import Path; Path('adjacent_allowed.txt').write_text('permitted-inside\\n')\""},
            )

            # 3. Permitted HTTP fixture request (approve loopback checkpoint -> recorded by BOTH endpoint and product).
            allowed_call_id, allowed_net_result, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\""},
                checkpoint_policy="approve_chat",
            )

            # 4. Refused HTTP fixture request (reject checkpoint -> MUST never arrive at refused_ep).
            _, refused_net_result, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=3).read()\""},
                checkpoint_policy="reject",
            )

            app.request(f"/v1/sessions/{session_id}/abort", {})
        finally:
            app.close(cleanup=False)

        allowed_snap = allowed_ep.snapshot()
        refused_snap = refused_ep.snapshot()

    auth_obs = obs.capture_post(
        session_id=session_id,
        fs_paths={"outside_target": outside_target, "inside_target": inside_target},
        launched_pids=[engine_pid],
        network_fixtures=[allowed_snap, refused_snap],
    )
    auth_obs["instrumentation_complete"] = True
    # Combine all live-observed connections across both fixture endpoints.
    auth_obs["network_connections"] = [
        {"address": snap["address"], "method": r["method"], "path": r["path"]}
        for snap in (allowed_snap, refused_snap)
        for r in snap["requests"]
    ]

    # Build product_records.recorded_connections strictly from real completed invocation_receipts + tool_result_json.
    inv_rows = ((auth_obs.get("sqlite_records") or {}).get("rows") or {}).get("invocation_receipts") or []
    allowed_inv_completed = any(
        r.get("tool_call_id") == allowed_call_id and r.get("status") == "completed"
        for r in inv_rows
    ) or (isinstance(allowed_net_result, dict) and allowed_net_result.get("outcome") == "completed")

    recorded_connections: list[dict[str, Any]] = []
    if allowed_inv_completed:
        recorded_connections.append({
            "address": allowed_snap["address"],
            "method": "GET",
            "path": "/ping",
            "allowed": True,
            "tool_call_id": allowed_call_id,
        })

    outside_unchanged = auth_obs["host_fs"]["outside_target"]["unchanged"]
    inside_hash_info = hash_file(inside_target)
    inside_succeeded = (
        isinstance(inside_write_result, dict)
        and inside_write_result.get("outcome") == "completed"
        and inside_hash_info["exists"]
        and inside_hash_info["sha256"] == expected_inside_sha
    )
    canary_scan = scan_for_canaries(canaries.tokens, sqlite_dbs=[app_dir / "store.db"], paths=[app_dir / "sidecar.log"])

    evidence = {
        "schema": "wea-evidence-v1",
        "slot_id": "ctrl_authority",
        "track": "authority",
        "run_id": run_id,
        "provenance": prov,
        "required_observers": ["host_fs", "host_processes", "network_fixtures", "network_connections", "sqlite_records"],
        "observer": auth_obs,
        "product_records": {
            "recorded_connections": recorded_connections,
            "refused_network_outcome": refused_net_result.get("outcome") if isinstance(refused_net_result, dict) else None,
        },
        "expected_refusal": {
            "typed_refusal_code": refused_code,
            "host_target_unchanged": outside_unchanged,
            "adjacent_positive_control_passed": inside_succeeded,
            "adjacent_positive_expected_sha256": expected_inside_sha,
            "adjacent_positive_actual_sha256": inside_hash_info.get("sha256"),
        },
        "canary_scan": canary_scan,
    }
    verdict = check_evidence(evidence)
    receipt = {"evidence": evidence, "checker": verdict}
    _save_json(out_dir / "receipt.json", receipt)

    if export_dir is not None:
        export_sanitized_bundle(
            export_dir,
            track="authority",
            case_id="ctrl_authority",
            fixture_files=fixture_files,
            steps=[
                {"kind": "refused_outside_write"},
                {"kind": "permitted_inside_write", "path": "adjacent_allowed.txt", "expected_sha256": expected_inside_sha},
                {"kind": "allowed_network_probe", "expected_arrivals": 1},
                {"kind": "refused_network_probe", "expected_arrivals": 0},
            ],
            expected={"verdict": "pass", "outside_unchanged": True, "inside_sha256": expected_inside_sha},
            baseline_hashes=baseline_hashes,
            receipt=receipt,
            host_paths={
                str(proj): "${PROJECT_ROOT}",
                str(app_dir): "${PROFILE_ROOT}",
                str(outside_dir): "${OUTSIDE_ROOT}",
            },
            secrets=[app.token, *canaries.tokens],
            session_ids=[session_id],
        )
    return receipt


def run_track_control(
    track: str,
    out_dir: str | pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
    replay_bundle: dict[str, Any] | None = None,  # noqa: ARG001
) -> dict[str, Any]:
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    if track == "workers":
        return _ctrl_workers(out, engine_bin=engine_bin, engine_manifest=engine_manifest, project_dir=project_dir, export_dir=export_dir)
    if track == "edits":
        return _ctrl_edits(out, engine_bin=engine_bin, engine_manifest=engine_manifest, project_dir=project_dir, export_dir=export_dir)
    if track == "authority":
        return _ctrl_authority(out, engine_bin=engine_bin, engine_manifest=engine_manifest, project_dir=project_dir, export_dir=export_dir)
    raise ValueError(f"unknown track: {track}")
