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
import os
import pathlib
import re
import sqlite3
import subprocess
import sys
import urllib.parse
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

_CODE_LINE_RE = re.compile(r"\bCode:\s*([A-Z][A-Z0-9_]{2,})\b")


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


def _resolve_engine_root(
    *,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
) -> pathlib.Path | None:
    """Resolve a staged LYCAON_ENGINE_ROOT from CLI arg, env, or TB01 build-manifest worktree."""
    if engine_root:
        p = pathlib.Path(engine_root).resolve()
        if p.is_dir():
            return p
    env_root = os.environ.get("LYCAON_ENGINE_ROOT", "").strip()
    if env_root:
        p = pathlib.Path(env_root).resolve()
        if p.is_dir():
            return p
    if engine_manifest:
        mp = pathlib.Path(engine_manifest).resolve()
        if mp.is_file():
            try:
                data = json.loads(mp.read_text(encoding="utf-8"))
                builds = data.get("builds") if isinstance(data, dict) else None
                if isinstance(builds, dict):
                    pb = builds.get("product_baseline") or next(iter(builds.values()), None)
                    wt = pb.get("worktree") if isinstance(pb, dict) else None
                    if wt:
                        cand = pathlib.Path(wt) / "lycaon-den" / "src-tauri" / "engine-root"
                        if cand.is_dir():
                            return cand.resolve()
            except (OSError, ValueError):
                pass
    default_root = REPO_ROOT / "lycaon-den" / "src-tauri" / "engine-root"
    return default_root.resolve() if default_root.is_dir() else None


def _create_application(
    app_dir: pathlib.Path,
    engine_bin: str | pathlib.Path | None = None,
    *,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
):
    """Instantiate smoke.Application strictly in isolated manual mode (source_config=None)."""
    import smoke

    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
    resolved_engine_root = _resolve_engine_root(engine_manifest=engine_manifest, engine_root=engine_root)
    orig_engine = smoke.engine_path
    orig_env_fn = smoke.application_environment
    smoke.engine_path = lambda _root: binary

    def _wrapped_env(overrides):
        merged = dict(overrides)
        if resolved_engine_root is not None:
            merged["LYCAON_ENGINE_ROOT"] = str(resolved_engine_root)
        return orig_env_fn(merged)

    smoke.application_environment = _wrapped_env
    try:
        return smoke.Application(app_dir, source_config=None)
    finally:
        smoke.engine_path = orig_engine
        smoke.application_environment = orig_env_fn


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
    path = (
        f"/harness/llm/pending?session_id={urllib.parse.quote(session_id)}"
        if session_id
        else "/harness/llm/pending"
    )

    def poll() -> dict[str, Any] | None:
        val = app.request(path)
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
    if not isinstance(result, dict):
        return ""
    for key in ("rejection", "refusal", "isolation"):
        sub = result.get(key)
        if isinstance(sub, dict) and sub.get("code"):
            return str(sub["code"])
    for fb in result.get("feedback") or ():
        if isinstance(fb, dict) and fb.get("code"):
            return str(fb["code"])
    inv = result.get("invocation") or {}
    if isinstance(inv, dict):
        for sub_key in ("failure", "isolation"):
            sub = inv.get(sub_key)
            if isinstance(sub, dict) and sub.get("code"):
                return str(sub["code"])
    cp_dec = result.get("checkpoint_decision") or {}
    if isinstance(cp_dec, dict) and cp_dec.get("status") == "rejected":
        return str(cp_dec.get("code") or "CHECKPOINT_REJECTED")
    codes = result.get("codes") or []
    if codes:
        return str(codes[0])
    content = str(result.get("content") or "")
    m = _CODE_LINE_RE.search(content)
    return m.group(1) if m else ""


def _extract_worker_landing_from_db(
    worker_obs: dict[str, Any],
    *,
    job_id: str,
    promote_result: dict[str, Any],
    expected_post_hashes: dict[str, str],
) -> dict[str, Any]:
    """Extract worker producer result paths INDEPENDENTLY from landing claims.

    - `result_paths` comes strictly from the producer's settled `worker_results.result_json`
      / `worker_jobs.result_json` (NEVER from `landed_changes` or `promote_overlay`).
    - `landed_paths` comes strictly from `landed_changes.changed_paths_json` joined with
      the product's `source_versions.content_sha256` (NEVER copied from disk hashes).
    """
    rows = (worker_obs.get("sqlite_records") or {}).get("rows") or {}
    job_rows = [r for r in rows.get("worker_jobs") or [] if r.get("id") == job_id]
    result_rows = [r for r in rows.get("worker_results") or [] if r.get("worker_job_id") == job_id]
    landed_rows = [r for r in rows.get("landed_changes") or [] if r.get("worker_job_id") == job_id]
    result_ids = [str(r["id"]) for r in result_rows if r.get("id")]
    attempt_ids = [str(r["worker_attempt_id"]) for r in result_rows if r.get("worker_attempt_id")]
    disp_rows = [
        r for r in (rows.get("worker_result_dispositions") or [])
        if r.get("result_id") in set(result_ids)
    ]

    # 1. Producer-side result_paths from worker_results / worker_jobs (independent of landed_changes).
    producer_paths: list[str] = []
    for src in [*result_rows, *job_rows]:
        raw_res = src.get("result_json") or ""
        if not raw_res:
            continue
        try:
            parsed = json.loads(raw_res) if isinstance(raw_res, str) else raw_res
        except ValueError:
            continue
        if isinstance(parsed, dict):
            for key in ("changed_paths", "modified_paths", "touched_paths", "paths", "files"):
                val = parsed.get(key)
                if isinstance(val, list):
                    for item in val:
                        if isinstance(item, str) and item:
                            producer_paths.append(item.lstrip("./"))
                        elif isinstance(item, dict) and item.get("path"):
                            producer_paths.append(str(item["path"]).lstrip("./"))
            report = parsed.get("completion_report")
            if isinstance(report, dict):
                for key in ("changed_paths", "files"):
                    val = report.get(key)
                    if isinstance(val, list):
                        producer_paths.extend(str(p).lstrip("./") for p in val if isinstance(p, str) and p)

    # Also check scope_paths / source_effects written by child_session_id if worker_results stores only summary.
    child_sids = {str(j.get("child_session_id")) for j in job_rows if j.get("child_session_id")}
    db_path = pathlib.Path((worker_obs.get("sqlite_records") or {}).get("db_path") or "")
    if not producer_paths and db_path.is_file() and child_sids:
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            for csid in sorted(child_sids):
                eff_rows = conn.execute(
                    "SELECT DISTINCT se.path FROM source_effects se "
                    "JOIN source_operations so ON so.id = se.operation_id "
                    "WHERE so.session_id = ?",
                    (csid,),
                ).fetchall()
                producer_paths.extend(str(r[0]).lstrip("./") for r in eff_rows if r and r[0])

    # 2. Landing-side paths from landed_changes + product source_versions SHA-256.
    claimed_landed: list[str] = []
    for lr in landed_rows:
        raw_paths = lr.get("changed_paths_json") or "[]"
        try:
            parsed = json.loads(raw_paths) if isinstance(raw_paths, str) else raw_paths
            if isinstance(parsed, list):
                claimed_landed.extend(str(p).lstrip("./") for p in parsed if p)
        except ValueError:
            pass

    landed_paths: dict[str, str] = {}
    if db_path.is_file() and claimed_landed:
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            conn.row_factory = sqlite3.Row
            for rel in sorted(set(claimed_landed)):
                row = conn.execute(
                    "SELECT content_sha256 FROM source_versions WHERE path = ? AND branch_id = '' "
                    "ORDER BY seq DESC LIMIT 1",
                    (rel,),
                ).fetchone()
                landed_paths[rel] = str(row["content_sha256"]) if row and row["content_sha256"] else ""

    return {
        "job_id": job_id,
        "worker_result_ids": result_ids,
        "worker_attempt_ids": attempt_ids,
        "disposition_states": [str(r.get("change_state") or "") for r in disp_rows if r.get("change_state")],
        "require_complete_proof": True,
        "promote_outcome": promote_result.get("outcome") if isinstance(promote_result, dict) else None,
        "landed_paths": landed_paths,
        "result_paths": sorted(set(producer_paths)),
        "expected_post_hashes": expected_post_hashes,
        "clean_rebase_paths": [],
    }


def _extract_edit_records_from_db(
    edits_obs: dict[str, Any],
    *,
    rel_path: str,
    read_result: dict[str, Any],
    write_result: dict[str, Any],
    disk_sha_before_write: str,
    expected_after_sha: str,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Extract real author attribution, read basis, and after-version hash from SQLite + tool results.

    Joins directly on the latest `source_versions` row for `rel_path` -> `source_effects` ->
    `source_operations` (never picking an earlier `external` baseline row and never hardcoding
    author or hashes). `disk_sha256_at_apply` is the independent host-side disk hash captured
    immediately before the write.
    """
    recorded_author = ""
    read_basis_sha = ""
    recorded_after_sha = ""
    version_id = ""

    db_path = pathlib.Path((edits_obs.get("sqlite_records") or {}).get("db_path") or "")
    if db_path.is_file():
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            conn.row_factory = sqlite3.Row
            ver_rows = conn.execute(
                "SELECT id, seq, parent_version_id, operation_id, content_sha256 "
                "FROM source_versions WHERE path = ? AND branch_id = '' ORDER BY seq DESC LIMIT 4",
                (rel_path,),
            ).fetchall()
            if ver_rows:
                latest_v = ver_rows[0]
                version_id = str(latest_v["id"] or "")
                recorded_after_sha = str(latest_v["content_sha256"] or "")

                # Join exact effect producing latest_v -> source_operations.origin & before_version_id.
                eff = conn.execute(
                    "SELECT se.before_version_id, so.origin "
                    "FROM source_effects se "
                    "JOIN source_operations so ON so.id = se.operation_id "
                    "WHERE se.after_version_id = ? "
                    "ORDER BY se.ordinal DESC LIMIT 1",
                    (version_id,),
                ).fetchone()
                if eff:
                    recorded_author = str(eff["origin"] or "")
                    if eff["before_version_id"]:
                        bv = conn.execute(
                            "SELECT content_sha256 FROM source_versions WHERE id = ?",
                            (eff["before_version_id"],),
                        ).fetchone()
                        if bv:
                            read_basis_sha = str(bv["content_sha256"] or "")
                elif latest_v["operation_id"]:
                    op = conn.execute(
                        "SELECT origin FROM source_operations WHERE id = ?",
                        (latest_v["operation_id"],),
                    ).fetchone()
                    if op:
                        recorded_author = str(op["origin"] or "")

                if not read_basis_sha and latest_v["parent_version_id"]:
                    pv = conn.execute(
                        "SELECT content_sha256 FROM source_versions WHERE id = ?",
                        (latest_v["parent_version_id"],),
                    ).fetchone()
                    if pv:
                        read_basis_sha = str(pv["content_sha256"] or "")
                if not read_basis_sha and len(ver_rows) >= 2:
                    read_basis_sha = str(ver_rows[1]["content_sha256"] or "")

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
            "disk_sha256_at_apply": disk_sha_before_write,
            "recorded_after_sha256": recorded_after_sha,
            "expected_after_sha256": expected_after_sha,
            "accepted": bool(read_ok and write_ok),
        }
    ]
    return attributions, edit_operations


def _extract_product_network_witnesses(
    auth_obs: dict[str, Any],
    *,
    allowed_call_id: str,
    allowed_tool_result: dict[str, Any],
) -> list[dict[str, Any]]:
    """Extract typed product network witnesses strictly from structured endpoint records.

    Reads ONLY:
      1. `authz_events` rows (`action IN ('mediated_endpoint', 'capability_applied')`,
         `outcome == 'allowed'`) -> `detail_json.external_access.endpoints[]`
      2. Typed `tool_result_json.external_access.endpoints[]` for `allowed_call_id` (only if
         no `authz_events` mediated endpoint rows exist).

    NEVER scans arbitrary strings for URLs, NEVER hardcodes HTTP methods, and NEVER
    deduplicates repeated connections (expands by `attempts` per distinct event/invocation ID).
    """
    rows = (auth_obs.get("sqlite_records") or {}).get("rows") or {}
    witnesses: list[dict[str, Any]] = []

    for ev in rows.get("authz_events") or []:
        if ev.get("action") not in {"mediated_endpoint", "capability_applied"}:
            continue
        if ev.get("outcome") != "allowed":
            continue
        raw_detail = ev.get("detail_json") or "{}"
        try:
            detail = json.loads(raw_detail) if isinstance(raw_detail, str) else raw_detail
        except ValueError:
            continue
        if not isinstance(detail, dict):
            continue
        ea = detail.get("external_access")
        if not isinstance(ea, dict):
            continue
        for ep in ea.get("endpoints") or ():
            if not isinstance(ep, dict):
                continue
            host = str(ep.get("host") or "").strip()
            port = ep.get("port")
            if not host or not isinstance(port, int) or port <= 0:
                continue
            attempts = max(1, int(ep.get("attempts") or 1))
            for idx in range(attempts):
                witnesses.append({
                    "address": f"{host}:{port}",
                    "transport": str(ep.get("transport") or ""),
                    "method": "",
                    "path": "",
                    "allowed": bool(ep.get("allowed", False)),
                    "event_id": str(ev.get("id") or ""),
                    "event_seq": ev.get("event_seq"),
                    "attempt_index": idx + 1,
                    "source": "authz_events.external_access.endpoints",
                })

    if not witnesses and isinstance(allowed_tool_result, dict):
        ea = allowed_tool_result.get("external_access")
        if isinstance(ea, dict):
            for ep in ea.get("endpoints") or ():
                if not isinstance(ep, dict):
                    continue
                host = str(ep.get("host") or "").strip()
                port = ep.get("port")
                if not host or not isinstance(port, int) or port <= 0:
                    continue
                attempts = max(1, int(ep.get("attempts") or 1))
                for idx in range(attempts):
                    witnesses.append({
                        "address": f"{host}:{port}",
                        "transport": str(ep.get("transport") or ""),
                        "method": "",
                        "path": "",
                        "allowed": bool(ep.get("allowed", False)),
                        "tool_call_id": allowed_call_id,
                        "attempt_index": idx + 1,
                        "source": "tool_result.external_access.endpoints",
                    })

    return witnesses


def _ctrl_workers(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Per-track control 1: update_progress -> task dispatch -> child worker native edit -> promote_overlay."""
    run_id = f"ctrl-workers-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("workers")
    fixture_files = {"worker_target.txt": "baseline line\n"}
    expected_content = "worker landed line\n"
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
    app = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
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
        # 0. Establish required progress checklist before calling `task` (avoids PROGRESS_MISSING).
        _send_tool_call(
            app,
            session_id,
            "update_progress",
            {"content": "## Progress\n- [ ] Update worker_target.txt\n- [ ] Land and verify\n"},
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
        job_id = str(
            ((task_res.get("invocation") or {}).get("evidence") or {}).get("ref")
            or (task_res.get("dispatch") or {}).get("job_id")
            or task_res.get("job_id")
            or ""
        )
        if not job_id:
            raise RuntimeError(f"task did not return a job_id: {task_res}")

        # 2. Park the parent coordinator on `wait` (non-blocking) so `/harness/llm/pending` FIFO
        # yields to `child_sid` instead of deadlocking behind the parent's next turn.
        parent_req = _wait_pending(app, session_id)
        if "wait" not in (parent_req.get("tools") or ()) and "request_tools" in (parent_req.get("tools") or ()):
            load_id = str(uuid.uuid4())
            app.request(
                "/harness/llm/respond",
                {"id": parent_req["id"], "tool_calls": [{"id": load_id, "name": "request_tools", "args": {"need": "wait"}}]},
            )
            _wait_tool_result(app, session_id, load_id)
            parent_req = _wait_pending(app, session_id)
        wait_call_id = str(uuid.uuid4())
        app.request(
            "/harness/llm/respond",
            {
                "id": parent_req["id"],
                "tool_calls": [
                    {
                        "id": wait_call_id,
                        "name": "wait",
                        "args": {"timeout_ms": 60000, "conditions": [{"kind": "next_worker_done"}]},
                    }
                ],
            },
        )

        # 3. Drive the real child worker session's model turns until the worker job settles (fail-fast on error).
        def _child_session_id() -> str | None:
            with contextlib.closing(sqlite3.connect((app_dir / "store.db").resolve().as_uri() + "?mode=ro", uri=True)) as db:
                row = db.execute(
                    "SELECT child_session_id, status, error, failure_json FROM worker_jobs WHERE id=?",
                    (job_id,),
                ).fetchone()
            if not row:
                return None
            if row[1] in {"failed", "canceled", "aborted"}:
                raise RuntimeError(f"worker job {job_id} ended in {row[1]}: error={row[2]!r} failure={row[3]!r}")
            return str(row[0]) if row[0] else None

        child_sid = app.wait_for(_child_session_id, "worker child session id", session_id)
        # Native command `edit` operation (D14) — records genuine agent/worker attribution.
        _send_tool_call(
            app,
            child_sid,
            "command",
            {"command": "cat worker_target.txt"},
        )
        _send_tool_call(
            app,
            child_sid,
            "command",
            {"command": 'edit --path worker_target.txt --old-string "baseline line" --new-string "worker landed line"'},
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
                row = db.execute(
                    "SELECT status, merge_status, error, failure_json FROM worker_jobs WHERE id=?",
                    (job_id,),
                ).fetchone()
            if not row:
                return False
            if row[0] in {"failed", "canceled", "aborted"}:
                raise RuntimeError(f"worker job {job_id} ended in {row[0]}: error={row[2]!r} failure={row[3]!r}")
            return row[0] == "complete"

        app.wait_for(_worker_settled, "worker job settled", session_id)
        _wait_tool_result(app, session_id, wait_call_id)

        # 4. Coordinator promotes the worker's overlay and captures the real tool_result_json.
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
            steps=[{"tool": "update_progress"}, {"tool": "task", "path": "worker_target.txt"}, {"tool": "promote_overlay"}],
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
    engine_root: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Per-track control 2: read + native command `edit` + independent pre-write disk hash + DB verification."""
    run_id = f"ctrl-edits-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("edits")
    fixture_files = {"notes.txt": "alpha\nbeta\n"}
    expected_after = "alpha\nBETA\n"
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
    app = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
    session_id = ""
    open_docs: dict[str, Any] = {}
    read_result: dict[str, Any] = {}
    write_result: dict[str, Any] = {}
    disk_sha_before_write = ""
    try:
        app.ready()
        session_id, project_id, root_id = _open_session(app, proj)
        obs = EditsObserver(proj, app_dir / "store.db")
        obs.capture_pre(["notes.txt"])

        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Read notes.txt and replace beta with BETA."},
        )
        # Establish progress checklist, read via native command `cat`, then mutate via native command `edit` (D14).
        _send_tool_call(
            app,
            session_id,
            "update_progress",
            {"content": "## Progress\n- [ ] Read and edit notes.txt\n"},
        )
        _, read_result, _ = _send_tool_call(app, session_id, "command", {"command": "cat notes.txt"})

        # Independently capture the host-side disk hash immediately before the native edit.
        disk_sha_before_write = str(hash_file(proj / "notes.txt").get("sha256") or "")

        _, write_result, _ = _send_tool_call(
            app,
            session_id,
            "command",
            {"command": "edit --path notes.txt --old-string beta --new-string BETA"},
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
        disk_sha_before_write=disk_sha_before_write,
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
    engine_root: str | pathlib.Path | None = None,
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
        app = _create_application(app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root)
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
            # 1. Refused write outside admitted root via native command redirect (emits WRITE_SCOPE_DENIED / SANDBOX_TRY_WRITE_ROOT).
            _, refused_res, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"printf 'mutated\\n' > {outside_target}"},
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

            # 3. Permitted HTTP fixture request with explicit loopback_connect capability request.
            allowed_call_id, allowed_net_result, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {
                    "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\"",
                    "capability_request": {"loopback_connect": {}},
                },
                checkpoint_policy="approve_chat",
            )

            # 4. Refused HTTP fixture request (reject checkpoint -> MUST never arrive at refused_ep).
            _, refused_net_result, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {
                    "command": f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=3).read()\"",
                    "capability_request": {"loopback_connect": {}},
                },
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
    auth_obs["network_connections"] = [
        {"address": snap["address"], "method": r["method"], "path": r["path"]}
        for snap in (allowed_snap, refused_snap)
        for r in snap["requests"]
    ]

    # Extract product-side network witnesses strictly from real product DB/tool records (NEVER from fixture snaps).
    recorded_connections = _extract_product_network_witnesses(
        auth_obs,
        allowed_call_id=allowed_call_id,
        allowed_tool_result=allowed_net_result,
    )

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
    engine_root: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
    replay_bundle: dict[str, Any] | None = None,  # noqa: ARG001
) -> dict[str, Any]:
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    if track == "workers":
        return _ctrl_workers(out, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root, project_dir=project_dir, export_dir=export_dir)
    if track == "edits":
        return _ctrl_edits(out, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root, project_dir=project_dir, export_dir=export_dir)
    if track == "authority":
        return _ctrl_authority(out, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root, project_dir=project_dir, export_dir=export_dir)
    raise ValueError(f"unknown track: {track}")
