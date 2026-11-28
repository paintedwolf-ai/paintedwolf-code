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
    _init_git_repo,
    _open_session,
    _resolve_engine_root,
    _save_json,
    _send_tool_call,
    _wait_pending,
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


def _query_latest_file_version_ro(db_path: pathlib.Path, project_id: str, rel_path: str) -> dict[str, Any]:
    """Read latest source_versions + source_operations row for rel_path via `?mode=ro`."""
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        row = conn.execute(
            """
            SELECT v.id AS version_id, v.file_id, v.root_id, v.path, v.state, v.content_sha256,
                   v.seq, o.branch_id, o.origin, o.job_id, o.cause, o.command_window_id
            FROM source_versions v
            JOIN source_operations o ON o.id = v.operation_id
            WHERE v.project_id = ? AND v.path = ?
            ORDER BY v.seq DESC LIMIT 1
            """,
            (project_id, rel_path),
        ).fetchone()
        return dict(row) if row else {}


def _list_file_versions_ro(db_path: pathlib.Path, project_id: str, rel_path: str) -> list[dict[str, Any]]:
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        rows = conn.execute(
            """
            SELECT v.id AS version_id, v.file_id, v.root_id, v.path, v.state, v.content_sha256,
                   v.seq, o.branch_id, o.origin, o.job_id, o.cause
            FROM source_versions v
            JOIN source_operations o ON o.id = v.operation_id
            WHERE v.project_id = ? AND v.path = ?
            ORDER BY v.seq ASC
            """,
            (project_id, rel_path),
        ).fetchall()
        return [dict(r) for r in rows]


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

    typed_result: dict[str, Any] = {"branch": branch, "author": author, "project_id": project_id, "root_id": active_root_id}
    failures: list[str] = []

    if author == "person":
        new_text = "line1-base\nline2-person\n"
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
    elif author in {"agent", "worker", "landed_worker"}:
        read_res, write_res, _ = _run_agent_edit_turn(
            app,
            session_id,
            "target.txt",
            "line2-base",
            f"line2-{author}",
            f"Update target.txt for {slot.slot_id}.",
        )
        typed_result.update({
            "read_result": read_res,
            "write_result": write_res,
            "expected_origin": "agent",
        })
    elif author == "command":
        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Append a line via shell command."},
        )
        _, prog_res, _ = _send_tool_call(
            app,
            session_id,
            "update_progress",
            {"content": "## Progress\n- [ ] Update target.txt via command\n"},
        )
        _send_tool_call(app, session_id, "command", {"command": "cat target.txt"})
        _, cmd_res, _ = _send_tool_call(
            app,
            session_id,
            "command",
            {"command": "edit --path target.txt --old-string line2-base --new-string line2-command"},
            checkpoint_policy="approve_once",
        )
        _complete_progress(app, session_id, prog_res, "## Progress\n- [x] Update target.txt via command\n")
        _finish_turn_text(app, session_id)
        typed_result.update({"command_result": cmd_res, "expected_origin": "agent"})
    else:  # external
        (active_root / "target.txt").write_text("line1-base\nline2-external\n", encoding="utf-8")
        # Trigger ledger reconciliation via source read/walk.
        status, resp = _http_call(
            app,
            f"/v1/projects/{project_id}/source/walk?baseline=presentation",
            method="GET",
        )
        typed_result.update({"http_status": status, "walk": resp, "expected_origin": "external"})

    post_obs = obs.capture_post(["target.txt"], session_id=session_id)
    attr_state = observe_attribution_state(db_path, project_id)
    latest = _query_latest_file_version_ro(db_path, project_id, "target.txt")
    post_disk_sha = str(hash_file(active_root / "target.txt").get("sha256") or "")

    if post_disk_sha == pre_sha:
        failures.append("e04_disk_unchanged")
    if author != "external" and latest and latest.get("content_sha256") and latest["content_sha256"] != post_disk_sha:
        failures.append("e04_ledger_sha_mismatch")
    if author == "person" and latest and latest.get("origin") != "user":
        failures.append("e04_wrong_origin")

    ind_obs = {
        "pre_sha256": pre_sha,
        "post_sha256": post_disk_sha,
        "latest_version": latest,
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

    # Produce v1 -> v2 via real agent edit or person PUT so ledger retains versions.
    if var in {"text_restore_api", "text_restore_tool", "disk_change_conflict_refusal", "unavailable_bytes_refusal", "base_version_mismatch_refusal", "idempotent_restore_replay", "git_produced_version_restore"}:
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

    st_walk, walk_resp = _http_call(
        app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET"
    )
    ind = observe_files_marks_counts(db_path, project_id)
    walk_files = (walk_resp.get("files") or []) if isinstance(walk_resp, dict) else []
    walk_ids = sorted(str(f.get("file_id") or "") for f in walk_files if f.get("file_id"))

    failures: list[str] = []
    typed_result: dict[str, Any] = {"variant": var, "walk_status": st_walk, "walk_file_ids": walk_ids}

    if st_walk != 200 or walk_ids != ind["marked_file_ids"]:
        failures.append("e07_marked_files_mismatch")

    if var == "open_marked_file_comparison" and walk_files:
        fid = walk_files[0].get("file_id", "")
        st_cmp, cmp_resp = _http_call(
            app,
            f"/v1/projects/{project_id}/source/comparison?file_id={urllib.parse.quote(fid)}&baseline=presentation",
            method="GET",
        )
        typed_result.update({"comparison_status": st_cmp, "in_range": (cmp_resp or {}).get("in_range")})
        if st_cmp != 200 or not (isinstance(cmp_resp, dict) and cmp_resp.get("in_range") is True):
            failures.append("e07_comparison_not_in_range")
    elif var in {"complete_presentation_clears_mark", "withdraw_presentation_restores_mark"} and walk_files:
        f0 = walk_files[0]
        fid = f0.get("file_id", "")
        eff_id = f0.get("presentation_effect_id", "")
        ordinal = int(f0.get("presentation_ordinal") or f0.get("ordinal") or 0)
        if ordinal <= 0 and eff_id and db_path.is_file():
            with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
                row = conn.execute("SELECT ordinal FROM source_effects WHERE id = ?", (eff_id,)).fetchone()
                if row:
                    ordinal = int(row[0])
        st_seen, seen_resp = _http_call(
            app,
            f"/v1/projects/{project_id}/source/seen",
            {"file_id": fid, "effect_id": eff_id, "ordinal": ordinal},
            method="POST",
        )
        typed_result.update({"complete_status": st_seen, "complete_resp": seen_resp, "ordinal": ordinal})
        if st_seen not in {200, 204}:
            failures.append("e07_complete_presentation_failed")
        ind_after = observe_files_marks_counts(db_path, project_id)
        if var == "withdraw_presentation_restores_mark":
            st_wd, wd_resp = _http_call(
                app,
                f"/v1/projects/{project_id}/source/seen/{urllib.parse.quote(fid)}?through_ordinal={ordinal}",
                method="DELETE",
            )
            typed_result.update({"withdraw_status": st_wd, "withdraw_resp": wd_resp})
            if st_wd not in {200, 204}:
                failures.append("e07_withdraw_presentation_failed")
        ind = ind_after
    elif var == "deterministic_reresolve_worker_isolated":
        st_walk2, walk_resp2 = _http_call(
            app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET"
        )
        walk_ids2 = sorted(
            str(f.get("file_id") or "")
            for f in ((walk_resp2.get("files") or []) if isinstance(walk_resp2, dict) else [])
            if f.get("file_id")
        )
        typed_result["walk2_status"] = st_walk2
        if walk_ids2 != walk_ids:
            failures.append("e07_nondeterministic_reresolve")

    return typed_result, {"independent_counts": ind}, failures


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
