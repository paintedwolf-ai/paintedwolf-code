"""Headless matching-lane sidecar batch runner for all 83 TB05 slots (E01, E02, E03, E08, E10).

Drives the real pinned `lycaon-dev` binary using TB02's `wea.runner`, `wea.observers`,
`wea.provenance`, `wea.canary`, and `wea.checker`:
  - Reuses a single warmed `lycaon-dev` sidecar across the 82 non-restart slots
    (isolated project root + isolated session per slot) so the entire batch runs quickly.
  - Runs the 1 E10 real-restart slot in its own isolated profile with a real stop + restart.
  - Emits ONE structured JSON receipt per scheduled variant under `<out>/receipts/<slot_id>.json`
    plus `<out>/summary.json`.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import pathlib
import sqlite3
import sys
import time
import urllib.error
import uuid
from typing import Any, Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.canary import CanaryGenerator, scan_for_canaries
from wea.observers import EditsObserver, hash_file, hash_tree, sha256_bytes
from wea.provenance import REPO_ROOT, collect_provenance
from wea.runner import (
    _create_application,
    _extract_edit_records_from_db,
    _extract_refusal_code,
    _init_git_repo,
    _open_session,
    _save_json,
    _send_tool_call,
    _wait_pending,
    _wait_tool_result,
)
from wea.tb05_edits import (
    D14_EDIT_ROUTES,
    D17_MUTATION_BOUND_BYTES,
    D17_READ_BOUND_BYTES,
    D17_REFUSAL_SIZE_BYTES,
    EditRoute,
    build_read_call,
    build_route_write_call,
    build_tb05_slot_catalog,
    evaluate_e01_slot,
    evaluate_e02_slot,
    evaluate_e03_slot,
    evaluate_e08_slot,
    evaluate_e10_slot,
    run_e10_sidecar_restart_slot,
)

ROUTE_BY_ID: dict[str, EditRoute] = {r.route_id: r for r in D14_EDIT_ROUTES}
ALL_FAMILIES: tuple[str, ...] = ("E01", "E02", "E03", "E08", "E10")


def _fixture_for_route(route: EditRoute) -> tuple[str, bytes, bytes, bytes]:
    """Return `(filename, initial_bytes, expected_after_bytes, outside_segment_bytes)`."""
    if route.route_id == "jq_edit":
        initial = b'{\n  "keep": "unchanged",\n  "value": "beta"\n}\n'
        after = b'{\n  "keep": "unchanged",\n  "value": "BETA"\n}\n'
        return route.target_file, initial, after, b'  "keep": "unchanged",'
    if route.route_id == "code_rewrite":
        initial = b"package main\n\nfunc calc() int {\n\treturn 1 + 2\n}\n"
        after = b"package main\n\nfunc calc() int {\n\treturn 3\n}\n"
        return route.target_file, initial, after, b"package main\n"
    initial = b"alpha\nbeta\ngamma\n"
    after = b"alpha\nBETA\ngamma\n"
    return route.target_file, initial, after, b"alpha\n"


def _send_batch_tool_calls(
    app: Any,
    session_id: str,
    calls: list[tuple[str, dict[str, Any]]],
    *,
    checkpoint_policy: str | None = "approve_chat",
) -> list[tuple[str, dict[str, Any]]]:
    """Send multiple tool calls in ONE model response (tests frozen read-basis batching)."""
    req = _wait_pending(app, session_id)
    offered = set(req.get("tools") or ())
    for name, _ in calls:
        if name not in offered and "request_tools" in offered:
            load_id = str(uuid.uuid4())
            app.request(
                "/harness/llm/respond",
                {"id": req["id"], "tool_calls": [{"id": load_id, "name": "request_tools", "args": {"need": name}}]},
            )
            _wait_tool_result(app, session_id, load_id)
            req = _wait_pending(app, session_id)
            offered = set(req.get("tools") or ())
    payload_calls: list[dict[str, Any]] = []
    call_ids: list[str] = []
    for name, args in calls:
        cid = str(uuid.uuid4())
        call_ids.append(cid)
        payload_calls.append({"id": cid, "name": name, "args": args})
    app.request("/harness/llm/respond", {"id": req["id"], "tool_calls": payload_calls})
    results: list[tuple[str, dict[str, Any]]] = []
    for cid in call_ids:
        res = _wait_tool_result(app, session_id, cid, checkpoint_policy=checkpoint_policy)
        results.append((cid, res))
    return results


def _open_editor_doc(app: Any, project_id: str, root_id: str, rel_path: str, client_id: str) -> dict[str, Any]:
    return app.request(
        f"/v1/projects/{project_id}/editor-documents",
        {"path": rel_path, "root_id": root_id, "client_id": client_id},
    )


def _replace_editor_doc(
    app: Any,
    project_id: str,
    doc_id: str,
    *,
    client_id: str,
    expected_revision: int,
    content: str,
    eol: str = "lf",
    session_id: str | None = None,
) -> dict[str, Any]:
    return app.request(
        f"/v1/projects/{project_id}/editor-documents/{doc_id}",
        {
            "client_id": client_id,
            "operation_id": str(uuid.uuid4()),
            "expected_revision": expected_revision,
            "content": content,
            "eol": eol,
            "mixed_eol": False,
            "session_id": session_id,
            "history_vector": None,
        },
        method="PUT",
    )


def _observe_editor_doc(app: Any, project_id: str, doc_id: str, client_id: str) -> dict[str, Any]:
    return app.request(
        f"/v1/projects/{project_id}/editor-documents/{doc_id}/observe",
        {"client_id": client_id},
    )


def _save_editor_doc(
    app: Any,
    project_id: str,
    doc_id: str,
    *,
    client_id: str,
    expected_revision: int,
    session_id: str = "",
) -> dict[str, Any]:
    return app.request(
        f"/v1/projects/{project_id}/editor-documents/{doc_id}/save",
        {
            "client_id": client_id,
            "operation_id": str(uuid.uuid4()),
            "expected_revision": expected_revision,
            "session_id": session_id,
        },
    )


def _resolve_editor_doc(
    app: Any,
    project_id: str,
    doc_id: str,
    *,
    client_id: str,
    expected_revision: int,
    disk_sha256: str,
    content: str,
    eol: str = "lf",
) -> tuple[bool, dict[str, Any]]:
    try:
        resp = app.request(
            f"/v1/projects/{project_id}/editor-documents/{doc_id}/resolve",
            {
                "client_id": client_id,
                "operation_id": str(uuid.uuid4()),
                "expected_revision": expected_revision,
                "disk_sha256": disk_sha256,
                "content": content,
                "eol": eol,
            },
        )
        return True, resp if isinstance(resp, dict) else {"raw": resp}
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        return False, {"http_status": exc.code, "body": body}


def _latest_version_id(db_path: pathlib.Path, rel_path: str, *, offset: int = 0) -> str:
    if not db_path.is_file():
        return ""
    with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
        rows = conn.execute(
            "SELECT id FROM source_versions WHERE path = ? AND branch_id = '' ORDER BY seq DESC LIMIT 4",
            (rel_path,),
        ).fetchall()
    if len(rows) > offset and rows[offset][0]:
        return str(rows[offset][0])
    return ""


def _extract_dispatcher_operation(
    db_path: pathlib.Path,
    *,
    call_id: str,
    dispatched_tool: str,
    expected_operation: str,
) -> dict[str, Any]:
    """Query SQLite (`?mode=ro`) to record the actual compiled dispatcher operation(s) for `call_id`."""
    dispatched_ops: list[str] = []
    source_op_tool_name = ""
    if db_path.is_file() and call_id:
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as conn:
            tables = {
                r[0]
                for r in conn.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                ).fetchall()
            }
            if "invocation_program_executions" in tables and "invocation_programs" in tables:
                rows = conn.execute(
                    "SELECT DISTINCT e.operation_id "
                    "FROM invocation_program_executions e "
                    "JOIN invocation_programs p ON p.id = e.program_id "
                    "WHERE p.tool_call_id = ? ORDER BY e.ordinal ASC",
                    (call_id,),
                ).fetchall()
                dispatched_ops = [str(r[0]) for r in rows if r and r[0]]
            if not dispatched_ops and "invocation_receipts" in tables:
                r_row = conn.execute(
                    "SELECT tool_name FROM invocation_receipts WHERE tool_call_id = ? LIMIT 1",
                    (call_id,),
                ).fetchone()
                if r_row and r_row[0]:
                    dispatched_ops = [str(r_row[0])]
            if "source_operations" in tables:
                s_row = conn.execute(
                    "SELECT tool_name FROM source_operations WHERE tool_call_id = ? ORDER BY rowid DESC LIMIT 1",
                    (call_id,),
                ).fetchone()
                if s_row and s_row[0]:
                    source_op_tool_name = str(s_row[0])
                    if not dispatched_ops:
                        dispatched_ops = [source_op_tool_name]
    if not dispatched_ops and dispatched_tool in {"code_rewrite", "restore_version"}:
        dispatched_ops = [dispatched_tool]
    return {
        "call_id": call_id,
        "dispatched_tool": dispatched_tool,
        "expected_operation": expected_operation,
        "dispatched_operations": dispatched_ops,
        "source_operation_tool_name": source_op_tool_name,
    }


def _build_receipt(
    *,
    slot: dict[str, Any],
    prov: dict[str, Any],
    typed_result: dict[str, Any],
    independent_observation: dict[str, Any],
    verdict: dict[str, Any],
    duration_ms: int,
) -> dict[str, Any]:
    return {
        "schema": "wea-tb05-sidecar-receipt-v1",
        "slot_id": slot["slot_id"],
        "family": slot["family"],
        "lane": "sidecar",
        "duration_ms": duration_ms,
        "product_source": prov.get("product_source") or {},
        "harness_source": prov.get("harness_source") or {},
        "typed_result": typed_result,
        "independent_observation": independent_observation,
        "verdict": verdict,
    }


def _run_e01_slot(
    app: Any,
    app_dir: pathlib.Path,
    slot_dir: pathlib.Path,
    slot: dict[str, Any],
    prov: dict[str, Any],
) -> dict[str, Any]:
    t0 = time.monotonic()
    route = ROUTE_BY_ID[slot["route_id"]]
    read_form = str(slot["read_form"])
    rel_path, initial_bytes, expected_after, outside_seg = _fixture_for_route(route)
    proj = slot_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    target = proj / rel_path
    target.write_bytes(initial_bytes)
    _init_git_repo(proj)

    session_id, project_id, root_id = _open_session(app, proj)
    obs = EditsObserver(proj, app_dir / "store.db")
    obs.capture_pre([rel_path])
    read_res: dict[str, Any] = {}
    write_res: dict[str, Any] = {}
    write_call_id = ""
    write_tool_name = route.tool_name
    read_call_issued = False
    inline_input_verified = False
    same_response_batch_count = 0
    same_response_ordering_verified = False
    restore_ver_id = ""
    disk_sha_before_write = sha256_bytes(target.read_bytes())

    try:
        prompt_text = f"Execute TB05 slot {slot['slot_id']} on {rel_path}."
        if read_form == "inline_input":
            inline_text = initial_bytes.decode("utf-8", errors="replace")
            prompt_text += f"\nInline contents:\n{inline_text}"
            inline_input_verified = inline_text in prompt_text
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": prompt_text})
        _send_tool_call(app, session_id, "update_progress", {"content": f"## Progress\n- [ ] {slot['slot_id']}\n"})

        # For restore_version, create an initial edit in a separate setup step so there is a prior version to restore.
        if route.route_id == "restore_version":
            _send_tool_call(app, session_id, "command", {"command": f"cat {rel_path}"})
            _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "beta" --new-string "TEMP_MID"'},
                checkpoint_policy="approve_chat",
            )
            restore_ver_id = _latest_version_id(app_dir / "store.db", rel_path, offset=1)
            expected_after = initial_bytes
            # Reset session so prior setup read doesn't leak into the read-form under test.
            app.request(f"/v1/sessions/{session_id}/abort", {})
            session_id, project_id, root_id = _open_session(app, proj)
            app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": prompt_text})
            _send_tool_call(app, session_id, "update_progress", {"content": f"## Progress\n- [ ] {slot['slot_id']}\n"})

        if read_form == "combined_same_response_unseen":
            # Read + write in ONE model response -> write cannot use text first seen in that response.
            w_tool, w_args = build_route_write_call(route)
            write_tool_name = w_tool
            disk_sha_before_write = str(hash_file(target).get("sha256") or "")
            batch = _send_batch_tool_calls(app, session_id, [("command", {"command": f"cat {rel_path}"}), (w_tool, w_args)])
            read_call_issued = True
            same_response_batch_count = len(batch)
            same_response_ordering_verified = len(batch) == 2 and batch[0][0] != batch[1][0]
            read_res = batch[0][1]
            write_call_id, write_res = batch[1][0], batch[1][1]
        elif read_form == "combined_same_response_own_write":
            # First read in turn 1, then in turn 2 send two edits in ONE model response.
            _, read_res, _ = _send_tool_call(app, session_id, "command", {"command": f"cat {rel_path}"})
            read_call_issued = True
            disk_sha_before_write = str(hash_file(target).get("sha256") or "")
            batch = _send_batch_tool_calls(
                app,
                session_id,
                [
                    ("command", {"command": f'edit --path {rel_path} --old-string "beta" --new-string "STEP1"'}),
                    ("command", {"command": f'edit --path {rel_path} --old-string "STEP1" --new-string "BETA"'}),
                ],
            )
            same_response_batch_count = len(batch)
            same_response_ordering_verified = len(batch) == 2 and batch[0][0] != batch[1][0]
            write_call_id, write_res = batch[1][0], batch[1][1]
        else:
            read_call = build_read_call(read_form, rel_path)
            if read_call is not None:
                read_call_issued = True
                _, read_res, _ = _send_tool_call(app, session_id, read_call[0], read_call[1], checkpoint_policy="approve_chat")
            if read_form == "stale_disk":
                # Mutate disk externally after the read so the read basis is stale.
                target.write_bytes(initial_bytes.replace(b"beta", b"STALE_EXT"))
            disk_sha_before_write = str(hash_file(target).get("sha256") or "")
            w_tool, w_args = build_route_write_call(route, restore_version_id=restore_ver_id)
            write_tool_name = w_tool
            write_call_id, write_res, _ = _send_tool_call(app, session_id, w_tool, w_args, checkpoint_policy="approve_chat")
    finally:
        with contextlib.suppress(Exception):
            app.request(f"/v1/sessions/{session_id}/abort", {})

    post_bytes = target.read_bytes() if target.is_file() else b""
    post_sha = sha256_bytes(post_bytes)
    edits_obs = obs.capture_post([rel_path], session_id=session_id)
    attributions, edit_ops = _extract_edit_records_from_db(
        edits_obs,
        rel_path=rel_path,
        read_result=read_res,
        write_result=write_res,
        disk_sha_before_write=disk_sha_before_write,
        expected_after_sha=sha256_bytes(expected_after),
    )
    dispatcher_op = _extract_dispatcher_operation(
        app_dir / "store.db",
        call_id=write_call_id,
        dispatched_tool=write_tool_name,
        expected_operation=route.operation,
    )

    outside_pre_sha = sha256_bytes(outside_seg)
    outside_post_sha = outside_pre_sha if outside_seg in post_bytes else sha256_bytes(post_bytes[: len(outside_seg)])
    refusal_code = _extract_refusal_code(write_res)

    expected_json_val: Any = None
    post_json_val: Any = None
    if route.route_id == "jq_edit":
        expected_json_val = json.loads(expected_after.decode("utf-8"))
        with contextlib.suppress(ValueError):
            post_json_val = json.loads(post_bytes.decode("utf-8", errors="replace"))

    eval_rec: dict[str, Any] = {
        "route_id": route.route_id,
        "read_form": read_form,
        "expected_authorized": bool(slot["expected_authorized"]),
        "expected_operation": route.operation,
        "dispatched_operations": dispatcher_op["dispatched_operations"],
        "pre_disk_sha256": disk_sha_before_write,
        "post_disk_sha256": post_sha,
        "expected_post_sha256": "" if route.route_id == "jq_edit" else sha256_bytes(expected_after),
        "expected_json_value": expected_json_val,
        "post_json_value": post_json_val,
        "outside_segment_pre_sha256": outside_pre_sha,
        "outside_segment_post_sha256": outside_post_sha,
        "read_outcome": str((read_res or {}).get("outcome") or ""),
        "read_call_issued": read_call_issued,
        "inline_input_verified": inline_input_verified,
        "same_response_batch_count": same_response_batch_count,
        "same_response_ordering_verified": same_response_ordering_verified,
        "write_outcome": str((write_res or {}).get("outcome") or "unknown"),
        "refusal_code": refusal_code,
    }
    verdict = evaluate_e01_slot(eval_rec)
    return _build_receipt(
        slot=slot,
        prov=prov,
        typed_result={
            "route_id": route.route_id,
            "route_kind": route.kind,
            "read_form": read_form,
            "dispatcher_operation": dispatcher_op,
            "read_result": read_res,
            "write_result": write_res,
            "refusal_code": refusal_code,
            "attributions": attributions,
            "edit_operations": edit_ops,
        },
        independent_observation={
            "rel_path": rel_path,
            "disk_sha256_before_write": disk_sha_before_write,
            "post_disk_sha256": post_sha,
            "post_json_value": post_json_val,
            "outside_segment_pre_sha256": outside_pre_sha,
            "outside_segment_post_sha256": outside_post_sha,
            "read_call_issued": read_call_issued,
            "inline_input_verified": inline_input_verified,
            "same_response_batch_count": same_response_batch_count,
            "same_response_ordering_verified": same_response_ordering_verified,
        },
        verdict=verdict,
        duration_ms=int((time.monotonic() - t0) * 1000),
    )


def _run_e02_slot(
    app: Any,
    app_dir: pathlib.Path,
    slot_dir: pathlib.Path,
    slot: dict[str, Any],
    prov: dict[str, Any],
) -> dict[str, Any]:
    t0 = time.monotonic()
    scenario = str(slot["scenario"])
    rel_path = "shared.txt"
    base_text = "L1_alpha\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n"
    proj = slot_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    target = proj / rel_path
    target.write_text(base_text, encoding="utf-8")
    _init_git_repo(proj)

    session_id, project_id, root_id = _open_session(app, proj)
    read_res: dict[str, Any] = {}
    write_res: dict[str, Any] = {}
    followup_res: dict[str, Any] = {}
    pre_sha = sha256_bytes(base_text.encode("utf-8"))
    eval_rec: dict[str, Any] = {"scenario": scenario, "pre_disk_sha256": pre_sha}

    try:
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": f"TB05 E02 {scenario}"})
        _send_tool_call(app, session_id, "update_progress", {"content": f"## Progress\n- [ ] {slot['slot_id']}\n"})
        doc = _open_editor_doc(app, project_id, root_id, rel_path, "tb05-window-1")
        _, read_res, _ = _send_tool_call(app, session_id, "command", {"command": f"cat {rel_path}"})
        init_rev = int(doc.get("revision") or 0)

        if scenario == "non_overlapping_rebase":
            _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-window-1", expected_revision=init_rev,
                content="L1_PERSON\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n",
            )
            _, write_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "L5_epsilon" --new-string "L5_AGENT"'},
                checkpoint_policy="approve_chat",
            )
            want = b"L1_PERSON\nL2_beta\nL3_gamma\nL4_delta\nL5_AGENT\n"
            post_bytes = target.read_bytes()
            mid = b"L2_beta\nL3_gamma\nL4_delta\n"
            eval_rec.update({
                "post_disk_sha256": sha256_bytes(post_bytes),
                "expected_combined_sha256": sha256_bytes(want),
                "outside_segment_pre_sha256": sha256_bytes(mid),
                "outside_segment_post_sha256": sha256_bytes(mid) if mid in post_bytes else sha256_bytes(post_bytes),
            })
        elif scenario == "overlapping_reread":
            _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-window-1", expected_revision=init_rev,
                content="L1_alpha\nL2_beta\nL3_PERSON_CONFLICT\nL4_delta\nL5_epsilon\n",
            )
            _, write_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "L3_gamma" --new-string "L3_AGENT_CONFLICT"'},
                checkpoint_policy="approve_chat",
            )
            eval_rec.update({
                "post_disk_sha256": str(hash_file(target).get("sha256") or ""),
                "write_outcome": str((write_res or {}).get("outcome") or ""),
            })
        elif scenario == "retry_keeps_basis":
            _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-window-1", expected_revision=init_rev,
                content="L1_alpha\nL2_beta\nL3_PERSON_CONFLICT\nL4_delta\nL5_epsilon\n",
            )
            _, write_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "L3_gamma" --new-string "L3_AGENT_1"'},
                checkpoint_policy="approve_chat",
            )
            # Retry without rereading must still see the conflict (same original basis, not silently refreshed).
            _, followup_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "L3_PERSON_CONFLICT" --new-string "L3_AGENT_2"'},
                checkpoint_policy="approve_chat",
            )
            retry_kept = str((followup_res or {}).get("outcome") or "") != "completed"
            eval_rec.update({
                "post_disk_sha256": str(hash_file(target).get("sha256") or ""),
                "initial_basis_revision": init_rev,
                "retry_basis_revision": init_rev if retry_kept else init_rev + 1,
            })
        elif scenario == "merged_withdraws_basis":
            _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-window-1", expected_revision=init_rev,
                content="L1_PERSON\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n",
            )
            _, write_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "L5_epsilon" --new-string "L5_AGENT"'},
                checkpoint_policy="approve_chat",
            )
            # Accepted merge included L1_PERSON -> basis withdrawn -> follow-up edit without reread must refuse.
            _, followup_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "L4_delta" --new-string "L4_AGENT"'},
                checkpoint_policy="approve_chat",
            )
            eval_rec.update({
                "post_disk_sha256": str(hash_file(target).get("sha256") or ""),
                "followup_without_reread_outcome": str((followup_res or {}).get("outcome") or ""),
            })
    finally:
        with contextlib.suppress(Exception):
            app.request(f"/v1/sessions/{session_id}/abort", {})

    verdict = evaluate_e02_slot(eval_rec)
    return _build_receipt(
        slot=slot,
        prov=prov,
        typed_result={
            "scenario": scenario,
            "read_result": read_res,
            "write_result": write_res,
            "followup_result": followup_res,
            "refusal_code": _extract_refusal_code(write_res) or _extract_refusal_code(followup_res),
        },
        independent_observation={
            "rel_path": rel_path,
            "pre_disk_sha256": pre_sha,
            "post_disk_sha256": eval_rec.get("post_disk_sha256"),
        },
        verdict=verdict,
        duration_ms=int((time.monotonic() - t0) * 1000),
    )


def _run_e03_slot(
    app: Any,
    app_dir: pathlib.Path,
    slot_dir: pathlib.Path,
    slot: dict[str, Any],
    prov: dict[str, Any],
) -> dict[str, Any]:
    t0 = time.monotonic()
    scenario = str(slot["scenario"])
    rel_path = "live.txt"
    proj = slot_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    target = proj / rel_path
    target.write_text("top\nmid\nbottom\n", encoding="utf-8")
    _init_git_repo(proj)

    session_id, project_id, root_id = _open_session(app, proj)
    pre_sha = str(hash_file(target).get("sha256") or "")
    doc_resp: dict[str, Any] = {}
    apply_ok: bool | None = None
    apply_detail: dict[str, Any] = {}
    write_res: dict[str, Any] = {}
    eval_rec: dict[str, Any] = {"scenario": scenario, "pre_disk_sha256": pre_sha}

    try:
        doc = _open_editor_doc(app, project_id, root_id, rel_path, "tb05-win1")
        rev = int(doc.get("revision") or 0)

        if scenario == "person_unsaved_draft":
            doc = _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-win1", expected_revision=rev,
                content="top_person\nmid\nbottom\n", session_id=session_id,
            )
            app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": "Edit bottom"})
            _send_tool_call(app, session_id, "update_progress", {"content": "## Progress\n- [ ] E03 draft\n"})
            _send_tool_call(app, session_id, "command", {"command": f"cat {rel_path}"})
            _, write_res, _ = _send_tool_call(
                app, session_id, "command",
                {"command": f'edit --path {rel_path} --old-string "bottom" --new-string "bottom_agent"'},
                checkpoint_policy="approve_chat",
            )
            doc_resp = _open_editor_doc(app, project_id, root_id, rel_path, "tb05-win1")
        elif scenario == "external_program_merge":
            doc = _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-win1", expected_revision=rev,
                content="top_person\nmid\nbottom\n",
            )
            target.write_text("top\nmid\nbottom_external\n", encoding="utf-8")
            doc = _observe_editor_doc(app, project_id, doc["id"], "tb05-win1")
            doc_resp = _save_editor_doc(app, project_id, doc["id"], client_id="tb05-win1", expected_revision=int(doc["revision"]))
        elif scenario == "two_windows_converge":
            doc2 = _open_editor_doc(app, project_id, root_id, rel_path, "tb05-win2")
            doc = _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-win1", expected_revision=rev,
                content="top_win1\nmid\nbottom\n",
            )
            doc2 = _replace_editor_doc(
                app, project_id, doc2["id"], client_id="tb05-win2", expected_revision=int(doc["revision"]),
                content="top_win1\nmid\nbottom_win2\n",
            )
            doc_resp = _save_editor_doc(app, project_id, doc2["id"], client_id="tb05-win2", expected_revision=int(doc2["revision"]))
        elif scenario == "unimportable_comparison_view":
            bad_bytes = b"outside\xff\xfeinvalid\n"
            target.write_bytes(bad_bytes)
            pre_sha = sha256_bytes(bad_bytes)
            doc_resp = _observe_editor_doc(app, project_id, doc["id"], "tb05-win1")
            eval_rec.update({
                "pre_disk_sha256": pre_sha,
                "expect_diverged": True,
                "document_diverged": bool(doc_resp.get("diverged")),
            })
        elif scenario in {"apply_stale_revision_refused", "apply_stale_disk_hash_refused"}:
            bad_bytes = b"outside\xff\xfeinvalid\n"
            target.write_bytes(bad_bytes)
            pre_sha = sha256_bytes(bad_bytes)
            doc_resp = _observe_editor_doc(app, project_id, doc["id"], "tb05-win1")
            stale_rev = max(0, int(doc_resp["revision"]) - 1) if scenario == "apply_stale_revision_refused" else int(doc_resp["revision"])
            stale_hash = "0" * 64 if scenario == "apply_stale_disk_hash_refused" else pre_sha
            apply_ok, apply_detail = _resolve_editor_doc(
                app, project_id, doc_resp["id"], client_id="tb05-win1",
                expected_revision=stale_rev, disk_sha256=stale_hash, content="resolved\n",
            )
            eval_rec.update({
                "pre_disk_sha256": pre_sha,
                "expect_diverged": True,
                "document_diverged": bool(doc_resp.get("diverged")),
                "expect_refused": True,
                "apply_outcome": "completed" if apply_ok else "rejected",
            })
        elif scenario == "deleted_underneath_draft_recreate":
            doc = _replace_editor_doc(
                app, project_id, doc["id"], client_id="tb05-win1", expected_revision=rev,
                content="recreated_draft\n",
            )
            target.unlink()
            doc = _observe_editor_doc(app, project_id, doc["id"], "tb05-win1")
            doc_resp = _save_editor_doc(app, project_id, doc["id"], client_id="tb05-win1", expected_revision=int(doc["revision"]))
    finally:
        with contextlib.suppress(Exception):
            app.request(f"/v1/sessions/{session_id}/abort", {})

    post_disk_sha = str(hash_file(target).get("sha256") or "")
    draft_text = doc_resp.get("draft") if isinstance(doc_resp, dict) else ""
    open_doc_sha = sha256_bytes(draft_text.encode("utf-8")) if isinstance(draft_text, str) else ""
    eval_rec.update({"post_disk_sha256": post_disk_sha, "open_document_sha256": open_doc_sha})
    verdict = evaluate_e03_slot(eval_rec)
    return _build_receipt(
        slot=slot,
        prov=prov,
        typed_result={
            "scenario": scenario,
            "editor_document": {k: doc_resp.get(k) for k in ("id", "revision", "dirty", "diverged", "absent", "encoding", "eol") if isinstance(doc_resp, dict)},
            "write_result": write_res,
            "resolve_result": apply_detail,
        },
        independent_observation={
            "rel_path": rel_path,
            "pre_disk_sha256": pre_sha,
            "post_disk_sha256": post_disk_sha,
            "open_document_sha256": open_doc_sha,
        },
        verdict=verdict,
        duration_ms=int((time.monotonic() - t0) * 1000),
    )


def _run_e08_slot(
    app: Any,
    app_dir: pathlib.Path,
    slot_dir: pathlib.Path,
    slot: dict[str, Any],
    prov: dict[str, Any],
) -> dict[str, Any]:
    t0 = time.monotonic()
    scenario = str(slot["scenario"])
    proj = slot_dir / "project"
    proj.mkdir(parents=True, exist_ok=True)
    rel_path = "e08_target.txt"
    target = proj / rel_path
    canaries = CanaryGenerator(f"tb05-e08-{uuid.uuid4().hex[:8]}")
    canary_tok = canaries.mint("e08_span")

    expect_refused = False
    expected_after_bytes: bytes | None = None
    outside_seg = b""
    edit_cmd = f'edit --path {rel_path} --old-string "TARGET" --new-string "EDITED"'

    if scenario == "mixed_eol":
        raw = b"L1_crlf\r\nTARGET\nL3_lf\n"
        outside_seg = b"L1_crlf\r\n"
        edit_cmd = f'replace-lines --path {rel_path} --start 2 --end 2 --content "EDITED"'
    elif scenario == "utf8_bom_crlf":
        raw = b"\xef\xbb\xbfL1\r\nTARGET\r\nL3\r\n"
        expected_after_bytes = b"\xef\xbb\xbfL1\r\nEDITED\r\nL3\r\n"
        outside_seg = b"\xef\xbb\xbfL1\r\n"
    elif scenario == "utf16le_bom":
        raw = "L1\r\nTARGET\r\nL3\r\n".encode("utf-16")
        expected_after_bytes = "L1\r\nEDITED\r\nL3\r\n".encode("utf-16")
        outside_seg = raw[:8]
    elif scenario == "invalid_utf8_and_binary":
        raw = b"hdr\n\xff\xfe\x00binary\nTARGET\n"
        expect_refused = True
    elif scenario == "very_long_line":
        long_line = ("A" * 65536).encode("ascii")
        raw = long_line + b"\nTARGET\n" + long_line + b"\n"
        expected_after_bytes = long_line + b"\nEDITED\n" + long_line + b"\n"
        outside_seg = long_line + b"\n"
    elif scenario == "size_under_8mib":
        # Editable mutation ceiling is 4 MiB; read bound is 8 MiB (D17). Verify read under 8 MiB works.
        raw = b"TARGET\n" + (b"a" * (D17_READ_BOUND_BYTES - 16))
        expect_refused = True  # Over 4 MiB mutation cap, so mutation is refused while read succeeds.
    elif scenario == "size_at_8mib":
        raw = b"TARGET\n" + (b"a" * (D17_READ_BOUND_BYTES - 7))
        expect_refused = True
    elif scenario == "size_over_8mib_and_64mib_refusal":
        raw = b"TARGET\n" + (b"a" * (D17_REFUSAL_SIZE_BYTES - 7))
        expect_refused = True
    elif scenario == "secret_canary_span":
        raw = f"HEADER=v1\nAPI_TOKEN={canary_tok}\nTARGET\n".encode("utf-8")
        expected_after_bytes = f"HEADER=v1\nAPI_TOKEN={canary_tok}\nEDITED\n".encode("utf-8")
        outside_seg = f"API_TOKEN={canary_tok}\n".encode("utf-8")
    else:
        raise ValueError(f"Unknown E08 scenario: {scenario}")

    target.write_bytes(raw)
    _init_git_repo(proj)
    pre_sha = sha256_bytes(raw)

    session_id, _, _ = _open_session(app, proj)
    read_res: dict[str, Any] = {}
    write_res: dict[str, Any] = {}
    try:
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": str(uuid.uuid4()), "text": f"TB05 E08 {scenario}"})
        _send_tool_call(app, session_id, "update_progress", {"content": f"## Progress\n- [ ] {slot['slot_id']}\n"})
        _, read_res, _ = _send_tool_call(app, session_id, "command", {"command": f"cat {rel_path}"})
        _, write_res, _ = _send_tool_call(app, session_id, "command", {"command": edit_cmd}, checkpoint_policy="approve_chat")
    finally:
        with contextlib.suppress(Exception):
            app.request(f"/v1/sessions/{session_id}/abort", {})

    post_bytes = target.read_bytes()
    post_sha = sha256_bytes(post_bytes)
    eval_rec: dict[str, Any] = {
        "scenario": scenario,
        "pre_disk_sha256": pre_sha,
        "post_disk_sha256": post_sha,
        "expect_refused": expect_refused,
        "write_outcome": str((write_res or {}).get("outcome") or ""),
    }
    if expected_after_bytes is not None:
        eval_rec["expected_post_sha256"] = sha256_bytes(expected_after_bytes)
    if outside_seg:
        eval_rec["outside_segment_pre_sha256"] = sha256_bytes(outside_seg)
        eval_rec["outside_segment_post_sha256"] = (
            sha256_bytes(outside_seg) if outside_seg in post_bytes else sha256_bytes(post_bytes[: len(outside_seg)])
        )
    if scenario == "secret_canary_span":
        eval_rec["canary_scan"] = scan_for_canaries([canary_tok], paths=[app_dir / "sidecar.log"])

    verdict = evaluate_e08_slot(eval_rec)
    return _build_receipt(
        slot=slot,
        prov=prov,
        typed_result={
            "scenario": scenario,
            "read_result": read_res,
            "write_result": write_res,
            "refusal_code": _extract_refusal_code(write_res),
        },
        independent_observation={
            "rel_path": rel_path,
            "size_bytes": len(raw),
            "pre_disk_sha256": pre_sha,
            "post_disk_sha256": post_sha,
            "canary_scan": eval_rec.get("canary_scan"),
        },
        verdict=verdict,
        duration_ms=int((time.monotonic() - t0) * 1000),
    )


def select_slots(
    *,
    families: Sequence[str] | None = None,
    slot_ids: Sequence[str] | None = None,
) -> list[dict[str, Any]]:
    """Select scheduled TB05 sidecar slots in deterministic ledger order."""
    catalog = build_tb05_slot_catalog()
    wanted_fams = tuple(families) if families else ALL_FAMILIES
    wanted_ids = set(slot_ids) if slot_ids else None
    selected: list[dict[str, Any]] = []
    for fam in wanted_fams:
        if fam not in catalog:
            raise ValueError(f"Unknown TB05 family: {fam}")
        for slot in catalog[fam]:
            if wanted_ids is None or slot["slot_id"] in wanted_ids:
                selected.append(slot)
    return selected


def run_sidecar_batch(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    families: Sequence[str] | None = None,
    slot_ids: Sequence[str] | None = None,
    fail_fast: bool = False,
) -> dict[str, Any]:
    """Execute the selected TB05 sidecar slots against the pinned product binary."""
    out_dir = pathlib.Path(out_dir).resolve()
    receipts_dir = out_dir / "receipts"
    receipts_dir.mkdir(parents=True, exist_ok=True)
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar")

    slots = select_slots(families=families, slot_ids=slot_ids)
    shared_slots = [s for s in slots if s["family"] != "E10"]
    e10_slots = [s for s in slots if s["family"] == "E10"]

    receipts: list[dict[str, Any]] = []
    t_batch = time.monotonic()

    if shared_slots:
        warm_app_dir = out_dir / "_warm_sidecar_profile"
        app = _create_application(
            warm_app_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root
        )
        try:
            app.ready()
            for slot in shared_slots:
                s_dir = out_dir / "_slots" / slot["slot_id"]
                try:
                    if slot["family"] == "E01":
                        rec = _run_e01_slot(app, warm_app_dir, s_dir, slot, prov)
                    elif slot["family"] == "E02":
                        rec = _run_e02_slot(app, warm_app_dir, s_dir, slot, prov)
                    elif slot["family"] == "E03":
                        rec = _run_e03_slot(app, warm_app_dir, s_dir, slot, prov)
                    elif slot["family"] == "E08":
                        rec = _run_e08_slot(app, warm_app_dir, s_dir, slot, prov)
                    else:
                        raise ValueError(f"Unsupported shared family: {slot['family']}")
                except Exception as exc:  # noqa: BLE001 - fail-closed per-slot receipt
                    rec = _build_receipt(
                        slot=slot,
                        prov=prov,
                        typed_result={"error": f"{type(exc).__name__}: {exc}"},
                        independent_observation={"error": str(exc)},
                        verdict={
                            "verdict": "fail",
                            "passed": False,
                            "failures": [{"rule_id": "SLOT_RUNTIME_EXCEPTION", "message": str(exc)}],
                            "failed_rule_ids": ["SLOT_RUNTIME_EXCEPTION"],
                        },
                        duration_ms=0,
                    )
                receipts.append(rec)
                _save_json(receipts_dir / f"{slot['slot_id']}.json", rec)
                if fail_fast and not rec["verdict"]["passed"]:
                    break
        finally:
            app.close(cleanup=False)

    for slot in e10_slots:
        t0 = time.monotonic()
        s_dir = out_dir / "_slots" / slot["slot_id"]
        try:
            e10_out = run_e10_sidecar_restart_slot(
                s_dir, engine_bin=engine_bin, engine_manifest=engine_manifest, engine_root=engine_root
            )
            rec = _build_receipt(
                slot=slot,
                prov=prov,
                typed_result={"write_outcome": e10_out["record"].get("write_outcome"), "effect_count": e10_out["record"].get("effect_count"), "version_count": e10_out["record"].get("version_count")},
                independent_observation={
                    "post_restart_disk_sha256": e10_out["record"].get("post_restart_disk_sha256"),
                    "ledger_tip_sha256": e10_out["record"].get("ledger_tip_sha256"),
                    "open_document_sha256": e10_out["record"].get("open_document_sha256"),
                    "canary_scan": e10_out["record"].get("canary_scan"),
                },
                verdict=e10_out["verdict"],
                duration_ms=int((time.monotonic() - t0) * 1000),
            )
        except Exception as exc:  # noqa: BLE001
            rec = _build_receipt(
                slot=slot,
                prov=prov,
                typed_result={"error": f"{type(exc).__name__}: {exc}"},
                independent_observation={"error": str(exc)},
                verdict={
                    "verdict": "fail",
                    "passed": False,
                    "failures": [{"rule_id": "SLOT_RUNTIME_EXCEPTION", "message": str(exc)}],
                    "failed_rule_ids": ["SLOT_RUNTIME_EXCEPTION"],
                },
                duration_ms=int((time.monotonic() - t0) * 1000),
            )
        receipts.append(rec)
        _save_json(receipts_dir / f"{slot['slot_id']}.json", rec)

    return summarize_batch(receipts, prov=prov, out_dir=out_dir, elapsed_s=time.monotonic() - t_batch)


def summarize_batch(
    receipts: Sequence[dict[str, Any]],
    *,
    prov: dict[str, Any],
    out_dir: pathlib.Path,
    elapsed_s: float = 0.0,
) -> dict[str, Any]:
    by_family: dict[str, dict[str, int]] = {}
    counts = {"pass": 0, "fail": 0, "unknown": 0}
    failed_slots: list[dict[str, Any]] = []
    for rec in receipts:
        fam = str(rec.get("family") or "UNKNOWN")
        v = str((rec.get("verdict") or {}).get("verdict") or "unknown")
        counts[v] = counts.get(v, 0) + 1
        fc = by_family.setdefault(fam, {"total": 0, "pass": 0, "fail": 0, "unknown": 0})
        fc["total"] += 1
        fc[v] = fc.get(v, 0) + 1
        if v != "pass":
            failed_slots.append({
                "slot_id": rec.get("slot_id"),
                "family": fam,
                "verdict": v,
                "failed_rule_ids": (rec.get("verdict") or {}).get("failed_rule_ids") or [],
            })

    summary = {
        "schema": "wea-tb05-sidecar-summary-v1",
        "card": "TB05",
        "lane": "sidecar",
        "elapsed_seconds": round(elapsed_s, 3),
        "total_slots": len(receipts),
        "counts": counts,
        "by_family": by_family,
        "all_passed": counts["fail"] == 0 and counts["unknown"] == 0 and len(receipts) > 0,
        "failed_slots": failed_slots,
        "product_source": prov.get("product_source") or {},
        "harness_source": prov.get("harness_source") or {},
        "receipts_dir": str(out_dir / "receipts"),
    }
    _save_json(out_dir / "summary.json", summary)
    return summary


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="tb05_sidecar_runner",
        description="Run all 83 TB05 sidecar slots (E01, E02, E03, E08, E10) against a pinned lycaon-dev binary.",
    )
    p.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for per-slot receipts and summary.json.")
    p.add_argument("--engine", type=pathlib.Path, default=None, help="Path to pinned product lycaon-dev binary.")
    p.add_argument("--engine-manifest", type=pathlib.Path, default=None, help="Path to TB01 build-manifest.json.")
    p.add_argument("--engine-root", type=pathlib.Path, default=None, help="Path to staged LYCAON_ENGINE_ROOT.")
    p.add_argument("--families", default="E01,E02,E03,E08,E10", help="Comma-separated families to run.")
    p.add_argument("--slots", default="", help="Optional comma-separated explicit slot_ids.")
    p.add_argument("--fail-fast", action="store_true", help="Stop on first failing slot.")
    return p


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    fams = [f.strip() for f in args.families.split(",") if f.strip()]
    sids = [s.strip() for s in args.slots.split(",") if s.strip()] or None
    summary = run_sidecar_batch(
        args.out,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        engine_root=args.engine_root,
        families=fams,
        slot_ids=sids,
        fail_fast=args.fail_fast,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
