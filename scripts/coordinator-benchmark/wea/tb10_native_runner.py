"""TB10 X01-X03 native Den runner for Linux actual-Den (`painted-wolf-code`) + pinned `lycaon-dev`.

For each X01-X03 variant:
  1. Drives the REAL product episode against the pinned `lycaon-dev` engine in an isolated
     profile (`source_config=None`), capturing the actual serialized model request
     (`messages`, `offered_tools`, `request_bytes`), real tool/worker/approval/edit outcomes,
     independent host-side disk SHA-256s, live fixture network observations, and independent
     `?mode=ro` SQLite rows (`sessions`, `messages`, `worker_jobs`, `worker_results`,
     `landed_changes`, `source_versions`, `source_effects`, `source_operations`, `checkpoints`,
     `authz_events`, `invocation_receipts`).
  2. Launches the REAL `painted-wolf-code` Tauri desktop binary (`LYCAON_SIDECAR_BIN` pinned
     to the same `lycaon-dev`) against the populated isolated profile under Xvfb + AT-SPI,
     captures pre/post owned-window PNG screenshots + keyboard activation, and verifies live
     API readback (`/health`, `/v1/projects`, `/v1/sessions/<id>`, `/v1/sessions/<id>/messages`).
  3. Fails closed if scenario SQLite tables have zero rows, if no real model request was
     captured, if guidance suggests an unpermitted tool or exceeds the shipped byte ceiling,
     if any D6 canary leaks, or if engine provenance is unverified.
"""

from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
import pathlib
import re
import sqlite3
import sys
import urllib.error
import uuid
from typing import Any, Callable, Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.fixtures_net import RecordingHTTPFixture
from wea.native_den_linux import run_linux_tauri_native
from wea.observers import (
    AUTHORITY_TABLES,
    EDITS_TABLES,
    WORKERS_TABLES,
    hash_file,
    hash_tree,
    read_sqlite_tables_ro,
    sha256_bytes,
)
from wea.provenance import REPO_ROOT, collect_provenance
from wea.runner import (
    _create_application,
    _extract_edit_records_from_db,
    _extract_planned_approved_destinations,
    _extract_product_network_witnesses,
    _extract_refusal_code,
    _extract_worker_landing_from_db,
    _init_git_repo,
    _open_session,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)

SHIPPED_REQUEST_CEILING_BYTES = 262_144  # 256 KiB serialized provider request ceiling

TB10_VARIANTS: tuple[dict[str, Any], ...] = (
    {
        "id": "X01-v1-worker-outside-write",
        "family": "X01",
        "episode_kind": "worker_outside_write",
        "title": "Worker outside-workspace write requires effect-bound authority; primary untouched before promotion",
        "track_tables": ("workers", "authority", "edits"),
        "expected_surfaces": ["worker_card", "approval_card", "saved_approvals"],
        "crosswalk": ["W02", "W04", "A01", "A05", "X01"],
    },
    {
        "id": "X01-v2-worker-network-send",
        "family": "X01",
        "episode_kind": "worker_network_send",
        "title": "Worker network send covered by port-scoped authority and refused without arrival",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card", "saved_approvals"],
        "crosswalk": ["W02", "A01", "A05", "X01"],
    },
    {
        "id": "X01-v3-worker-host-exec",
        "family": "X01",
        "episode_kind": "worker_host_exec",
        "title": "Confinement and authorization remain separate composable layers",
        "track_tables": ("workers", "authority", "edits"),
        "expected_surfaces": ["worker_card", "approval_card"],
        "crosswalk": ["W04", "A05", "X01"],
    },
    {
        "id": "X02-v1-rewind-landed-turn",
        "family": "X02",
        "episode_kind": "rewind_landed_turn",
        "title": "Rewind landed turn restores primary bytes and retains attribution",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["worker_card", "files"],
        "crosswalk": ["E04", "E06", "X02"],
    },
    {
        "id": "X02-v2-restore-pre-landing-version",
        "family": "X02",
        "episode_kind": "restore_pre_landing_version",
        "title": "Restore file to pre-landing version keeps permanent turn identity",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["files", "worker_card"],
        "crosswalk": ["E04", "E06", "X02"],
    },
    {
        "id": "X02-v3-rewind-blocker-later-contrib",
        "family": "X02",
        "episode_kind": "rewind_blocker_later_contrib",
        "title": "Rewind blocker surfaced when later contributions exist",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["files", "worker_card"],
        "crosswalk": ["E06", "X02"],
    },
    {
        "id": "X03-v1-secret-in-agent-edit",
        "family": "X03",
        "episode_kind": "secret_in_agent_edit",
        "title": "Managed secret in agent edit materialized only at screened save; A11 canary scan clean",
        "track_tables": ("edits", "authority"),
        "expected_surfaces": ["files", "approval_card"],
        "crosswalk": ["E08", "A11", "X03"],
    },
    {
        "id": "X03-v2-secret-in-worker-result",
        "family": "X03",
        "episode_kind": "secret_in_worker_result",
        "title": "Managed secret in worker result stays screened by reference across records",
        "track_tables": ("workers", "edits", "authority"),
        "expected_surfaces": ["worker_card", "approval_card"],
        "crosswalk": ["E08", "A11", "X03"],
    },
    {
        "id": "X03-v3-guidance-and-markup-screen",
        "family": "X03",
        "episode_kind": "guidance_and_markup_screen",
        "title": "Refusal guidance names only permitted calls within byte ceiling and neutralizes markup",
        "track_tables": ("workers", "edits", "authority"),
        "expected_surfaces": ["approval_card", "saved_approvals"],
        "crosswalk": ["TB10", "X03"],
    },
)

VARIANTS_BY_ID: dict[str, dict[str, Any]] = {v["id"]: v for v in TB10_VARIANTS}


def verify_guidance_surface(
    *,
    offered_tools: Sequence[str],
    suggested_tools: Sequence[str],
    request_bytes: int,
    ceiling_bytes: int = SHIPPED_REQUEST_CEILING_BYTES,
) -> dict[str, Any]:
    """Verify guidance suggests only calls the surface permits and stays within the byte ceiling."""
    offered = set(offered_tools)
    disallowed = sorted({t for t in suggested_tools if t not in offered})
    within_ceiling = 0 < int(request_bytes) <= int(ceiling_bytes)
    return {
        "offered_tools": sorted(offered),
        "suggested_tools": sorted(set(suggested_tools)),
        "disallowed_suggestions": disallowed,
        "request_bytes": int(request_bytes),
        "ceiling_bytes": int(ceiling_bytes),
        "within_ceiling": within_ceiling,
        "passed": (len(disallowed) == 0 and within_ceiling),
    }


def _tables_for_variant(variant: dict[str, Any]) -> tuple[str, ...]:
    tables: list[str] = ["sessions", "messages"]
    groups = variant.get("track_tables") or ()
    if "workers" in groups:
        tables.extend(WORKERS_TABLES)
    if "edits" in groups:
        tables.extend(EDITS_TABLES)
    if "authority" in groups:
        tables.extend(AUTHORITY_TABLES)
    return tuple(dict.fromkeys(tables))


_SECRET_REF_RE = re.compile(r"\{\{paintedwolf-secret:[0-9a-f-]{36}\}\}")


def _http_call(
    app: Any,
    path: str,
    body: dict[str, Any] | None = None,
    *,
    method: str | None = None,
) -> tuple[int, Any]:
    """Invoke `app.request`, returning `(status_code, parsed_body)` without raising on HTTP 4xx/5xx."""
    try:
        resp = app.request(path, body, method=method)
        return 200, resp
    except json.JSONDecodeError:
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


def _ro_query(db_path: pathlib.Path, sql: str, params: tuple[Any, ...] = ()) -> list[dict[str, Any]]:
    if not db_path.is_file():
        return []
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        return [dict(r) for r in conn.execute(sql, params).fetchall()]


def _ro_one(db_path: pathlib.Path, sql: str, params: tuple[Any, ...] = ()) -> dict[str, Any] | None:
    rows = _ro_query(db_path, sql, params)
    return rows[0] if rows else None


def _list_file_versions_ro(db_path: pathlib.Path, project_id: str, rel_path: str) -> list[dict[str, Any]]:
    """Read source_versions + source_operations + latest source_effects id for rel_path via `?mode=ro`."""
    sql = """
        SELECT v.id AS version_id, v.file_id, v.root_id, v.path, v.state, v.content_sha256,
               v.seq, o.id AS operation_id, o.branch_id, o.origin, o.job_id, o.cause,
               o.command_window_id, o.tool_name,
               (SELECT e.id FROM source_effects e
                WHERE e.project_id = v.project_id AND e.operation_id = v.operation_id AND e.file_id = v.file_id
                ORDER BY e.ordinal DESC LIMIT 1) AS effect_id
        FROM source_versions v
        JOIN source_operations o ON o.id = v.operation_id
        WHERE v.project_id = ? AND v.path = ?
        ORDER BY v.seq ASC
    """
    return _ro_query(db_path, sql, (project_id, rel_path))


def _complete_progress(app: Any, session_id: str, prog_res: dict[str, Any], done_content: str) -> None:
    """Close out an active progress checklist so the session turn can finish cleanly."""
    args: dict[str, Any] = {"content": done_content}
    inv = prog_res.get("invocation") if isinstance(prog_res, dict) else None
    ev = inv.get("evidence") if isinstance(inv, dict) else None
    if isinstance(ev, dict):
        if ev.get("plan_id"):
            args["plan_id"] = ev["plan_id"]
        if ev.get("revision") is not None:
            args["expected_revision"] = ev["revision"]
    _send_tool_call(app, session_id, "update_progress", args)


def _finish_turn_text(app: Any, session_id: str, text: str = "Done.") -> None:
    req = _wait_pending(app, session_id)
    app.request("/harness/llm/respond", {"id": req["id"], "content": text})
    app.wait_for(
        lambda: app.request(f"/v1/sessions/{session_id}").get("status") == "idle",
        "tb10 session idle",
        session_id,
    )


def _extract_anchor_message_id(
    app: Any,
    db_path: pathlib.Path,
    session_id: str,
    prompt_resp: Any,
) -> str:
    msg_id = ""
    if isinstance(prompt_resp, dict):
        msg_id = str(prompt_resp.get("message_id") or prompt_resp.get("id") or "")
    if not msg_id:
        st_m, msgs_resp = _http_call(app, f"/v1/sessions/{session_id}/messages", method="GET")
        msgs = (msgs_resp.get("messages") or []) if st_m == 200 and isinstance(msgs_resp, dict) else []
        user_msgs = [m for m in msgs if m.get("role") == "user"]
        if user_msgs:
            msg_id = str(user_msgs[-1].get("id") or "")
    if not msg_id:
        row = _ro_one(
            db_path,
            "SELECT id FROM messages WHERE session_id = ? AND role = 'user' ORDER BY ord DESC LIMIT 1",
            (session_id,),
        )
        if row and row.get("id"):
            msg_id = str(row["id"])
    return msg_id


def _dispatch_worker_and_wait_child(
    app: Any,
    db_path: pathlib.Path,
    session_id: str,
    *,
    goal: str,
    done_when: list[str],
    scope_paths: list[str],
    worker_name: str | None = None,
) -> tuple[str, str, str]:
    """Dispatch an implementer worker via `task`, park `wait(next_worker_done)`, and return `(job_id, child_sid, wait_call_id)`."""
    task_args: dict[str, Any] = {
        "agent_type": "implementer",
        "brief": {"goal": goal, "done_when": done_when},
        "scope": {"mode": "write", "paths": scope_paths},
    }
    if worker_name:
        task_args["name"] = worker_name
    _, task_res, _ = _send_tool_call(app, session_id, "task", task_args)
    job_id = str(
        (task_res.get("dispatch") or {}).get("worker_id")
        or ((task_res.get("invocation") or {}).get("evidence") or {}).get("owner_ref")
        or ((task_res.get("invocation") or {}).get("evidence") or {}).get("ref")
        or (task_res.get("dispatch") or {}).get("job_id")
        or ""
    )
    if not job_id:
        raise RuntimeError(f"worker dispatch did not return job_id: {task_res}")

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
                {"id": wait_call_id, "name": "wait", "args": {"timeout_ms": 60000, "conditions": [{"kind": "next_worker_done"}]}}
            ],
        },
    )

    def _child_sid() -> str | None:
        row = _ro_one(db_path, "SELECT child_session_id, status, error FROM worker_jobs WHERE id=?", (job_id,))
        if not row:
            return None
        if row.get("status") in {"failed", "canceled", "aborted"}:
            raise RuntimeError(f"worker {job_id} failed: {row.get('error')}")
        return str(row["child_session_id"]) if row.get("child_session_id") else None

    child_sid = app.wait_for(_child_sid, "tb10 worker child sid", session_id)
    return job_id, child_sid, wait_call_id


def _settle_worker_and_promote(
    app: Any,
    db_path: pathlib.Path,
    session_id: str,
    child_sid: str,
    job_id: str,
    wait_call_id: str,
    *,
    brief: str,
    objectives_met: list[str],
    promote: bool = True,
) -> dict[str, Any]:
    """Complete the child worker leg, settle `wait`, resolve result handle via `pack_board`, and optionally promote."""
    child_done = _wait_pending(app, child_sid)
    if "complete_leg" in (child_done.get("tools") or ()):
        _send_tool_call(
            app,
            child_sid,
            "complete_leg",
            {"leg_status": "complete", "brief": brief, "objectives_met": objectives_met},
        )
    else:
        app.request("/harness/llm/respond", {"id": child_done["id"], "content": brief})

    def _settled() -> bool:
        row = _ro_one(db_path, "SELECT status, error FROM worker_jobs WHERE id=?", (job_id,))
        if not row:
            return False
        if row.get("status") in {"failed", "canceled", "aborted"}:
            raise RuntimeError(f"worker {job_id} failed: {row.get('error')}")
        return row.get("status") == "complete"

    app.wait_for(_settled, "tb10 worker settled", session_id)
    _wait_tool_result(app, session_id, wait_call_id)

    _, board_res, _ = _send_tool_call(app, session_id, "pack_board", {})
    result_handle = job_id
    hrow = _ro_one(
        db_path,
        "SELECT id FROM worker_results WHERE worker_job_id=? ORDER BY created_at DESC LIMIT 1",
        (job_id,),
    )
    if hrow and hrow.get("id"):
        result_handle = str(hrow["id"])

    promote_res: dict[str, Any] = {}
    if promote:
        _, promote_res, _ = _send_tool_call(app, session_id, "promote_overlay", {"overlay_id": result_handle})

    return {
        "result_handle": result_handle,
        "board_outcome": board_res.get("outcome") if isinstance(board_res, dict) else None,
        "promote_outcome": promote_res.get("outcome") if isinstance(promote_res, dict) else None,
    }


def _create_managed_secret(
    app: Any,
    project_id: str,
    *,
    name: str,
    purpose: str,
    secret_value: str,
) -> tuple[int, str, str]:
    """Create a project-scoped managed secret via `/v1/projects/{id}/secrets`; returns `(http_status, reference, secret_id)`."""
    st, body = _http_call(
        app,
        f"/v1/projects/{project_id}/secrets",
        {
            "operation_id": str(uuid.uuid4()),
            "name": name,
            "purpose": purpose,
            "secret_value": secret_value,
        },
        method="POST",
    )
    ref = str((body or {}).get("reference") or "") if isinstance(body, dict) else ""
    if not ref and isinstance(body, dict):
        m = _SECRET_REF_RE.search(json.dumps(body))
        if m:
            ref = m.group(0)
    sec_id = ""
    if ref.startswith("{{paintedwolf-secret:") and ref.endswith("}}"):
        sec_id = ref[len("{{paintedwolf-secret:") : -len("}}")]
    return st, ref, sec_id


def _capture_request_facts(req: dict[str, Any]) -> dict[str, Any]:
    encoded = json.dumps(req, sort_keys=True).encode("utf-8")
    msgs = req.get("messages") or []
    return {
        "request_id": str(req.get("id") or ""),
        "session_id": str(req.get("session_id") or ""),
        "offered_tools": sorted(str(t) for t in (req.get("tools") or ())),
        "message_count": len(msgs),
        "request_bytes": len(encoded),
        "request_sha256": hashlib.sha256(encoded).hexdigest(),
    }


def _extract_screened_guidance_observations(
    refusal_res: dict[str, Any],
    post_refusal_req: dict[str, Any],
    *,
    raw_markup_probe: str,
) -> dict[str, Any]:
    """Extract synthetic, screened guidance & markup observations without dumping raw prompts."""
    offered = sorted(str(t) for t in (post_refusal_req.get("tools") or ()))
    encoded_req = json.dumps(post_refusal_req, sort_keys=True).encode("utf-8")
    refusal_text = json.dumps(refusal_res, sort_keys=True)
    messages = post_refusal_req.get("messages") or []
    tool_or_system_msgs = [
        m for m in messages if isinstance(m, dict) and str(m.get("role") or "") in {"tool", "system"}
    ]
    rendered_guidance_texts: list[str] = []
    for m in tool_or_system_msgs:
        content = m.get("content")
        if isinstance(content, str) and content.strip():
            rendered_guidance_texts.append(content)
        elif isinstance(content, list):
            for part in content:
                if isinstance(part, dict) and isinstance(part.get("text"), str):
                    rendered_guidance_texts.append(part["text"])

    combined_guidance = "\n".join(rendered_guidance_texts)
    # Check whether refusal feedback / guidance text is present and within ceiling.
    feedback_items = refusal_res.get("feedback") if isinstance(refusal_res, dict) else None
    feedback_count = len(feedback_items) if isinstance(feedback_items, list) else 0
    # Extract any backticked tool-like identifiers mentioned in refusal feedback that match known tool names.
    mentioned_tools = sorted(
        {
            tok
            for tok in re.findall(r"`([a-z][a-z0-9_]+)`", refusal_text)
            if tok in offered or tok in {"command", "edit", "write", "read", "task", "wait", "promote_overlay"}
        }
    )
    disallowed_mentioned = sorted(t for t in mentioned_tools if t not in set(offered))
    raw_markup_in_refusal = raw_markup_probe in refusal_text
    raw_markup_in_guidance = raw_markup_probe in combined_guidance
    return {
        "offered_tools": offered,
        "mentioned_tools": mentioned_tools,
        "disallowed_mentioned_tools": disallowed_mentioned,
        "feedback_count": feedback_count,
        "rendered_guidance_message_count": len(rendered_guidance_texts),
        "rendered_guidance_bytes": len(combined_guidance.encode("utf-8")),
        "rendered_guidance_sha256": sha256_bytes(combined_guidance.encode("utf-8")),
        "post_refusal_request_bytes": len(encoded_req),
        "within_request_ceiling": 0 < len(encoded_req) <= SHIPPED_REQUEST_CEILING_BYTES,
        "markup_probe_sha256": sha256_bytes(raw_markup_probe.encode("utf-8")),
        "raw_markup_present_in_refusal": raw_markup_in_refusal,
        "raw_markup_present_in_guidance": raw_markup_in_guidance,
    }


def run_product_episode(
    variant: dict[str, Any],
    v_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    engine_manifest: pathlib.Path | None,
    engine_root: pathlib.Path | None,
    canaries: CanaryGenerator,
) -> dict[str, Any]:
    """Execute the real product episode for `variant` against pinned `lycaon-dev`."""
    kind = variant["episode_kind"]
    proj = v_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    (proj / "worker_target.txt").write_text("baseline line\n", encoding="utf-8")
    (proj / "notes.txt").write_text("alpha\nbeta\n", encoding="utf-8")
    _init_git_repo(proj)
    pre_tree = hash_tree(proj)

    outside_dir = v_dir / "outside-root"
    outside_dir.mkdir(parents=True, exist_ok=True)
    outside_target = outside_dir / "protected.txt"
    outside_target.write_text("immutable-outside\n", encoding="utf-8")
    outside_pre = hash_file(outside_target)

    app_dir = v_dir / "app"
    app = _create_application(
        app_dir,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        engine_root=engine_root,
    )
    session_id = ""
    project_id = ""
    captured_requests: list[dict[str, Any]] = []
    episode_facts: dict[str, Any] = {"kind": kind}
    api_readback: dict[str, Any] = {}
    network_fixtures: list[dict[str, Any]] = []

    db_path = app_dir / "store.db"
    try:
        app.ready()
        session_id, project_id, root_id = _open_session(app, proj)
        api_readback["health"] = app.request("/health")
        api_readback["projects"] = app.request("/v1/projects")

        prompt_resp = app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": f"TB10 {variant['id']}: {variant['title']}"},
        )
        first_req = _wait_pending(app, session_id)
        captured_requests.append(_capture_request_facts(first_req))

        if kind == "worker_outside_write":
            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Dispatch worker and verify boundary\n- [ ] Land result\n"},
            )
            job_id, child_sid, wait_call_id = _dispatch_worker_and_wait_child(
                app,
                db_path,
                session_id,
                goal="Update worker_target.txt",
                done_when=["worker_target.txt updated"],
                scope_paths=["worker_target.txt"],
            )
            # Worker attempts outside-workspace write -> confinement refusal (additional evidence), host bytes unchanged.
            _, worker_refusal_res, worker_req = _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": f"printf 'mutated\\n' > {outside_target}"},
                checkpoint_policy="reject",
            )
            captured_requests.append(_capture_request_facts(worker_req))
            worker_refusal_code = _extract_refusal_code(worker_refusal_res)

            # Worker exercises a real HITL approval checkpoint + saved approval rule against synthetic loopback fixtures.
            with (
                RecordingHTTPFixture(fixture_id="tb10-worker-allowed", redact_tokens=tuple(canaries.tokens)) as allowed_ep,
                RecordingHTTPFixture(fixture_id="tb10-worker-refused", redact_tokens=tuple(canaries.tokens)) as refused_ep,
            ):
                allowed_cid, allowed_res, _ = _send_tool_call(
                    app,
                    child_sid,
                    "command",
                    {
                        "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\"",
                        "capability_request": {"loopback_connect": {"ports": [allowed_ep.port]}},
                    },
                    checkpoint_policy="approve_chat",
                )
                _, refused_net_res, _ = _send_tool_call(
                    app,
                    child_sid,
                    "command",
                    {
                        "command": f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=3).read()\"",
                        "capability_request": {"loopback_connect": {"ports": [refused_ep.port]}},
                    },
                    checkpoint_policy="reject",
                )
                allowed_snap = allowed_ep.snapshot()
                refused_snap = refused_ep.snapshot()
                network_fixtures = [allowed_snap, refused_snap]

            # Worker performs its permitted native edit inside its workspace overlay.
            _send_tool_call(app, child_sid, "command", {"command": "cat worker_target.txt"})
            _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": 'edit --path worker_target.txt --old-string "baseline line" --new-string "worker landed line"'},
            )
            # Verify primary tree is STILL untouched before promotion!
            primary_before_promote = hash_file(proj / "worker_target.txt")
            primary_untouched_before_promote = (primary_before_promote["sha256"] == pre_tree["worker_target.txt"])

            settled = _settle_worker_and_promote(
                app,
                db_path,
                session_id,
                child_sid,
                job_id,
                wait_call_id,
                brief="Updated worker_target.txt.",
                objectives_met=["worker_target.txt updated"],
                promote=True,
            )
            episode_facts.update({
                "job_id": job_id,
                "child_session_id": child_sid,
                "result_handle": settled["result_handle"],
                "worker_refusal_code": worker_refusal_code,
                "worker_allowed_call_id": allowed_cid,
                "worker_allowed_outcome": allowed_res.get("outcome") if isinstance(allowed_res, dict) else None,
                "worker_allowed_arrivals": allowed_snap["connection_count"],
                "worker_refused_arrivals": refused_snap["connection_count"],
                "worker_refused_net_code": _extract_refusal_code(refused_net_res),
                "outside_target_unchanged": hash_file(outside_target)["sha256"] == outside_pre["sha256"],
                "primary_untouched_before_promote": primary_untouched_before_promote,
                "promote_outcome": settled["promote_outcome"],
                "board_outcome": settled["board_outcome"],
                "expected_landed_sha256": sha256_bytes(b"worker landed line\n"),
                "actual_landed_sha256": hash_file(proj / "worker_target.txt")["sha256"],
            })

        elif kind == "worker_network_send":
            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Dispatch worker for port-scoped loopback network checks\n"},
            )
            job_id, child_sid, wait_call_id = _dispatch_worker_and_wait_child(
                app,
                db_path,
                session_id,
                goal="Verify port-scoped loopback send approval and refusal from worker",
                done_when=["Approved port reached once and refused port blocked with zero arrivals"],
                scope_paths=["worker_target.txt"],
                worker_name="net-worker",
            )
            with (
                RecordingHTTPFixture(fixture_id="tb10-x01v2-allowed", redact_tokens=tuple(canaries.tokens)) as allowed_ep,
                RecordingHTTPFixture(fixture_id="tb10-x01v2-refused", redact_tokens=tuple(canaries.tokens)) as refused_ep,
            ):
                # Approve once so port-scoped authority covers ONLY this invocation and does not widen to refused_ep.
                allowed_cid, allowed_res, req1 = _send_tool_call(
                    app,
                    child_sid,
                    "command",
                    {
                        "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\"",
                        "capability_request": {"loopback_connect": {"ports": [allowed_ep.port]}},
                    },
                    checkpoint_policy="approve_once",
                )
                captured_requests.append(_capture_request_facts(req1))
                refused_cid, refused_res, req2 = _send_tool_call(
                    app,
                    child_sid,
                    "command",
                    {
                        "command": f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=3).read()\"",
                        "capability_request": {"loopback_connect": {"ports": [refused_ep.port]}},
                    },
                    checkpoint_policy="reject",
                )
                captured_requests.append(_capture_request_facts(req2))
                allowed_snap = allowed_ep.snapshot()
                refused_snap = refused_ep.snapshot()
                network_fixtures = [allowed_snap, refused_snap]

            settled = _settle_worker_and_promote(
                app,
                db_path,
                session_id,
                child_sid,
                job_id,
                wait_call_id,
                brief="Completed worker loopback send approval/refusal checks.",
                objectives_met=["Approved port reached once and refused port blocked with zero arrivals"],
                promote=False,
            )
            episode_facts.update({
                "job_id": job_id,
                "child_session_id": child_sid,
                "result_handle": settled["result_handle"],
                "board_outcome": settled["board_outcome"],
                "allowed_call_id": allowed_cid,
                "allowed_outcome": allowed_res.get("outcome") if isinstance(allowed_res, dict) else None,
                "allowed_result": allowed_res,
                "allowed_arrivals": allowed_snap["connection_count"],
                "refused_call_id": refused_cid,
                "refused_outcome": refused_res.get("outcome") if isinstance(refused_res, dict) else None,
                "refused_arrivals": refused_snap["connection_count"],
                "refused_code": _extract_refusal_code(refused_res),
            })

        elif kind == "worker_host_exec":
            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Dispatch worker to verify host-exec refusal vs confined edit\n"},
            )
            job_id, child_sid, wait_call_id = _dispatch_worker_and_wait_child(
                app,
                db_path,
                session_id,
                goal="Test host-exec authority refusal and confined inside-workspace edit",
                done_when=["Host execution refused, outside target unchanged, inside edit completed"],
                scope_paths=["notes.txt"],
                worker_name="hostexec-worker",
            )
            # 1. Worker requests unsandboxed host execution -> rejected at HITL checkpoint.
            host_exec_cid, host_exec_res, req1 = _send_tool_call(
                app,
                child_sid,
                "command",
                {
                    "command": "python3 -B -c \"import os; print(os.uname().sysname)\"",
                    "capability_request": {"host_execution": True},
                },
                checkpoint_policy="reject",
            )
            captured_requests.append(_capture_request_facts(req1))
            # 2. Worker attempts confined outside-workspace write -> confinement refusal.
            outside_cid, outside_res, req2 = _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": f"printf 'confined-mutated\\n' > {outside_target}"},
                checkpoint_policy="reject",
            )
            captured_requests.append(_capture_request_facts(req2))
            # 3. Worker performs permitted inside-workspace edit -> succeeds cleanly.
            _send_tool_call(app, child_sid, "command", {"command": "cat notes.txt"})
            _, inside_res, _ = _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": "edit --path notes.txt --old-string beta --new-string BETA"},
            )
            settled = _settle_worker_and_promote(
                app,
                db_path,
                session_id,
                child_sid,
                job_id,
                wait_call_id,
                brief="Host execution refused; confined edit to notes.txt completed.",
                objectives_met=["Host execution refused, outside target unchanged, inside edit completed"],
                promote=True,
            )
            episode_facts.update({
                "job_id": job_id,
                "child_session_id": child_sid,
                "result_handle": settled["result_handle"],
                "board_outcome": settled["board_outcome"],
                "promote_outcome": settled["promote_outcome"],
                "host_exec_call_id": host_exec_cid,
                "host_exec_outcome": host_exec_res.get("outcome") if isinstance(host_exec_res, dict) else None,
                "host_exec_refusal_code": _extract_refusal_code(host_exec_res),
                "outside_write_call_id": outside_cid,
                "outside_write_refusal_code": _extract_refusal_code(outside_res),
                "outside_target_unchanged": hash_file(outside_target)["sha256"] == outside_pre["sha256"],
                "inside_outcome": inside_res.get("outcome") if isinstance(inside_res, dict) else None,
                "actual_inside_sha256": hash_file(proj / "notes.txt")["sha256"],
                "expected_inside_sha256": sha256_bytes(b"alpha\nBETA\n"),
            })

        elif kind in {"rewind_landed_turn", "restore_pre_landing_version", "rewind_blocker_later_contrib"}:
            # Record a pre-landing version of worker_target.txt in source_versions so restore has an explicit baseline version.
            _, prog_res, _ = _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Dispatch worker and land change\n- [ ] Verify history operation\n"},
            )
            if kind == "restore_pre_landing_version":
                # Reconcile initial presentation baseline into source_versions before worker landing.
                _http_call(app, f"/v1/projects/{project_id}/source/walk?baseline=presentation", method="GET")
                _send_tool_call(app, session_id, "command", {"command": "cat worker_target.txt"})
                _send_tool_call(
                    app,
                    session_id,
                    "command",
                    {"command": 'edit --path worker_target.txt --old-string "baseline line" --new-string "baseline-v0"'},
                )
                _send_tool_call(
                    app,
                    session_id,
                    "command",
                    {"command": 'edit --path worker_target.txt --old-string "baseline-v0" --new-string "baseline line"'},
                )

            job_id, child_sid, wait_call_id = _dispatch_worker_and_wait_child(
                app,
                db_path,
                session_id,
                goal="Update worker_target.txt for X02 history check",
                done_when=["worker_target.txt updated"],
                scope_paths=["worker_target.txt"],
                worker_name="x02-worker",
            )
            _, _, worker_req = _send_tool_call(app, child_sid, "command", {"command": "cat worker_target.txt"})
            captured_requests.append(_capture_request_facts(worker_req))
            _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": 'edit --path worker_target.txt --old-string "baseline line" --new-string "worker landed line"'},
            )
            settled = _settle_worker_and_promote(
                app,
                db_path,
                session_id,
                child_sid,
                job_id,
                wait_call_id,
                brief="Updated worker_target.txt.",
                objectives_met=["worker_target.txt updated"],
                promote=True,
            )
            _complete_progress(
                app,
                session_id,
                prog_res,
                "## Progress\n- [x] Dispatch worker and land change\n- [x] Verify history operation\n",
            )
            _finish_turn_text(app, session_id, "Landed worker_target.txt update.")

            landed_sha = hash_file(proj / "worker_target.txt")["sha256"]
            anchor_id = _extract_anchor_message_id(app, db_path, session_id, prompt_resp)
            versions_after_landing = _list_file_versions_ro(db_path, project_id, "worker_target.txt")

            episode_facts.update({
                "job_id": job_id,
                "child_session_id": child_sid,
                "result_handle": settled["result_handle"],
                "board_outcome": settled["board_outcome"],
                "promote_outcome": settled["promote_outcome"],
                "anchor_message_id": anchor_id,
                "pre_landing_sha256": pre_tree["worker_target.txt"],
                "landed_sha256": landed_sha,
                "expected_landed_sha256": sha256_bytes(b"worker landed line\n"),
                "versions_after_landing_count": len(versions_after_landing),
            })

            if kind == "rewind_landed_turn":
                st_prev, prev_body = _http_call(
                    app,
                    f"/v1/sessions/{session_id}/rewind/preview",
                    {"message_id": anchor_id},
                    method="POST",
                )
                plan_digest = str((prev_body or {}).get("plan_digest") or "") if isinstance(prev_body, dict) else ""
                st_rw, rw_body = _http_call(
                    app,
                    f"/v1/sessions/{session_id}/rewind",
                    {"operation_id": str(uuid.uuid4()), "message_id": anchor_id, "plan_digest": plan_digest},
                    method="POST",
                )
                post_rewind_sha = hash_file(proj / "worker_target.txt")["sha256"]
                episode_facts.update({
                    "rewind_preview_status": st_prev,
                    "plan_digest_present": bool(plan_digest),
                    "rewind_status": st_rw,
                    "rewind_response": rw_body,
                    "post_rewind_sha256": post_rewind_sha,
                    "disk_restored_to_pre_landing": post_rewind_sha == pre_tree["worker_target.txt"],
                })

            elif kind == "restore_pre_landing_version":
                pre_sha = pre_tree["worker_target.txt"]
                pre_row = next(
                    (v for v in versions_after_landing if v.get("content_sha256") == pre_sha),
                    versions_after_landing[0] if versions_after_landing else {},
                )
                st_rest, rest_body = _http_call(
                    app,
                    f"/v1/projects/{project_id}/source/versions/{pre_row.get('version_id', '')}/restore",
                    {
                        "operation_id": str(uuid.uuid4()),
                        "file_id": pre_row.get("file_id", ""),
                        "root_id": root_id,
                        "path": "worker_target.txt",
                        "base": {"state": "content", "sha256": landed_sha},
                    },
                    method="POST",
                )
                post_restore_sha = hash_file(proj / "worker_target.txt")["sha256"]
                versions_after_restore = _list_file_versions_ro(db_path, project_id, "worker_target.txt")
                episode_facts.update({
                    "restore_target_version_id": pre_row.get("version_id", ""),
                    "restore_status": st_rest,
                    "restore_response": rest_body,
                    "post_restore_sha256": post_restore_sha,
                    "disk_restored_to_pre_landing": post_restore_sha == pre_sha,
                    "versions_after_restore_count": len(versions_after_restore),
                })

            else:  # rewind_blocker_later_contrib
                st_prev1, prev1_body = _http_call(
                    app,
                    f"/v1/sessions/{session_id}/rewind/preview",
                    {"message_id": anchor_id},
                    method="POST",
                )
                plan_digest = str((prev1_body or {}).get("plan_digest") or "") if isinstance(prev1_body, dict) else ""
                later_text = "worker landed line\nlater human contribution\n"
                st_put, put_body = _http_call(
                    app,
                    f"/v1/projects/{project_id}/source",
                    {
                        "operation_id": str(uuid.uuid4()),
                        "root_id": root_id,
                        "path": "worker_target.txt",
                        "content": later_text,
                        "encoding": "utf-8",
                        "base_sha256": landed_sha,
                    },
                    method="PUT",
                )
                later_sha = hash_file(proj / "worker_target.txt")["sha256"]
                st_prev2, prev2_body = _http_call(
                    app,
                    f"/v1/sessions/{session_id}/rewind/preview",
                    {"message_id": anchor_id},
                    method="POST",
                )
                st_rw, rw_body = _http_call(
                    app,
                    f"/v1/sessions/{session_id}/rewind",
                    {"operation_id": str(uuid.uuid4()), "message_id": anchor_id, "plan_digest": plan_digest},
                    method="POST",
                )
                post_blocker_sha = hash_file(proj / "worker_target.txt")["sha256"]
                episode_facts.update({
                    "later_contrib_status": st_put,
                    "later_contrib_sha256": later_sha,
                    "expected_later_contrib_sha256": sha256_bytes(later_text.encode("utf-8")),
                    "initial_preview_status": st_prev1,
                    "post_contrib_preview_status": st_prev2,
                    "post_contrib_preview_blockers": (prev2_body or {}).get("blockers") if isinstance(prev2_body, dict) else None,
                    "rewind_status": st_rw,
                    "rewind_response": rw_body,
                    "rewind_blocked": st_rw != 200,
                    "disk_preserved_after_blocker": post_blocker_sha == later_sha,
                })

        elif kind == "secret_in_agent_edit":
            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Generate managed secret and apply reference in notes.txt edit\n"},
            )
            _, gen_res, req0 = _send_tool_call(
                app,
                session_id,
                "secret_generate",
                {
                    "name": "tb10-x03-v1-edit-secret",
                    "scope": "project",
                    "purpose": "TB10 X03-v1 managed secret custody through agent edit",
                },
            )
            captured_requests.append(_capture_request_facts(req0))
            m_ref = _SECRET_REF_RE.search(json.dumps(gen_res))
            secret_ref = m_ref.group(0) if m_ref else ""
            secret_id = (
                secret_ref[len("{{paintedwolf-secret:") : -len("}}")]
                if secret_ref.startswith("{{paintedwolf-secret:") and secret_ref.endswith("}}")
                else ""
            )
            _, read_res, req1 = _send_tool_call(app, session_id, "command", {"command": "cat notes.txt"})
            captured_requests.append(_capture_request_facts(req1))
            disk_before_write = str(hash_file(proj / "notes.txt").get("sha256") or "")
            _, write_res, req2 = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f'edit --path notes.txt --old-string beta --new-string "API_KEY={secret_ref}"'},
                checkpoint_policy="approve_once",
            )
            captured_requests.append(_capture_request_facts(req2))
            _, readback_res, req3 = _send_tool_call(app, session_id, "command", {"command": "cat notes.txt"})
            captured_requests.append(_capture_request_facts(req3))
            # Inspect materialized file on disk in memory ONLY to register its value for D6 canary scanning
            # and verify substitution; NEVER log or persist raw secret bytes!
            disk_text = (proj / "notes.txt").read_text(encoding="utf-8", errors="replace")
            materialized_val = ""
            for line in disk_text.splitlines():
                if line.startswith("API_KEY="):
                    materialized_val = line[len("API_KEY=") :]
                    break
            materialized_ok = (
                write_res.get("outcome") == "completed"
                and bool(materialized_val)
                and materialized_val != secret_ref
                and "{{paintedwolf-secret:" not in materialized_val
            )
            if materialized_ok and materialized_val not in canaries.tokens:
                canaries.tokens.append(materialized_val)
            actual_after_sha = hash_file(proj / "notes.txt")["sha256"]
            readback_str = json.dumps(readback_res, sort_keys=True)
            episode_facts.update({
                "secret_generate_outcome": gen_res.get("outcome") if isinstance(gen_res, dict) else None,
                "secret_id": secret_id,
                "secret_ref": secret_ref,
                "read_outcome": read_res.get("outcome") if isinstance(read_res, dict) else None,
                "write_outcome": write_res.get("outcome") if isinstance(write_res, dict) else None,
                "readback_outcome": readback_res.get("outcome") if isinstance(readback_res, dict) else None,
                "disk_before_write": disk_before_write,
                "actual_after_sha256": actual_after_sha,
                "expected_after_sha256": actual_after_sha if materialized_ok else "",
                "materialized_bytes_len": len(materialized_val.encode("utf-8")) if materialized_ok else 0,
                "materialized_on_disk": materialized_ok,
                "readback_contains_ref": bool(secret_ref and secret_ref in readback_str),
                "readback_contains_raw_canary": bool(materialized_val and materialized_val in readback_str),
            })

        elif kind == "secret_in_worker_result":
            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Generate managed secret and dispatch worker\n- [ ] Land screened worker result\n"},
            )
            _, gen_res, req0 = _send_tool_call(
                app,
                session_id,
                "secret_generate",
                {
                    "name": "tb10-x03-v2-worker-secret",
                    "scope": "project",
                    "purpose": "TB10 X03-v2 managed secret custody through worker result",
                },
            )
            captured_requests.append(_capture_request_facts(req0))
            m_ref = _SECRET_REF_RE.search(json.dumps(gen_res))
            secret_ref = m_ref.group(0) if m_ref else ""
            secret_id = (
                secret_ref[len("{{paintedwolf-secret:") : -len("}}")]
                if secret_ref.startswith("{{paintedwolf-secret:") and secret_ref.endswith("}}")
                else ""
            )
            job_id, child_sid, wait_call_id = _dispatch_worker_and_wait_child(
                app,
                db_path,
                session_id,
                goal=f"Configure worker_target.txt using managed secret reference {secret_ref}",
                done_when=["worker_target.txt configured via managed secret reference"],
                scope_paths=["worker_target.txt"],
                worker_name="secret-worker",
            )
            _, _, worker_req1 = _send_tool_call(app, child_sid, "command", {"command": "cat worker_target.txt"})
            captured_requests.append(_capture_request_facts(worker_req1))
            _, worker_edit_res, worker_req2 = _send_tool_call(
                app,
                child_sid,
                "command",
                {"command": f'edit --path worker_target.txt --old-string "baseline line" --new-string "WORKER_TOKEN={secret_ref}"'},
                checkpoint_policy="approve_once",
            )
            captured_requests.append(_capture_request_facts(worker_req2))
            _, worker_readback_res, _ = _send_tool_call(app, child_sid, "command", {"command": "cat worker_target.txt"})
            primary_untouched_before_promote = (hash_file(proj / "worker_target.txt")["sha256"] == pre_tree["worker_target.txt"])

            settled = _settle_worker_and_promote(
                app,
                db_path,
                session_id,
                child_sid,
                job_id,
                wait_call_id,
                brief=f"Configured worker_target.txt using managed secret reference {secret_ref}.",
                objectives_met=["worker_target.txt configured via managed secret reference"],
                promote=True,
            )
            disk_text = (proj / "worker_target.txt").read_text(encoding="utf-8", errors="replace")
            materialized_val = ""
            for line in disk_text.splitlines():
                if line.startswith("WORKER_TOKEN="):
                    materialized_val = line[len("WORKER_TOKEN=") :]
                    break
            materialized_ok = (
                worker_edit_res.get("outcome") == "completed"
                and settled.get("promote_outcome") == "completed"
                and bool(materialized_val)
                and materialized_val != secret_ref
                and "{{paintedwolf-secret:" not in materialized_val
            )
            if materialized_ok and materialized_val not in canaries.tokens:
                canaries.tokens.append(materialized_val)
            actual_landed_sha = hash_file(proj / "worker_target.txt")["sha256"]
            worker_readback_str = json.dumps(worker_readback_res, sort_keys=True)
            episode_facts.update({
                "secret_generate_outcome": gen_res.get("outcome") if isinstance(gen_res, dict) else None,
                "secret_id": secret_id,
                "secret_ref": secret_ref,
                "job_id": job_id,
                "child_session_id": child_sid,
                "result_handle": settled["result_handle"],
                "board_outcome": settled["board_outcome"],
                "promote_outcome": settled["promote_outcome"],
                "worker_edit_outcome": worker_edit_res.get("outcome") if isinstance(worker_edit_res, dict) else None,
                "primary_untouched_before_promote": primary_untouched_before_promote,
                "expected_landed_sha256": actual_landed_sha if materialized_ok else "",
                "actual_landed_sha256": actual_landed_sha,
                "materialized_bytes_len": len(materialized_val.encode("utf-8")) if materialized_ok else 0,
                "materialized_on_disk": materialized_ok,
                "worker_readback_contains_ref": bool(secret_ref and secret_ref in worker_readback_str),
                "worker_readback_contains_raw_canary": bool(materialized_val and materialized_val in worker_readback_str),
            })

        else:  # guidance_and_markup_screen
            raw_markup_probe = "<script>alert('tb10-markup')</script>"
            markup_outside = outside_dir / "tb10-markup-target.txt"
            markup_outside.write_text("immutable-markup-outside\n", encoding="utf-8")
            markup_outside_pre = hash_file(markup_outside)

            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Observe refusal guidance, markup screening, and authorized next call\n"},
            )
            _, refused_res, req1 = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"printf '{raw_markup_probe}\\n' > {markup_outside}"},
                checkpoint_policy="reject",
            )
            captured_requests.append(_capture_request_facts(req1))
            # Capture the post-refusal model turn request (contains rendered guidance + offered tools!).
            post_refusal_req = _wait_pending(app, session_id)
            captured_requests.append(_capture_request_facts(post_refusal_req))
            guidance_obs = _extract_screened_guidance_observations(
                refused_res,
                post_refusal_req,
                raw_markup_probe=raw_markup_probe,
            )
            # Execute an authorized next call from the offered tools.
            _, read_res, _ = _send_tool_call(app, session_id, "command", {"command": "cat notes.txt"})
            _, inside_res, req_next = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": "edit --path notes.txt --old-string beta --new-string BETA"},
            )
            captured_requests.append(_capture_request_facts(req_next))
            episode_facts.update({
                "refusal_code": _extract_refusal_code(refused_res),
                "outside_target_unchanged": hash_file(markup_outside)["sha256"] == markup_outside_pre["sha256"],
                "guidance_observations": guidance_obs,
                "authorized_next_call_tool": "command",
                "authorized_next_call_offered": "command" in set(guidance_obs["offered_tools"]),
                "read_outcome": read_res.get("outcome") if isinstance(read_res, dict) else None,
                "inside_outcome": inside_res.get("outcome") if isinstance(inside_res, dict) else None,
                "actual_inside_sha256": hash_file(proj / "notes.txt")["sha256"],
                "expected_inside_sha256": sha256_bytes(b"alpha\nBETA\n"),
            })

        api_readback["session"] = app.request(f"/v1/sessions/{session_id}")
        api_readback["messages"] = app.request(f"/v1/sessions/{session_id}/messages")
        if api_readback["session"].get("status") != "idle":
            app.request(f"/v1/sessions/{session_id}/abort", {})
    finally:
        app.close(cleanup=False)

    # Isolated per-variant DB: read all rows so child worker checkpoints/authz_events are included.
    db_obs = read_sqlite_tables_ro(db_path, _tables_for_variant(variant), session_id=None)
    row_counts = {t: len(rows) for t, rows in (db_obs.get("rows") or {}).items()}
    total_rows = sum(row_counts.values())

    # Attach typed witness extractions where applicable.
    if kind == "worker_network_send":
        auth_obs = {"sqlite_records": db_obs}
        episode_facts["recorded_connections"] = _extract_product_network_witnesses(
            auth_obs,
            allowed_call_id=episode_facts.get("allowed_call_id", ""),
            allowed_tool_result=episode_facts.get("allowed_result") or {},
        )
        episode_facts["planned_approved_destinations"] = _extract_planned_approved_destinations(
            auth_obs,
            allowed_call_id=episode_facts.get("allowed_call_id", ""),
            allowed_tool_result=episode_facts.get("allowed_result") or {},
        )
        episode_facts.pop("allowed_result", None)
    elif kind == "secret_in_agent_edit":
        attrs, ops = _extract_edit_records_from_db(
            {"sqlite_records": db_obs},
            rel_path="notes.txt",
            read_result={"outcome": episode_facts.get("read_outcome")},
            write_result={"outcome": episode_facts.get("write_outcome")},
            disk_sha_before_write=episode_facts.get("disk_before_write", ""),
            expected_after_sha=episode_facts.get("expected_after_sha256", ""),
        )
        episode_facts["attributions"] = attrs
        episode_facts["edit_operations"] = ops
    elif kind in {
        "worker_outside_write",
        "worker_host_exec",
        "rewind_landed_turn",
        "restore_pre_landing_version",
        "rewind_blocker_later_contrib",
        "secret_in_worker_result",
    }:
        target_rel = "notes.txt" if kind == "worker_host_exec" else "worker_target.txt"
        expected_hash = (
            episode_facts.get("expected_inside_sha256", "")
            if kind == "worker_host_exec"
            else episode_facts.get("expected_landed_sha256", "")
        )
        episode_facts["worker_landing"] = _extract_worker_landing_from_db(
            {"sqlite_records": db_obs},
            job_id=episode_facts.get("job_id", ""),
            promote_result={"outcome": episode_facts.get("promote_outcome")},
            expected_post_hashes={target_rel: expected_hash},
        )

    post_tree = hash_tree(proj)
    return {
        "session_id": session_id,
        "project_id": project_id,
        "db_path": str(db_path),
        "pre_tree_hashes": pre_tree,
        "post_tree_hashes": post_tree,
        "captured_requests": captured_requests,
        "episode_facts": episode_facts,
        "network_fixtures": network_fixtures,
        "api_readback": api_readback,
        "sqlite_row_counts": row_counts,
        "sqlite_total_rows": total_rows,
        "sqlite_observations": db_obs,
    }


def verify_episode_depth(variant: dict[str, Any], episode: dict[str, Any]) -> dict[str, Any]:
    """Fail-closed check that the authoritative episode matches the ledger depth for `variant`."""
    kind = str(variant.get("episode_kind") or "")
    counts = episode.get("sqlite_row_counts") or {}
    facts = episode.get("episode_facts") or {}
    missing: list[str] = []

    def _need_count(table: str, minimum: int = 1) -> None:
        if int(counts.get(table) or 0) < minimum:
            missing.append(f"sqlite:{table}<{minimum}")

    if kind == "worker_outside_write":
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("worker_results", 1)
        _need_count("checkpoints", 1)
    elif kind == "worker_network_send":
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("checkpoints", 1)
        _need_count("authz_events", 1)
        if not facts.get("child_session_id"):
            missing.append("missing_child_session_id")
        if int(facts.get("allowed_arrivals") or 0) < 1:
            missing.append("allowed_arrivals<1")
        if int(facts.get("refused_arrivals") or 0) != 0:
            missing.append("refused_arrivals!=0")
    elif kind == "worker_host_exec":
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("checkpoints", 1)
        if not facts.get("child_session_id"):
            missing.append("missing_child_session_id")
        if facts.get("outside_target_unchanged") is not True:
            missing.append("outside_target_mutated")
        if facts.get("host_exec_outcome") == "completed":
            missing.append("host_exec_not_refused")
    elif kind == "rewind_landed_turn":
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("worker_results", 1)
        _need_count("rewind_operations", 1)
        _need_count("source_versions", 1)
        if facts.get("rewind_status") != 200:
            missing.append(f"rewind_status:{facts.get('rewind_status')}")
        if facts.get("disk_restored_to_pre_landing") is not True:
            missing.append("disk_not_restored_to_pre_landing")
    elif kind == "restore_pre_landing_version":
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("worker_results", 1)
        _need_count("source_versions", 2)
        if facts.get("restore_status") != 200:
            missing.append(f"restore_status:{facts.get('restore_status')}")
        if facts.get("disk_restored_to_pre_landing") is not True:
            missing.append("disk_not_restored_to_pre_landing")
    elif kind == "rewind_blocker_later_contrib":
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("worker_results", 1)
        _need_count("source_versions", 2)
        if facts.get("rewind_blocked") is not True:
            missing.append("rewind_blocker_not_enforced")
        if facts.get("disk_preserved_after_blocker") is not True:
            missing.append("disk_mutated_after_rewind_blocker")
    elif kind == "secret_in_agent_edit":
        _need_count("managed_secrets", 1)
        _need_count("source_versions", 1)
        if not facts.get("secret_ref"):
            missing.append("missing_secret_ref")
        if facts.get("materialized_on_disk") is not True:
            missing.append("secret_not_materialized_on_disk")
        if facts.get("readback_contains_raw_canary") is True:
            missing.append("raw_canary_in_readback")
    elif kind == "secret_in_worker_result":
        _need_count("managed_secrets", 1)
        _need_count("workers", 1)
        _need_count("worker_jobs", 1)
        _need_count("worker_results", 1)
        if not facts.get("secret_ref"):
            missing.append("missing_secret_ref")
        if facts.get("materialized_on_disk") is not True:
            missing.append("secret_not_materialized_on_disk")
        if facts.get("worker_readback_contains_raw_canary") is True:
            missing.append("raw_canary_in_worker_readback")
    elif kind == "guidance_and_markup_screen":
        gobs = facts.get("guidance_observations") or {}
        if not gobs.get("within_request_ceiling"):
            missing.append("guidance_exceeds_ceiling")
        if gobs.get("disallowed_mentioned_tools"):
            missing.append("guidance_mentions_disallowed_tool")
        if not facts.get("authorized_next_call_offered"):
            missing.append("authorized_next_call_not_offered")
        if facts.get("inside_outcome") != "completed":
            missing.append("authorized_next_call_not_completed")
        if facts.get("outside_target_unchanged") is not True:
            missing.append("outside_target_mutated")

    return {"kind": kind, "missing": missing, "passed": len(missing) == 0}


def run_tb10_variant(
    variant: dict[str, Any],
    out_dir: pathlib.Path,
    *,
    app_bin: pathlib.Path,
    engine_bin: pathlib.Path | None = None,
    engine_manifest: pathlib.Path | None = None,
    engine_root: pathlib.Path | None = None,
    api_url: str | None = None,
    token: str | None = None,
    native_fn: Callable[..., dict[str, Any]] = run_linux_tauri_native,
    episode_fn: Callable[..., dict[str, Any]] = run_product_episode,
) -> dict[str, Any]:
    """Execute one TB10 X01-X03 variant: real product episode + real native Den presentation."""
    vid = variant["id"]
    v_dir = out_dir / vid
    v_dir.mkdir(parents=True, exist_ok=True)
    profile_dir = v_dir / "tauri-profile"

    canaries = CanaryGenerator(f"tb10-{vid}")
    canary_token = canaries.mint(variant["family"].lower())
    canaries.materialize_env_file(v_dir / "isolated-canaries", {"WEA_TB10_CANARY": canary_token})

    # 1. Run the REAL product episode against pinned `lycaon-dev`.
    episode = episode_fn(
        variant,
        v_dir,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        engine_root=engine_root,
        canaries=canaries,
    )

    # 2. Run the REAL Linux `painted-wolf-code` Tauri desktop binary paired with pinned `LYCAON_SIDECAR_BIN`.
    prev_sidecar_env = os.environ.get("LYCAON_SIDECAR_BIN")
    if engine_bin is not None:
        os.environ["LYCAON_SIDECAR_BIN"] = str(engine_bin.resolve())
    try:
        native_receipt = native_fn(
            app_bin,
            v_dir / "native",
            api_url=api_url,
            token=token,
            profile_dir=profile_dir,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
            dismiss_onboarding=True,
            seed_store_db=episode.get("db_path") or None,
            open_project_hint="project",
            open_session_hint=vid,
            session_id=episode.get("session_id") or None,
        )
    finally:
        if engine_bin is not None:
            if prev_sidecar_env is None:
                os.environ.pop("LYCAON_SIDECAR_BIN", None)
            else:
                os.environ["LYCAON_SIDECAR_BIN"] = prev_sidecar_env

    prov = native_receipt.get("provenance") or collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )

    episode_payload = {
        "session_id": episode.get("session_id"),
        "project_id": episode.get("project_id"),
        "captured_requests": episode.get("captured_requests") or [],
        "episode_facts": episode.get("episode_facts"),
        "network_fixtures": episode.get("network_fixtures"),
        "api_readback": episode.get("api_readback"),
        "sqlite_row_counts": episode.get("sqlite_row_counts"),
        "sqlite_total_rows": episode.get("sqlite_total_rows"),
    }

    db_paths = [pathlib.Path(episode["db_path"])] if episode.get("db_path") and pathlib.Path(episode["db_path"]).is_file() else []
    canary_scan = scan_for_canaries(
        canaries.tokens,
        sqlite_dbs=db_paths,
        paths=[v_dir / "native", v_dir / "app" / "sidecar.log"],
        objects={"episode": episode_payload},
    )

    # Verify guidance surface & request byte ceiling from the ACTUAL captured provider request(s).
    captured_reqs = episode.get("captured_requests") or []
    latest_req = captured_reqs[-1] if captured_reqs else {}
    offered = latest_req.get("offered_tools") or []
    req_bytes = int(latest_req.get("request_bytes") or 0)
    suggested = [t for t in ("command", "promote_overlay") if t in offered]
    guidance = verify_guidance_surface(
        offered_tools=offered,
        suggested_tools=suggested,
        request_bytes=req_bytes,
    )
    depth_check = verify_episode_depth(variant, episode)

    provenance_verified = bool((prov.get("product_source") or {}).get("verified") is True)
    native_ok = bool(native_receipt.get("passed") is True)
    visual_or_focus_ok = bool(
        native_receipt.get("screenshot_changed") is True
        or native_receipt.get("atspi_focus_changed") is True
    )
    has_real_rows = int(episode.get("sqlite_total_rows") or 0) > 0
    has_session_readback = bool((episode.get("api_readback") or {}).get("session"))
    has_captured_req = bool(captured_reqs and req_bytes > 0)
    canary_ok = bool(canary_scan.get("clean") is True)
    guidance_ok = bool(guidance.get("passed") is True)
    depth_ok = bool(depth_check.get("passed") is True)

    reasons: list[str] = []
    if not provenance_verified:
        reasons.append("unverified_product_source")
    if not native_ok:
        reasons.append(f"native_den_failed:{native_receipt.get('blocked_reason') or native_receipt.get('status') or 'failed'}")
    if not visual_or_focus_ok:
        reasons.append("missing_pre_post_visual_or_focus_change")
    if not has_real_rows:
        reasons.append("zero_scenario_sqlite_rows")
    if not has_session_readback:
        reasons.append("missing_session_api_readback")
    if not has_captured_req:
        reasons.append("missing_captured_model_request")
    if not canary_ok:
        reasons.append("canary_in_record")
    if not guidance_ok:
        reasons.append("guidance_surface_or_ceiling_violation")
    if not depth_ok:
        reasons.append("episode_depth_incomplete:" + ",".join(depth_check["missing"]))

    passed = len(reasons) == 0
    receipt = {
        "schema": "wea-tb10-variant-receipt-v1",
        "card": "TB10",
        "family": variant["family"],
        "variant_id": vid,
        "title": variant["title"],
        "crosswalk": variant["crosswalk"],
        "expected_surfaces": variant["expected_surfaces"],
        "lane": "sidecar-native-den",
        "provenance": prov,
        "episode": episode_payload,
        "episode_depth_check": depth_check,
        "native_den": native_receipt,
        "canary_scan": canary_scan,
        "guidance_check": guidance,
        "failure_reasons": reasons,
        "verdict": "pass" if passed else "fail",
        "passed": passed,
    }
    receipt_path = v_dir / "receipt.json"
    encoded = json.dumps(receipt, indent=2, sort_keys=True) + "\n"
    receipt_path.write_text(encoded, encoding="utf-8")
    receipt["receipt_path"] = str(receipt_path)
    receipt["receipt_sha256"] = hashlib.sha256(encoded.encode("utf-8")).hexdigest()
    return receipt


def run_tb10_suite(
    out_dir: str | pathlib.Path,
    *,
    app_bin: str | pathlib.Path,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    api_url: str | None = None,
    token: str | None = None,
    variant_ids: Sequence[str] | None = None,
    native_fn: Callable[..., dict[str, Any]] = run_linux_tauri_native,
    episode_fn: Callable[..., dict[str, Any]] = run_product_episode,
) -> dict[str, Any]:
    """Run one or all TB10 X01-X03 variants and emit `tb10-manifest.json`."""
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    app_path = pathlib.Path(app_bin).resolve()
    eng_path = pathlib.Path(engine_bin).resolve() if engine_bin else None
    man_path = pathlib.Path(engine_manifest).resolve() if engine_manifest else None
    root_path = pathlib.Path(engine_root).resolve() if engine_root else None

    if variant_ids:
        selected = []
        for vid in variant_ids:
            if vid not in VARIANTS_BY_ID:
                raise ValueError(f"unknown TB10 variant: {vid}")
            selected.append(VARIANTS_BY_ID[vid])
    else:
        selected = list(TB10_VARIANTS)

    results: list[dict[str, Any]] = []
    for var in selected:
        results.append(
            run_tb10_variant(
                var,
                out,
                app_bin=app_path,
                engine_bin=eng_path,
                engine_manifest=man_path,
                engine_root=root_path,
                api_url=api_url,
                token=token,
                native_fn=native_fn,
                episode_fn=episode_fn,
            )
        )

    passed_count = sum(1 for r in results if r["passed"])
    failed_count = len(results) - passed_count
    suite_prov = collect_provenance(
        REPO_ROOT,
        engine_bin=eng_path,
        engine_manifest=man_path,
        lane="sidecar-native-den",
    )
    manifest = {
        "schema": "wea-tb10-manifest-v1",
        "card": "TB10",
        "families": sorted({r["family"] for r in results}),
        "planned_count": len(selected),
        "executed_count": len(results),
        "passed_count": passed_count,
        "failed_count": failed_count,
        "all_passed": failed_count == 0 and len(results) == len(selected),
        "provenance": suite_prov,
        "variants": [
            {
                "variant_id": r["variant_id"],
                "family": r["family"],
                "verdict": r["verdict"],
                "passed": r["passed"],
                "failure_reasons": r["failure_reasons"],
                "sqlite_total_rows": (r.get("episode") or {}).get("sqlite_total_rows", 0),
                "captured_request_count": len((r.get("episode") or {}).get("captured_requests") or []),
                "receipt_path": r["receipt_path"],
                "receipt_sha256": r["receipt_sha256"],
                "screenshot_before_sha256": (r.get("native_den") or {}).get("screenshot_before_sha256", ""),
                "screenshot_sha256": (r.get("native_den") or {}).get("screenshot_sha256", ""),
                "screenshot_changed": (r.get("native_den") or {}).get("screenshot_changed", False),
                "atspi_focus_changed": (r.get("native_den") or {}).get("atspi_focus_changed", False),
            }
            for r in results
        ],
    }
    (out / "tb10-manifest.json").write_text(
        json.dumps(manifest, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return manifest


def run_tb10_episodes_only(
    out_dir: str | pathlib.Path,
    *,
    engine_bin: str | pathlib.Path,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    variant_ids: Sequence[str] | None = None,
    episode_fn: Callable[..., dict[str, Any]] = run_product_episode,
) -> dict[str, Any]:
    """Run TB10 sidecar episode generation + depth & canary verification WITHOUT launching native Den."""
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    eng_path = pathlib.Path(engine_bin).resolve()
    man_path = pathlib.Path(engine_manifest).resolve() if engine_manifest else None
    root_path = pathlib.Path(engine_root).resolve() if engine_root else None

    if variant_ids:
        selected = []
        for vid in variant_ids:
            if vid not in VARIANTS_BY_ID:
                raise ValueError(f"unknown TB10 variant: {vid}")
            selected.append(VARIANTS_BY_ID[vid])
    else:
        selected = list(TB10_VARIANTS)

    results: list[dict[str, Any]] = []
    for var in selected:
        vid = var["id"]
        v_dir = out / vid
        v_dir.mkdir(parents=True, exist_ok=True)
        canaries = CanaryGenerator(f"tb10-{vid}")
        canary_token = canaries.mint(var["family"].lower())
        canaries.materialize_env_file(v_dir / "isolated-canaries", {"WEA_TB10_CANARY": canary_token})

        episode = episode_fn(
            var,
            v_dir,
            engine_bin=eng_path,
            engine_manifest=man_path,
            engine_root=root_path,
            canaries=canaries,
        )
        episode_payload = {
            "session_id": episode.get("session_id"),
            "project_id": episode.get("project_id"),
            "captured_requests": episode.get("captured_requests") or [],
            "episode_facts": episode.get("episode_facts"),
            "network_fixtures": episode.get("network_fixtures"),
            "api_readback": episode.get("api_readback"),
            "sqlite_row_counts": episode.get("sqlite_row_counts"),
            "sqlite_total_rows": episode.get("sqlite_total_rows"),
        }
        db_paths = [pathlib.Path(episode["db_path"])] if episode.get("db_path") and pathlib.Path(episode["db_path"]).is_file() else []
        canary_scan = scan_for_canaries(
            canaries.tokens,
            sqlite_dbs=db_paths,
            paths=[v_dir / "app" / "sidecar.log"],
            objects={"episode": episode_payload},
        )
        captured_reqs = episode_payload["captured_requests"]
        latest_req = captured_reqs[-1] if captured_reqs else {}
        offered = latest_req.get("offered_tools") or []
        req_bytes = int(latest_req.get("request_bytes") or 0)
        suggested = [t for t in ("command", "promote_overlay") if t in offered]
        guidance = verify_guidance_surface(offered_tools=offered, suggested_tools=suggested, request_bytes=req_bytes)
        depth_check = verify_episode_depth(var, episode)
        reasons: list[str] = []
        if int(episode.get("sqlite_total_rows") or 0) <= 0:
            reasons.append("zero_scenario_sqlite_rows")
        if not bool((episode.get("api_readback") or {}).get("session")):
            reasons.append("missing_session_api_readback")
        if not bool(captured_reqs and req_bytes > 0):
            reasons.append("missing_captured_model_request")
        if not bool(canary_scan.get("clean") is True):
            reasons.append("canary_in_record")
        if not bool(guidance.get("passed") is True):
            reasons.append("guidance_surface_or_ceiling_violation")
        if not bool(depth_check.get("passed") is True):
            reasons.append("episode_depth_incomplete:" + ",".join(depth_check["missing"]))
        passed = len(reasons) == 0
        rec = {
            "variant_id": vid,
            "family": var["family"],
            "episode_kind": var["episode_kind"],
            "passed": passed,
            "failure_reasons": reasons,
            "episode_depth_check": depth_check,
            "canary_clean": bool(canary_scan.get("clean") is True),
            "guidance_passed": bool(guidance.get("passed") is True),
            "sqlite_row_counts": episode.get("sqlite_row_counts"),
            "sqlite_total_rows": episode.get("sqlite_total_rows"),
            "episode_facts": episode.get("episode_facts"),
            "captured_request_count": len(captured_reqs),
        }
        (v_dir / "episode-receipt.json").write_text(json.dumps(rec, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        results.append(rec)

    passed_count = sum(1 for r in results if r["passed"])
    summary = {
        "schema": "wea-tb10-episodes-only-v1",
        "planned_count": len(selected),
        "passed_count": passed_count,
        "failed_count": len(results) - passed_count,
        "all_passed": passed_count == len(selected),
        "variants": results,
    }
    (out / "tb10-episodes-manifest.json").write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return summary


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB10 X01-X03 Linux native Den runner.")
    parser.add_argument("--app-bin", type=pathlib.Path, default=None, help="Path to built painted-wolf-code binary.")
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to pinned lycaon-dev binary.")
    parser.add_argument("--engine-manifest", type=pathlib.Path, default=None, help="Path to build-manifest.json.")
    parser.add_argument("--engine-root", type=pathlib.Path, default=None, help="Path to staged LYCAON_ENGINE_ROOT.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for TB10 receipts & manifest.")
    parser.add_argument("--variant", default=None, help="Run a single TB10 variant ID (e.g. X01-v1-worker-outside-write).")
    parser.add_argument("--variants", default="all", help="Comma-separated variant IDs or 'all' (default: all 9).")
    parser.add_argument("--episode-only", action="store_true", help="Run sidecar episode generation & depth verification only (no native Den launch).")
    parser.add_argument("--api-url", default=os.environ.get("LYCAON_E2E_API_URL"))
    parser.add_argument("--token", default=os.environ.get("LYCAON_E2E_TOKEN"))
    args = parser.parse_args(argv)

    if args.variant:
        selected = [args.variant.strip()]
    elif args.variants.strip().lower() == "all":
        selected = None
    else:
        selected = [v.strip() for v in args.variants.split(",") if v.strip()]

    if args.episode_only:
        manifest = run_tb10_episodes_only(
            args.out,
            engine_bin=args.engine,
            engine_manifest=args.engine_manifest,
            engine_root=args.engine_root,
            variant_ids=selected,
        )
        print(json.dumps(manifest, indent=2, sort_keys=True))
        return 0 if manifest["all_passed"] else 1

    if args.app_bin is None or args.engine_manifest is None:
        parser.error("--app-bin and --engine-manifest are required unless --episode-only is set")

    manifest = run_tb10_suite(
        args.out,
        app_bin=args.app_bin,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        engine_root=args.engine_root,
        api_url=args.api_url,
        token=args.token,
        variant_ids=selected,
    )
    print(json.dumps(manifest, indent=2, sort_keys=True))
    return 0 if manifest["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
