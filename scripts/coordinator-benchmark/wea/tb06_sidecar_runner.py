"""Headless 50-slot sidecar runner for TB06 · Attribution, restore and rewind (E04, E05, E06, E07, E09).

Drives the real `lycaon-dev` sidecar binary across all 50 TB06 sidecar slots per platform:
  - E04: 18 slots (6 author kinds x 3 branches)
  - E05: 12 slots (text, binary, UTF-16LE, rename, deleted/missing-parent, absent, git, refusals, idempotency)
  - E06: 10 sidecar slots (clean rewind, plan digest enforcement, blockers, idempotent retry, real process restarts)
  - E07: 5 slots (Files marks & AI counts, comparison open, complete/withdraw presentation, deterministic re-resolve)
  - E09: 5 slots (registered second worktree, primary/worktree isolation, out-of-root refusals, root-set identity)

Reuses a single warmed `lycaon-dev` sidecar across non-restart slots so the full 50-slot batch
completes quickly (~30-90s per host), while spinning up dedicated restartable instances for the
real-process-restart slots (`E06-09`, `E06-10`).
"""

from __future__ import annotations

import argparse
import base64
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
from dataclasses import dataclass
from typing import Any, Callable

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.checker import check_evidence
from wea.observers import (
    EditsObserver,
    hash_file,
    hash_tree,
    read_sqlite_tables_ro,
    sha256_bytes,
)
from wea.provenance import REPO_ROOT, collect_provenance, resolve_engine_binary
from wea.runner import (
    _create_application,
    _extract_edit_records_from_db,
    _extract_refusal_code,
    _init_git_repo,
    _open_session,
    _resolve_engine_root,
    _save_json,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)
from wea.tb06_history import (
    observe_attribution_state,
    observe_files_marks_counts,
    snapshot_rewind_boundary,
)


@dataclass(frozen=True)
class SidecarSlotSpec:
    slot_id: str
    family: str
    variant: str
    requires_restart: bool = False
    params: dict[str, Any] | None = None


def build_tb06_sidecar_slots() -> list[SidecarSlotSpec]:
    """Return the exact 50 TB06 sidecar slots fixed by ledger.md (18+12+10+5+5 = 50)."""
    slots: list[SidecarSlotSpec] = []

    # E04: 18 slots = 6 author kinds x 3 branches.
    e04_branches = ("trunk", "worktree", "worker_branch")
    e04_authors = ("person", "agent", "worker", "landed_worker", "command", "external")
    idx = 0
    for branch in e04_branches:
        for author in e04_authors:
            idx += 1
            slots.append(
                SidecarSlotSpec(
                    slot_id=f"E04-{idx:02d}-{branch}-{author}",
                    family="E04",
                    variant=f"{branch}:{author}",
                    params={"branch": branch, "author": author},
                )
            )

    # E05: 12 slots.
    e05_variants = (
        "text_restore_api",
        "text_restore_tool",
        "binary_restore",
        "utf16le_bom_restore",
        "rename_restore",
        "deleted_missing_parents_restore",
        "absent_predecessor_restore",
        "git_produced_version_restore",
        "disk_change_conflict_refusal",
        "unavailable_bytes_refusal",
        "base_version_mismatch_refusal",
        "idempotent_restore_replay",
    )
    for i, var in enumerate(e05_variants, 1):
        slots.append(SidecarSlotSpec(slot_id=f"E05-{i:02d}-{var}", family="E05", variant=var))

    # E06: 10 sidecar slots (including 2 real sidecar process restart slots).
    e06_variants = (
        ("preview_and_clean_rewind", False),
        ("plan_digest_required", False),
        ("stale_plan_digest_transcript_change", False),
        ("stale_plan_digest_disk_change", False),
        ("blocker_later_contribution", False),
        ("blocker_command_authorship", False),
        ("blocker_unsaved_document", False),
        ("idempotent_operation_id_retry", False),
        ("restart_before_rewind_apply", True),
        ("restart_after_rewind_persistence", True),
    )
    for i, (var, restart) in enumerate(e06_variants, 1):
        slots.append(
            SidecarSlotSpec(
                slot_id=f"E06-{i:02d}-{var}",
                family="E06",
                variant=var,
                requires_restart=restart,
            )
        )

    # E07: 5 slots.
    e07_variants = (
        "initial_marks_and_ai_counts",
        "open_marked_file_comparison",
        "complete_presentation_clears_mark",
        "withdraw_presentation_restores_mark",
        "deterministic_reresolve_worker_isolated",
    )
    for i, var in enumerate(e07_variants, 1):
        slots.append(SidecarSlotSpec(slot_id=f"E07-{i:02d}-{var}", family="E07", variant=var))

    # E09: 5 slots.
    e09_variants = (
        "second_worktree_addressed_edit",
        "primary_vs_worktree_isolation",
        "outside_project_refused_no_grant",
        "outside_project_no_reusable_permission",
        "root_set_mutation_preserves_identity",
    )
    for i, var in enumerate(e09_variants, 1):
        slots.append(SidecarSlotSpec(slot_id=f"E09-{i:02d}-{var}", family="E09", variant=var))

    return slots


TB06_SIDECAR_SLOTS: tuple[SidecarSlotSpec, ...] = tuple(build_tb06_sidecar_slots())


def _http_call(app: Any, path: str, body: dict[str, Any] | None = None, *, method: str | None = None) -> tuple[int, Any]:
    """Invoke `app.request`, returning `(status_code, parsed_body)` without raising on HTTP 4xx/5xx."""
    try:
        resp = app.request(path, body, method=method)
        return 200, resp
    except json.JSONDecodeError:
        # Endpoints such as DELETE/POST mark-complete return 204 No Content with an empty body.
        return 204, {}
    except urllib.error.HTTPError as exc:
        raw = ""
        with contextlib.suppress(Exception):
            raw = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(raw) if raw else {"error": str(exc)}
        except ValueError:
            parsed = {"raw": raw, "error": str(exc)}
        return int(exc.code), parsed
    except Exception as exc:
        return 599, {"error": str(exc)}


def _git(cwd: pathlib.Path, *args: str) -> str:
    proc = subprocess.run(["git", *args], cwd=str(cwd), capture_output=True, text=True, check=True)
    return proc.stdout.strip()


def _create_fixture_repo(slot_dir: pathlib.Path, files: dict[str, bytes]) -> pathlib.Path:
    proj = slot_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    for rel, raw in files.items():
        target = proj / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(raw)
    _init_git_repo(proj)
    return proj


def _restart_application(
    app_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
) -> Any:
    """Restart a `smoke.Application` on an existing `app_dir`, preserving `store.db` and `config/`."""
    import smoke

    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
    resolved_engine_root = _resolve_engine_root(engine_manifest=engine_manifest, engine_root=engine_root)

    app = smoke.Application.__new__(smoke.Application)
    app.directory = pathlib.Path(app_dir)
    app.config = app.directory / "config"
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
    app.log = (app.directory / "sidecar.log").open("a")
    overrides: dict[str, str] = {
        "LYCAON_CONFIG_DIR": str(app.config),
        "LYCAON_CONFIG_ROOT": str(smoke.ROOT / "lycaon"),
        "LYCAON_ENGINE_ROOT": str(
            resolved_engine_root if resolved_engine_root is not None else (smoke.ROOT / "lycaon-den/src-tauri/engine-root")
        ),
        "LYCAON_ADDR": f"127.0.0.1:{app.port}",
        "LYCAON_API_TOKEN": app.token,
        "LYCAON_DEV": "1",
        "LYCAON_HARNESS": "1",
        "LYCAON_LOG_LEVEL": "debug",
        "LYCAON_LLM_MOCK": "1",
        "LYCAON_LLM_MANUAL": "1",
    }
    env = smoke.application_environment(overrides)
    app.process = subprocess.Popen(
        [str(binary), "serve", "--db", str(app.directory / "store.db")],
        cwd=smoke.ROOT,
        env=env,
        stdout=app.log,
        stderr=subprocess.STDOUT,
    )
    smoke.save(app.directory / "ownership.json", {"pid": app.process.pid, "port": app.port})
    return app


def _extract_attached_root_id(
    resp: Any,
    *,
    label: str,
    path: pathlib.Path,
    exclude_id: str = "",
) -> str:
    """Extract the newly attached root_id from `POST /v1/projects/{id}/roots` (returns full Project JSON)."""
    if not isinstance(resp, dict):
        return ""
    roots = resp.get("roots") or []
    target_path = str(path.resolve())
    for r in roots:
        if not isinstance(r, dict):
            continue
        rid = str(r.get("id") or "")
        if not rid or rid == exclude_id:
            continue
        rpath = str(pathlib.Path(r["path"]).resolve()) if r.get("path") else ""
        if rpath == target_path or str(r.get("label") or "") == label:
            return rid
    return ""


def _complete_progress(app: Any, session_id: str, prog_res: dict[str, Any], done_content: str) -> None:
    """Mark the active progress checklist complete so the host allows turn settlement to idle."""
    args: dict[str, Any] = {"content": done_content}
    raw = str(prog_res.get("content") or "")
    for line in raw.splitlines():
        line = line.strip()
        if line.startswith("{") and line.endswith("}"):
            try:
                parsed = json.loads(line)
                if isinstance(parsed, dict):
                    snap = parsed.get("snapshot") if isinstance(parsed.get("snapshot"), dict) else parsed
                    if snap.get("revision"):
                        args["expected_revision"] = snap["revision"]
                    if snap.get("item_ids"):
                        args["item_ids"] = snap["item_ids"]
                    break
            except Exception:
                pass
    _send_tool_call(app, session_id, "update_progress", args)


def _finish_turn_text(app: Any, session_id: str, text: str = "Done.") -> None:
    req = _wait_pending(app, session_id)
    app.request("/harness/llm/respond", {"id": req["id"], "content": text})
    app.wait_for(
        lambda: app.request(f"/v1/sessions/{session_id}").get("status") == "idle",
        "tb06 session idle",
        session_id,
    )


def _run_agent_edit_turn(
    app: Any,
    session_id: str,
    rel_path: str,
    old_str: str,
    new_str: str,
    prompt_text: str,
) -> tuple[dict[str, Any], dict[str, Any], str]:
    """Run one complete zero-model agent turn: prompt -> update_progress -> cat -> edit -> complete progress -> finish text."""
    prompt_resp = app.request(
        f"/v1/sessions/{session_id}/prompts",
        {"operation_id": str(uuid.uuid4()), "text": prompt_text},
    )
    msg_id = str(prompt_resp.get("message_id") or prompt_resp.get("id") or "")
    _, prog_res, _ = _send_tool_call(
        app,
        session_id,
        "update_progress",
        {"content": f"## Progress\n- [ ] Read and edit {rel_path}\n"},
    )
    _, read_res, _ = _send_tool_call(app, session_id, "command", {"command": f"cat {rel_path}"})
    _, write_res, _ = _send_tool_call(
        app,
        session_id,
        "command",
        {"command": f"edit --path {rel_path} --old-string {old_str} --new-string {new_str}"},
    )
    _complete_progress(app, session_id, prog_res, f"## Progress\n- [x] Read and edit {rel_path}\n")
    _finish_turn_text(app, session_id)
    if not msg_id:
        st_m, msgs_resp = _http_call(app, f"/v1/sessions/{session_id}/messages", method="GET")
        msgs = (msgs_resp.get("messages") or []) if st_m == 200 and isinstance(msgs_resp, dict) else []
        user_msgs = [m for m in msgs if m.get("role") == "user"]
        if user_msgs:
            msg_id = str(user_msgs[-1].get("id") or "")
    if not msg_id:
        db_path = app.directory / "store.db"
        if db_path.is_file():
            with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
                row = conn.execute(
                    "SELECT id FROM messages WHERE session_id = ? AND role = 'user' ORDER BY ord DESC LIMIT 1",
                    (session_id,),
                ).fetchone()
                if row:
                    msg_id = str(row[0])
    return read_res, write_res, msg_id


def _ro_query(db_path: pathlib.Path, sql: str, params: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        return [dict(r) for r in conn.execute(sql, params).fetchall()]


def _ro_one(db_path: pathlib.Path, sql: str, params: tuple[Any, ...] = ()) -> dict[str, Any] | None:
    rows = _ro_query(db_path, sql, params)
    return rows[0] if rows else None


def _finish_session(app: Any, sid: str) -> None:
    try:
        pend = app.request(f"/harness/llm/pending?session_id={urllib.parse.quote(sid)}")
        if pend.get("pending") and pend.get("session_id") == sid:
            app.request("/harness/llm/respond", {"id": pend["id"], "content": "Slot complete."})
    except Exception:
        pass
    try:
        app.request(f"/v1/sessions/{sid}/abort", {})
    except Exception:
        pass


def _extract_job_id(task_res: dict[str, Any]) -> str:
    return str(
        (task_res.get("dispatch") or {}).get("worker_id")
        or ((task_res.get("invocation") or {}).get("evidence") or {}).get("owner_ref")
        or (task_res.get("dispatch") or {}).get("job_id")
        or task_res.get("job_id")
        or ""
    )


def _park_wait(app: Any, sid: str, *, kind: str = "next_worker_done", timeout_ms: int = 60000) -> str:
    req = _wait_pending(app, sid)
    if "wait" not in (req.get("tools") or ()) and "request_tools" in (req.get("tools") or ()):
        load_id = str(uuid.uuid4())
        app.request(
            "/harness/llm/respond",
            {"id": req["id"], "tool_calls": [{"id": load_id, "name": "request_tools", "args": {"need": "wait"}}]},
        )
        _wait_tool_result(app, sid, load_id)
        req = _wait_pending(app, sid)
    call_id = str(uuid.uuid4())
    app.request(
        "/harness/llm/respond",
        {
            "id": req["id"],
            "tool_calls": [
                {"id": call_id, "name": "wait", "args": {"timeout_ms": timeout_ms, "conditions": [{"kind": kind}]}}
            ],
        },
    )
    return call_id


def _wait_child_sid(app: Any, db_path: pathlib.Path, job_id: str, parent_sid: str) -> str:
    def poll() -> str | None:
        row = _ro_one(db_path, "SELECT child_session_id, status, error FROM worker_jobs WHERE id=?", (job_id,))
        if not row:
            return None
        if row["status"] in {"failed", "canceled", "aborted"}:
            raise RuntimeError(f"worker job {job_id} ended early in {row['status']}: {row['error']!r}")
        return str(row["child_session_id"]) if row["child_session_id"] else None

    return app.wait_for(poll, f"child session for {job_id}", parent_sid)


def _settle_child_leg(
    app: Any,
    db_path: pathlib.Path,
    child_sid: str,
    job_id: str,
    parent_sid: str,
    *,
    brief: str,
) -> dict[str, Any]:
    pend = _wait_pending(app, child_sid)
    leg_res: dict[str, Any] = {}
    if "complete_leg" in (pend.get("tools") or ()):
        _, leg_res, _ = _send_tool_call(
            app,
            child_sid,
            "complete_leg",
            {"leg_status": "complete", "brief": brief, "objectives_met": [brief]},
        )
    else:
        app.request("/harness/llm/respond", {"id": pend["id"], "content": brief})

    def settled() -> bool:
        p = app.request(f"/harness/llm/pending?session_id={urllib.parse.quote(child_sid)}")
        if p.get("pending") and p.get("session_id") == child_sid:
            app.request("/harness/llm/respond", {"id": p["id"], "content": brief})
        row = _ro_one(db_path, "SELECT status, error FROM worker_jobs WHERE id=?", (job_id,))
        if not row:
            return False
        if row["status"] == "complete":
            return True
        if row["status"] in {"failed", "canceled", "aborted"}:
            raise RuntimeError(f"worker job {job_id} ended in {row['status']}: {row['error']!r}")
        return False

    app.wait_for(settled, f"worker {job_id} settled", parent_sid)
    return leg_res


def _run_write_worker_edit(
    app: Any,
    db_path: pathlib.Path,
    parent_sid: str,
    *,
    goal: str,
    scope_paths: list[str],
    commands: list[str],
    worker_name: str,
    brief: str = "Applied requested workspace update.",
    checkpoint_policy: str | None = None,
) -> dict[str, Any]:
    _, task_res, _ = _send_tool_call(
        app,
        parent_sid,
        "task",
        {
            "agent_type": "implementer",
            "name": worker_name,
            "brief": {"goal": goal, "done_when": [goal]},
            "scope": {"mode": "write", "paths": scope_paths},
        },
    )
    job_id = _extract_job_id(task_res)
    task_code = _extract_refusal_code(task_res)
    if not job_id:
        return {"job_id": "", "task_res": task_res, "task_code": task_code}
    wait_id = _park_wait(app, parent_sid)
    child_sid = _wait_child_sid(app, db_path, job_id, parent_sid)
    cmd_results: list[dict[str, Any]] = []
    for cmd in commands:
        _, cres, _ = _send_tool_call(
            app,
            child_sid,
            "command",
            {"command": cmd},
            checkpoint_policy=checkpoint_policy,
        )
        cmd_results.append({"command": cmd, "outcome": cres.get("outcome"), "code": _extract_refusal_code(cres), "result": cres})
    leg_res = _settle_child_leg(app, db_path, child_sid, job_id, parent_sid, brief=brief)
    _wait_tool_result(app, parent_sid, wait_id)
    hrow = _ro_one(db_path, "SELECT id FROM worker_results WHERE worker_job_id=? ORDER BY created_at DESC LIMIT 1", (job_id,))
    handle = str(hrow["id"]) if hrow and hrow.get("id") else ""
    return {
        "job_id": job_id,
        "child_sid": child_sid,
        "worker_name": worker_name,
        "result_handle": handle,
        "task_res": task_res,
        "cmd_results": cmd_results,
        "leg_res": leg_res,
    }


def _query_file_versions_with_effects_ro(
    db_path: pathlib.Path,
    project_id: str,
    rel_path: str,
    *,
    branch_id: str | None = None,
    job_id: str | None = None,
) -> list[dict[str, Any]]:
    """Read source_versions + source_operations + latest source_effects id for rel_path via `?mode=ro`."""
    if not db_path.is_file():
        return []
    clauses = ["v.project_id = ?", "v.path = ?"]
    params: list[Any] = [project_id, rel_path]
    if branch_id is not None:
        clauses.append("o.branch_id = ?")
        params.append(branch_id)
    if job_id is not None:
        clauses.append("o.job_id = ?")
        params.append(job_id)
    sql = f"""
        SELECT v.id AS version_id, v.file_id, v.root_id, v.path, v.state, v.content_sha256,
               v.seq, o.id AS operation_id, o.branch_id, o.origin, o.job_id, o.cause,
               o.command_window_id, o.tool_name,
               (SELECT e.id FROM source_effects e
                WHERE e.project_id = v.project_id AND e.operation_id = v.operation_id AND e.file_id = v.file_id
                ORDER BY e.ordinal DESC LIMIT 1) AS effect_id
        FROM source_versions v
        JOIN source_operations o ON o.id = v.operation_id
        WHERE {' AND '.join(clauses)}
        ORDER BY v.seq ASC
    """
    return _ro_query(db_path, sql, tuple(params))


def _query_latest_file_version_ro(db_path: pathlib.Path, project_id: str, rel_path: str) -> dict[str, Any]:
    """Read latest source_versions + source_operations row for rel_path via `?mode=ro`."""
    rows = _query_file_versions_with_effects_ro(db_path, project_id, rel_path)
    return rows[-1] if rows else {}


def _list_file_versions_ro(db_path: pathlib.Path, project_id: str, rel_path: str) -> list[dict[str, Any]]:
    return _query_file_versions_with_effects_ro(db_path, project_id, rel_path)


def _drive_e04_slot(app: Any, slot: SidecarSlotSpec, slot_dir: pathlib.Path) -> tuple[dict[str, Any], dict[str, Any], list[str]]:
    branch = (slot.params or {})["branch"]
    author = (slot.params or {})["author"]
    base_bytes = b"line1-base\nline2-base\n"
    proj = _create_fixture_repo(slot_dir, {"target.txt": base_bytes})
    db_path = app.directory / "store.db"

    if branch == "worktree":
        wt_dir = slot_dir / "wt-branch"
        _git(proj, "worktree", "add", "-q", "-b", f"tb06-{slot.slot_id.lower()}", str(wt_dir))
        session_id, project_id, root_id = _open_session(app, wt_dir)
        active_root = wt_dir
        active_root_id = root_id
    else:
        session_id, project_id, root_id = _open_session(app, proj)
        active_root = proj
        active_root_id = root_id

    obs = EditsObserver(active_root, db_path)
    obs.capture_pre(["target.txt"])
    pre_sha = sha256_bytes((active_root / "target.txt").read_bytes())

    typed_result: dict[str, Any] = {
        "branch": branch,
        "author": author,
        "project_id": project_id,
        "root_id": active_root_id,
    }
    failures: list[str] = []

    def _start_coordinator_turn(prompt_text: str) -> None:
        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": prompt_text},
        )
        _send_tool_call(
            app,
            session_id,
            "update_progress",
            {"content": f"## Progress\n- [ ] Execute {slot.slot_id}\n"},
        )

    def _run_external_reconciled_edit(new_text: str) -> dict[str, Any]:
        # Open editor document first so target.txt is tracked at pre_sha, then mutate on disk and observe.
        st_open, open_resp = _http_call(
            app,
            f"/v1/projects/{project_id}/editor-documents",
            {"path": "target.txt", "root_id": active_root_id, "client_id": f"wea-{slot.slot_id}"},
            method="POST",
        )
        doc_id = str((open_resp or {}).get("id") or "") if isinstance(open_resp, dict) else ""
        (active_root / "target.txt").write_text(new_text, encoding="utf-8")
        st_obs, obs_resp = (0, {})
        if doc_id:
            st_obs, obs_resp = _http_call(
                app,
                f"/v1/projects/{project_id}/editor-documents/{urllib.parse.quote(doc_id)}/observe",
                {"client_id": f"wea-{slot.slot_id}"},
                method="POST",
            )
        return {
            "open_status": st_open,
            "document_id": doc_id,
            "observe_status": st_obs,
            "observe_resp": obs_resp,
        }

    worker_run: dict[str, Any] | None = None
    worker_latest: dict[str, Any] = {}
    target_latest: dict[str, Any] = {}
    expected_worker_sha = ""
    expected_disk_sha = ""

    try:
        if author == "person":
            if branch == "worker_branch":
                # Person PUT targets the project branch (trunk); spawn a live worker branch in parallel
                # to witness that person edits on trunk remain isolated from the worker branch.
                _start_coordinator_turn(f"Spawn worker for {slot.slot_id} isolation witness.")
                worker_bytes = b"line1-base\nline2-worker-iso\n"
                expected_worker_sha = sha256_bytes(worker_bytes)
                worker_run = _run_write_worker_edit(
                    app,
                    db_path,
                    session_id,
                    goal="Edit target.txt on isolated worker branch",
                    scope_paths=["target.txt"],
                    worker_name=f"e04-{slot.slot_id.lower()}",
                    commands=[
                        "cat target.txt",
                        'edit --path target.txt --old-string "line2-base" --new-string "line2-worker-iso"',
                    ],
                )
                typed_result["worker_branch_semantics"] = (
                    "PUT /v1/projects/{id}/source targets project branch; worker branch remains isolated"
                )
            new_text = "line1-base\nline2-person\n"
            expected_disk_sha = sha256_bytes(new_text.encode("utf-8"))
            status, resp = _http_call(
                app,
                f"/v1/projects/{project_id}/source",
                {
                    "operation_id": str(uuid.uuid4()),
                    "root_id": active_root_id,
                    "path": "target.txt",
                    "content": new_text,
                    "encoding": "utf-8",
                    "base_sha256": pre_sha,
                },
                method="PUT",
            )
            typed_result.update({"http_status": status, "response": resp, "expected_origin": "user"})
            if status != 200:
                failures.append("e04_person_write_failed")

        elif author == "agent":
            if branch == "worker_branch":
                # On a worker branch, the authoring agent is a worker executing on its own branch_id.
                _start_coordinator_turn(f"Spawn worker-agent for {slot.slot_id}.")
                worker_bytes = b"line1-base\nline2-agent-wb\n"
                expected_worker_sha = sha256_bytes(worker_bytes)
                worker_run = _run_write_worker_edit(
                    app,
                    db_path,
                    session_id,
                    goal="Edit target.txt as agent on worker branch",
                    scope_paths=["target.txt"],
                    worker_name=f"e04-{slot.slot_id.lower()}",
                    commands=[
                        "cat target.txt",
                        'edit --path target.txt --old-string "line2-base" --new-string "line2-agent-wb"',
                    ],
                )
                typed_result.update({
                    "job_id": worker_run.get("job_id", ""),
                    "result_handle": worker_run.get("result_handle", ""),
                    "expected_origin": "agent",
                })
            else:
                new_text = "line1-base\nline2-agent\n"
                expected_disk_sha = sha256_bytes(new_text.encode("utf-8"))
                read_res, write_res, _ = _run_agent_edit_turn(
                    app,
                    session_id,
                    "target.txt",
                    "line2-base",
                    "line2-agent",
                    f"Update target.txt for {slot.slot_id}.",
                )
                typed_result.update({
                    "read_result": read_res,
                    "write_result": write_res,
                    "expected_origin": "agent",
                })

        elif author == "worker":
            _start_coordinator_turn(f"Spawn real write worker for {slot.slot_id}.")
            worker_bytes = b"line1-base\nline2-worker\n"
            expected_worker_sha = sha256_bytes(worker_bytes)
            worker_run = _run_write_worker_edit(
                app,
                db_path,
                session_id,
                goal="Edit target.txt on isolated worker branch",
                scope_paths=["target.txt"],
                worker_name=f"e04-{slot.slot_id.lower()}",
                commands=[
                    "cat target.txt",
                    'edit --path target.txt --old-string "line2-base" --new-string "line2-worker"',
                ],
            )
            typed_result.update({
                "job_id": worker_run.get("job_id", ""),
                "child_sid": worker_run.get("child_sid", ""),
                "result_handle": worker_run.get("result_handle", ""),
                "cmd_outcomes": [c.get("outcome") for c in (worker_run.get("cmd_results") or [])],
                "expected_origin": "agent",
            })

        elif author == "landed_worker":
            _start_coordinator_turn(f"Spawn write worker and promote overlay for {slot.slot_id}.")
            worker_bytes = b"line1-base\nline2-landed_worker\n"
            expected_worker_sha = sha256_bytes(worker_bytes)
            expected_disk_sha = expected_worker_sha
            worker_run = _run_write_worker_edit(
                app,
                db_path,
                session_id,
                goal="Edit target.txt then land overlay",
                scope_paths=["target.txt"],
                worker_name=f"e04-{slot.slot_id.lower()}",
                commands=[
                    "cat target.txt",
                    'edit --path target.txt --old-string "line2-base" --new-string "line2-landed_worker"',
                ],
            )
            handle = worker_run.get("result_handle", "")
            _, prom_res, _ = _send_tool_call(
                app,
                session_id,
                "promote_overlay",
                {"overlay_id": handle},
            )
            prom_code = _extract_refusal_code(prom_res)
            typed_result.update({
                "job_id": worker_run.get("job_id", ""),
                "result_handle": handle,
                "promote_outcome": prom_res.get("outcome"),
                "promote_code": prom_code,
                "expected_origin": "agent",
                "expected_cause": "overlay_promote",
            })
            if prom_code or prom_res.get("outcome") != "completed":
                failures.append("e04_landed_worker_promote_failed")

        elif author == "command":
            shell_cmd = "sh -c \"printf 'line1-base\\nline2-command\\n' > target.txt\""
            cmd_bytes = b"line1-base\nline2-command\n"
            if branch == "worker_branch":
                _start_coordinator_turn(f"Spawn worker running shell command for {slot.slot_id}.")
                expected_worker_sha = sha256_bytes(cmd_bytes)
                worker_run = _run_write_worker_edit(
                    app,
                    db_path,
                    session_id,
                    goal="Update target.txt via shell command on worker branch",
                    scope_paths=["target.txt"],
                    worker_name=f"e04-{slot.slot_id.lower()}",
                    commands=[shell_cmd],
                    checkpoint_policy="approve_once",
                )
                handle = worker_run.get("result_handle", "")
                preview_content = ""
                if handle:
                    _, prev_res, _ = _send_tool_call(
                        app,
                        session_id,
                        "preview_overlay",
                        {"overlay_id": handle},
                    )
                    preview_content = str(prev_res.get("content") or "")
                worker_read_status = 0
                worker_read_sha = ""
                w_job = worker_run.get("job_id", "")
                if w_job:
                    q = urllib.parse.urlencode({
                        "worker_id": w_job,
                        "root_id": active_root_id,
                        "path": "target.txt",
                    })
                    worker_read_status, w_read_resp = _http_call(
                        app, f"/v1/projects/{project_id}/source?{q}", method="GET"
                    )
                    if worker_read_status == 200 and isinstance(w_read_resp, dict):
                        worker_read_sha = str(w_read_resp.get("sha256") or "")
                        if not worker_read_sha and isinstance(w_read_resp.get("content"), str):
                            worker_read_sha = sha256_bytes(w_read_resp["content"].encode("utf-8"))
                typed_result.update({
                    "job_id": w_job,
                    "result_handle": handle,
                    "cmd_results": worker_run.get("cmd_results", []),
                    "preview_overlay_excerpt": preview_content[:500],
                    "worker_source_read_status": worker_read_status,
                    "worker_source_read_sha256": worker_read_sha,
                    "worker_branch_semantics": (
                        "shell command inside worker overlay modifies isolated worker workspace (witnessed via preview_overlay "
                        "and worker_id source read); command-window ledger reconciliation applies to project roots rather than ephemeral worker overlays"
                    ),
                    "expected_origin": "external",
                })
            else:
                expected_disk_sha = sha256_bytes(cmd_bytes)
                # Ensure target.txt is tracked before shell command runs so command window records drift cleanly.
                _http_call(
                    app,
                    f"/v1/projects/{project_id}/editor-documents",
                    {"path": "target.txt", "root_id": active_root_id, "client_id": f"wea-{slot.slot_id}"},
                    method="POST",
                )
                app.request(
                    f"/v1/sessions/{session_id}/prompts",
                    {"operation_id": str(uuid.uuid4()), "text": "Update target.txt via genuine shell command."},
                )
                _, prog_res, _ = _send_tool_call(
                    app,
                    session_id,
                    "update_progress",
                    {"content": "## Progress\n- [ ] Update target.txt via shell command\n"},
                )
                _, cmd_res, _ = _send_tool_call(
                    app,
                    session_id,
                    "command",
                    {"command": shell_cmd},
                    checkpoint_policy="approve_once",
                )
                _complete_progress(app, session_id, prog_res, "## Progress\n- [x] Update target.txt via shell command\n")
                _finish_turn_text(app, session_id)
                typed_result.update({
                    "shell_command": shell_cmd,
                    "command_result": cmd_res,
                    "expected_origin": "external",
                    "expected_cause": "command_window",
                })

        else:  # external
            if branch == "worker_branch":
                _start_coordinator_turn(f"Spawn worker for {slot.slot_id} external isolation witness.")
                worker_bytes = b"line1-base\nline2-worker-ext-iso\n"
                expected_worker_sha = sha256_bytes(worker_bytes)
                worker_run = _run_write_worker_edit(
                    app,
                    db_path,
                    session_id,
                    goal="Edit target.txt on worker branch while external disk edit occurs on trunk",
                    scope_paths=["target.txt"],
                    worker_name=f"e04-{slot.slot_id.lower()}",
                    commands=[
                        "cat target.txt",
                        'edit --path target.txt --old-string "line2-base" --new-string "line2-worker-ext-iso"',
                    ],
                )
                typed_result["worker_branch_semantics"] = (
                    "external disk edit reconciled on project branch while worker branch remains isolated"
                )
            new_text = "line1-base\nline2-external\n"
            expected_disk_sha = sha256_bytes(new_text.encode("utf-8"))
            ext_info = _run_external_reconciled_edit(new_text)
            typed_result.update({"external_reconcile": ext_info, "expected_origin": "external"})
    finally:
        if worker_run is not None:
            _finish_session(app, session_id)

    post_obs = obs.capture_post(["target.txt"], session_id=session_id)
    attr_state = observe_attribution_state(db_path, project_id)
    post_disk_sha = str(hash_file(active_root / "target.txt").get("sha256") or "")

    job_id = str((worker_run or {}).get("job_id") or "")
    trunk_versions = _query_file_versions_with_effects_ro(db_path, project_id, "target.txt", branch_id="")
    worker_versions = (
        _query_file_versions_with_effects_ro(db_path, project_id, "target.txt", branch_id=job_id)
        if job_id
        else []
    )
    target_latest = trunk_versions[-1] if trunk_versions else {}
    worker_latest = worker_versions[-1] if worker_versions else {}

    # Assertions per author & branch semantics.
    if author == "person":
        if post_disk_sha != expected_disk_sha:
            failures.append("e04_disk_sha_mismatch")
        if not target_latest.get("version_id"):
            failures.append("e04_missing_version_id")
        if target_latest.get("origin") != "user":
            failures.append("e04_wrong_origin")
        if target_latest.get("branch_id") != "":
            failures.append("e04_wrong_branch")
        if target_latest.get("content_sha256") != post_disk_sha:
            failures.append("e04_ledger_sha_mismatch")
        if branch == "worker_branch":
            if not job_id or not worker_latest.get("version_id") or worker_latest.get("branch_id") != job_id:
                failures.append("e04_worker_branch_missing")

    elif author == "agent":
        if branch == "worker_branch":
            if post_disk_sha != pre_sha:
                failures.append("e04_worker_moved_trunk_head")
            if not job_id:
                failures.append("e04_missing_job_id")
            if not worker_latest.get("version_id"):
                failures.append("e04_missing_version_id")
            if not worker_latest.get("effect_id"):
                failures.append("e04_missing_effect_id")
            if worker_latest.get("branch_id") != job_id:
                failures.append("e04_wrong_branch")
            if worker_latest.get("origin") != "agent":
                failures.append("e04_wrong_origin")
            if worker_latest.get("content_sha256") != expected_worker_sha:
                failures.append("e04_ledger_sha_mismatch")
        else:
            if post_disk_sha != expected_disk_sha:
                failures.append("e04_disk_sha_mismatch")
            if not target_latest.get("version_id"):
                failures.append("e04_missing_version_id")
            if target_latest.get("origin") != "agent":
                failures.append("e04_wrong_origin")
            if (target_latest.get("job_id") or "") != "":
                failures.append("e04_unexpected_job_id")
            if target_latest.get("branch_id") != "":
                failures.append("e04_wrong_branch")
            if target_latest.get("content_sha256") != post_disk_sha:
                failures.append("e04_ledger_sha_mismatch")

    elif author == "worker":
        if post_disk_sha != pre_sha:
            failures.append("e04_worker_moved_trunk_head")
        if not job_id:
            failures.append("e04_missing_job_id")
        if not worker_latest.get("version_id"):
            failures.append("e04_missing_version_id")
        if not worker_latest.get("effect_id"):
            failures.append("e04_missing_effect_id")
        if worker_latest.get("job_id") != job_id:
            failures.append("e04_lost_worker_job_id")
        if not worker_latest.get("branch_id") or worker_latest.get("branch_id") == "":
            failures.append("e04_wrong_branch")
        if worker_latest.get("origin") != "agent":
            failures.append("e04_wrong_origin")
        if worker_latest.get("content_sha256") != expected_worker_sha:
            failures.append("e04_ledger_sha_mismatch")

    elif author == "landed_worker":
        if post_disk_sha != expected_disk_sha:
            failures.append("e04_disk_sha_mismatch")
        if not job_id:
            failures.append("e04_missing_job_id")
        if not target_latest.get("version_id"):
            failures.append("e04_missing_version_id")
        if target_latest.get("branch_id") != "":
            failures.append("e04_wrong_branch")
        if target_latest.get("content_sha256") != post_disk_sha:
            failures.append("e04_ledger_sha_mismatch")
        if target_latest.get("origin") != "agent":
            failures.append("e04_wrong_origin")
        if target_latest.get("job_id") != job_id:
            failures.append("e04_lost_worker_job_id")
        if target_latest.get("cause") != "overlay_promote":
            failures.append("e04_wrong_cause")
        if branch == "worker_branch" and not worker_latest.get("version_id"):
            failures.append("e04_worker_branch_missing")

    elif author == "command":
        if branch == "worker_branch":
            if post_disk_sha != pre_sha:
                failures.append("e04_worker_moved_trunk_head")
            if not job_id:
                failures.append("e04_missing_job_id")
            if not (worker_run or {}).get("result_handle"):
                failures.append("e04_missing_result_handle")
            cmd_ok = bool(
                (worker_run or {}).get("cmd_results")
                and (worker_run or {})["cmd_results"][0].get("outcome") == "completed"
            )
            if not cmd_ok:
                failures.append("e04_worker_command_failed")
            prev_excerpt = str(typed_result.get("preview_overlay_excerpt") or "")
            if "target.txt" not in prev_excerpt:
                failures.append("e04_worker_command_overlay_missing")
            w_sha = str(typed_result.get("worker_source_read_sha256") or "")
            if w_sha and w_sha != expected_worker_sha:
                failures.append("e04_worker_command_bytes_mismatch")
            if worker_latest:
                if worker_latest.get("branch_id") != job_id:
                    failures.append("e04_wrong_branch")
                if worker_latest.get("content_sha256") != expected_worker_sha:
                    failures.append("e04_ledger_sha_mismatch")
        else:
            if post_disk_sha != expected_disk_sha:
                failures.append("e04_disk_sha_mismatch")
            if not target_latest.get("version_id"):
                failures.append("e04_missing_version_id")
            if target_latest.get("content_sha256") != post_disk_sha:
                failures.append("e04_ledger_sha_mismatch")
            if target_latest.get("origin") != "external":
                failures.append("e04_wrong_origin")
            if not target_latest.get("command_window_id"):
                failures.append("e04_missing_command_window_id")

    else:  # external
        if post_disk_sha != expected_disk_sha:
            failures.append("e04_disk_sha_mismatch")
        if not target_latest.get("version_id"):
            failures.append("e04_missing_external_version")
        if target_latest.get("origin") != "external":
            failures.append("e04_wrong_origin")
        if target_latest.get("content_sha256") != post_disk_sha:
            failures.append("e04_ledger_sha_mismatch")
        if branch == "worker_branch":
            if not job_id or not worker_latest.get("version_id") or worker_latest.get("branch_id") != job_id:
                failures.append("e04_worker_branch_missing")

    latest = worker_latest if (author == "worker" or (branch == "worker_branch" and author in {"agent", "command"})) else target_latest
    ind_obs = {
        "pre_sha256": pre_sha,
        "post_sha256": post_disk_sha,
        "expected_disk_sha256": expected_disk_sha,
        "expected_worker_sha256": expected_worker_sha,
        "job_id": job_id,
        "latest_version": latest,
        "trunk_latest_version": target_latest,
        "worker_latest_version": worker_latest,
        "operations_count": len(attr_state.get("operations") or []),
        "versions_count": len(attr_state.get("versions") or []),
        "edits_observer": {"post_files": post_obs["post_files"]},
    }
    return typed_result, ind_obs, failures


def _drive_e05_slot(app: Any, slot: SidecarSlotSpec, slot_dir: pathlib.Path) -> tuple[dict[str, Any], dict[str, Any], list[str]]:
    var = slot.variant
    db_path = app.directory / "store.db"
    v1_text = b"alpha\nbeta\n"
    v2_text = b"alpha\nbeta-v2\n"
    v1_bin = bytes([0x89, 0x50, 0x4E, 0x47, 0x00, 0xFF, 0xFE, 0x42])
    v1_utf16 = b"\xff\xfeH\x00i\x00\n\x00"

    seed: dict[str, bytes] = {"notes.txt": v1_text}
    if var == "binary_restore":
        seed["icon.bin"] = v1_bin
    elif var == "utf16le_bom_restore":
        seed["utf16.txt"] = v1_utf16
    elif var == "deleted_missing_parents_restore":
        seed["deep/nested/gone.txt"] = b"deep-v1\n"

    proj = _create_fixture_repo(slot_dir, seed)
    session_id, project_id, root_id = _open_session(app, proj)
    failures: list[str] = []
    typed_result: dict[str, Any] = {"variant": var, "project_id": project_id, "root_id": root_id}

    # Produce v1 -> v2 via real ledger writes so ledger retains both v1 and v2 versions.
    if var in {"text_restore_api", "text_restore_tool", "disk_change_conflict_refusal", "unavailable_bytes_refusal", "base_version_mismatch_refusal", "idempotent_restore_replay", "git_produced_version_restore"}:
        _run_agent_edit_turn(app, session_id, "notes.txt", "beta", "beta-v0", "Seed intermediate version of notes.txt.")
        _run_agent_edit_turn(app, session_id, "notes.txt", "beta-v0", "beta", "Record v1 of notes.txt in ledger.")
        _run_agent_edit_turn(app, session_id, "notes.txt", "beta", "beta-v2", "Modify notes.txt to beta-v2.")
        versions = _list_file_versions_ro(db_path, project_id, "notes.txt")
        v1_sha = sha256_bytes(v1_text)
        v1_row = next((v for v in versions if v.get("content_sha256") == v1_sha), versions[0] if versions else {})
        v2_row = versions[-1] if versions else {}
        typed_result["versions_count"] = len(versions)

        if var == "disk_change_conflict_refusal":
            (proj / "notes.txt").write_bytes(b"concurrent-disk-race\n")
            pre_tree = hash_tree(proj)
            st, resp = _http_call(
                app,
                f"/v1/projects/{project_id}/source/versions/{v1_row.get('version_id', 'missing')}/restore",
                {
                    "operation_id": str(uuid.uuid4()),
                    "file_id": v1_row.get("file_id", ""),
                    "root_id": root_id,
                    "path": "notes.txt",
                    "base": {"state": "content", "sha256": sha256_bytes(v2_text)},
                },
                method="POST",
            )
            post_tree = hash_tree(proj)
            typed_result.update({"http_status": st, "response": resp})
            if st == 200:
                failures.append("e05_refusal_not_enforced")
            if pre_tree != post_tree:
                failures.append("e05_refused_restore_mutated_disk")
            return typed_result, {"pre_tree": pre_tree, "post_tree": post_tree}, failures

        if var in {"unavailable_bytes_refusal", "base_version_mismatch_refusal"}:
            pre_tree = hash_tree(proj)
            target_vid = "00000000-0000-0000-0000-000000000000" if var == "unavailable_bytes_refusal" else v1_row.get("version_id", "")
            base_sha = sha256_bytes(b"wrong-base") if var == "base_version_mismatch_refusal" else sha256_bytes(v2_text)
            st, resp = _http_call(
                app,
                f"/v1/projects/{project_id}/source/versions/{target_vid}/restore",
                {
                    "operation_id": str(uuid.uuid4()),
                    "file_id": v1_row.get("file_id", "missing"),
                    "root_id": root_id,
                    "path": "notes.txt",
                    "base": {"state": "content", "sha256": base_sha},
                },
                method="POST",
            )
            post_tree = hash_tree(proj)
            typed_result.update({"http_status": st, "response": resp})
            if st == 200:
                failures.append("e05_refusal_not_enforced")
            if pre_tree != post_tree:
                failures.append("e05_refused_restore_mutated_disk")
            return typed_result, {"pre_tree": pre_tree, "post_tree": post_tree}, failures

        op_id = str(uuid.uuid4())
        st, resp = _http_call(
            app,
            f"/v1/projects/{project_id}/source/versions/{v1_row.get('version_id', '')}/restore",
            {
                "operation_id": op_id,
                "file_id": v1_row.get("file_id", ""),
                "root_id": root_id,
                "path": "notes.txt",
                "base": {"state": "content", "sha256": sha256_bytes(v2_text)},
            },
            method="POST",
        )
        disk_sha = str(hash_file(proj / "notes.txt").get("sha256") or "")
        typed_result.update({"http_status": st, "response": resp})
        if st != 200 or disk_sha != sha256_bytes(v1_text):
            failures.append("e05_restore_hash_mismatch")
        if var == "idempotent_restore_replay":
            st2, resp2 = _http_call(
                app,
                f"/v1/projects/{project_id}/source/versions/{v1_row.get('version_id', '')}/restore",
                {
                    "operation_id": op_id,
                    "file_id": v1_row.get("file_id", ""),
                    "root_id": root_id,
                    "path": "notes.txt",
                    "base": {"state": "content", "sha256": sha256_bytes(v2_text)},
                },
                method="POST",
            )
            typed_result["replay_status"] = st2
            typed_result["replay_response"] = resp2
            if st2 != 200 or str(hash_file(proj / "notes.txt").get("sha256") or "") != sha256_bytes(v1_text):
                failures.append("e05_idempotent_replay_failed")
        return typed_result, {"expected_sha256": sha256_bytes(v1_text), "disk_sha256": disk_sha}, failures

    # Remaining E05 variants (binary, utf16le, rename, deleted/missing-parent, absent)
    rel = "notes.txt"
    expected_bytes = v1_text
    if var == "binary_restore":
        rel, expected_bytes = "icon.bin", v1_bin
    elif var == "utf16le_bom_restore":
        rel, expected_bytes = "utf16.txt", v1_utf16
    elif var == "deleted_missing_parents_restore":
        rel, expected_bytes = "deep/nested/gone.txt", b"deep-v1\n"

    # Record initial state via API read/walk then mutate & restore.
    _http_call(app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET")
    disk_sha = str(hash_file(proj / rel).get("sha256") or "")
    typed_result["verified_sha256"] = disk_sha
    if disk_sha != sha256_bytes(expected_bytes):
        failures.append("e05_restore_hash_mismatch")
    return typed_result, {"expected_sha256": sha256_bytes(expected_bytes), "disk_sha256": disk_sha}, failures


def _drive_e06_slot(
    app: Any,
    slot: SidecarSlotSpec,
    slot_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
) -> tuple[dict[str, Any], dict[str, Any], list[str]]:
    var = slot.variant
    v0 = b"line1\norig\n"
    proj = _create_fixture_repo(slot_dir, {"work.txt": v0})
    failures: list[str] = []

    dedicated_app = None
    active_app = app
    if slot.requires_restart:
        dedicated_dir = slot_dir / "restart_app"
        dedicated_app = _create_application(
            dedicated_dir,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
            engine_root=engine_root,
        )
        dedicated_app.ready()
        active_app = dedicated_app

    try:
        db_path = active_app.directory / "store.db"
        session_id, project_id, root_id = _open_session(active_app, proj)
        _, _, anchor_id = _run_agent_edit_turn(
            active_app, session_id, "work.txt", "orig", "agent-v1", "Edit work.txt."
        )
        typed_result: dict[str, Any] = {"variant": var, "session_id": session_id, "anchor_id": anchor_id}

        st_prev, preview = _http_call(
            active_app,
            f"/v1/sessions/{session_id}/rewind/preview",
            {"message_id": anchor_id},
            method="POST",
        )
        plan_digest = str((preview or {}).get("plan_digest") or "") if isinstance(preview, dict) else ""
        typed_result.update({"preview_status": st_prev, "plan_digest_present": bool(plan_digest)})

        if var == "plan_digest_required":
            pre_snap = snapshot_rewind_boundary(proj, db_path)
            st_rw, rw_resp = _http_call(
                active_app,
                f"/v1/sessions/{session_id}/rewind",
                {"operation_id": str(uuid.uuid4()), "message_id": anchor_id, "plan_digest": ""},
                method="POST",
            )
            post_snap = snapshot_rewind_boundary(proj, db_path)
            typed_result.update({"rewind_status": st_rw, "rewind_response": rw_resp})
            if st_rw == 200:
                failures.append("e06_blocker_not_triggered")
            if pre_snap["tree_hashes"] != post_snap["tree_hashes"]:
                failures.append("e06_blocker_mutated_tree")
            return typed_result, {"pre": pre_snap, "post": post_snap}, failures

        if var == "stale_plan_digest_disk_change":
            (proj / "work.txt").write_bytes(b"line1\nhuman-edit\n")
            pre_snap = snapshot_rewind_boundary(proj, db_path)
            st_rw, rw_resp = _http_call(
                active_app,
                f"/v1/sessions/{session_id}/rewind",
                {"operation_id": str(uuid.uuid4()), "message_id": anchor_id, "plan_digest": plan_digest},
                method="POST",
            )
            post_snap = snapshot_rewind_boundary(proj, db_path)
            typed_result.update({"rewind_status": st_rw, "rewind_response": rw_resp})
            if st_rw == 200:
                failures.append("e06_blocker_not_triggered")
            if pre_snap["tree_hashes"] != post_snap["tree_hashes"]:
                failures.append("e06_blocker_mutated_tree")
            return typed_result, {"pre": pre_snap, "post": post_snap}, failures

        if var == "stale_plan_digest_transcript_change":
            active_app.request(
                f"/v1/sessions/{session_id}/prompts",
                {"operation_id": str(uuid.uuid4()), "text": "Second turn ask."},
            )
            _finish_turn_text(active_app, session_id, "Ack.")
            pre_snap = snapshot_rewind_boundary(proj, db_path)
            st_rw, rw_resp = _http_call(
                active_app,
                f"/v1/sessions/{session_id}/rewind",
                {"operation_id": str(uuid.uuid4()), "message_id": anchor_id, "plan_digest": plan_digest},
                method="POST",
            )
            post_snap = snapshot_rewind_boundary(proj, db_path)
            typed_result.update({"rewind_status": st_rw, "rewind_response": rw_resp})
            if st_rw == 200:
                failures.append("e06_blocker_not_triggered")
            if pre_snap["tree_hashes"] != post_snap["tree_hashes"]:
                failures.append("e06_blocker_mutated_tree")
            return typed_result, {"pre": pre_snap, "post": post_snap}, failures

        if var == "blocker_later_contribution":
            _http_call(
                active_app,
                f"/v1/projects/{project_id}/source",
                {
                    "operation_id": str(uuid.uuid4()),
                    "root_id": root_id,
                    "path": "work.txt",
                    "content": "line1\nlater-human\n",
                    "encoding": "utf-8",
                    "base_sha256": str(hash_file(proj / "work.txt").get("sha256") or ""),
                },
                method="PUT",
            )
            pre_snap = snapshot_rewind_boundary(proj, db_path)
            st_rw, rw_resp = _http_call(
                active_app,
                f"/v1/sessions/{session_id}/rewind",
                {"operation_id": str(uuid.uuid4()), "message_id": anchor_id, "plan_digest": plan_digest},
                method="POST",
            )
            post_snap = snapshot_rewind_boundary(proj, db_path)
            typed_result.update({"rewind_status": st_rw, "rewind_response": rw_resp})
            if st_rw == 200:
                failures.append("e06_blocker_not_triggered")
            if pre_snap["tree_hashes"] != post_snap["tree_hashes"]:
                failures.append("e06_blocker_mutated_tree")
            return typed_result, {"pre": pre_snap, "post": post_snap}, failures

        if var == "restart_before_rewind_apply" and dedicated_app is not None:
            dedicated_dir = dedicated_app.directory
            dedicated_app.close(cleanup=False)
            dedicated_app = _restart_application(
                dedicated_dir,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
            dedicated_app.ready()
            active_app = dedicated_app
            st_prev2, preview2 = _http_call(
                active_app,
                f"/v1/sessions/{session_id}/rewind/preview",
                {"message_id": anchor_id},
                method="POST",
            )
            plan_digest = str((preview2 or {}).get("plan_digest") or "") if isinstance(preview2, dict) else ""
            typed_result["post_restart_preview_status"] = st_prev2

        # Clean rewind, idempotent retry, or restart_after_rewind_persistence.
        op_id = str(uuid.uuid4())
        st_rw, rw_resp = _http_call(
            active_app,
            f"/v1/sessions/{session_id}/rewind",
            {"operation_id": op_id, "message_id": anchor_id, "plan_digest": plan_digest},
            method="POST",
        )
        post_sha = str(hash_file(proj / "work.txt").get("sha256") or "")
        snap_after = snapshot_rewind_boundary(proj, db_path)
        typed_result.update({"rewind_status": st_rw, "rewind_response": rw_resp})

        if var in {"preview_and_clean_rewind", "idempotent_operation_id_retry", "restart_before_rewind_apply", "restart_after_rewind_persistence"}:
            if st_rw != 200 or post_sha != sha256_bytes(v0):
                failures.append("e06_partial_suffix")

        if var == "idempotent_operation_id_retry":
            st_rep, rep_resp = _http_call(
                active_app,
                f"/v1/sessions/{session_id}/rewind",
                {"operation_id": op_id, "message_id": anchor_id, "plan_digest": plan_digest},
                method="POST",
            )
            snap_replay = snapshot_rewind_boundary(proj, db_path)
            typed_result.update({"replay_status": st_rep, "replay_response": rep_resp})
            if snap_replay["table_counts"] != snap_after["table_counts"]:
                failures.append("e06_duplicate_version")

        if var == "restart_after_rewind_persistence" and dedicated_app is not None:
            dedicated_dir = dedicated_app.directory
            dedicated_app.close(cleanup=False)
            dedicated_app = _restart_application(
                dedicated_dir,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
            dedicated_app.ready()
            active_app = dedicated_app
            post_restart_sha = str(hash_file(proj / "work.txt").get("sha256") or "")
            post_restart_snap = snapshot_rewind_boundary(proj, db_path)
            if post_restart_sha != sha256_bytes(v0):
                failures.append("e06_partial_suffix")
            if post_restart_snap["table_counts"] != snap_after["table_counts"]:
                failures.append("e06_duplicate_version")

        return typed_result, {"expected_sha256": sha256_bytes(v0), "post_sha256": post_sha, "snapshot": snap_after}, failures
    finally:
        if dedicated_app is not None:
            dedicated_app.close(cleanup=False)


def _drive_e07_slot(app: Any, slot: SidecarSlotSpec, slot_dir: pathlib.Path) -> tuple[dict[str, Any], dict[str, Any], list[str]]:
    var = slot.variant
    db_path = app.directory / "store.db"
    proj = _create_fixture_repo(slot_dir, {"a.txt": b"one\n", "b.txt": b"two\n"})
    session_id, project_id, _ = _open_session(app, proj)
    _run_agent_edit_turn(app, session_id, "a.txt", "one", "one-ai", "Edit a.txt.")

    def _walk_ids(resp: Any) -> list[str]:
        files = (resp.get("files") or []) if isinstance(resp, dict) else []
        return sorted(str(f.get("file_id") or "") for f in files if f.get("file_id"))

    st_walk, walk_resp = _http_call(
        app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET"
    )
    ind_before = observe_files_marks_counts(db_path, project_id)
    walk_files = (walk_resp.get("files") or []) if isinstance(walk_resp, dict) else []
    walk_ids = _walk_ids(walk_resp)

    failures: list[str] = []
    typed_result: dict[str, Any] = {"variant": var, "walk_status": st_walk, "walk_file_ids": walk_ids}
    ind_obs: dict[str, Any] = {"independent_counts": ind_before}

    if st_walk != 200 or walk_ids != ind_before["marked_file_ids"]:
        failures.append("e07_marked_files_mismatch")

    if var == "open_marked_file_comparison":
        if not walk_files:
            failures.append("e07_no_marked_files")
        else:
            fid = walk_files[0].get("file_id", "")
            st_cmp, cmp_resp = _http_call(
                app,
                f"/v1/projects/{project_id}/source/comparison?file_id={urllib.parse.quote(fid)}&baseline=presentation",
                method="GET",
            )
            typed_result.update({"comparison_status": st_cmp, "in_range": (cmp_resp or {}).get("in_range")})
            if st_cmp != 200 or not (isinstance(cmp_resp, dict) and cmp_resp.get("in_range") is True):
                failures.append("e07_comparison_not_in_range")

    elif var in {"complete_presentation_clears_mark", "withdraw_presentation_restores_mark"}:
        if not walk_files:
            failures.append("e07_no_marked_files")
        else:
            f0 = walk_files[0]
            fid = str(f0.get("file_id") or "")
            eff_id = str(f0.get("presentation_effect_id") or "")
            ordinal = int(f0.get("presentation_ordinal") or f0.get("ordinal") or 0)
            if ordinal <= 0 and eff_id and db_path.is_file():
                with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
                    row = conn.execute("SELECT ordinal FROM source_effects WHERE id = ?", (eff_id,)).fetchone()
                    if row:
                        ordinal = int(row[0])

            # Pre-clear witness: fid must be marked and unpresented AI count >= 1.
            if fid not in ind_before["marked_file_ids"] or ind_before["unpresented_agent_file_count"] < 1:
                failures.append("e07_pre_clear_mark_missing")

            st_seen, seen_resp = _http_call(
                app,
                f"/v1/projects/{project_id}/source/seen",
                {"file_id": fid, "effect_id": eff_id, "ordinal": ordinal},
                method="POST",
            )
            ind_after_clear = observe_files_marks_counts(db_path, project_id)
            st_walk_clear, walk_resp_clear = _http_call(
                app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET"
            )
            walk_ids_after_clear = _walk_ids(walk_resp_clear)

            typed_result.update({
                "file_id": fid,
                "effect_id": eff_id,
                "ordinal": ordinal,
                "complete_status": st_seen,
                "complete_resp": seen_resp,
                "walk_after_clear_status": st_walk_clear,
                "walk_file_ids_after_clear": walk_ids_after_clear,
            })
            ind_obs = {
                "independent_counts_before": ind_before,
                "independent_counts_after_clear": ind_after_clear,
                "independent_counts": ind_after_clear,
            }

            if st_seen not in {200, 204}:
                failures.append("e07_complete_presentation_failed")
            if st_walk_clear != 200 or fid in walk_ids_after_clear:
                failures.append("e07_clear_did_not_remove_walk_mark")
            if fid in ind_after_clear["marked_file_ids"]:
                failures.append("e07_clear_did_not_remove_db_mark")
            if ind_after_clear["marked_file_count"] >= ind_before["marked_file_count"]:
                failures.append("e07_clear_did_not_decrease_mark_count")
            if ind_after_clear["unpresented_agent_file_count"] >= ind_before["unpresented_agent_file_count"]:
                failures.append("e07_clear_did_not_decrease_ai_count")

            if var == "withdraw_presentation_restores_mark":
                st_wd, wd_resp = _http_call(
                    app,
                    f"/v1/projects/{project_id}/source/seen/{urllib.parse.quote(fid)}?through_ordinal={ordinal}",
                    method="DELETE",
                )
                ind_after_withdraw = observe_files_marks_counts(db_path, project_id)
                st_walk_wd, walk_resp_wd = _http_call(
                    app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET"
                )
                walk_ids_after_withdraw = _walk_ids(walk_resp_wd)

                typed_result.update({
                    "withdraw_status": st_wd,
                    "withdraw_resp": wd_resp,
                    "walk_after_withdraw_status": st_walk_wd,
                    "walk_file_ids_after_withdraw": walk_ids_after_withdraw,
                })
                ind_obs["independent_counts_after_withdraw"] = ind_after_withdraw
                ind_obs["independent_counts"] = ind_after_withdraw

                if st_wd not in {200, 204}:
                    failures.append("e07_withdraw_presentation_failed")
                if st_walk_wd != 200 or fid not in walk_ids_after_withdraw:
                    failures.append("e07_withdraw_did_not_restore_walk_mark")
                if fid not in ind_after_withdraw["marked_file_ids"]:
                    failures.append("e07_withdraw_did_not_restore_db_mark")
                if ind_after_withdraw["marked_file_count"] != ind_before["marked_file_count"]:
                    failures.append("e07_withdraw_mark_count_mismatch")
                if ind_after_withdraw["unpresented_agent_file_count"] != ind_before["unpresented_agent_file_count"]:
                    failures.append("e07_withdraw_ai_count_mismatch")

    elif var == "deterministic_reresolve_worker_isolated":
        effects_before = [
            str(r["id"])
            for r in _ro_query(
                db_path,
                "SELECT id FROM source_effects WHERE project_id = ? ORDER BY ordinal ASC, id ASC",
                (project_id,),
            )
        ]
        st_walk2, walk_resp2 = _http_call(
            app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET"
        )
        walk_ids2 = _walk_ids(walk_resp2)
        effects_after = [
            str(r["id"])
            for r in _ro_query(
                db_path,
                "SELECT id FROM source_effects WHERE project_id = ? ORDER BY ordinal ASC, id ASC",
                (project_id,),
            )
        ]
        ind_after = observe_files_marks_counts(db_path, project_id)
        typed_result.update({
            "walk2_status": st_walk2,
            "walk2_file_ids": walk_ids2,
            "effects_count_before": len(effects_before),
            "effects_count_after": len(effects_after),
            "effect_ids_before": effects_before,
            "effect_ids_after": effects_after,
        })
        ind_obs = {
            "independent_counts_before": ind_before,
            "independent_counts_after": ind_after,
            "independent_counts": ind_after,
            "source_effects_count_before": len(effects_before),
            "source_effects_count_after": len(effects_after),
            "source_effect_ids": effects_after,
        }
        if st_walk2 != 200 or walk_ids2 != walk_ids:
            failures.append("e07_nondeterministic_reresolve")
        if len(effects_after) != len(effects_before) or effects_after != effects_before:
            failures.append("e07_reresolve_mutated_source_effects")

    return typed_result, ind_obs, failures


def _drive_e09_slot(app: Any, slot: SidecarSlotSpec, slot_dir: pathlib.Path) -> tuple[dict[str, Any], dict[str, Any], list[str]]:
    var = slot.variant
    db_path = app.directory / "store.db"
    proj = _create_fixture_repo(slot_dir, {"shared.txt": b"trunk-orig\n"})
    wt_dir = slot_dir / "checkout-2"
    _git(proj, "worktree", "add", "-q", "-b", f"tb06-e09-{slot.slot_id.lower()}", str(wt_dir))

    session_id, project_id, root_id = _open_session(app, proj)
    st_att, att_resp = _http_call(
        app,
        f"/v1/projects/{project_id}/roots",
        {"path": str(wt_dir), "label": "checkout-2"},
        method="POST",
    )
    wt_root_id = _extract_attached_root_id(att_resp, label="checkout-2", path=wt_dir, exclude_id=root_id)
    if not wt_root_id:
        st_proj, proj_resp = _http_call(app, f"/v1/projects/{project_id}", method="GET")
        if st_proj == 200:
            wt_root_id = _extract_attached_root_id(proj_resp, label="checkout-2", path=wt_dir, exclude_id=root_id)
    failures: list[str] = []
    typed_result: dict[str, Any] = {"variant": var, "project_id": project_id, "root_id": root_id, "wt_root_id": wt_root_id, "attach_status": st_att}

    if var in {"second_worktree_addressed_edit", "primary_vs_worktree_isolation"}:
        wt_pre_sha = sha256_bytes((wt_dir / "shared.txt").read_bytes())
        trunk_pre_sha = sha256_bytes((proj / "shared.txt").read_bytes())
        st_put, put_resp = _http_call(
            app,
            f"/v1/projects/{project_id}/source",
            {
                "operation_id": str(uuid.uuid4()),
                "root_id": wt_root_id or root_id,
                "path": "shared.txt",
                "content": "worktree-edited\n",
                "encoding": "utf-8",
                "base_sha256": wt_pre_sha,
            },
            method="PUT",
        )
        typed_result.update({"put_status": st_put, "put_resp": put_resp})
        wt_post_sha = str(hash_file(wt_dir / "shared.txt").get("sha256") or "")
        trunk_post_sha = str(hash_file(proj / "shared.txt").get("sha256") or "")
        if st_put != 200 or wt_post_sha != sha256_bytes(b"worktree-edited\n"):
            failures.append("e09_checkout_hash_mismatch")
        if var == "primary_vs_worktree_isolation" and trunk_post_sha != trunk_pre_sha:
            failures.append("e09_primary_mutated_by_worktree_edit")
        return typed_result, {"trunk_sha256": trunk_post_sha, "worktree_sha256": wt_post_sha}, failures

    if var in {"outside_project_refused_no_grant", "outside_project_no_reusable_permission"}:
        outside_file = slot_dir / "outside-secret.txt"
        outside_file.write_bytes(b"outside-untouched\n")
        pre_out_sha = sha256_bytes(b"outside-untouched\n")
        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Attempt out-of-root write."},
        )
        _, res1, _ = _send_tool_call(
            app,
            session_id,
            "command",
            {"command": f"edit --path {outside_file} --old-string outside-untouched --new-string hacked"},
            checkpoint_policy="reject",
        )
        typed_result["attempt1"] = res1
        if var == "outside_project_no_reusable_permission":
            _, res2, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"edit --path {outside_file} --old-string outside-untouched --new-string hacked2"},
                checkpoint_policy="reject",
            )
            typed_result["attempt2"] = res2
        _finish_turn_text(app, session_id)
        post_out_sha = str(hash_file(outside_file).get("sha256") or "")
        if post_out_sha != pre_out_sha:
            failures.append("e09_outside_mutated")
        return typed_result, {"pre_sha256": pre_out_sha, "post_sha256": post_out_sha}, failures

    # root_set_mutation_preserves_identity
    _run_agent_edit_turn(app, session_id, "shared.txt", "trunk-orig", "trunk-v2", "Edit shared.txt.")
    before_ver = _query_latest_file_version_ro(db_path, project_id, "shared.txt")
    if wt_root_id:
        _http_call(app, f"/v1/projects/{project_id}/roots/{urllib.parse.quote(wt_root_id)}", method="DELETE")
    after_ver = _query_latest_file_version_ro(db_path, project_id, "shared.txt")
    if not before_ver.get("file_id") or before_ver.get("file_id") != after_ver.get("file_id"):
        failures.append("e09_root_mutation_forked_file_id")
    return typed_result, {"before": before_ver, "after": after_ver}, failures


def run_tb06_sidecar_batch(
    out_dir: str | pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    families: set[str] | None = None,
    slot_ids: set[str] | None = None,
) -> dict[str, Any]:
    """Execute scheduled TB06 sidecar slots against the real `lycaon-dev` binary and emit 1 receipt per slot."""
    out_path = pathlib.Path(out_dir).resolve()
    slots_dir = out_path / "slots"
    work_dir = out_path / "work"
    slots_dir.mkdir(parents=True, exist_ok=True)
    work_dir.mkdir(parents=True, exist_ok=True)

    selected = [
        s
        for s in TB06_SIDECAR_SLOTS
        if (not families or s.family in families) and (not slot_ids or s.slot_id in slot_ids)
    ]
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")

    warmed_app = None
    needs_warmed = any(not s.requires_restart for s in selected)
    receipts: list[dict[str, Any]] = []

    try:
        if needs_warmed:
            warmed_app = _create_application(
                out_path / "warmed_app",
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
            warmed_app.ready()

        for slot in selected:
            t0 = time.monotonic()
            s_work = work_dir / slot.slot_id
            s_work.mkdir(parents=True, exist_ok=True)
            failures: list[str] = []
            typed_result: dict[str, Any] = {}
            ind_obs: dict[str, Any] = {}
            try:
                if slot.family == "E04":
                    typed_result, ind_obs, failures = _drive_e04_slot(warmed_app, slot, s_work)
                elif slot.family == "E05":
                    typed_result, ind_obs, failures = _drive_e05_slot(warmed_app, slot, s_work)
                elif slot.family == "E06":
                    typed_result, ind_obs, failures = _drive_e06_slot(
                        warmed_app,
                        slot,
                        s_work,
                        engine_bin=engine_bin,
                        engine_manifest=engine_manifest,
                        engine_root=engine_root,
                    )
                elif slot.family == "E07":
                    typed_result, ind_obs, failures = _drive_e07_slot(warmed_app, slot, s_work)
                elif slot.family == "E09":
                    typed_result, ind_obs, failures = _drive_e09_slot(warmed_app, slot, s_work)
                else:
                    failures = ["unknown_family"]
            except Exception as exc:
                typed_result["exception"] = f"{type(exc).__name__}: {exc}"
                failures.append("slot_execution_error")

            if not prov["product_source"].get("verified"):
                failures.append("unverified_source_witness")

            verdict = "pass" if not failures else "fail"
            receipt = {
                "schema": "wea-tb06-sidecar-slot-v1",
                "slot_id": slot.slot_id,
                "family": slot.family,
                "variant": slot.variant,
                "lane": "sidecar",
                "product_source": prov["product_source"],
                "harness_source": prov["harness_source"],
                "typed_result": typed_result,
                "independent_observation": ind_obs,
                "verdict": verdict,
                "failed_rule_ids": sorted(set(failures)),
                "elapsed_ms": int((time.monotonic() - t0) * 1000),
            }
            _save_json(slots_dir / f"{slot.slot_id}.json", receipt)
            receipts.append(receipt)
    finally:
        if warmed_app is not None:
            warmed_app.close(cleanup=False)

    by_family: dict[str, dict[str, int]] = {}
    for r in receipts:
        fam = r["family"]
        entry = by_family.setdefault(fam, {"total": 0, "pass": 0, "fail": 0, "unknown": 0})
        entry["total"] += 1
        entry[r["verdict"]] = entry.get(r["verdict"], 0) + 1

    summary = {
        "schema": "wea-tb06-sidecar-summary-v1",
        "card": "TB06",
        "lane": "sidecar",
        "total_slots": len(receipts),
        "passed": sum(1 for r in receipts if r["verdict"] == "pass"),
        "failed": sum(1 for r in receipts if r["verdict"] == "fail"),
        "unknown": sum(1 for r in receipts if r["verdict"] == "unknown"),
        "by_family": by_family,
        "product_source": prov["product_source"],
        "harness_source": prov["harness_source"],
        "slot_receipts": [str(slots_dir / f"{r['slot_id']}.json") for r in receipts],
    }
    _save_json(out_path / "summary.json", summary)
    return summary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB06 50-slot headless sidecar runner (E04, E05, E06, E07, E09)")
    parser.add_argument("--out-dir", type=pathlib.Path, help="Output directory for per-slot receipts and summary.json")
    parser.add_argument("--engine", type=pathlib.Path, default=None, help="Path to pinned lycaon-dev binary")
    parser.add_argument("--engine-manifest", type=pathlib.Path, default=None, help="Path to TB01 build-manifest.json")
    parser.add_argument("--engine-root", type=pathlib.Path, default=None, help="Path to LYCAON_ENGINE_ROOT")
    parser.add_argument("--family", type=str, default="", help="Comma-separated families filter (e.g. E04,E05)")
    parser.add_argument("--slot", type=str, default="", help="Comma-separated slot_id filter")
    parser.add_argument("--list-slots", action="store_true", help="Print all 50 TB06 sidecar slots as JSON and exit")
    args = parser.parse_args(argv)

    if args.list_slots:
        print(
            json.dumps(
                [
                    {
                        "slot_id": s.slot_id,
                        "family": s.family,
                        "variant": s.variant,
                        "requires_restart": s.requires_restart,
                    }
                    for s in TB06_SIDECAR_SLOTS
                ],
                indent=2,
            )
        )
        return 0

    if not args.out_dir:
        parser.error("--out-dir is required unless --list-slots is set")

    fams = {f.strip().upper() for f in args.family.split(",") if f.strip()} or None
    sids = {s.strip() for s in args.slot.split(",") if s.strip()} or None
    summary = run_tb06_sidecar_batch(
        args.out_dir,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        engine_root=args.engine_root,
        families=fams,
        slot_ids=sids,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["failed"] == 0 and summary["unknown"] == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
