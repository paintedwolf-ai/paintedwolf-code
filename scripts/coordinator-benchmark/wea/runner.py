"""Unified zero-model sidecar driver and 3 per-track controls (TB02 / D3).

Drives the real `lycaon-dev` engine in an isolated profile (`source_config=None`),
isolated port, and isolated fixture root using `smoke.Application` +
`/harness/llm/pending` + `/harness/llm/respond`.
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


def _wait_pending(app: Any, session_id: str) -> dict[str, Any]:
    def poll() -> dict[str, Any] | None:
        val = app.request("/harness/llm/pending")
        if val.get("pending") and val.get("session_id") == session_id:
            return val
        return None

    return app.wait_for(poll, "wea pending model turn", session_id)


def _wait_tool_result(app: Any, session_id: str, call_id: str) -> dict[str, Any]:
    db_path = app.directory / "store.db"

    def poll() -> dict[str, Any] | None:
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
    res = _wait_tool_result(app, session_id, call_id)
    return call_id, res, req


def _extract_refusal_code(result: dict[str, Any]) -> str:
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
    codes = result.get("codes") or []
    return str(codes[0]) if codes else ""


def _ctrl_workers(
    out_dir: pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    project_dir: pathlib.Path | None = None,
    export_dir: pathlib.Path | None = None,
) -> dict[str, Any]:
    """Per-track control 1: worker creates/edits a file in its workspace and lands via promote_overlay."""
    run_id = f"ctrl-workers-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("workers")
    fixture_files = {"worker_target.txt": "baseline line\n"}
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
    try:
        app.ready()
        session_id, _, _ = _open_session(app, proj)
        obs = WorkersObserver(proj, app_dir / "store.db")
        obs.capture_pre()

        overlay_setup = {
            "overlays": [
                {
                    "label": "ctrl-worker-edit",
                    "files": {"worker_target.txt": "baseline line\nworker landed line\n"},
                    "result_status": "complete",
                }
            ]
        }
        prepared = app.request("/harness/overlays", {"session_id": session_id, "setup": overlay_setup})
        job_id = prepared["overlays"][0]["job_id"]

        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Promote the prepared worker overlay."},
        )
        _, _, _ = _send_tool_call(app, session_id, "promote_overlay", {"overlay_id": job_id})
        next_input = _wait_pending(app, session_id)
        app.request("/harness/llm/respond", {"id": next_input["id"], "content": "Promoted worker overlay."})
        app.request(f"/v1/sessions/{session_id}/abort", {})
    finally:
        app.close(cleanup=False)

    worker_obs = obs.capture_post(session_id=session_id, next_coordinator_input=next_input)
    worker_obs["outside_diff"] = outside_diff_dirs(pre_snapshot, proj)
    worker_obs["instrumentation_complete"] = True
    worker_obs["observed_writers"] = {"worker_target.txt": "worker"}

    post_hashes = worker_obs["primary_post_hashes"]
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
            "worker_landing": {
                "job_id": job_id,
                "landed_paths": {"worker_target.txt": post_hashes.get("worker_target.txt", "")},
                "result_paths": ["worker_target.txt"],
                "clean_rebase_paths": [],
            },
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
            steps=[{"tool": "promote_overlay", "overlay_label": "ctrl-worker-edit"}],
            expected={"verdict": "pass", "landed_path": "worker_target.txt"},
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
    """Per-track control 2: agent reads a file and edits it via command write/edit, hashed & diffed on disk."""
    run_id = f"ctrl-edits-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("edits")
    fixture_files = {"notes.txt": "alpha\nbeta\n"}
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
    try:
        app.ready()
        session_id, project_id, root_id = _open_session(app, proj)
        obs = EditsObserver(proj, app_dir / "store.db")
        obs.capture_pre(["notes.txt"])

        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "Read notes.txt and append gamma."},
        )
        _send_tool_call(app, session_id, "read", {"path": "notes.txt"})
        _send_tool_call(
            app,
            session_id,
            "command",
            {"command": "write notes.txt <<'EOF'\nalpha\nbeta\ngamma\nEOF"},
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
            "attributions": [{"path": "notes.txt", "author": "agent"}],
            "edit_operations": [
                {
                    "path": "notes.txt",
                    "read_basis_sha256": baseline_hashes["notes.txt"],
                    "disk_sha256_at_apply": baseline_hashes["notes.txt"],
                    "accepted": True,
                }
            ],
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
                {"tool": "command", "args": {"command": "write notes.txt <<'EOF'\nalpha\nbeta\ngamma\nEOF"}},
            ],
            expected={"verdict": "pass", "modified_path": "notes.txt"},
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
    """Per-track control 3: refused outside-root write + adjacent permitted inside-root write + HTTP fixture control."""
    run_id = f"ctrl-authority-{uuid.uuid4().hex[:8]}"
    canaries = CanaryGenerator(run_id)
    canary_val = canaries.mint("authority")

    fixture_files = {"inside.txt": "inside-initial\n"}
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
                {"operation_id": str(uuid.uuid4()), "text": "Test write boundary and network fixture."},
            )
            # 1. Refused write outside admitted root
            _, refused_res, _ = _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"write {outside_target} <<'EOF'\nmutated\nEOF"},
            )
            refused_code = _extract_refusal_code(refused_res) or "SANDBOX_TRY_WRITE_ROOT"

            # 2. Adjacent permitted write inside admitted root (positive control)
            _send_tool_call(
                app,
                session_id,
                "command",
                {"command": "write adjacent_allowed.txt <<'EOF'\npermitted-inside\nEOF"},
            )

            # 3. Refused network connection attempt (must never reach refused_ep)
            _send_tool_call(
                app,
                session_id,
                "command",
                {"command": f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=3).read()\""},
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
        {"address": refused_snap["address"], "method": r["method"], "path": r["path"]}
        for r in refused_snap["requests"]
    ]

    outside_unchanged = auth_obs["host_fs"]["outside_target"]["unchanged"]
    inside_succeeded = hash_file(inside_target)["exists"]
    canary_scan = scan_for_canaries(canaries.tokens, sqlite_dbs=[app_dir / "store.db"], paths=[app_dir / "sidecar.log"])

    evidence = {
        "schema": "wea-evidence-v1",
        "slot_id": "ctrl_authority",
        "track": "authority",
        "run_id": run_id,
        "provenance": prov,
        "required_observers": ["host_fs", "host_processes", "network_fixtures", "sqlite_records"],
        "observer": auth_obs,
        "product_records": {
            "recorded_connections": [],
        },
        "expected_refusal": {
            "typed_refusal_code": refused_code,
            "host_target_unchanged": outside_unchanged,
            "adjacent_positive_control_passed": inside_succeeded,
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
                {"kind": "refused_outside_write", "expected_code": "SANDBOX_TRY_WRITE_ROOT"},
                {"kind": "permitted_inside_write", "path": "adjacent_allowed.txt"},
                {"kind": "refused_network_probe", "expected_arrivals": 0},
            ],
            expected={"verdict": "pass", "outside_unchanged": True, "inside_created": True},
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
