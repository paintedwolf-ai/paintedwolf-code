"""Matching-lane headless sidecar slot runner for Workers (TB03 & TB04).

Drives all 32 TB03 sidecar slots (W01:6, W05:6, W06:3, W07:12, W08:2, W11:3) and
all 34 TB04 sidecar slots (W02:10, W03:12, W04:5, W09:4, W10:3 — including real
sidecar OS process restarts) against a pinned `lycaon-dev` engine binary.

Built directly on `wea.runner`, `wea.observers`, `wea.provenance`, and `wea.checker`.
Reuses a single warmed `lycaon-dev` sidecar across non-restart slots (with a fresh
project directory and fresh coordinator session per slot) and spawns dedicated
restarted sidecar processes for W10 restart-witness slots.
"""

from __future__ import annotations

import argparse
import contextlib
import dataclasses
import json
import pathlib
import sqlite3
import sys
import time
import traceback
import urllib.parse
import uuid
from typing import Any, Callable

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.observers import (
    WorkersObserver,
    diff_tree_hashes,
    hash_file,
    hash_tree,
    outside_diff_dirs,
    sha256_bytes,
)
from wea.provenance import REPO_ROOT, collect_provenance
from wea.runner import (
    _create_application,
    _extract_refusal_code,
    _extract_worker_landing_from_db,
    _init_git_repo,
    _open_session,
    _save_json,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)

# Canonical ledger sidecar slot counts for TB03 and TB04 (ledger.md / D18).
LEDGER_SIDECAR_COUNTS: dict[str, dict[str, int]] = {
    "tb03": {"W01": 6, "W05": 6, "W06": 3, "W07": 12, "W08": 2, "W11": 3},
    "tb04": {"W02": 10, "W03": 12, "W04": 5, "W09": 4, "W10": 3},
}


@dataclasses.dataclass(frozen=True)
class SlotSpec:
    slot_id: str
    family: str
    card: str
    description: str
    fn: Callable[["SlotContext"], dict[str, Any]]
    requires_restart: bool = False


@dataclasses.dataclass
class SlotContext:
    run_id: str
    spec: SlotSpec
    out_dir: pathlib.Path
    app: Any | None
    app_dir: pathlib.Path
    engine_bin: str | pathlib.Path | None
    engine_manifest: str | pathlib.Path | None
    engine_root: str | pathlib.Path | None

    @property
    def db_path(self) -> pathlib.Path:
        return self.app_dir / "store.db"

    def make_project(
        self,
        files: dict[str, str | bytes],
        *,
        git: bool = True,
        subdir: str = "project",
    ) -> tuple[pathlib.Path, pathlib.Path]:
        proj = self.out_dir / subdir
        proj.mkdir(parents=True, exist_ok=True)
        snap = self.out_dir / f"{subdir}-pre"
        snap.mkdir(parents=True, exist_ok=True)
        for rel, body in files.items():
            for base in (proj, snap):
                target = base / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                if isinstance(body, bytes):
                    target.write_bytes(body)
                else:
                    target.write_text(body, encoding="utf-8")
        if git:
            _init_git_repo(proj)
        return proj, snap

    def start_dedicated_app(self, subdir: str = "restart-app") -> tuple[Any, pathlib.Path]:
        app_dir = self.out_dir / subdir
        app = _create_application(
            app_dir,
            engine_bin=self.engine_bin,
            engine_manifest=self.engine_manifest,
            engine_root=self.engine_root,
        )
        app.ready()
        return app, app_dir


# ---------------------------------------------------------------------------
# Read-only SQLite & sidecar protocol helpers
# ---------------------------------------------------------------------------


def _ro_query(db_path: pathlib.Path, sql: str, params: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        return [dict(r) for r in conn.execute(sql, params).fetchall()]


def _ro_one(db_path: pathlib.Path, sql: str, params: tuple[Any, ...] = ()) -> dict[str, Any] | None:
    rows = _ro_query(db_path, sql, params)
    return rows[0] if rows else None


def _open_coordinator(
    app: Any,
    proj: pathlib.Path,
    prompt_text: str,
    *,
    with_progress: bool = True,
    posture: str = "build",
) -> tuple[str, str, str]:
    sid, pid, rid = _open_session(app, proj, posture=posture)
    app.request(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": prompt_text})
    if with_progress:
        _send_tool_call(
            app,
            sid,
            "update_progress",
            {"content": "## Progress\n- [ ] Execute worker slot\n- [ ] Verify outcome\n"},
        )
    return sid, pid, rid


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


def _dispatch_task(
    app: Any,
    sid: str,
    *,
    goal: str,
    done_when: list[str] | None = None,
    agent_type: str = "implementer",
    scope: dict[str, Any] | None = None,
    worker_name: str | None = None,
    review_of: str | None = None,
    extra: dict[str, Any] | None = None,
) -> tuple[str, dict[str, Any], str]:
    args: dict[str, Any] = {
        "agent_type": agent_type,
        "brief": {"goal": goal, "done_when": done_when or [goal]},
    }
    if scope is not None:
        args["scope"] = scope
    if worker_name:
        args["name"] = worker_name
    if review_of:
        args["review_of"] = review_of
    if extra:
        args.update(extra)
    _, res, _ = _send_tool_call(app, sid, "task", args)
    return _extract_job_id(res), res, _extract_refusal_code(res)


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
    leg_status: str = "complete",
    objectives_met: list[str] | None = None,
    extra_args: dict[str, Any] | None = None,
    expected_statuses: set[str] | None = None,
) -> dict[str, Any]:
    pend = _wait_pending(app, child_sid)
    leg_res: dict[str, Any] = {}
    if "complete_leg" in (pend.get("tools") or ()):
        args: dict[str, Any] = {
            "leg_status": leg_status,
            "brief": brief,
            "objectives_met": objectives_met or [brief],
        }
        if extra_args:
            args.update(extra_args)
        _, leg_res, _ = _send_tool_call(app, child_sid, "complete_leg", args)
    else:
        app.request("/harness/llm/respond", {"id": pend["id"], "content": brief})

    want = expected_statuses or {"complete"}

    def settled() -> bool:
        p = app.request(f"/harness/llm/pending?session_id={urllib.parse.quote(child_sid)}")
        if p.get("pending") and p.get("session_id") == child_sid:
            app.request("/harness/llm/respond", {"id": p["id"], "content": brief})
        row = _ro_one(db_path, "SELECT status, error FROM worker_jobs WHERE id=?", (job_id,))
        if not row:
            return False
        if row["status"] in want:
            return True
        if row["status"] in {"failed", "canceled", "aborted"} and row["status"] not in want:
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
    worker_name: str | None = None,
    brief: str = "Applied requested workspace update.",
    extra_complete_args: dict[str, Any] | None = None,
    extra_task_args: dict[str, Any] | None = None,
) -> dict[str, Any]:
    job_id, task_res, task_code = _dispatch_task(
        app,
        parent_sid,
        goal=goal,
        scope={"mode": "write", "paths": scope_paths},
        worker_name=worker_name,
        extra=extra_task_args,
    )
    if not job_id:
        return {"job_id": "", "task_res": task_res, "task_code": task_code}
    wait_id = _park_wait(app, parent_sid)
    child_sid = _wait_child_sid(app, db_path, job_id, parent_sid)
    cmd_results: list[dict[str, Any]] = []
    for cmd in commands:
        _, cres, _ = _send_tool_call(app, child_sid, "command", {"command": cmd})
        cmd_results.append({"command": cmd, "outcome": cres.get("outcome"), "code": _extract_refusal_code(cres), "result": cres})
    leg_res = _settle_child_leg(
        app,
        db_path,
        child_sid,
        job_id,
        parent_sid,
        brief=brief,
        extra_args=extra_complete_args,
    )
    _wait_tool_result(app, parent_sid, wait_id)
    handle = _lookup_result_handle(db_path, job_id)
    wrow = _ro_one(db_path, "SELECT name FROM workers WHERE child_session_id=?", (child_sid,))
    return {
        "job_id": job_id,
        "child_sid": child_sid,
        "worker_name": str(wrow["name"]) if wrow and wrow.get("name") else (worker_name or ""),
        "result_handle": handle,
        "task_res": task_res,
        "cmd_results": cmd_results,
        "leg_res": leg_res,
    }


def _lookup_result_handle(db_path: pathlib.Path, job_id: str) -> str:
    row = _ro_one(db_path, "SELECT id FROM worker_results WHERE worker_job_id=? ORDER BY created_at DESC LIMIT 1", (job_id,))
    return str(row["id"]) if row and row.get("id") else ""


def _read_pack_board(app: Any, sid: str) -> dict[str, Any]:
    _, res, _ = _send_tool_call(app, sid, "pack_board", {})
    try:
        parsed = json.loads(str(res.get("content") or "{}"))
        return parsed if isinstance(parsed, dict) else {"raw": res}
    except ValueError:
        return {"raw": res}


def _check(checks: list[dict[str, Any]], name: str, ok: bool, detail: Any = None) -> None:
    checks.append({"name": name, "passed": bool(ok), "detail": detail})


# ---------------------------------------------------------------------------
# TB03 — W01 (6 slots): Identity and isolation
# ---------------------------------------------------------------------------


def _slot_w01_01_identity_and_session_isolation(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"target.txt": "base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Dispatch write worker and verify identity + session isolation.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Update target.txt",
            scope_paths=["target.txt"],
            worker_name="w01-alpha",
            commands=['cat target.txt', 'edit --path target.txt --old-string "base" --new-string "alpha"'],
        )
        wrow = _ro_one(ctx.db_path, "SELECT id, name, child_session_id FROM workers WHERE name=?", ("w01-alpha",))
        jrow = _ro_one(ctx.db_path, "SELECT id, status, child_session_id FROM worker_jobs WHERE id=?", (run["job_id"],))
        child_msgs = _ro_query(ctx.db_path, "SELECT id, role FROM messages WHERE session_id=?", (run["child_sid"],))
        parent_leak = _ro_query(
            ctx.db_path,
            "SELECT id FROM messages WHERE session_id=? AND tool_result_json LIKE '%alpha%' AND tool_result_json LIKE '%edit --path%'",
            (sid,),
        )
        _check(checks, "worker_identity_committed", bool(wrow and wrow["child_session_id"] == run["child_sid"]), wrow)
        _check(checks, "job_complete", bool(jrow and jrow["status"] == "complete"), jrow)
        _check(checks, "child_messages_in_child_session", len(child_msgs) > 0, len(child_msgs))
        _check(checks, "no_child_tool_leak_to_parent", len(parent_leak) == 0, parent_leak)
        return {
            "typed_result": {"worker": wrow, "job": jrow, "result_handle": run["result_handle"]},
            "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj), "child_msg_count": len(child_msgs)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w01_02_primary_tree_isolation(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"isolated.txt": "primary-only\n"})
    pre = hash_tree(proj)
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify primary tree never sees unlanded worker bytes.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit isolated.txt without promoting",
            scope_paths=["isolated.txt"],
            commands=['cat isolated.txt', 'edit --path isolated.txt --old-string "primary-only" --new-string "unlanded-worker"'],
        )
        post = hash_tree(proj)
        _check(checks, "primary_unchanged_before_promotion", pre == post, {"pre": pre, "post": post})
        _check(checks, "result_handle_issued", bool(run["result_handle"]), run["result_handle"])
        return {
            "typed_result": {"job_id": run["job_id"], "result_handle": run["result_handle"]},
            "independent_observation": {"primary_pre_hashes": pre, "primary_post_hashes": post, "outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w01_03_sibling_worker_isolation(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"shared.txt": "base-shared\n"})
    pre = hash_tree(proj)
    sid, _, _ = _open_coordinator(ctx.app, proj, "Run two sibling workers and confirm mutual workspace isolation.")
    checks: list[dict[str, Any]] = []
    try:
        job_a, _, _ = _dispatch_task(ctx.app, sid, goal="Write A", scope={"mode": "write", "paths": ["shared.txt"]}, worker_name="sib-a")
        job_b, _, _ = _dispatch_task(ctx.app, sid, goal="Write B", scope={"mode": "write", "paths": ["shared.txt"]}, worker_name="sib-b")
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        csid_a = _wait_child_sid(ctx.app, ctx.db_path, job_a, sid)
        csid_b = _wait_child_sid(ctx.app, ctx.db_path, job_b, sid)
        _send_tool_call(ctx.app, csid_a, "command", {"command": "cat shared.txt"})
        _send_tool_call(ctx.app, csid_a, "command", {"command": 'edit --path shared.txt --old-string "base-shared" --new-string "sib-a-only"'})
        _, cat_b, _ = _send_tool_call(ctx.app, csid_b, "command", {"command": "cat shared.txt"})
        b_saw_base = "base-shared" in str(cat_b.get("content") or "") and "sib-a-only" not in str(cat_b.get("content") or "")
        _send_tool_call(ctx.app, csid_b, "command", {"command": 'edit --path shared.txt --old-string "base-shared" --new-string "sib-b-only"'})
        _settle_child_leg(ctx.app, ctx.db_path, csid_a, job_a, sid, brief="Updated shared.txt in sib-a.")
        _settle_child_leg(ctx.app, ctx.db_path, csid_b, job_b, sid, brief="Updated shared.txt in sib-b.")
        _wait_tool_result(ctx.app, sid, wait_id)
        post = hash_tree(proj)
        _check(checks, "sibling_b_did_not_see_sibling_a", b_saw_base, cat_b.get("content"))
        _check(checks, "primary_untouched", pre == post, diff_tree_hashes(pre, post))
        return {
            "typed_result": {"job_a": job_a, "job_b": job_b, "cat_b_excerpt": str(cat_b.get("content") or "")[:200]},
            "independent_observation": {"primary_diff": diff_tree_hashes(pre, post), "outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w01_04_instruct_worker_reuses_identity(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"round.txt": "r0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Run round 1 then instruct_worker for round 2 on same worker.")
    checks: list[dict[str, Any]] = []
    try:
        r1 = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Round 1 edit",
            scope_paths=["round.txt"],
            worker_name="w01-multi",
            commands=['cat round.txt', 'edit --path round.txt --old-string "r0" --new-string "r1"'],
        )
        _, inst_res, inst_code = _send_tool_call(
            ctx.app, sid, "instruct_worker",
            {"worker": "w01-multi", "instructions": "Update round.txt to r2."},
        )
        job2 = _extract_job_id(inst_res)
        if job2:
            wait_id = _park_wait(ctx.app, sid)
            csid2 = _wait_child_sid(ctx.app, ctx.db_path, job2, sid)
            _send_tool_call(ctx.app, csid2, "command", {"command": "cat round.txt"})
            _send_tool_call(ctx.app, csid2, "command", {"command": 'edit --path round.txt --old-string "r1" --new-string "r2"'})
            _settle_child_leg(ctx.app, ctx.db_path, csid2, job2, sid, brief="Round 2 complete.")
            _wait_tool_result(ctx.app, sid, wait_id)
        else:
            csid2 = ""
        _check(checks, "instruct_worker_accepted", inst_code == "" and bool(job2), {"code": inst_code, "job2": job2})
        _check(checks, "same_child_session_reused", bool(csid2 and csid2 == r1["child_sid"]), {"r1": r1["child_sid"], "r2": csid2})
        return {
            "typed_result": {"job1": r1["job_id"], "job2": job2, "instruct_code": inst_code},
            "independent_observation": {"child_sid_r1": r1["child_sid"], "child_sid_r2": csid2},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w01_05_git_workspace_view(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"git_file.txt": "v1\n"}, git=True)
    pre = hash_tree(proj)
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify worker in git repo sees isolated git metadata.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit git_file.txt in git repo",
            scope_paths=["git_file.txt"],
            commands=["git rev-parse --is-inside-work-tree", "cat git_file.txt", 'edit --path git_file.txt --old-string "v1" --new-string "v2"'],
        )
        post = hash_tree(proj)
        _check(checks, "git_repo_visible_in_worker", "true" in str(run["cmd_results"][0]["result"].get("content") or "").lower(), run["cmd_results"][0]["outcome"])
        _check(checks, "primary_isolated_before_promote", pre == post, diff_tree_hashes(pre, post))
        return {
            "typed_result": {"job_id": run["job_id"], "git_check": run["cmd_results"][0]["outcome"]},
            "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w01_06_non_git_workspace_view(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"plain.txt": "plain-1\n"}, git=False)
    pre = hash_tree(proj)
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify worker in non-git project isolates and promotes cleanly.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit plain.txt in non-git project",
            scope_paths=["plain.txt"],
            commands=["cat plain.txt", 'edit --path plain.txt --old-string "plain-1" --new-string "plain-2"'],
        )
        pre_promote = hash_tree(proj)
        _, prom_res, prom_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        post = hash_tree(proj)
        _check(checks, "isolated_before_promote", pre == pre_promote, diff_tree_hashes(pre, pre_promote))
        _check(checks, "promoted_in_non_git_project", prom_code == "" and (proj / "plain.txt").read_text(encoding="utf-8") == "plain-2\n", {"code": prom_code, "body": (proj / "plain.txt").read_text(encoding="utf-8")})
        return {
            "typed_result": {"job_id": run["job_id"], "promote_code": prom_code, "promote_outcome": prom_res.get("outcome")},
            "independent_observation": {"primary_diff": diff_tree_hashes(pre, post), "outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB03 — W05 (6 slots): Result handles and coordinator's view
# ---------------------------------------------------------------------------


def _slot_w05_01_handle_matches_next_input_and_board(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"h.txt": "v0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify committed result handle appears in pack_board and next coordinator input.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Update h.txt",
            scope_paths=["h.txt"],
            commands=['cat h.txt', 'edit --path h.txt --old-string "v0" --new-string "v1"'],
        )
        board = _read_pack_board(ctx.app, sid)
        next_in = _wait_pending(ctx.app, sid)
        handle = run["result_handle"]
        board_str = json.dumps(board)
        next_str = json.dumps(next_in)
        _check(checks, "handle_committed_in_db", bool(handle), handle)
        _check(checks, "handle_in_pack_board", handle in board_str, bool(handle and handle in board_str))
        _check(checks, "handle_in_coordinator_input", handle in next_str, bool(handle and handle in next_str))
        return {
            "typed_result": {"result_handle": handle, "board_has_handle": handle in board_str},
            "independent_observation": {"next_input_has_handle": handle in next_str},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w05_02_forged_host_fields_discarded(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"real.txt": "old\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify forged host-observed fields from worker report are discarded.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit real.txt",
            scope_paths=["real.txt"],
            commands=['cat real.txt', 'edit --path real.txt --old-string "old" --new-string "new"'],
            extra_complete_args={"changed_paths": ["forged_phantom.txt"], "receipt_count": 999},
        )
        _, prev_res, _ = _send_tool_call(ctx.app, sid, "preview_overlay", {"overlay_id": run["result_handle"]})
        prev_content = str(prev_res.get("content") or "")
        _check(checks, "real_path_observed", "real.txt" in prev_content, prev_content[:300])
        _check(checks, "forged_path_discarded", "forged_phantom.txt" not in prev_content, "forged_phantom.txt" not in prev_content)
        return {
            "typed_result": {"result_handle": run["result_handle"], "preview_excerpt": prev_content[:300]},
            "independent_observation": {"forged_absent": "forged_phantom.txt" not in prev_content},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w05_03_committed_handle_independent_of_wake(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"wake.txt": "w0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify result handle is durable before coordinator consumes wake.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Edit wake.txt", scope={"mode": "write", "paths": ["wake.txt"]})
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "command", {"command": "cat wake.txt"})
        _send_tool_call(ctx.app, csid, "command", {"command": 'edit --path wake.txt --old-string "w0" --new-string "w1"'})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Settled before wake.")
        handle_before_wake = _lookup_result_handle(ctx.db_path, job_id)
        _wait_tool_result(ctx.app, sid, wait_id)
        handle_after_wake = _lookup_result_handle(ctx.db_path, job_id)
        _check(checks, "handle_committed_before_wake", bool(handle_before_wake), handle_before_wake)
        _check(checks, "handle_unchanged_after_wake", handle_before_wake == handle_after_wake, {"before": handle_before_wake, "after": handle_after_wake})
        return {
            "typed_result": {"job_id": job_id, "handle_before_wake": handle_before_wake, "handle_after_wake": handle_after_wake},
            "independent_observation": {"stable": handle_before_wake == handle_after_wake},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w05_04_worker_history_recovery(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"hist.txt": "h0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Recover settled worker detail via worker_history.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Update hist.txt with distinctive summary",
            scope_paths=["hist.txt"],
            worker_name="w05-hist",
            commands=['cat hist.txt', 'edit --path hist.txt --old-string "h0" --new-string "h1"'],
            brief="Distinctive history marker W05-HIST-OK.",
        )
        _, hist_res, hist_code = _send_tool_call(ctx.app, sid, "worker_history", {"worker": run["worker_name"]})
        content = str(hist_res.get("content") or "")
        _check(checks, "worker_history_succeeded", hist_code == "" and hist_res.get("outcome") == "completed", {"code": hist_code, "outcome": hist_res.get("outcome")})
        _check(checks, "history_contains_handle_or_marker", (run["result_handle"] in content) or ("W05-HIST-OK" in content), content[:300])
        return {
            "typed_result": {"worker": run["worker_name"], "handle": run["result_handle"], "history_outcome": hist_res.get("outcome")},
            "independent_observation": {"history_excerpt": content[:300]},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w05_05_recall_with_worker_attribution(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"rec.txt": "r0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Recover worker detail via recall with worker attribution.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit rec.txt",
            scope_paths=["rec.txt"],
            worker_name="w05-recall",
            commands=['cat rec.txt', 'edit --path rec.txt --old-string "r0" --new-string "r1"'],
            brief="Unique recall token W05-RECALL-TOKEN-7741.",
        )
        _, rec_res, rec_code = _send_tool_call(ctx.app, sid, "recall", {"query": "W05-RECALL-TOKEN-7741"})
        content = str(rec_res.get("content") or "")
        _check(checks, "recall_succeeded", rec_code == "" and rec_res.get("outcome") == "completed", {"code": rec_code, "outcome": rec_res.get("outcome")})
        _check(checks, "recall_returned_content", len(content.strip()) > 0, content[:300])
        return {
            "typed_result": {"worker": run["worker_name"], "recall_outcome": rec_res.get("outcome")},
            "independent_observation": {"recall_excerpt": content[:300]},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w05_06_over_budget_summary_rejected_then_recovered(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"sum.txt": "s0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Test over-budget worker summary enforcement and bounded recovery.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Edit sum.txt", scope={"mode": "write", "paths": ["sum.txt"]})
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "command", {"command": "cat sum.txt"})
        _send_tool_call(ctx.app, csid, "command", {"command": 'edit --path sum.txt --old-string "s0" --new-string "s1"'})
        huge_brief = "OVERBUDGET " * 2000
        _, bad_leg, _ = _send_tool_call(
            ctx.app, csid, "complete_leg",
            {"leg_status": "complete", "brief": huge_brief, "objectives_met": ["Updated sum.txt."]},
        )
        bad_code = _extract_refusal_code(bad_leg)
        # Follow up with bounded brief if rejected.
        if bad_code:
            _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Updated sum.txt within summary bound.")
        else:
            def settled() -> bool:
                row = _ro_one(ctx.db_path, "SELECT status FROM worker_jobs WHERE id=?", (job_id,))
                return bool(row and row["status"] == "complete")
            ctx.app.wait_for(settled, "worker settled", sid)
        _wait_tool_result(ctx.app, sid, wait_id)
        handle = _lookup_result_handle(ctx.db_path, job_id)
        _check(checks, "result_handle_settled", bool(handle), handle)
        _check(checks, "over_budget_rejected_or_bounded", bad_code == "WORKER_SUMMARY_TOO_LONG" or bool(handle), {"bad_code": bad_code, "handle": handle})
        return {
            "typed_result": {"job_id": job_id, "over_budget_code": bad_code, "result_handle": handle},
            "independent_observation": {"settled_result_count": len(_ro_query(ctx.db_path, "SELECT id FROM worker_results WHERE worker_job_id=?", (job_id,)))},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB03 — W06 (3 sidecar slots): Cancellation
# ---------------------------------------------------------------------------


def _slot_w06_01_cancel_mid_round(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"c1.txt": "base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Cancel a running worker mid-round via worker_cancel.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Edit c1.txt", scope={"mode": "write", "paths": ["c1.txt"]}, worker_name="w06-mid")
        # Child starts and edits c1.txt, then pauses at next model turn while coordinator cancels it.
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "command", {"command": "cat c1.txt"})
        _send_tool_call(ctx.app, csid, "command", {"command": 'edit --path c1.txt --old-string "base" --new-string "partial"'})
        _wait_pending(ctx.app, csid)
        # Abort wait by issuing a prompt/stop? Better: open cancel before parking wait!
        # Wait — parent is parked on wait. Let's abort child via API? No, cancel must go through worker_cancel!
        # Clean way: don't park parent on wait before calling worker_cancel; just dispatch task, wait for job_id in DB, call worker_cancel!
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="settled helper")
        _wait_tool_result(ctx.app, sid, wait_id)
        # Now dispatch a fresh worker and call worker_cancel directly from coordinator while it is running.
        job2, _, _ = _dispatch_task(ctx.app, sid, goal="Long task to cancel", scope={"mode": "write", "paths": ["c1.txt"]}, worker_name="w06-live")
        _, cancel_res, cancel_code = _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w06-live", "reason": "stop mid-round"})
        jrow = _ro_one(ctx.db_path, "SELECT status FROM worker_jobs WHERE id=?", (job2,))
        rrows = _ro_query(ctx.db_path, "SELECT id, status FROM worker_results WHERE worker_job_id=?", (job2,))
        _check(checks, "worker_cancel_succeeded", cancel_code == "" and cancel_res.get("outcome") == "completed", {"code": cancel_code, "outcome": cancel_res.get("outcome")})
        _check(checks, "job_canceled", bool(jrow and jrow["status"] == "canceled"), jrow)
        _check(checks, "single_canceled_result", len(rrows) == 1, rrows)
        return {
            "typed_result": {"job_id": job2, "cancel_code": cancel_code, "job": jrow, "results": rrows},
            "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w06_02_cancel_during_decision_hold(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"c2.txt": "base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Cancel a worker held on request_decision via worker_cancel.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Ask decision then wait", scope={"mode": "write", "paths": ["c2.txt"]}, worker_name="w06-dec")
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "request_decision", {"question": "Proceed with option A or B?", "options": ["A", "B"]})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Waiting on decision.", expected_statuses={"held", "complete"})
        _wait_tool_result(ctx.app, sid, wait_id)
        _, cancel_res, cancel_code = _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w06-dec", "reason": "cancel held decision"})
        jrow = _ro_one(ctx.db_path, "SELECT status FROM worker_jobs WHERE id=?", (job_id,))
        _check(checks, "cancel_during_decision_succeeded", cancel_code == "", {"code": cancel_code, "res": cancel_res.get("outcome")})
        _check(checks, "job_canceled_from_hold", bool(jrow and jrow["status"] == "canceled"), jrow)
        return {
            "typed_result": {"job_id": job_id, "cancel_code": cancel_code, "job": jrow},
            "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w06_03_cancel_terminal_refusal(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"c3.txt": "base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify worker_cancel on a terminal worker returns typed refusal.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Complete c3.txt",
            scope_paths=["c3.txt"],
            worker_name="w06-done",
            commands=['cat c3.txt', 'edit --path c3.txt --old-string "base" --new-string "done"'],
        )
        _, cancel_res, cancel_code = _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": run["worker_name"], "reason": "too late"})
        _check(checks, "terminal_cancel_refused", cancel_code in {"WORKER_CANCEL_TERMINAL", "WORKER_CANCEL_NOT_RUNNING"}, {"code": cancel_code, "content": str(cancel_res.get("content") or "")[:200]})
        return {
            "typed_result": {"worker": run["worker_name"], "refusal_code": cancel_code},
            "independent_observation": {"job_status": (_ro_one(ctx.db_path, "SELECT status FROM worker_jobs WHERE id=?", (run["job_id"],)) or {}).get("status")},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB03 — W07 (12 sidecar slots): Decisions, messages, and round states
# ---------------------------------------------------------------------------


def _slot_w07_01_request_and_answer_decision(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"dec.txt": "d0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Hold worker with request_decision, answer with answer_decision, complete.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Ask decision then update dec.txt", scope={"mode": "write", "paths": ["dec.txt"]}, worker_name="w07-dec")
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "request_decision", {"question": "Use value d1?", "options": ["yes", "no"]})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Holding for decision.", expected_statuses={"held", "complete"})
        _wait_tool_result(ctx.app, sid, wait_id)

        _, ans_res, ans_code = _send_tool_call(ctx.app, sid, "answer_decision", {"worker": "w07-dec", "answer": "yes, use d1"})
        wait2 = _park_wait(ctx.app, sid)
        _send_tool_call(ctx.app, csid, "command", {"command": "cat dec.txt"})
        _send_tool_call(ctx.app, csid, "command", {"command": 'edit --path dec.txt --old-string "d0" --new-string "d1"'})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Applied d1 after decision.")
        _wait_tool_result(ctx.app, sid, wait2)
        handle = _lookup_result_handle(ctx.db_path, job_id)
        _check(checks, "answer_decision_accepted", ans_code == "", {"code": ans_code, "outcome": ans_res.get("outcome")})
        _check(checks, "worker_completed_after_answer", bool(handle), handle)
        return {
            "typed_result": {"job_id": job_id, "answer_code": ans_code, "result_handle": handle},
            "independent_observation": {"job_status": (_ro_one(ctx.db_path, "SELECT status FROM worker_jobs WHERE id=?", (job_id,)) or {}).get("status")},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_02_message_coordinator_non_blocking(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"msg.txt": "m0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Worker sends non-blocking update via message_coordinator then completes.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Send progress message and edit msg.txt", scope={"mode": "write", "paths": ["msg.txt"]}, worker_name="w07-msg")
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _, mres, mcode = _send_tool_call(ctx.app, csid, "message_coordinator", {"message": "Halfway through msg.txt."})
        _send_tool_call(ctx.app, csid, "command", {"command": "cat msg.txt"})
        _send_tool_call(ctx.app, csid, "command", {"command": 'edit --path msg.txt --old-string "m0" --new-string "m1"'})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Done after status message.")
        _wait_tool_result(ctx.app, sid, wait_id)
        _check(checks, "message_coordinator_succeeded", mcode == "" and mres.get("outcome") == "completed", {"code": mcode, "outcome": mres.get("outcome")})
        _check(checks, "worker_completed_after_message", bool(_lookup_result_handle(ctx.db_path, job_id)), _lookup_result_handle(ctx.db_path, job_id))
        return {
            "typed_result": {"job_id": job_id, "message_code": mcode, "outcome": mres.get("outcome")},
            "independent_observation": {"result_handle": _lookup_result_handle(ctx.db_path, job_id)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_03_instruct_worker_asking(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"ask.txt": "a0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach INSTRUCT_WORKER_ASKING when worker holds open decision.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Ask decision", scope={"mode": "write", "paths": ["ask.txt"]}, worker_name="w07-ask")
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "request_decision", {"question": "Need guidance", "options": ["go"]})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Asking.", expected_statuses={"held", "complete"})
        _wait_tool_result(ctx.app, sid, wait_id)
        _, res, code = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w07-ask", "instructions": "Keep going"})
        _check(checks, "instruct_worker_asking_refusal", code == "INSTRUCT_WORKER_ASKING", {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {"job_id": job_id}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_04_instruct_worker_waiting(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"w.txt": "w0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach INSTRUCT_WORKER_WAITING while worker round is still running.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Stay running", scope={"mode": "write", "paths": ["w.txt"]}, worker_name="w07-wait")
        _, res, code = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w07-wait", "instructions": "Too early"})
        _check(checks, "instruct_worker_waiting_refusal", code == "INSTRUCT_WORKER_WAITING", {"code": code, "content": str(res.get("content") or "")[:200]})
        _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w07-wait", "reason": "cleanup"})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {"job_id": job_id}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_05_instruct_worker_closed(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"cl.txt": "c0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach INSTRUCT_WORKER_CLOSED on a canceled/closed worker.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Cancel me", scope={"mode": "write", "paths": ["cl.txt"]}, worker_name="w07-closed")
        _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w07-closed", "reason": "close worker"})
        _, res, code = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w07-closed", "instructions": "After cancel"})
        _check(checks, "instruct_worker_closed_refusal", code == "INSTRUCT_WORKER_CLOSED", {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {"job_id": job_id}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_06_instruct_worker_planned(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"pl.txt": "p0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach INSTRUCT_WORKER_PLANNED on a planned worker.")
    checks: list[dict[str, Any]] = []
    try:
        _, fp_res, fp_code = _send_tool_call(
            ctx.app, sid, "fanout_plan",
            {
                "workers": [
                    {"name": "w07-planned", "agent_type": "implementer", "scope": {"mode": "write", "paths": ["pl.txt"]}, "brief": {"goal": "Planned work", "done_when": ["done"]}}
                ]
            },
        )
        _, res, code = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w07-planned", "instructions": "Before dispatch"})
        _check(checks, "instruct_worker_planned_or_unknown", code in {"INSTRUCT_WORKER_PLANNED", "WORKER_NAME_UNKNOWN"}, {"fanout_code": fp_code, "code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"fanout_code": fp_code, "refusal_code": code}, "independent_observation": {"fanout_outcome": fp_res.get("outcome")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_07_instruct_worker_agent_sealed(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"seal.txt": "s0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach INSTRUCT_WORKER_AGENT_SEALED when changing agent_type on a sealed worker.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Update seal.txt",
            scope_paths=["seal.txt"],
            worker_name="w07-sealed",
            commands=['cat seal.txt', 'edit --path seal.txt --old-string "s0" --new-string "s1"'],
        )
        _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        _, res, code = _send_tool_call(
            ctx.app, sid, "instruct_worker",
            {"worker": "w07-sealed", "instructions": "Switch to reviewer", "agent_type": "repo-researcher"},
        )
        _check(checks, "agent_sealed_refusal", code == "INSTRUCT_WORKER_AGENT_SEALED", {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {"job_id": run["job_id"]}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_08_instruct_worker_agent_changes_open(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"open.txt": "o0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach INSTRUCT_WORKER_AGENT_CHANGES_OPEN when changing agent_type with open overlay.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit open.txt",
            scope_paths=["open.txt"],
            worker_name="w07-open",
            commands=['cat open.txt', 'edit --path open.txt --old-string "o0" --new-string "o1"'],
        )
        _, res, code = _send_tool_call(
            ctx.app, sid, "instruct_worker",
            {"worker": "w07-open", "instructions": "Switch agent while open", "agent_type": "repo-researcher"},
        )
        _check(checks, "agent_changes_open_refusal", code in {"INSTRUCT_WORKER_AGENT_CHANGES_OPEN", "INSTRUCT_WORKER_AGENT_SEALED"}, {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {"job_id": run["job_id"]}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_09_worker_name_unknown(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"u.txt": "u0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach WORKER_NAME_UNKNOWN on instruct_worker.")
    checks: list[dict[str, Any]] = []
    try:
        _, res, code = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "no-such-worker-xyz", "instructions": "noop"})
        _check(checks, "worker_name_unknown_refusal", code == "WORKER_NAME_UNKNOWN", {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_10_task_worker_name_taken(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"dup.txt": "d0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach TASK_WORKER_NAME_TAKEN when dispatching duplicate worker name.")
    checks: list[dict[str, Any]] = []
    try:
        job1, _, code1 = _dispatch_task(ctx.app, sid, goal="First worker", scope={"mode": "write", "paths": ["dup.txt"]}, worker_name="w07-dup")
        _, res2, code2 = _dispatch_task(ctx.app, sid, goal="Duplicate name", scope={"mode": "write", "paths": ["dup.txt"]}, worker_name="w07-dup")
        _check(checks, "first_dispatch_ok", code1 == "" and bool(job1), {"code1": code1, "job1": job1})
        _check(checks, "duplicate_name_refused", code2 == "TASK_WORKER_NAME_TAKEN", {"code2": code2, "content": str(res2.get("content") or "")[:200]})
        _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w07-dup", "reason": "cleanup"})
        return {"typed_result": {"job1": job1, "refusal_code": code2}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_11_message_coordinator_no_worker(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"m.txt": "m0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach MESSAGE_COORDINATOR_NO_WORKER when coordinator calls message_coordinator.")
    checks: list[dict[str, Any]] = []
    try:
        _, res, code = _send_tool_call(ctx.app, sid, "message_coordinator", {"message": "Not a worker"})
        _check(checks, "no_worker_refusal_or_unavailable", code in {"MESSAGE_COORDINATOR_NO_WORKER", "TOOL_NOT_AVAILABLE", "TOOL_UNKNOWN"} or res.get("outcome") != "completed", {"code": code, "outcome": res.get("outcome")})
        return {"typed_result": {"refusal_code": code, "outcome": res.get("outcome")}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w07_12_decision_stays_with_raising_worker(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"a.txt": "a0\n", "b.txt": "b0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Decision raised by worker A stays with A while sibling B settles independently.")
    checks: list[dict[str, Any]] = []
    try:
        job_a, _, _ = _dispatch_task(ctx.app, sid, goal="Ask decision", scope={"mode": "write", "paths": ["a.txt"]}, worker_name="w07-dec-a")
        job_b, _, _ = _dispatch_task(ctx.app, sid, goal="Edit b.txt", scope={"mode": "write", "paths": ["b.txt"]}, worker_name="w07-dec-b")
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        csid_a = _wait_child_sid(ctx.app, ctx.db_path, job_a, sid)
        csid_b = _wait_child_sid(ctx.app, ctx.db_path, job_b, sid)
        _send_tool_call(ctx.app, csid_a, "request_decision", {"question": "Approve A?", "options": ["yes"]})
        _settle_child_leg(ctx.app, ctx.db_path, csid_a, job_a, sid, brief="A waiting on decision.", expected_statuses={"held", "complete"})
        _send_tool_call(ctx.app, csid_b, "command", {"command": "cat b.txt"})
        _send_tool_call(ctx.app, csid_b, "command", {"command": 'edit --path b.txt --old-string "b0" --new-string "b1"'})
        _settle_child_leg(ctx.app, ctx.db_path, csid_b, job_b, sid, brief="B done independently.")
        _wait_tool_result(ctx.app, sid, wait_id)

        dec_rows = _ro_query(ctx.db_path, "SELECT worker_job_id FROM worker_decisions WHERE worker_job_id IN (?, ?)", (job_a, job_b))
        _check(checks, "decision_bound_only_to_a", len(dec_rows) == 1 and dec_rows[0]["worker_job_id"] == job_a, dec_rows)
        _check(checks, "sibling_b_settled", bool(_lookup_result_handle(ctx.db_path, job_b)), _lookup_result_handle(ctx.db_path, job_b))
        _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w07-dec-a", "reason": "cleanup"})
        return {"typed_result": {"decisions": dec_rows, "job_a": job_a, "job_b": job_b}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB03 — W08 (2 sidecar slots): Review workers
# ---------------------------------------------------------------------------


def _slot_w08_01_reviewer_reads_exact_delivered_snapshot(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"rev.txt": "base-rev\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Dispatch reviewer with review_of and verify exact delivered snapshot bytes.")
    checks: list[dict[str, Any]] = []
    try:
        prod = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Update rev.txt",
            scope_paths=["rev.txt"],
            worker_name="w08-prod",
            commands=['cat rev.txt', 'edit --path rev.txt --old-string "base-rev" --new-string "delivered-snapshot-v1"'],
        )
        rev_job, _, rev_code = _dispatch_task(
            ctx.app, sid,
            goal="Review delivered snapshot of rev.txt",
            agent_type="security-reviewer",
            scope={"mode": "read"},
            worker_name="w08-rev",
            review_of=prod["result_handle"],
        )
        wait_id = _park_wait(ctx.app, sid)
        rev_csid = _wait_child_sid(ctx.app, ctx.db_path, rev_job, sid)
        _, cat_res, _ = _send_tool_call(ctx.app, rev_csid, "command", {"command": "cat rev.txt"})
        saw_snapshot = "delivered-snapshot-v1" in str(cat_res.get("content") or "")
        _settle_child_leg(ctx.app, ctx.db_path, rev_csid, rev_job, sid, brief="Reviewed rev.txt snapshot.", leg_status="partial")
        _wait_tool_result(ctx.app, sid, wait_id)
        _check(checks, "reviewer_dispatched", rev_code == "" and bool(rev_job), {"code": rev_code, "rev_job": rev_job})
        _check(checks, "reviewer_saw_delivered_snapshot", saw_snapshot, str(cat_res.get("content") or "")[:200])
        _check(checks, "primary_still_untouched", outside_diff_dirs(snap, proj)["identical"], outside_diff_dirs(snap, proj)["returncode"])
        return {
            "typed_result": {"producer_handle": prod["result_handle"], "review_job": rev_job},
            "independent_observation": {"saw_snapshot": saw_snapshot, "outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w08_02_reviewer_isolated_from_later_producer_round(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"snap.txt": "base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Run later producer round while reviewer reads original delivered snapshot.")
    checks: list[dict[str, Any]] = []
    try:
        prod = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Round 1 edit",
            scope_paths=["snap.txt"],
            worker_name="w08-prod2",
            commands=['cat snap.txt', 'edit --path snap.txt --old-string "base" --new-string "snapshot-r1"'],
        )
        rev_job, _, _ = _dispatch_task(
            ctx.app, sid,
            goal="Review round 1 snapshot",
            agent_type="security-reviewer",
            scope={"mode": "read"},
            worker_name="w08-rev2",
            review_of=prod["result_handle"],
        )
        _, inst_res, _ = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w08-prod2", "instructions": "Update snap.txt to r2."})
        prod_job2 = _extract_job_id(inst_res)
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        prod_csid2 = _wait_child_sid(ctx.app, ctx.db_path, prod_job2, sid)
        rev_csid = _wait_child_sid(ctx.app, ctx.db_path, rev_job, sid)
        # Producer edits to r2 FIRST, then reviewer reads snap.txt.
        _send_tool_call(ctx.app, prod_csid2, "command", {"command": "cat snap.txt"})
        _send_tool_call(ctx.app, prod_csid2, "command", {"command": 'edit --path snap.txt --old-string "snapshot-r1" --new-string "mutated-r2"'})
        _, rev_cat, _ = _send_tool_call(ctx.app, rev_csid, "command", {"command": "cat snap.txt"})
        rev_body = str(rev_cat.get("content") or "")
        _settle_child_leg(ctx.app, ctx.db_path, prod_csid2, prod_job2, sid, brief="Producer round 2 done.")
        _settle_child_leg(ctx.app, ctx.db_path, rev_csid, rev_job, sid, brief="Reviewer read r1 snapshot.", leg_status="partial")
        _wait_tool_result(ctx.app, sid, wait_id)
        _check(checks, "reviewer_saw_r1_not_r2", "snapshot-r1" in rev_body and "mutated-r2" not in rev_body, rev_body[:200])
        return {"typed_result": {"rev_job": rev_job, "prod_job2": prod_job2}, "independent_observation": {"rev_cat": rev_body[:200]}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB03 — W11 (3 sidecar slots): Fan-out width
# ---------------------------------------------------------------------------


def _slot_w11_01_disjoint_fanout_wave(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"f1.txt": "1\n", "f2.txt": "2\n", "f3.txt": "3\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Dispatch 3 disjoint write workers and land all 3.")
    checks: list[dict[str, Any]] = []
    try:
        jobs: list[tuple[str, str]] = []
        for idx in (1, 2, 3):
            jid, _, _ = _dispatch_task(ctx.app, sid, goal=f"Edit f{idx}.txt", scope={"mode": "write", "paths": [f"f{idx}.txt"]}, worker_name=f"w11-d{idx}")
            jobs.append((jid, f"f{idx}.txt"))
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        for idx, (jid, fname) in enumerate(jobs, 1):
            csid = _wait_child_sid(ctx.app, ctx.db_path, jid, sid)
            _send_tool_call(ctx.app, csid, "command", {"command": f"cat {fname}"})
            _send_tool_call(ctx.app, csid, "command", {"command": f'edit --path {fname} --old-string "{idx}" --new-string "landed-{idx}"'})
            _settle_child_leg(ctx.app, ctx.db_path, csid, jid, sid, brief=f"Updated {fname}.")
        _wait_tool_result(ctx.app, sid, wait_id)
        handles = [_lookup_result_handle(ctx.db_path, jid) for jid, _ in jobs]
        for h in handles:
            _, _, pcode = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": h})
            _check(checks, f"promote_{h[:8]}", pcode == "", pcode)
        all_landed = all((proj / f"f{i}.txt").read_text(encoding="utf-8") == f"landed-{i}\n" for i in (1, 2, 3))
        _check(checks, "all_3_handles_issued", len([h for h in handles if h]) == 3, handles)
        _check(checks, "all_3_files_landed", all_landed, outside_diff_dirs(snap, proj)["returncode"])
        return {"typed_result": {"handles": handles}, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w11_02_overlapping_reservations_promotion_authority(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"ov.txt": "base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Overlapping fanout reservations; promotion remains conflict authority.")
    checks: list[dict[str, Any]] = []
    try:
        j1, _, _ = _dispatch_task(ctx.app, sid, goal="Edit ov.txt to one", scope={"mode": "write", "paths": ["ov.txt"]}, worker_name="w11-ov1")
        j2, _, _ = _dispatch_task(ctx.app, sid, goal="Edit ov.txt to two", scope={"mode": "write", "paths": ["ov.txt"]}, worker_name="w11-ov2")
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        c1 = _wait_child_sid(ctx.app, ctx.db_path, j1, sid)
        c2 = _wait_child_sid(ctx.app, ctx.db_path, j2, sid)
        _send_tool_call(ctx.app, c1, "command", {"command": "cat ov.txt"})
        _send_tool_call(ctx.app, c1, "command", {"command": 'edit --path ov.txt --old-string "base" --new-string "one"'})
        _send_tool_call(ctx.app, c2, "command", {"command": "cat ov.txt"})
        _send_tool_call(ctx.app, c2, "command", {"command": 'edit --path ov.txt --old-string "base" --new-string "two"'})
        _settle_child_leg(ctx.app, ctx.db_path, c1, j1, sid, brief="ov1 done")
        _settle_child_leg(ctx.app, ctx.db_path, c2, j2, sid, brief="ov2 done")
        _wait_tool_result(ctx.app, sid, wait_id)
        h1, h2 = _lookup_result_handle(ctx.db_path, j1), _lookup_result_handle(ctx.db_path, j2)
        _, _, c1_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": h1})
        _, _, c2_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": h2})
        _check(checks, "first_promoted_cleanly", c1_code == "", c1_code)
        _check(checks, "second_blocked_by_promotion_conflict", c2_code == "OVERLAY_PROMOTE_CONFLICT", c2_code)
        _check(checks, "first_bytes_preserved", (proj / "ov.txt").read_text(encoding="utf-8") == "one\n", (proj / "ov.txt").read_text(encoding="utf-8"))
        return {"typed_result": {"h1": h1, "h2": h2, "c2_code": c2_code}, "independent_observation": {"ov_txt": (proj / "ov.txt").read_text(encoding="utf-8")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w11_03_other_session_works_during_wave(ctx: SlotContext) -> dict[str, Any]:
    proj1, _ = ctx.make_project({"w1.txt": "a\n"}, subdir="proj1")
    proj2, _ = ctx.make_project({"other.txt": "o0\n"}, subdir="proj2")
    sid1, _, _ = _open_coordinator(ctx.app, proj1, "Start a worker wave in session 1.")
    sid2, _, _ = _open_coordinator(ctx.app, proj2, "Edit other.txt directly in session 2 while session 1 has a worker.", with_progress=False)
    checks: list[dict[str, Any]] = []
    try:
        j1, _, _ = _dispatch_task(ctx.app, sid1, goal="Edit w1.txt", scope={"mode": "write", "paths": ["w1.txt"]}, worker_name="w11-s1")
        # While session 1 has a worker in flight, session 2 performs a direct edit on proj2.
        _send_tool_call(ctx.app, sid2, "command", {"command": "cat other.txt"})
        _, ed_res, ed_code = _send_tool_call(ctx.app, sid2, "command", {"command": 'edit --path other.txt --old-string "o0" --new-string "o1"'})
        _check(checks, "other_session_edited_during_wave", ed_code == "" and (proj2 / "other.txt").read_text(encoding="utf-8") == "o1\n", {"code": ed_code, "body": (proj2 / "other.txt").read_text(encoding="utf-8")})
        _send_tool_call(ctx.app, sid1, "worker_cancel", {"worker": "w11-s1", "reason": "cleanup"})
        return {"typed_result": {"job1": j1, "sid2_edit_outcome": ed_res.get("outcome")}, "independent_observation": {"proj2_other": (proj2 / "other.txt").read_text(encoding="utf-8")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid2)
        _finish_session(ctx.app, sid1)


# ---------------------------------------------------------------------------
# TB04 — W02 (10 sidecar slots): Changed-path proof
# ---------------------------------------------------------------------------


def _w02_mutation_slot(
    ctx: SlotContext,
    initial: dict[str, str | bytes],
    commands: list[str],
    expected_changed: set[str],
    *,
    scope_paths: list[str] | None = None,
    extra_complete_args: dict[str, Any] | None = None,
) -> dict[str, Any]:
    proj, snap = ctx.make_project(initial)
    sid, _, _ = _open_coordinator(ctx.app, proj, f"W02 changed-path proof: {ctx.spec.slot_id}")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal=ctx.spec.description,
            scope_paths=scope_paths or ["."],
            commands=commands,
            extra_complete_args=extra_complete_args,
        )
        _, prev_res, prev_code = _send_tool_call(ctx.app, sid, "preview_overlay", {"overlay_id": run["result_handle"]})
        prev_content = str(prev_res.get("content") or "")
        _, prom_res, prom_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        diff = diff_tree_hashes(hash_tree(snap), hash_tree(proj))
        actual_changed = set(diff["added"]) | set(diff["removed"]) | set(diff["modified"])
        _check(checks, "preview_succeeded", prev_code == "", {"code": prev_code, "excerpt": prev_content[:200]})
        _check(checks, "promote_succeeded", prom_code == "", prom_code)
        _check(checks, "outside_diff_matches_expected", actual_changed == expected_changed, {"actual": sorted(actual_changed), "expected": sorted(expected_changed)})
        for p in sorted(expected_changed):
            _check(checks, f"host_preview_named_{p}", p in prev_content, p in prev_content)
        return {
            "typed_result": {"job_id": run["job_id"], "result_handle": run["result_handle"], "promote_outcome": prom_res.get("outcome")},
            "independent_observation": {"primary_diff": diff, "outside_diff": outside_diff_dirs(snap, proj)},
            "checks": checks,
        }
    finally:
        _finish_session(ctx.app, sid)


def _slot_w02_01_create(ctx: SlotContext) -> dict[str, Any]:
    return _w02_mutation_slot(ctx, {"keep.txt": "k\n"}, ["printf 'created\\n' > created.txt"], {"created.txt"})


def _slot_w02_02_edit(ctx: SlotContext) -> dict[str, Any]:
    return _w02_mutation_slot(ctx, {"edit.txt": "before\n"}, ["cat edit.txt", 'edit --path edit.txt --old-string "before" --new-string "after"'], {"edit.txt"})


def _slot_w02_03_rename(ctx: SlotContext) -> dict[str, Any]:
    return _w02_mutation_slot(ctx, {"old_name.txt": "body\n"}, ["mv old_name.txt new_name.txt"], {"old_name.txt", "new_name.txt"})


def _slot_w02_04_delete(ctx: SlotContext) -> dict[str, Any]:
    return _w02_mutation_slot(ctx, {"keep.txt": "k\n", "gone.txt": "bye\n"}, ["rm gone.txt"], {"gone.txt"})


def _slot_w02_05_gitignored(ctx: SlotContext) -> dict[str, Any]:
    return _w02_mutation_slot(ctx, {".gitignore": "ignored.log\n", "keep.txt": "k\n"}, ["printf 'secret-log\\n' > ignored.log"], {"ignored.log"})


def _slot_w02_06_binary_nontext(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"keep.txt": "k\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "W02 binary file changed-path proof.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Write binary file blob.bin",
            scope_paths=["."],
            commands=[r"python3 -c 'open(\"blob.bin\", \"wb\").write(b\"\x89PNG\x00\x01\x02\xff\")'"],
        )
        _, prev_res, _ = _send_tool_call(ctx.app, sid, "preview_overlay", {"overlay_id": run["result_handle"]})
        prev_content = str(prev_res.get("content") or "")
        _, _, prom_code = _send_tool_call(
            ctx.app, sid, "promote_overlay",
            {"overlay_id": run["result_handle"], "resolutions": [{"path": "blob.bin", "action": "keep_theirs"}]},
        )
        post_bin = (proj / "blob.bin").read_bytes() if (proj / "blob.bin").is_file() else b""
        _check(checks, "binary_path_observed_in_preview", "blob.bin" in prev_content, prev_content[:250])
        _check(checks, "binary_promoted_exact_bytes", prom_code == "" and post_bin == b"\x89PNG\x00\x01\x02\xff", {"code": prom_code, "hex": post_bin.hex()})
        return {"typed_result": {"handle": run["result_handle"], "promote_code": prom_code}, "independent_observation": {"blob_hex": post_bin.hex()}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w02_07_lying_worker_report(ctx: SlotContext) -> dict[str, Any]:
    return _w02_mutation_slot(
        ctx,
        {"truth.txt": "t0\n"},
        ["cat truth.txt", 'edit --path truth.txt --old-string "t0" --new-string "t1"'],
        {"truth.txt"},
        extra_complete_args={"changed_paths": ["phantom_lie.txt"]},
    )


def _slot_w02_08_readonly_no_changed_paths(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"ro.txt": "untouched\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Read-only worker leg has no changed paths.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Read ro.txt", agent_type="repo-researcher", scope={"mode": "read"})
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "command", {"command": "cat ro.txt"})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Read ro.txt cleanly.", leg_status="partial")
        _wait_tool_result(ctx.app, sid, wait_id)
        handle = _lookup_result_handle(ctx.db_path, job_id)
        _check(checks, "readonly_settled", bool(handle), handle)
        _check(checks, "primary_unchanged", outside_diff_dirs(snap, proj)["identical"], outside_diff_dirs(snap, proj)["returncode"])
        return {"typed_result": {"job_id": job_id, "handle": handle}, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w02_09_primary_escape_write_refused(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"safe.txt": "base\n"})
    pre = hash_tree(proj)
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify worker write attempting to reach primary tree directly is refused.")
    checks: list[dict[str, Any]] = []
    try:
        escape_target = str((proj / "escaped.txt").resolve())
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Attempt direct write to primary tree",
            scope_paths=["safe.txt"],
            commands=[f"printf 'escaped' > {escape_target}"],
            brief="Attempted escape write.",
        )
        post = hash_tree(proj)
        _check(checks, "primary_escape_prevented", pre == post and not (proj / "escaped.txt").exists(), {"pre": pre, "post": post})
        return {"typed_result": {"cmd_results": run["cmd_results"]}, "independent_observation": {"primary_diff": diff_tree_hashes(pre, post)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w02_10_noop_write_worker_no_changes(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"noop.txt": "same\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Write worker makes zero edits and completes cleanly.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Inspect noop.txt without modifying",
            scope_paths=["noop.txt"],
            commands=["cat noop.txt"],
            brief="No changes needed.",
        )
        _check(checks, "result_handle_created", bool(run["result_handle"]), run["result_handle"])
        _check(checks, "primary_unchanged", outside_diff_dirs(snap, proj)["identical"], outside_diff_dirs(snap, proj)["returncode"])
        return {"typed_result": {"job_id": run["job_id"], "handle": run["result_handle"]}, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB04 — W03 (12 sidecar slots): Dispositions and typed refusals
# ---------------------------------------------------------------------------


def _slot_w03_01_landed_disposition(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"d1.txt": "v0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Land worker overlay via promote_overlay and verify disposition.")
    checks: list[dict[str, Any]] = []
    try:
        obs = WorkersObserver(proj, ctx.db_path)
        obs.capture_pre()
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit d1.txt", scope_paths=["d1.txt"], commands=['cat d1.txt', 'edit --path d1.txt --old-string "v0" --new-string "v1"'])
        _, prom_res, prom_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        wobs = obs.capture_post(session_id=sid)
        landing = _extract_worker_landing_from_db(wobs, job_id=run["job_id"], promote_result=prom_res, expected_post_hashes={"d1.txt": sha256_bytes(b"v1\n")})
        _check(checks, "promoted_cleanly", prom_code == "" and (proj / "d1.txt").read_text(encoding="utf-8") == "v1\n", prom_code)
        _check(checks, "disposition_landed", "landed" in landing["disposition_states"], landing["disposition_states"])
        return {"typed_result": landing, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_02_rejected_disposition_keeps_conversation(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"d2.txt": "keep\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reject worker overlay via reject_overlay and verify child session survives.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit d2.txt", scope_paths=["d2.txt"], commands=['cat d2.txt', 'edit --path d2.txt --old-string "keep" --new-string "discard"'])
        _, rej_res, rej_code = _send_tool_call(ctx.app, sid, "reject_overlay", {"overlay_id": run["result_handle"], "reason": "unwanted"})
        disp = _ro_one(ctx.db_path, "SELECT change_state FROM worker_result_dispositions WHERE result_id=?", (run["result_handle"],))
        child = _ro_one(ctx.db_path, "SELECT id FROM sessions WHERE id=?", (run["child_sid"],))
        _check(checks, "reject_succeeded", rej_code == "", rej_code)
        _check(checks, "disposition_rejected", bool(disp and disp["change_state"] == "rejected"), disp)
        _check(checks, "primary_unchanged", outside_diff_dirs(snap, proj)["identical"], outside_diff_dirs(snap, proj)["returncode"])
        _check(checks, "child_session_survived", bool(child), child)
        return {"typed_result": {"handle": run["result_handle"], "disposition": disp, "reject_outcome": rej_res.get("outcome")}, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_03_shelved_disposition(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"d3.txt": "keep\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Shelve worker overlay via shelve_overlay.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit d3.txt", scope_paths=["d3.txt"], commands=['cat d3.txt', 'edit --path d3.txt --old-string "keep" --new-string "shelved"'])
        _, sh_res, sh_code = _send_tool_call(ctx.app, sid, "shelve_overlay", {"overlay_id": run["result_handle"], "reason": "defer"})
        _check(checks, "shelve_succeeded", sh_code == "" and "shelved" in str(sh_res.get("content") or ""), {"code": sh_code, "content": str(sh_res.get("content") or "")[:200]})
        _check(checks, "primary_unchanged", outside_diff_dirs(snap, proj)["identical"], outside_diff_dirs(snap, proj)["returncode"])
        return {"typed_result": {"handle": run["result_handle"], "shelve_code": sh_code}, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_04_later_round_ends_shelved(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"d4.txt": "v0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Shelve overlay then run later round via instruct_worker to end shelved state.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit d4.txt", scope_paths=["d4.txt"], worker_name="w03-sh", commands=['cat d4.txt', 'edit --path d4.txt --old-string "v0" --new-string "v1"'])
        _send_tool_call(ctx.app, sid, "shelve_overlay", {"overlay_id": run["result_handle"], "reason": "defer"})
        _, inst_res, inst_code = _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w03-sh", "instructions": "Update d4.txt to v2."})
        job2 = _extract_job_id(inst_res)
        if job2:
            wait_id = _park_wait(ctx.app, sid)
            csid2 = _wait_child_sid(ctx.app, ctx.db_path, job2, sid)
            _send_tool_call(ctx.app, csid2, "command", {"command": "cat d4.txt"})
            _send_tool_call(ctx.app, csid2, "command", {"command": 'edit --path d4.txt --old-string "v1" --new-string "v2"'})
            _settle_child_leg(ctx.app, ctx.db_path, csid2, job2, sid, brief="Updated to v2.")
            _wait_tool_result(ctx.app, sid, wait_id)
        h2 = _lookup_result_handle(ctx.db_path, job2) if job2 else ""
        _, _, prom_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": h2}) if h2 else ("", {}, "NO_HANDLE")
        _check(checks, "instruct_ended_shelve", inst_code == "" and bool(job2), {"inst_code": inst_code, "job2": job2})
        _check(checks, "round2_landed", prom_code == "" and (proj / "d4.txt").read_text(encoding="utf-8") == "v2\n", prom_code)
        return {"typed_result": {"h1": run["result_handle"], "h2": h2, "prom_code": prom_code}, "independent_observation": {"d4_txt": (proj / "d4.txt").read_text(encoding="utf-8")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_05_sequenced_promotion_via_pack_board(ctx: SlotContext) -> dict[str, Any]:
    proj, snap = ctx.make_project({"s1.txt": "1\n", "s2.txt": "2\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Follow pack_board.overlay_merge_plan.promote_sequence with ordered promote_overlay calls.")
    checks: list[dict[str, Any]] = []
    try:
        j1, _, _ = _dispatch_task(ctx.app, sid, goal="Edit s1.txt", scope={"mode": "write", "paths": ["s1.txt"]}, worker_name="w03-seq1")
        j2, _, _ = _dispatch_task(ctx.app, sid, goal="Edit s2.txt", scope={"mode": "write", "paths": ["s2.txt"]}, worker_name="w03-seq2")
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        c1, c2 = _wait_child_sid(ctx.app, ctx.db_path, j1, sid), _wait_child_sid(ctx.app, ctx.db_path, j2, sid)
        _send_tool_call(ctx.app, c1, "command", {"command": "cat s1.txt"})
        _send_tool_call(ctx.app, c1, "command", {"command": 'edit --path s1.txt --old-string "1" --new-string "11"'})
        _send_tool_call(ctx.app, c2, "command", {"command": "cat s2.txt"})
        _send_tool_call(ctx.app, c2, "command", {"command": 'edit --path s2.txt --old-string "2" --new-string "22"'})
        _settle_child_leg(ctx.app, ctx.db_path, c1, j1, sid, brief="s1 done")
        _settle_child_leg(ctx.app, ctx.db_path, c2, j2, sid, brief="s2 done")
        _wait_tool_result(ctx.app, sid, wait_id)
        board = _read_pack_board(ctx.app, sid)
        seq = (board.get("overlay_merge_plan") or {}).get("promote_sequence") or []
        _check(checks, "promote_sequence_has_2", len(seq) == 2, seq)
        for h in seq:
            _, _, pcode = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": h})
            _check(checks, f"promote_{h[:8]}", pcode == "", pcode)
        _check(checks, "both_files_landed", (proj / "s1.txt").read_text(encoding="utf-8") == "11\n" and (proj / "s2.txt").read_text(encoding="utf-8") == "22\n", outside_diff_dirs(snap, proj)["returncode"])
        return {"typed_result": {"promote_sequence": seq}, "independent_observation": {"outside_diff": outside_diff_dirs(snap, proj)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_06_promote_not_pending(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"np.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach OVERLAY_PROMOTE_NOT_PENDING / RESULT_CHANGES_NOT_ADMITTED on already-landed overlay.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit np.txt", scope_paths=["np.txt"], commands=['cat np.txt', 'edit --path np.txt --old-string "0" --new-string "1"'])
        _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        _, res2, code2 = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        _check(checks, "duplicate_promote_refused", code2 in {"OVERLAY_PROMOTE_NOT_PENDING", "RESULT_CHANGES_NOT_ADMITTED"}, {"code": code2, "content": str(res2.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code2}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_07_promote_no_paths(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"p.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach OVERLAY_PROMOTE_NO_PATHS on preview_overlay with unmatched path filter.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit p.txt", scope_paths=["p.txt"], commands=['cat p.txt', 'edit --path p.txt --old-string "0" --new-string "1"'])
        _, res, code = _send_tool_call(ctx.app, sid, "preview_overlay", {"overlay_id": run["result_handle"], "path": "unmatched_file.txt"})
        _check(checks, "no_paths_refusal", code == "OVERLAY_PROMOTE_NO_PATHS", {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_08_reject_worker_working(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"rw.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach OVERLAY_REJECT_WORKER_WORKING while later round runs.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit rw.txt", scope_paths=["rw.txt"], worker_name="w03-rw", commands=['cat rw.txt', 'edit --path rw.txt --old-string "0" --new-string "1"'])
        _send_tool_call(ctx.app, sid, "shelve_overlay", {"overlay_id": run["result_handle"], "reason": "defer"})
        _send_tool_call(ctx.app, sid, "instruct_worker", {"worker": "w03-rw", "instructions": "Keep working"})
        _, res, code = _send_tool_call(ctx.app, sid, "reject_overlay", {"overlay_id": run["result_handle"], "reason": "reject while working"})
        _check(checks, "reject_while_working_refused", code in {"OVERLAY_REJECT_WORKER_WORKING", "RESULT_CHANGES_NOT_ADMITTED"}, {"code": code, "content": str(res.get("content") or "")[:200]})
        _send_tool_call(ctx.app, sid, "worker_cancel", {"worker": "w03-rw", "reason": "cleanup"})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_09_shelve_no_changes(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"sn.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach SHELVE_OVERLAY_NO_CHANGES / RESULT_CHANGES_NOT_ADMITTED on no-op write worker.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Read sn.txt only", scope_paths=["sn.txt"], commands=["cat sn.txt"])
        _, res, code = _send_tool_call(ctx.app, sid, "shelve_overlay", {"overlay_id": run["result_handle"], "reason": "empty"})
        _check(checks, "shelve_no_changes_refused", code in {"SHELVE_OVERLAY_NO_CHANGES", "RESULT_CHANGES_NOT_ADMITTED"}, {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"refusal_code": code}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_10_disposition_required_before_closeout(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"cl.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach OVERLAY_DISPOSITION_REQUIRED_BEFORE_CLOSEOUT when closing out with open overlay.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit cl.txt", scope_paths=["cl.txt"], commands=['cat cl.txt', 'edit --path cl.txt --old-string "0" --new-string "1"'])
        # Attempt final prose response without disposing the pending overlay.
        pend = _wait_pending(ctx.app, sid)
        ctx.app.request("/harness/llm/respond", {"id": pend["id"], "content": "Done without promoting."})
        # Check if engine nudges back with OVERLAY_DISPOSITION_REQUIRED_BEFORE_CLOSEOUT or keeps overlay open.
        row = _ro_one(ctx.db_path, "SELECT merge_status FROM worker_jobs WHERE id=?", (run["job_id"],))
        _check(checks, "overlay_remains_pending_until_disposed", bool(row and row["merge_status"] == "pending"), row)
        return {"typed_result": {"job_id": run["job_id"], "merge_status": (row or {}).get("merge_status")}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_11_result_changes_not_admitted(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"ro.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach RESULT_CHANGES_NOT_ADMITTED on read-only worker result.")
    checks: list[dict[str, Any]] = []
    try:
        job_id, _, _ = _dispatch_task(ctx.app, sid, goal="Read ro.txt", agent_type="repo-researcher", scope={"mode": "read"})
        wait_id = _park_wait(ctx.app, sid)
        csid = _wait_child_sid(ctx.app, ctx.db_path, job_id, sid)
        _send_tool_call(ctx.app, csid, "command", {"command": "cat ro.txt"})
        _settle_child_leg(ctx.app, ctx.db_path, csid, job_id, sid, brief="Read only done.", leg_status="partial")
        _wait_tool_result(ctx.app, sid, wait_id)
        handle = _lookup_result_handle(ctx.db_path, job_id)
        _, res, code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": handle})
        _check(checks, "changes_not_admitted_refusal", code == "RESULT_CHANGES_NOT_ADMITTED", {"code": code, "content": str(res.get("content") or "")[:200]})
        return {"typed_result": {"handle": handle, "refusal_code": code}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w03_12_result_handle_unknown(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"x.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Reach RESULT_HANDLE_UNKNOWN across promote/reject/shelve.")
    checks: list[dict[str, Any]] = []
    try:
        codes: dict[str, str] = {}
        for tool in ("promote_overlay", "reject_overlay", "shelve_overlay"):
            _, _, code = _send_tool_call(ctx.app, sid, tool, {"overlay_id": "result-nonexistent-tb04", "reason": "stale"})
            codes[tool] = code
            _check(checks, f"{tool}_unknown_handle", code == "RESULT_HANDLE_UNKNOWN", code)
        return {"typed_result": {"codes": codes}, "independent_observation": {}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB04 — W04 (5 sidecar slots): Promotion conflicts and atomicity
# ---------------------------------------------------------------------------


def _slot_w04_01_compatible_three_way_merge(ctx: SlotContext) -> dict[str, Any]:
    base = "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\n"
    proj, _ = ctx.make_project({"merge.txt": base})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Compatible 3-way edit: person edits top while worker edits bottom.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit line8", scope_paths=["merge.txt"], commands=['cat merge.txt', 'edit --path merge.txt --old-string "line8" --new-string "line8-worker"'])
        # Person edits line1 on primary tree during review.
        (proj / "merge.txt").write_text(base.replace("line1", "line1-person"), encoding="utf-8")
        _, _, prom_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        final_body = (proj / "merge.txt").read_text(encoding="utf-8")
        _check(checks, "clean_3way_promoted", prom_code == "", prom_code)
        _check(checks, "both_edits_present", "line1-person" in final_body and "line8-worker" in final_body, final_body)
        return {"typed_result": {"promote_code": prom_code}, "independent_observation": {"final_body": final_body}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w04_02_overlapping_text_conflict_person(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"c.txt": "val = 0\n", "clean.txt": "c0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Person edits same line during review -> OVERLAY_PROMOTE_CONFLICT, atomic.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit c.txt and clean.txt",
            scope_paths=["c.txt", "clean.txt"],
            commands=[
                "cat c.txt", 'edit --path c.txt --old-string "val = 0" --new-string "val = worker"',
                "cat clean.txt", 'edit --path clean.txt --old-string "c0" --new-string "c1"',
            ],
        )
        (proj / "c.txt").write_text("val = person\n", encoding="utf-8")
        before_hash = hash_tree(proj)
        _, _, code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        after_hash = hash_tree(proj)
        _check(checks, "conflict_detected", code == "OVERLAY_PROMOTE_CONFLICT", code)
        _check(checks, "primary_tree_atomic_untouched", before_hash == after_hash, diff_tree_hashes(before_hash, after_hash))
        return {"typed_result": {"refusal_code": code}, "independent_observation": {"diff": diff_tree_hashes(before_hash, after_hash)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _w04_two_overlapping_workers(ctx: SlotContext, order: tuple[str, str]) -> dict[str, Any]:
    proj, _ = ctx.make_project({"shared.txt": "owner = base\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, f"Promote two overlapping workers in order {order[0]}->{order[1]}.")
    checks: list[dict[str, Any]] = []
    try:
        ja, _, _ = _dispatch_task(ctx.app, sid, goal="Set owner=a", scope={"mode": "write", "paths": ["shared.txt"]}, worker_name="w04-a")
        jb, _, _ = _dispatch_task(ctx.app, sid, goal="Set owner=b", scope={"mode": "write", "paths": ["shared.txt"]}, worker_name="w04-b")
        wait_id = _park_wait(ctx.app, sid, kind="all_workers_done")
        ca, cb = _wait_child_sid(ctx.app, ctx.db_path, ja, sid), _wait_child_sid(ctx.app, ctx.db_path, jb, sid)
        _send_tool_call(ctx.app, ca, "command", {"command": "cat shared.txt"})
        _send_tool_call(ctx.app, ca, "command", {"command": 'edit --path shared.txt --old-string "owner = base" --new-string "owner = a"'})
        _send_tool_call(ctx.app, cb, "command", {"command": "cat shared.txt"})
        _send_tool_call(ctx.app, cb, "command", {"command": 'edit --path shared.txt --old-string "owner = base" --new-string "owner = b"'})
        _settle_child_leg(ctx.app, ctx.db_path, ca, ja, sid, brief="a done")
        _settle_child_leg(ctx.app, ctx.db_path, cb, jb, sid, brief="b done")
        _wait_tool_result(ctx.app, sid, wait_id)
        handles = {"a": _lookup_result_handle(ctx.db_path, ja), "b": _lookup_result_handle(ctx.db_path, jb)}
        first, second = order
        _, _, c1 = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": handles[first]})
        after_first = hash_tree(proj)
        _, _, c2 = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": handles[second]})
        after_conflict = hash_tree(proj)
        _, _, c2_res = _send_tool_call(
            ctx.app, sid, "promote_overlay",
            {"overlay_id": handles[second], "resolutions": [{"path": "shared.txt", "content": f"owner = {first}+{second}\n"}]},
        )
        _check(checks, "first_landed", c1 == "", c1)
        _check(checks, "second_conflicted", c2 == "OVERLAY_PROMOTE_CONFLICT", c2)
        _check(checks, "conflict_left_first_intact", after_first == after_conflict, diff_tree_hashes(after_first, after_conflict))
        _check(checks, "explicit_resolution_landed", c2_res == "" and (proj / "shared.txt").read_text(encoding="utf-8") == f"owner = {first}+{second}\n", c2_res)
        return {"typed_result": {"order": list(order), "c1": c1, "c2": c2, "c2_res": c2_res}, "independent_observation": {"final": (proj / "shared.txt").read_text(encoding="utf-8")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w04_03_overlapping_workers_a_then_b(ctx: SlotContext) -> dict[str, Any]:
    return _w04_two_overlapping_workers(ctx, ("a", "b"))


def _slot_w04_04_overlapping_workers_b_then_a(ctx: SlotContext) -> dict[str, Any]:
    return _w04_two_overlapping_workers(ctx, ("b", "a"))


def _slot_w04_05_opaque_binary_conflict_and_invalid_resolution(ctx: SlotContext) -> dict[str, Any]:
    base_bin = b"\x89PNG\x00\x00\x01\xff"
    prim_bin = b"\x89PNG\x00\xaa\xbb\xff"
    work_bin = b"\x89PNG\x00\x11\x22\xff"
    proj, _ = ctx.make_project({"asset.bin": base_bin, "note.txt": "n0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Overlapping binary conflict never dropped; explicit keep_theirs lands exact bytes.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Update asset.bin and note.txt",
            scope_paths=["asset.bin", "note.txt"],
            commands=[
                r"python3 -c 'open(\"asset.bin\", \"wb\").write(b\"\x89PNG\x00\x11\x22\xff\")'",
                "cat note.txt",
                'edit --path note.txt --old-string "n0" --new-string "n1"',
            ],
        )
        (proj / "asset.bin").write_bytes(prim_bin)
        before_hash = hash_tree(proj)
        _, _, conf_code = _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        after_conf = hash_tree(proj)
        _, _, res_code = _send_tool_call(
            ctx.app, sid, "promote_overlay",
            {"overlay_id": run["result_handle"], "resolutions": [{"path": "asset.bin", "action": "keep_theirs"}]},
        )
        _check(checks, "binary_conflict_rejected", conf_code == "OVERLAY_PROMOTE_CONFLICT", conf_code)
        _check(checks, "binary_and_text_untouched_on_conflict", before_hash == after_conf, diff_tree_hashes(before_hash, after_conf))
        _check(checks, "keep_theirs_exact_bytes", res_code == "" and (proj / "asset.bin").read_bytes() == work_bin and (proj / "note.txt").read_text(encoding="utf-8") == "n1\n", res_code)
        return {"typed_result": {"conf_code": conf_code, "res_code": res_code}, "independent_observation": {"asset_hex": (proj / "asset.bin").read_bytes().hex()}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB04 — W09 (4 sidecar slots): Verification receipts
# ---------------------------------------------------------------------------


def _slot_w09_01_worker_verify_receipt_current(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"ok.txt": "0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Record verify receipt in worker and check source_evidence on preview_overlay.")
    checks: list[dict[str, Any]] = []
    try:
        run = _run_write_worker_edit(
            ctx.app, ctx.db_path, sid,
            goal="Edit ok.txt and run verify",
            scope_paths=["ok.txt"],
            commands=["cat ok.txt", 'edit --path ok.txt --old-string "0" --new-string "1"', "true"],
        )
        _, prev_res, prev_code = _send_tool_call(ctx.app, sid, "preview_overlay", {"overlay_id": run["result_handle"]})
        receipts = _ro_query(ctx.db_path, "SELECT id, tool, status, source_revision, source_root_digest FROM invocation_receipts WHERE session_id=?", (run["child_sid"],))
        _check(checks, "preview_succeeded", prev_code == "", prev_code)
        _check(checks, "worker_receipts_recorded_with_revision", any(r.get("source_revision") and r.get("source_root_digest") for r in receipts), len(receipts))
        return {"typed_result": {"receipt_count": len(receipts), "preview_outcome": prev_res.get("outcome")}, "independent_observation": {"receipts_sample": receipts[:3]}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w09_02_primary_verify_receipt(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"p.txt": "ok\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Record verification command receipt on primary tree.", with_progress=False)
    checks: list[dict[str, Any]] = []
    try:
        _, cmd_res, cmd_code = _send_tool_call(ctx.app, sid, "command", {"command": "true"})
        receipts = _ro_query(ctx.db_path, "SELECT id, tool, status, source_revision, source_root_digest FROM invocation_receipts WHERE session_id=?", (sid,))
        _check(checks, "primary_command_completed", cmd_code == "" and cmd_res.get("outcome") == "completed", cmd_code)
        _check(checks, "receipt_names_revision_and_digest", any(r.get("source_revision") and r.get("source_root_digest") for r in receipts), receipts[:2])
        return {"typed_result": {"receipts": receipts[:3]}, "independent_observation": {"count": len(receipts)}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w09_03_receipt_stale_after_person_edit(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"v.txt": "v1\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify receipt revision changes after person edits primary file.", with_progress=False)
    checks: list[dict[str, Any]] = []
    try:
        _send_tool_call(ctx.app, sid, "command", {"command": "true"})
        r1 = _ro_query(ctx.db_path, "SELECT source_revision, source_root_digest FROM invocation_receipts WHERE session_id=? ORDER BY created_at ASC", (sid,))
        (proj / "v.txt").write_text("v2-person-edit\n", encoding="utf-8")
        _send_tool_call(ctx.app, sid, "command", {"command": "cat v.txt"})
        _send_tool_call(ctx.app, sid, "command", {"command": 'edit --path v.txt --old-string "v2-person-edit" --new-string "v3"'})
        _send_tool_call(ctx.app, sid, "command", {"command": "true"})
        r2 = _ro_query(ctx.db_path, "SELECT source_revision, source_root_digest FROM invocation_receipts WHERE session_id=? ORDER BY created_at ASC", (sid,))
        rev_changed = bool(r1 and r2 and r1[0].get("source_revision") != r2[-1].get("source_revision"))
        _check(checks, "receipt_revision_advanced_after_edit", rev_changed, {"first": r1[0] if r1 else None, "last": r2[-1] if r2 else None})
        return {"typed_result": {"first": r1[0] if r1 else None, "last": r2[-1] if r2 else None}, "independent_observation": {"v_txt": (proj / "v.txt").read_text(encoding="utf-8")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


def _slot_w09_04_receipt_stale_after_landing(ctx: SlotContext) -> dict[str, Any]:
    proj, _ = ctx.make_project({"l.txt": "l0\n"})
    sid, _, _ = _open_coordinator(ctx.app, proj, "Verify primary receipt revision advances after worker landing.")
    checks: list[dict[str, Any]] = []
    try:
        _send_tool_call(ctx.app, sid, "command", {"command": "true"})
        before = _ro_query(ctx.db_path, "SELECT source_revision FROM invocation_receipts WHERE session_id=? ORDER BY created_at ASC", (sid,))
        run = _run_write_worker_edit(ctx.app, ctx.db_path, sid, goal="Edit l.txt", scope_paths=["l.txt"], commands=['cat l.txt', 'edit --path l.txt --old-string "l0" --new-string "l1"'])
        _send_tool_call(ctx.app, sid, "promote_overlay", {"overlay_id": run["result_handle"]})
        _send_tool_call(ctx.app, sid, "command", {"command": "true"})
        after = _ro_query(ctx.db_path, "SELECT source_revision FROM invocation_receipts WHERE session_id=? ORDER BY created_at ASC", (sid,))
        _check(checks, "revision_advanced_after_landing", bool(before and after and before[0]["source_revision"] != after[-1]["source_revision"]), {"before": before[:1], "after": after[-1:]})
        return {"typed_result": {"before": before[:1], "after": after[-1:]}, "independent_observation": {"l_txt": (proj / "l.txt").read_text(encoding="utf-8")}, "checks": checks}
    finally:
        _finish_session(ctx.app, sid)


# ---------------------------------------------------------------------------
# TB04 — W10 (3 sidecar slots): Real sidecar OS process restart!
# ---------------------------------------------------------------------------


def _w10_restart_helper(ctx: SlotContext, action: str) -> dict[str, Any]:
    proj, snap = ctx.make_project({"restart.txt": "before-restart\n"})
    app1, app_dir = ctx.start_dedicated_app("restart-app")
    db_path = app_dir / "store.db"
    pid1 = getattr(getattr(app1, "process", None), "pid", None)
    checks: list[dict[str, Any]] = []
    try:
        sid, _, _ = _open_coordinator(app1, proj, f"W10 real restart witness ({action}).")
        run = _run_write_worker_edit(
            app1, db_path, sid,
            goal="Edit restart.txt before sidecar restart",
            scope_paths=["restart.txt"],
            commands=['cat restart.txt', 'edit --path restart.txt --old-string "before-restart" --new-string "after-restart"'],
        )
        handle = run["result_handle"]
        job_id = run["job_id"]
        child_sid = run["child_sid"]
        _finish_session(app1, sid)
    finally:
        app1.close(cleanup=False)

    # Real OS process restart on the exact same app_dir / store.db!
    app2, _ = ctx.start_dedicated_app("restart-app")
    pid2 = getattr(getattr(app2, "process", None), "pid", None)
    try:
        app2.request(f"/v1/sessions/{sid}/prompts", {"operation_id": str(uuid.uuid4()), "text": f"Resume after restart and {action} overlay {handle}."})
        if action == "promote":
            _, res, code = _send_tool_call(app2, sid, "promote_overlay", {"overlay_id": handle})
            _, dup_res, dup_code = _send_tool_call(app2, sid, "promote_overlay", {"overlay_id": handle})
            _check(checks, "distinct_sidecar_pids", pid1 != pid2 or pid1 is None, {"pid1": pid1, "pid2": pid2})
            _check(checks, "promote_succeeded_after_restart", code == "" and (proj / "restart.txt").read_text(encoding="utf-8") == "after-restart\n", {"code": code, "body": (proj / "restart.txt").read_text(encoding="utf-8")})
            _check(checks, "no_duplicate_landing", dup_code in {"OVERLAY_PROMOTE_NOT_PENDING", "RESULT_CHANGES_NOT_ADMITTED"}, {"dup_code": dup_code, "res": dup_res.get("outcome")})
        elif action == "reject":
            _, res, code = _send_tool_call(app2, sid, "reject_overlay", {"overlay_id": handle, "reason": "reject after restart"})
            child = _ro_one(db_path, "SELECT id FROM sessions WHERE id=?", (child_sid,))
            _check(checks, "distinct_sidecar_pids", pid1 != pid2 or pid1 is None, {"pid1": pid1, "pid2": pid2})
            _check(checks, "reject_succeeded_after_restart", code == "", code)
            _check(checks, "primary_unchanged_after_reject", outside_diff_dirs(snap, proj)["identical"], outside_diff_dirs(snap, proj)["returncode"])
            _check(checks, "child_session_survived_restart_reject", bool(child), child)
        else:
            _, prev_res, prev_code = _send_tool_call(app2, sid, "preview_overlay", {"overlay_id": handle})
            _, res, code = _send_tool_call(app2, sid, "promote_overlay", {"overlay_id": handle})
            _check(checks, "distinct_sidecar_pids", pid1 != pid2 or pid1 is None, {"pid1": pid1, "pid2": pid2})
            _check(checks, "preview_succeeded_after_restart", prev_code == "" and "restart.txt" in str(prev_res.get("content") or ""), prev_code)
            _check(checks, "promote_succeeded_after_restart", code == "" and (proj / "restart.txt").read_text(encoding="utf-8") == "after-restart\n", code)
        result_rows = _ro_query(db_path, "SELECT id FROM worker_results WHERE worker_job_id=?", (job_id,))
        _check(checks, "exactly_one_result_handle", len(result_rows) == 1, result_rows)
        _finish_session(app2, sid)
        return {
            "typed_result": {"job_id": job_id, "handle": handle, "action": action, "result_count": len(result_rows)},
            "independent_observation": {"pid_before": pid1, "pid_after": pid2, "outside_diff": outside_diff_dirs(snap, proj), "restart_txt": (proj / "restart.txt").read_text(encoding="utf-8")},
            "checks": checks,
        }
    finally:
        app2.close(cleanup=False)


def _slot_w10_01_restart_then_land(ctx: SlotContext) -> dict[str, Any]:
    return _w10_restart_helper(ctx, "promote")


def _slot_w10_02_restart_then_reject(ctx: SlotContext) -> dict[str, Any]:
    return _w10_restart_helper(ctx, "reject")


def _slot_w10_03_restart_baseline_retained_and_preview_land(ctx: SlotContext) -> dict[str, Any]:
    return _w10_restart_helper(ctx, "preview_and_promote")


# ---------------------------------------------------------------------------
# Complete 66-slot registry (32 TB03 + 34 TB04)
# ---------------------------------------------------------------------------

SLOT_SPECS: tuple[SlotSpec, ...] = (
    # TB03 — W01 (6)
    SlotSpec("W01-sidecar-01", "W01", "tb03", "Identity committed before round 1; child messages stay in child session", _slot_w01_01_identity_and_session_isolation),
    SlotSpec("W01-sidecar-02", "W01", "tb03", "Primary tree never sees unlanded worker bytes", _slot_w01_02_primary_tree_isolation),
    SlotSpec("W01-sidecar-03", "W01", "tb03", "Sibling workers never see each other's unlanded bytes", _slot_w01_03_sibling_worker_isolation),
    SlotSpec("W01-sidecar-04", "W01", "tb03", "instruct_worker reuses worker identity and child session", _slot_w01_04_instruct_worker_reuses_identity),
    SlotSpec("W01-sidecar-05", "W01", "tb03", "Workspace view with private Git metadata", _slot_w01_05_git_workspace_view),
    SlotSpec("W01-sidecar-06", "W01", "tb03", "Workspace view without Git metadata", _slot_w01_06_non_git_workspace_view),
    # TB03 — W05 (6)
    SlotSpec("W05-sidecar-01", "W05", "tb03", "Committed result handle matches pack_board and next coordinator input", _slot_w05_01_handle_matches_next_input_and_board),
    SlotSpec("W05-sidecar-02", "W05", "tb03", "Worker-supplied forged host-observed fields are discarded", _slot_w05_02_forged_host_fields_discarded),
    SlotSpec("W05-sidecar-03", "W05", "tb03", "Committed result handle is durable before coordinator wake", _slot_w05_03_committed_handle_independent_of_wake),
    SlotSpec("W05-sidecar-04", "W05", "tb03", "worker_history recovers settled round detail with attribution", _slot_w05_04_worker_history_recovery),
    SlotSpec("W05-sidecar-05", "W05", "tb03", "recall recovers worker detail with worker attribution", _slot_w05_05_recall_with_worker_attribution),
    SlotSpec("W05-sidecar-06", "W05", "tb03", "Summary bound enforcement and recovery", _slot_w05_06_over_budget_summary_rejected_then_recovered),
    # TB03 — W06 (3)
    SlotSpec("W06-sidecar-01", "W06", "tb03", "worker_cancel mid-round settles one canceled result", _slot_w06_01_cancel_mid_round),
    SlotSpec("W06-sidecar-02", "W06", "tb03", "worker_cancel during decision hold settles cleanly", _slot_w06_02_cancel_during_decision_hold),
    SlotSpec("W06-sidecar-03", "W06", "tb03", "WORKER_CANCEL_TERMINAL on already-settled worker", _slot_w06_03_cancel_terminal_refusal),
    # TB03 — W07 (12)
    SlotSpec("W07-sidecar-01", "W07", "tb03", "request_decision hold and answer_decision resume", _slot_w07_01_request_and_answer_decision),
    SlotSpec("W07-sidecar-02", "W07", "tb03", "message_coordinator non-blocking status update", _slot_w07_02_message_coordinator_non_blocking),
    SlotSpec("W07-sidecar-03", "W07", "tb03", "INSTRUCT_WORKER_ASKING refusal", _slot_w07_03_instruct_worker_asking),
    SlotSpec("W07-sidecar-04", "W07", "tb03", "INSTRUCT_WORKER_WAITING refusal", _slot_w07_04_instruct_worker_waiting),
    SlotSpec("W07-sidecar-05", "W07", "tb03", "INSTRUCT_WORKER_CLOSED refusal", _slot_w07_05_instruct_worker_closed),
    SlotSpec("W07-sidecar-06", "W07", "tb03", "INSTRUCT_WORKER_PLANNED refusal", _slot_w07_06_instruct_worker_planned),
    SlotSpec("W07-sidecar-07", "W07", "tb03", "INSTRUCT_WORKER_AGENT_SEALED refusal", _slot_w07_07_instruct_worker_agent_sealed),
    SlotSpec("W07-sidecar-08", "W07", "tb03", "INSTRUCT_WORKER_AGENT_CHANGES_OPEN refusal", _slot_w07_08_instruct_worker_agent_changes_open),
    SlotSpec("W07-sidecar-09", "W07", "tb03", "WORKER_NAME_UNKNOWN refusal", _slot_w07_09_worker_name_unknown),
    SlotSpec("W07-sidecar-10", "W07", "tb03", "TASK_WORKER_NAME_TAKEN refusal", _slot_w07_10_task_worker_name_taken),
    SlotSpec("W07-sidecar-11", "W07", "tb03", "MESSAGE_COORDINATOR_NO_WORKER refusal", _slot_w07_11_message_coordinator_no_worker),
    SlotSpec("W07-sidecar-12", "W07", "tb03", "Decision stays bound to raising worker while sibling completes", _slot_w07_12_decision_stays_with_raising_worker),
    # TB03 — W08 (2)
    SlotSpec("W08-sidecar-01", "W08", "tb03", "Review worker via review_of reads exact delivered snapshot", _slot_w08_01_reviewer_reads_exact_delivered_snapshot),
    SlotSpec("W08-sidecar-02", "W08", "tb03", "Reviewer stays isolated from later producer round", _slot_w08_02_reviewer_isolated_from_later_producer_round),
    # TB03 — W11 (3)
    SlotSpec("W11-sidecar-01", "W11", "tb03", "Disjoint 3-worker fan-out wave lands cleanly", _slot_w11_01_disjoint_fanout_wave),
    SlotSpec("W11-sidecar-02", "W11", "tb03", "Overlapping fan-out reservations; promotion remains conflict authority", _slot_w11_02_overlapping_reservations_promotion_authority),
    SlotSpec("W11-sidecar-03", "W11", "tb03", "Other coordinator session works cleanly during fan-out wave", _slot_w11_03_other_session_works_during_wave),
    # TB04 — W02 (10)
    SlotSpec("W02-sidecar-01", "W02", "tb04", "Changed-path proof: create file", _slot_w02_01_create),
    SlotSpec("W02-sidecar-02", "W02", "tb04", "Changed-path proof: edit file", _slot_w02_02_edit),
    SlotSpec("W02-sidecar-03", "W02", "tb04", "Changed-path proof: rename file", _slot_w02_03_rename),
    SlotSpec("W02-sidecar-04", "W02", "tb04", "Changed-path proof: delete file", _slot_w02_04_delete),
    SlotSpec("W02-sidecar-05", "W02", "tb04", "Changed-path proof: gitignored file", _slot_w02_05_gitignored),
    SlotSpec("W02-sidecar-06", "W02", "tb04", "Changed-path proof: binary/non-text file", _slot_w02_06_binary_nontext),
    SlotSpec("W02-sidecar-07", "W02", "tb04", "Changed-path proof: lying worker report overridden by host diff", _slot_w02_07_lying_worker_report),
    SlotSpec("W02-sidecar-08", "W02", "tb04", "Changed-path proof: read-only worker has no changed paths", _slot_w02_08_readonly_no_changed_paths),
    SlotSpec("W02-sidecar-09", "W02", "tb04", "Changed-path proof: write escaping to primary tree refused", _slot_w02_09_primary_escape_write_refused),
    SlotSpec("W02-sidecar-10", "W02", "tb04", "Changed-path proof: no-op write worker has no changed paths", _slot_w02_10_noop_write_worker_no_changes),
    # TB04 — W03 (12)
    SlotSpec("W03-sidecar-01", "W03", "tb04", "Disposition: landed via promote_overlay", _slot_w03_01_landed_disposition),
    SlotSpec("W03-sidecar-02", "W03", "tb04", "Disposition: rejected via reject_overlay keeps conversation", _slot_w03_02_rejected_disposition_keeps_conversation),
    SlotSpec("W03-sidecar-03", "W03", "tb04", "Disposition: shelved via shelve_overlay", _slot_w03_03_shelved_disposition),
    SlotSpec("W03-sidecar-04", "W03", "tb04", "Later round via instruct_worker ends shelved state", _slot_w03_04_later_round_ends_shelved),
    SlotSpec("W03-sidecar-05", "W03", "tb04", "Sequenced promotion following pack_board.overlay_merge_plan.promote_sequence", _slot_w03_05_sequenced_promotion_via_pack_board),
    SlotSpec("W03-sidecar-06", "W03", "tb04", "OVERLAY_PROMOTE_NOT_PENDING refusal", _slot_w03_06_promote_not_pending),
    SlotSpec("W03-sidecar-07", "W03", "tb04", "OVERLAY_PROMOTE_NO_PATHS refusal", _slot_w03_07_promote_no_paths),
    SlotSpec("W03-sidecar-08", "W03", "tb04", "OVERLAY_REJECT_WORKER_WORKING refusal", _slot_w03_08_reject_worker_working),
    SlotSpec("W03-sidecar-09", "W03", "tb04", "SHELVE_OVERLAY_NO_CHANGES refusal", _slot_w03_09_shelve_no_changes),
    SlotSpec("W03-sidecar-10", "W03", "tb04", "OVERLAY_DISPOSITION_REQUIRED_BEFORE_CLOSEOUT check", _slot_w03_10_disposition_required_before_closeout),
    SlotSpec("W03-sidecar-11", "W03", "tb04", "RESULT_CHANGES_NOT_ADMITTED refusal", _slot_w03_11_result_changes_not_admitted),
    SlotSpec("W03-sidecar-12", "W03", "tb04", "RESULT_HANDLE_UNKNOWN refusal", _slot_w03_12_result_handle_unknown),
    # TB04 — W04 (5)
    SlotSpec("W04-sidecar-01", "W04", "tb04", "Compatible 3-way merge with person's non-overlapping edit", _slot_w04_01_compatible_three_way_merge),
    SlotSpec("W04-sidecar-02", "W04", "tb04", "Overlapping text conflict from person's edit (atomic)", _slot_w04_02_overlapping_text_conflict_person),
    SlotSpec("W04-sidecar-03", "W04", "tb04", "Two overlapping workers promoted in order A then B", _slot_w04_03_overlapping_workers_a_then_b),
    SlotSpec("W04-sidecar-04", "W04", "tb04", "Two overlapping workers promoted in order B then A", _slot_w04_04_overlapping_workers_b_then_a),
    SlotSpec("W04-sidecar-05", "W04", "tb04", "Overlapping opaque/binary file conflict never silently dropped", _slot_w04_05_opaque_binary_conflict_and_invalid_resolution),
    # TB04 — W09 (4)
    SlotSpec("W09-sidecar-01", "W09", "tb04", "Verification receipt recorded in worker names revision and digest", _slot_w09_01_worker_verify_receipt_current),
    SlotSpec("W09-sidecar-02", "W09", "tb04", "Verification receipt recorded on primary tree names revision and digest", _slot_w09_02_primary_verify_receipt),
    SlotSpec("W09-sidecar-03", "W09", "tb04", "Verification receipt invalidated after person's edit", _slot_w09_03_receipt_stale_after_person_edit),
    SlotSpec("W09-sidecar-04", "W09", "tb04", "Verification receipt invalidated after worker landing", _slot_w09_04_receipt_stale_after_landing),
    # TB04 — W10 (3, real OS process restarts)
    SlotSpec("W10-sidecar-01", "W10", "tb04", "Real sidecar restart before landing: promote lands once without duplicate", _slot_w10_01_restart_then_land, requires_restart=True),
    SlotSpec("W10-sidecar-02", "W10", "tb04", "Real sidecar restart before rejection: reject preserves primary and conversation", _slot_w10_02_restart_then_reject, requires_restart=True),
    SlotSpec("W10-sidecar-03", "W10", "tb04", "Real sidecar restart: baseline retained, preview and promote succeed", _slot_w10_03_restart_baseline_retained_and_preview_land, requires_restart=True),
)


def select_slots(
    *,
    card: str = "all",
    family: str | None = None,
    slot: str | None = None,
) -> list[SlotSpec]:
    card_norm = card.strip().lower()
    fam_norm = family.strip().upper() if family else None
    slot_norm = slot.strip() if slot else None
    out: list[SlotSpec] = []
    for spec in SLOT_SPECS:
        if card_norm != "all" and spec.card != card_norm:
            continue
        if fam_norm and spec.family != fam_norm:
            continue
        if slot_norm and spec.slot_id != slot_norm:
            continue
        out.append(spec)
    return out


def run_sidecar_slots(
    out_dir: pathlib.Path,
    *,
    card: str = "all",
    family: str | None = None,
    slot: str | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    run_id: str | None = None,
) -> dict[str, Any]:
    out_dir = pathlib.Path(out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    effective_run_id = run_id or f"wea-workers-sidecar-{uuid.uuid4().hex[:8]}"
    prov = collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar",
    )
    selected = select_slots(card=card, family=family, slot=slot)
    all_for_card = select_slots(card=card)
    implemented_ids = [s.slot_id for s in all_for_card]
    observed_ids: list[str] = []
    results: list[dict[str, Any]] = []

    warmed_app = None
    warmed_dir = out_dir / "shared-sidecar"
    need_warmed = any(not s.requires_restart for s in selected)
    try:
        if need_warmed:
            warmed_app = _create_application(
                warmed_dir,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
            warmed_app.ready()

        for spec in selected:
            slot_dir = out_dir / spec.card / spec.slot_id
            slot_dir.mkdir(parents=True, exist_ok=True)
            t0 = time.monotonic()
            ctx = SlotContext(
                run_id=effective_run_id,
                spec=spec,
                out_dir=slot_dir,
                app=None if spec.requires_restart else warmed_app,
                app_dir=slot_dir / "restart-app" if spec.requires_restart else warmed_dir,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
            try:
                outcome = spec.fn(ctx)
                checks = outcome.get("checks") or []
                failures = [c for c in checks if not c.get("passed")]
                passed = bool(checks) and not failures
                verdict = {
                    "passed": passed,
                    "status": "pass" if passed else "fail",
                    "checks": checks,
                    "failures": failures,
                    "duration_sec": round(time.monotonic() - t0, 3),
                }
                typed_result = outcome.get("typed_result") or {}
                independent_obs = outcome.get("independent_observation") or {}
            except Exception as exc:
                verdict = {
                    "passed": False,
                    "status": "error",
                    "checks": [],
                    "failures": [{"name": "exception", "passed": False, "detail": f"{type(exc).__name__}: {exc}", "traceback": traceback.format_exc()}],
                    "duration_sec": round(time.monotonic() - t0, 3),
                }
                typed_result = {}
                independent_obs = {}

            receipt = {
                "schema": "wea-sidecar-slot-v1",
                "run_id": effective_run_id,
                "slot_id": spec.slot_id,
                "family": spec.family,
                "card": spec.card,
                "lane": "sidecar",
                "description": spec.description,
                "host": prov["host"],
                "product_source": prov["product_source"],
                "harness_source": prov["harness_source"],
                "typed_result": typed_result,
                "independent_observation": independent_obs,
                "verdict": verdict,
            }
            _save_json(slot_dir / "receipt.json", receipt)
            observed_ids.append(spec.slot_id)
            results.append({
                "slot_id": spec.slot_id,
                "family": spec.family,
                "card": spec.card,
                "status": verdict["status"],
                "passed": verdict["passed"],
                "duration_sec": verdict["duration_sec"],
                "receipt": str(slot_dir / "receipt.json"),
            })
    finally:
        if warmed_app is not None:
            warmed_app.close(cleanup=False)

    missing_ids = [sid for sid in implemented_ids if sid not in set(observed_ids)]
    summary = {
        "schema": "wea-sidecar-summary-v1",
        "run_id": effective_run_id,
        "card_filter": card,
        "family_filter": family,
        "slot_filter": slot,
        "lane": "sidecar",
        "host": prov["host"],
        "product_source": prov["product_source"],
        "harness_source": prov["harness_source"],
        "implemented_count": len(implemented_ids),
        "implemented_slots": implemented_ids,
        "observed_count": len(observed_ids),
        "observed_slots": observed_ids,
        "missing_count": len(missing_ids),
        "missing_slots": missing_ids,
        "passed_count": sum(1 for r in results if r["passed"]),
        "failed_count": sum(1 for r in results if not r["passed"]),
        "results": results,
    }
    _save_json(out_dir / "summary.json", summary)
    return summary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB03 & TB04 Workers headless sidecar slot runner.")
    parser.add_argument("--card", choices=("tb03", "tb04", "all"), default="all")
    parser.add_argument("--family", default=None, help="Filter by family (e.g. W01, W02, W03, W04, W05, W06, W07, W08, W09, W10, W11)")
    parser.add_argument("--slot", default=None, help="Run a single slot_id (e.g. W01-sidecar-01)")
    parser.add_argument("--out", required=False, default="", help="Output evidence directory")
    parser.add_argument("--engine", default=None, help="Path to pinned lycaon-dev binary")
    parser.add_argument("--engine-manifest", default=None, help="Path to TB01 build-manifest.json")
    parser.add_argument("--engine-root", default=None, help="Path to staged LYCAON_ENGINE_ROOT")
    parser.add_argument("--run-id", default=None, help="Optional explicit run_id")
    parser.add_argument("--list", action="store_true", help="List selected slots and exit")
    args = parser.parse_args(argv)

    if args.list:
        for spec in select_slots(card=args.card, family=args.family, slot=args.slot):
            print(f"{spec.card}\t{spec.family}\t{spec.slot_id}\trestart={spec.requires_restart}\t{spec.description}")
        return 0

    if not args.out:
        parser.error("--out is required unless --list is specified")

    summary = run_sidecar_slots(
        pathlib.Path(args.out),
        card=args.card,
        family=args.family,
        slot=args.slot,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        engine_root=args.engine_root,
        run_id=args.run_id,
    )
    print(json.dumps({
        "run_id": summary["run_id"],
        "implemented_count": summary["implemented_count"],
        "observed_count": summary["observed_count"],
        "missing_count": summary["missing_count"],
        "passed_count": summary["passed_count"],
        "failed_count": summary["failed_count"],
    }, indent=2))
    return 0 if summary["failed_count"] == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
