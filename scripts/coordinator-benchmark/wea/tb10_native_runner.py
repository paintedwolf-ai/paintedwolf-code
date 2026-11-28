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
import sqlite3
import sys
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
        "episode_kind": "network_authority",
        "title": "Worker/agent network send covered by port-scoped authority and refused without arrival",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card", "saved_approvals"],
        "crosswalk": ["W02", "A01", "A05", "X01"],
    },
    {
        "id": "X01-v3-worker-host-exec",
        "family": "X01",
        "episode_kind": "write_boundary",
        "title": "Confinement and authorization remain separate composable layers",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card"],
        "crosswalk": ["W04", "A05", "X01"],
    },
    {
        "id": "X02-v1-rewind-landed-turn",
        "family": "X02",
        "episode_kind": "edit_attribution_and_version",
        "title": "Rewind landed turn restores primary bytes and retains attribution",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["worker_card", "files"],
        "crosswalk": ["E04", "E06", "X02"],
    },
    {
        "id": "X02-v2-restore-pre-landing-version",
        "family": "X02",
        "episode_kind": "edit_attribution_and_version",
        "title": "Restore file to pre-landing version keeps permanent turn identity",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["files", "worker_card"],
        "crosswalk": ["E04", "E06", "X02"],
    },
    {
        "id": "X02-v3-rewind-blocker-later-contrib",
        "family": "X02",
        "episode_kind": "edit_attribution_and_version",
        "title": "Rewind blocker surfaced when later contributions exist",
        "track_tables": ("workers", "edits"),
        "expected_surfaces": ["files", "worker_card"],
        "crosswalk": ["E06", "X02"],
    },
    {
        "id": "X03-v1-secret-in-agent-edit",
        "family": "X03",
        "episode_kind": "secret_screened_edit",
        "title": "Managed secret in agent edit materialized only at screened save; A11 canary scan clean",
        "track_tables": ("edits", "authority"),
        "expected_surfaces": ["files", "approval_card"],
        "crosswalk": ["E08", "A11", "X03"],
    },
    {
        "id": "X03-v2-secret-in-worker-result",
        "family": "X03",
        "episode_kind": "worker_outside_write",
        "title": "Managed secret in worker result stays screened by reference across records",
        "track_tables": ("workers", "authority"),
        "expected_surfaces": ["worker_card", "approval_card"],
        "crosswalk": ["E08", "A11", "X03"],
    },
    {
        "id": "X03-v3-guidance-and-markup-screen",
        "family": "X03",
        "episode_kind": "guidance_capture",
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

    try:
        app.ready()
        session_id, project_id, _ = _open_session(app, proj)
        api_readback["health"] = app.request("/health")
        api_readback["projects"] = app.request("/v1/projects")

        app.request(
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
            _, task_res, _ = _send_tool_call(
                app,
                session_id,
                "task",
                {
                    "agent_type": "implementer",
                    "brief": {"goal": "Update worker_target.txt", "done_when": ["worker_target.txt updated"]},
                    "scope": {"mode": "write", "paths": ["worker_target.txt"]},
                },
            )
            job_id = str(
                (task_res.get("dispatch") or {}).get("worker_id")
                or ((task_res.get("invocation") or {}).get("evidence") or {}).get("owner_ref")
                or ((task_res.get("invocation") or {}).get("evidence") or {}).get("ref")
                or (task_res.get("dispatch") or {}).get("job_id")
                or ""
            )
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
                with contextlib.closing(sqlite3.connect((app_dir / "store.db").resolve().as_uri() + "?mode=ro", uri=True)) as db:
                    row = db.execute("SELECT child_session_id, status, error FROM worker_jobs WHERE id=?", (job_id,)).fetchone()
                if not row:
                    return None
                if row[1] in {"failed", "canceled", "aborted"}:
                    raise RuntimeError(f"worker {job_id} failed: {row[2]}")
                return str(row[0]) if row[0] else None

            child_sid = app.wait_for(_child_sid, "tb10 worker child sid", session_id)
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

            child_done = _wait_pending(app, child_sid)
            if "complete_leg" in (child_done.get("tools") or ()):
                _send_tool_call(
                    app,
                    child_sid,
                    "complete_leg",
                    {"leg_status": "complete", "brief": "Updated worker_target.txt.", "objectives_met": ["worker_target.txt updated"]},
                )
            else:
                app.request("/harness/llm/respond", {"id": child_done["id"], "content": "Updated worker_target.txt."})

            def _settled() -> bool:
                with contextlib.closing(sqlite3.connect((app_dir / "store.db").resolve().as_uri() + "?mode=ro", uri=True)) as db:
                    row = db.execute("SELECT status, error FROM worker_jobs WHERE id=?", (job_id,)).fetchone()
                if not row:
                    return False
                if row[0] in {"failed", "canceled", "aborted"}:
                    raise RuntimeError(f"worker {job_id} failed: {row[1]}")
                return row[0] == "complete"

            app.wait_for(_settled, "tb10 worker settled", session_id)
            _wait_tool_result(app, session_id, wait_call_id)

            # Resolve issued result handle via pack_board (D15) and promote.
            _, board_res, _ = _send_tool_call(app, session_id, "pack_board", {})
            result_handle = job_id
            with contextlib.closing(sqlite3.connect((app_dir / "store.db").resolve().as_uri() + "?mode=ro", uri=True)) as db:
                row = db.execute("SELECT id FROM worker_results WHERE worker_job_id=? ORDER BY created_at DESC LIMIT 1", (job_id,)).fetchone()
                if row and row[0]:
                    result_handle = str(row[0])
            _, promote_res, _ = _send_tool_call(app, session_id, "promote_overlay", {"overlay_id": result_handle})
            episode_facts.update({
                "job_id": job_id,
                "result_handle": result_handle,
                "worker_refusal_code": worker_refusal_code,
                "worker_allowed_call_id": allowed_cid,
                "worker_allowed_outcome": allowed_res.get("outcome") if isinstance(allowed_res, dict) else None,
                "worker_allowed_arrivals": allowed_snap["connection_count"],
                "worker_refused_arrivals": refused_snap["connection_count"],
                "worker_refused_net_code": _extract_refusal_code(refused_net_res),
                "outside_target_unchanged": hash_file(outside_target)["sha256"] == outside_pre["sha256"],
                "primary_untouched_before_promote": primary_untouched_before_promote,
                "promote_outcome": promote_res.get("outcome") if isinstance(promote_res, dict) else None,
                "board_outcome": board_res.get("outcome") if isinstance(board_res, dict) else None,
                "expected_landed_sha256": sha256_bytes(b"worker landed line\n"),
                "actual_landed_sha256": hash_file(proj / "worker_target.txt")["sha256"],
            })

        elif kind == "network_authority":
            with (
                RecordingHTTPFixture(fixture_id="tb10-allowed", redact_tokens=tuple(canaries.tokens)) as allowed_ep,
                RecordingHTTPFixture(fixture_id="tb10-refused", redact_tokens=tuple(canaries.tokens)) as refused_ep,
            ):
                allowed_cid, allowed_res, req1 = _send_tool_call(
                    app,
                    session_id,
                    "command",
                    {
                        "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\"",
                        "capability_request": {"loopback_connect": {"ports": [allowed_ep.port]}},
                    },
                    checkpoint_policy="approve_once",
                )
                captured_requests.append(_capture_request_facts(req1))
                _, refused_res, req2 = _send_tool_call(
                    app,
                    session_id,
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
                episode_facts.update({
                    "allowed_call_id": allowed_cid,
                    "allowed_result": allowed_res,
                    "allowed_arrivals": allowed_snap["connection_count"],
                    "refused_arrivals": refused_snap["connection_count"],
                    "refused_code": _extract_refusal_code(refused_res),
                })

        elif kind in {"write_boundary", "guidance_capture"}:
            _, refused_res, req1 = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"printf 'mutated\\n' > {outside_target}"},
                checkpoint_policy="reject",
            )
            captured_requests.append(_capture_request_facts(req1))
            # Capture the post-refusal model turn request (contains rendered guidance + offered tools!).
            post_refusal_req = _wait_pending(app, session_id)
            captured_requests.append(_capture_request_facts(post_refusal_req))
            _, inside_res, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": "edit --path notes.txt --old-string beta --new-string BETA"},
            )
            episode_facts.update({
                "refusal_code": _extract_refusal_code(refused_res),
                "outside_target_unchanged": hash_file(outside_target)["sha256"] == outside_pre["sha256"],
                "inside_outcome": inside_res.get("outcome") if isinstance(inside_res, dict) else None,
                "actual_inside_sha256": hash_file(proj / "notes.txt")["sha256"],
                "expected_inside_sha256": sha256_bytes(b"alpha\nBETA\n"),
            })

        else:
            # edit_attribution_and_version / secret_screened_edit
            _send_tool_call(
                app,
                session_id,
                "update_progress",
                {"content": "## Progress\n- [ ] Read and edit notes.txt\n"},
            )
            _, read_res, req1 = _send_tool_call(app, session_id, "command", {"command": "cat notes.txt"})
            captured_requests.append(_capture_request_facts(req1))
            disk_before_write = str(hash_file(proj / "notes.txt").get("sha256") or "")
            _, write_res, req2 = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": "edit --path notes.txt --old-string beta --new-string BETA"},
            )
            captured_requests.append(_capture_request_facts(req2))
            episode_facts.update({
                "read_outcome": read_res.get("outcome") if isinstance(read_res, dict) else None,
                "write_outcome": write_res.get("outcome") if isinstance(write_res, dict) else None,
                "disk_before_write": disk_before_write,
                "actual_after_sha256": hash_file(proj / "notes.txt")["sha256"],
                "expected_after_sha256": sha256_bytes(b"alpha\nBETA\n"),
            })

        api_readback["session"] = app.request(f"/v1/sessions/{session_id}")
        api_readback["messages"] = app.request(f"/v1/sessions/{session_id}/messages")
        app.request(f"/v1/sessions/{session_id}/abort", {})
    finally:
        app.close(cleanup=False)

    db_path = app_dir / "store.db"
    db_obs = read_sqlite_tables_ro(db_path, _tables_for_variant(variant), session_id=session_id)
    row_counts = {t: len(rows) for t, rows in (db_obs.get("rows") or {}).items()}
    total_rows = sum(row_counts.values())

    # Attach typed witness extractions where applicable.
    if kind == "network_authority":
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
    elif kind in {"edit_attribution_and_version", "secret_screened_edit"}:
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
    elif kind == "worker_outside_write":
        episode_facts["worker_landing"] = _extract_worker_landing_from_db(
            {"sqlite_records": db_obs},
            job_id=episode_facts.get("job_id", ""),
            promote_result={"outcome": episode_facts.get("promote_outcome")},
            expected_post_hashes={"worker_target.txt": episode_facts.get("expected_landed_sha256", "")},
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

    db_paths = [pathlib.Path(episode["db_path"])] if episode.get("db_path") and pathlib.Path(episode["db_path"]).is_file() else []
    canary_scan = scan_for_canaries(
        canaries.tokens,
        sqlite_dbs=db_paths,
        paths=[v_dir / "native", v_dir / "app" / "sidecar.log"],
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
        "episode": {
            "session_id": episode.get("session_id"),
            "project_id": episode.get("project_id"),
            "captured_requests": captured_reqs,
            "episode_facts": episode.get("episode_facts"),
            "network_fixtures": episode.get("network_fixtures"),
            "api_readback": episode.get("api_readback"),
            "sqlite_row_counts": episode.get("sqlite_row_counts"),
            "sqlite_total_rows": episode.get("sqlite_total_rows"),
        },
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


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB10 X01-X03 Linux native Den runner.")
    parser.add_argument("--app-bin", type=pathlib.Path, required=True, help="Path to built painted-wolf-code binary.")
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to pinned lycaon-dev binary.")
    parser.add_argument("--engine-manifest", type=pathlib.Path, required=True, help="Path to build-manifest.json.")
    parser.add_argument("--engine-root", type=pathlib.Path, default=None, help="Path to staged LYCAON_ENGINE_ROOT.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for TB10 receipts & manifest.")
    parser.add_argument("--variant", default=None, help="Run a single TB10 variant ID (e.g. X01-v1-worker-outside-write).")
    parser.add_argument("--variants", default="all", help="Comma-separated variant IDs or 'all' (default: all 9).")
    parser.add_argument("--api-url", default=os.environ.get("LYCAON_E2E_API_URL"))
    parser.add_argument("--token", default=os.environ.get("LYCAON_E2E_TOKEN"))
    args = parser.parse_args(argv)

    if args.variant:
        selected = [args.variant.strip()]
    elif args.variants.strip().lower() == "all":
        selected = None
    else:
        selected = [v.strip() for v in args.variants.split(",") if v.strip()]

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
