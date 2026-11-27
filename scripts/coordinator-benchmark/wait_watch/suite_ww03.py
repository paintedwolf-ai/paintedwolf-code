#!/usr/bin/env python3
"""WW03 live sidecar suite (C02, C03, C05, C06) extending wait_watch.driver."""
from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
import pathlib
import socket
import subprocess
import sys
import threading
import time
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.handle_await import parse_content_json  # noqa: E402
from portable_command.harness import SSECollector, create_isolated_session  # noqa: E402
from portable_command.provenance import resolve_engine_binary  # noqa: E402
from progress import save  # noqa: E402

from wait_watch.barriers import (  # noqa: E402
    BarrierTimeoutError,
    HTTPReadinessFixture,
    ProcessBarrier,
    TimerGuard,
)
from wait_watch.checker import SCHEMA_VERSION, check_bundle  # noqa: E402
from wait_watch.driver import (  # noqa: E402
    WaitWatchApplication,
    WaitWatchDriver,
    _confirm_parked_lease,
    _extract_exit_code_from_text,
    _extract_handle,
    _extract_handle_from_text,
    _extract_report_from_pending,
    _extract_report_from_wait_result,
    _git_head,
    _host_identity,
    _make_wait_watch_app_factory,
    _observe_product_identities,
    _observe_report_publication,
)
from wait_watch.observers import (  # noqa: E402
    AdmissionObserver,
    DenObserver,
    RenderedInputObserver,
    SourceObserver,
    StorageObserver,
    canonical_sha256,
    correlate_observers,
)

WW03_SIDECAR_SLOTS = (
    "C02-eligibility-alternatives",
    "C03-until-complete-watch-match",
    "C05-http-port-readiness",
    "C06-refusal-while-live",
    "C06-git-ssh-transport-refusal",
)


class HTTP204ReadinessFixture(HTTPReadinessFixture):
    """Extends HTTPReadinessFixture: serves 503 until released, then 204 for GET and HEAD."""

    def start(self) -> str:
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def _handle(self, send_body: bool) -> None:
                with fixture._lock:
                    ready = fixture._ready
                    status = 204 if (ready and self.path == fixture.path) else 503
                    fixture._probes.append({
                        "method": self.command,
                        "path": self.path,
                        "status": status,
                        "ts_ns": time.time_ns(),
                    })
                body = b"" if status == 204 else b"not_ready\n"
                self.send_response(status)
                if status != 204:
                    self.send_header("Content-Type", "text/plain")
                    self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                if send_body and body:
                    self.wfile.write(body)

            def do_GET(self) -> None:  # noqa: N802
                self._handle(send_body=True)

            def do_HEAD(self) -> None:  # noqa: N802
                self._handle(send_body=False)

            def log_message(self, format: str, *args: Any) -> None:  # noqa: A002
                return

        self._server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self._server.server_address[1]
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self.url


def cold_restart_sidecar(app: WaitWatchApplication, engine_bin: pathlib.Path | None) -> None:
    """Terminate the running sidecar, checkpoint store.db, and respawn in-place on a new free port."""
    app.close(cleanup=False)
    proc_prev = getattr(app, "process", None)
    if proc_prev is not None:
        checkpoint_closed_store(app.directory / "store.db", proc_prev)

    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        app.port = sock.getsockname()[1]
    app.url = f"http://127.0.0.1:{app.port}"
    app.log = (app.directory / "sidecar.log").open("a", encoding="utf-8")
    env = smoke.application_environment({
        "LYCAON_CONFIG_DIR": str(app.config),
        "LYCAON_CONFIG_ROOT": str(REPO_ROOT / "lycaon"),
        "LYCAON_ENGINE_ROOT": str(REPO_ROOT / "lycaon-den/src-tauri/engine-root"),
        "LYCAON_ADDR": f"127.0.0.1:{app.port}",
        "LYCAON_API_TOKEN": app.token,
        "LYCAON_DEV": "1",
        "LYCAON_HARNESS": "1",
        "LYCAON_LOG_LEVEL": "debug",
        "LYCAON_LLM_MOCK": "1" if app.manual else "0",
        "LYCAON_LLM_MANUAL": "1" if app.manual else "0",
    })
    app.process = subprocess.Popen(
        [str(binary), "serve", "--db", str(app.directory / "store.db")],
        cwd=REPO_ROOT,
        env=env,
        stdout=app.log,
        stderr=subprocess.STDOUT,
    )
    save(app.directory / "ownership.json", {"pid": app.process.pid, "port": app.port})
    app.ready()


def _extract_new_handle(tool_result: dict[str, Any], seen: set[str]) -> str:
    payload = parse_content_json(tool_result)
    bg_jobs = payload.get("background_jobs")
    if isinstance(bg_jobs, list):
        for job in reversed(bg_jobs):
            if isinstance(job, dict) and isinstance(job.get("handle"), str):
                h = job["handle"].strip()
                if h and h not in seen:
                    return h
    return _extract_handle(tool_result)


def _extract_terminal_report_for_handle(pending_payload: dict[str, Any], expected_handle: str) -> tuple[str, int | None, str]:
    """Scan messages in reverse preferring a terminal report matching expected_handle with exit_code/termination."""
    messages = pending_payload.get("messages") or []
    for msg in reversed(messages):
        content = msg.get("content")
        if not isinstance(content, str):
            continue
        if expected_handle and expected_handle in content and ("exit_code=" in content or '"exit_code"' in content or "termination=" in content):
            ec = _extract_exit_code_from_text(content)
            obs_h = _extract_handle_from_text(content) or expected_handle
            return content, ec, obs_h
    return _extract_report_from_pending(pending_payload, expected_handle=expected_handle)


def _extract_refusal_from_pending(pending_payload: dict[str, Any], expected_handle: str) -> tuple[str, str]:
    """Scan messages in reverse for a live refusal notice (without termination=exited)."""
    messages = pending_payload.get("messages") or []
    refusal_markers = (
        "sandbox_refusal",
        "unshown_refusals",
        "state=running",
        "refused",
        "network-bind",
        "file-write",
        "agents.md",
        "outside-escape",
    )
    for msg in reversed(messages):
        content = msg.get("content")
        if not isinstance(content, str):
            continue
        low = content.lower()
        if "termination=exited" in low:
            continue
        if any(m in low for m in refusal_markers):
            obs_h = _extract_handle_from_text(content) or (expected_handle if expected_handle in content else "")
            return content, obs_h
    text, _, obs_h = _extract_report_from_pending(pending_payload, expected_handle=expected_handle)
    return text, obs_h


def _pid_alive(pid: int | None) -> bool:
    if not pid or pid <= 0:
        return False
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def _file_sha256(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _write_json(path: pathlib.Path, payload: dict[str, Any]) -> str:
    path.parent.mkdir(parents=True, exist_ok=True)
    text = json.dumps(payload, indent=2, sort_keys=True) + "\n"
    path.write_text(text, encoding="utf-8")
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def _build_bundle(
    *,
    slot_id: str,
    run_id: str,
    candidate_head: str,
    host: dict[str, str],
    seed: int,
    elapsed_ms: float,
    source_obs: SourceObserver,
    storage_snap: dict[str, Any],
    admission_obs: AdmissionObserver,
    rendered_obs: RenderedInputObserver,
    den_obs: DenObserver,
    correlation: dict[str, Any],
    assertions: dict[str, Any],
    barrier_error: dict[str, Any] | None = None,
    expectations: dict[str, Any] | None = None,
) -> dict[str, Any]:
    observers = correlate_observers(
        source=source_obs.snapshot(),
        storage=storage_snap,
        admission=admission_obs.snapshot(),
        rendered_input=rendered_obs.snapshot(),
        den=den_obs.snapshot(),
        correlation=correlation,
    )
    all_ok = barrier_error is None and all(bool(v) for v in assertions.values())
    bundle: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "suite": "WW03",
        "slot_id": slot_id,
        "case_id": slot_id,
        "run_id": run_id,
        "lane": "sidecar",
        "clock_domain": "system",
        "candidate_head": candidate_head,
        "host": host,
        "seed": seed,
        "elapsed_ms": round(elapsed_ms, 2),
        "assertions": assertions,
        "barrier_error": barrier_error,
        "expectations": expectations or {},
        "observers": observers,
        "status": "passed" if all_ok else ("incomplete" if barrier_error else "failed"),
    }
    if expectations:
        bundle["checker_verdict"] = check_bundle(bundle)
    return bundle


def run_slot_c02(out_dir: pathlib.Path, *, engine_bin: pathlib.Path | None, seed: int, timeout_s: float) -> dict[str, Any]:
    """C02-eligibility-alternatives: exact handle selection, sibling remains live, stale retry cannot settle new handle."""
    started = time.monotonic() * 1000.0
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW03 C02 fixture\n", encoding="utf-8")

    b_primary = ProcessBarrier(project_dir / ".ww03-c02-a", exit_code=17, marker=f"ww03-c02-a-s{seed}")
    b_sibling = ProcessBarrier(project_dir / ".ww03-c02-b", exit_code=0, marker=f"ww03-c02-b-s{seed}")
    b_retry = ProcessBarrier(project_dir / ".ww03-c02-retry", exit_code=23, marker=f"ww03-c02-retry-s{seed}")

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    host = _host_identity()
    den_obs = DenObserver(status="not_run", os_name=host["os"])
    candidate_head = _git_head(REPO_ROOT)
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-c02-s{seed}-{uuid.uuid4().hex[:8]}"

    app = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    submission_id = ""
    handle_a = ""
    handle_b = ""
    handle_retry = ""
    barrier_error: dict[str, Any] | None = None
    assertions: dict[str, bool] = {
        "exact_handle_selected": False,
        "sibling_remained_live": False,
        "stale_attempt_did_not_settle_retry": False,
        "retry_handle_settled_with_23": False,
    }

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3, 4]})
        submission_id = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": submission_id, "text": "Run WW03 C02 eligibility."})
        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        # Launch primary and sibling barriers.
        seen_handles: set[str] = set()
        _, cmd_res_a, handle_a = driver.call_command(b_primary.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C02 primary")
        if handle_a:
            seen_handles.add(handle_a)
        b_primary.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        _, cmd_res_b, raw_handle_b = driver.call_command(b_sibling.command_string(relative_to=project_dir, background=True))
        handle_b = _extract_new_handle(cmd_res_b, seen_handles) or raw_handle_b
        if handle_b:
            seen_handles.add(handle_b)
        b_sibling.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("two_jobs_live", handle_a=handle_a, handle_b=handle_b)

        # Park on exact handle_a.
        driver.call_wait([{"kind": "process_done", "handles": [handle_a]}], until_complete=True, timeout_ms=int(timeout_s * 1000), reason="C02 exact handle A")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        # Release primary; sibling stays blocked at barrier.
        b_primary.release()
        resumed_a = driver.await_pending("c02:resumed_a")
        rep_a, exit_a, obs_ha = _extract_terminal_report_for_handle(resumed_a, handle_a)
        rendered_obs.record_pending(resumed_a, handle=obs_ha, exit_code=exit_a, report=rep_a, report_readable=bool(rep_a))
        # Confirm sibling is still live without relying on host PID visibility (Linux runs in a PID namespace).
        sibling_pub = _observe_report_publication(app_dir / "sidecar.log", session_id=session_id, handle=handle_b)
        sibling_alive = b_sibling.is_reached() and (not b_sibling.release_path.is_file()) and (sibling_pub is None)
        assertions["exact_handle_selected"] = (obs_ha == handle_a and exit_a == 17)
        assertions["sibling_remained_live"] = sibling_alive
        source_obs.record_event(
            "primary_settled",
            handle=obs_ha,
            expected_handle=handle_a,
            exit_code=exit_a,
            sibling_alive=sibling_alive,
            sibling_pub=sibling_pub,
        )

        # Release stale sibling so it exits, then launch retry job and verify stale handle cannot settle retry.
        b_sibling.release()
        b_sibling.wait_child_exited(timeout_s=min(10.0, timeout_s))
        _, cmd_res_r, raw_handle_r = driver.call_command(b_retry.command_string(relative_to=project_dir, background=True))
        handle_retry = _extract_new_handle(cmd_res_r, seen_handles) or raw_handle_r
        b_retry.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)

        _, _, wait_retry_parsed = driver.call_wait(
            [{"kind": "process_done", "handles": [handle_retry]}],
            until_complete=True,
            timeout_ms=int(timeout_s * 1000),
            reason="C02 retry handle",
        )
        assertions["stale_attempt_did_not_settle_retry"] = (wait_retry_parsed.get("status") == "parked")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        b_retry.release()
        resumed_retry = driver.await_pending("c02:resumed_retry")
        rep_r, exit_r, obs_hr = _extract_terminal_report_for_handle(resumed_retry, handle_retry)
        rendered_obs.record_pending(resumed_retry, handle=obs_hr, exit_code=exit_r, report=rep_r, report_readable=bool(rep_r))
        assertions["retry_handle_settled_with_23"] = (obs_hr == handle_retry and exit_r == 23)
        driver.finish_turn(resumed_retry, "C02 eligibility verified.")

        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        for b in (b_primary, b_sibling, b_retry):
            if b.is_reached() and not b.release_path.is_file():
                try:
                    b.release()
                except Exception:
                    pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    proc = getattr(app, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)
    admission_obs.load_sse_jsonl(sse_path)
    admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    source_obs.set_facts({
        "primary": b_primary.source_facts(),
        "sibling": b_sibling.source_facts(),
        "retry": b_retry.source_facts(),
        "handles": {"primary": handle_a, "sibling": handle_b, "retry": handle_retry},
    })
    return _build_bundle(
        slot_id="C02-eligibility-alternatives",
        run_id=run_id,
        candidate_head=candidate_head,
        host=host,
        seed=seed,
        elapsed_ms=time.monotonic() * 1000.0 - started,
        source_obs=source_obs,
        storage_snap=storage_snap,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        correlation={"session_id": session_id, "workspace_id": project_id, "handle": handle_retry or handle_a, "clock_domain": "system"},
        assertions=assertions,
        barrier_error=barrier_error,
    )


def run_slot_c03(out_dir: pathlib.Path, *, engine_bin: pathlib.Path | None, seed: int, timeout_s: float) -> dict[str, Any]:
    """C03-until-complete-watch-match: sidecar restart with armed until_complete + watch_match per D4."""
    started = time.monotonic() * 1000.0
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW03 C03 sidecar restart fixture\n", encoding="utf-8")

    barrier = ProcessBarrier(project_dir / ".ww03-c03-watch", exit_code=17, marker=f"ww03-c03-s{seed}")
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    host = _host_identity()
    den_obs = DenObserver(status="not_run", os_name=host["os"])
    candidate_head = _git_head(REPO_ROOT)
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-c03-s{seed}-{uuid.uuid4().hex[:8]}"

    app_factory = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)
    app = app_factory(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    submission_id = ""
    proc_handle = ""
    watch_handle = ""
    barrier_error: dict[str, Any] | None = None
    assertions: dict[str, bool] = {
        "watch_registered": False,
        "until_complete_watch_match_parked": False,
        "armed_lease_persisted_before_restart": False,
        "sidecar_restarted_cleanly": False,
        "watch_and_lease_survived_restart": False,
    }

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        submission_id = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": submission_id, "text": "Run WW03 C03 watch_match restart."})
        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        _, _, proc_handle = driver.call_command(barrier.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C03 watch_match")
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)

        # Register a process_done watch using the exact watch.yaml schema (conditions array, no extra fields).
        _, _, watch_parsed = driver.call_watch({
            "conditions": [{"kind": "process_done", "handles": [proc_handle]}],
        })
        watch_handle = str(watch_parsed.get("handle") or watch_parsed.get("watch_handle") or watch_parsed.get("watch_id") or "")
        wait_cond = watch_parsed.get("wait_condition") if isinstance(watch_parsed.get("wait_condition"), dict) else None
        if not watch_handle and isinstance(wait_cond, dict) and wait_cond.get("handles"):
            watch_handle = str(wait_cond["handles"][0])
        assertions["watch_registered"] = bool(watch_handle)
        source_obs.record_event("watch_registered", proc_handle=proc_handle, watch_handle=watch_handle, raw=watch_parsed)

        if watch_handle:
            cond_arg = wait_cond if isinstance(wait_cond, dict) else {"kind": "watch_match", "handles": [watch_handle]}
            _, _, wait_parsed = driver.call_wait(
                [cond_arg],
                until_complete=True,
                timeout_ms=int(max(timeout_s, 60.0) * 1000),
                reason="C03 until_complete watch_match",
            )
            assertions["until_complete_watch_match_parked"] = (
                wait_parsed.get("status") == "parked" and bool(wait_parsed.get("until_complete"))
            )
            _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        pre_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        pre_armed = [l for l in (pre_snap.get("tables", {}).get("wait_leases") or []) if l.get("status") == "armed"]
        assertions["armed_lease_persisted_before_restart"] = bool(pre_armed)
        source_obs.record_event("pre_restart_snapshot", armed_count=len(pre_armed))

        # Cold restart of the sidecar in-place against the same app_dir/store.db (D4).
        if sse is not None:
            sse.close()
            sse = None
        cold_restart_sidecar(app, engine_bin)
        assertions["sidecar_restarted_cleanly"] = True
        source_obs.record_event("sidecar_restarted", port=app.port, pid=app.process.pid)

        post_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        watches_after = post_snap.get("tables", {}).get("watch_registrations") or []
        leases_after = post_snap.get("tables", {}).get("wait_leases") or []
        assertions["watch_and_lease_survived_restart"] = bool(watches_after) and bool(leases_after)
        source_obs.record_event("post_restart_snapshot", watch_count=len(watches_after), lease_count=len(leases_after))

        # Release barrier and capture admission state.
        barrier.release()
        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if barrier.is_reached() and not barrier.release_path.is_file():
            try:
                barrier.release()
            except Exception:
                pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    proc = getattr(app, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)
    admission_obs.load_sse_jsonl(sse_path)
    admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    source_obs.set_facts({"barrier": barrier.source_facts(), "proc_handle": proc_handle, "watch_handle": watch_handle})
    return _build_bundle(
        slot_id="C03-until-complete-watch-match",
        run_id=run_id,
        candidate_head=candidate_head,
        host=host,
        seed=seed,
        elapsed_ms=time.monotonic() * 1000.0 - started,
        source_obs=source_obs,
        storage_snap=storage_snap,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        correlation={"session_id": session_id, "workspace_id": project_id, "handle": watch_handle or proc_handle, "clock_domain": "system"},
        assertions=assertions,
        barrier_error=barrier_error,
    )


def run_slot_c05(out_dir: pathlib.Path, *, engine_bin: pathlib.Path | None, seed: int, timeout_s: float) -> dict[str, Any]:
    """C05-http-port-readiness: real HTTPReadinessFixture (503 -> 204), missing-grant refusal vs loopback readiness."""
    started = time.monotonic() * 1000.0
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW03 C05 HTTP/port readiness fixture\n", encoding="utf-8")

    fixture = HTTP204ReadinessFixture(path="/ready")
    url = fixture.start()
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    host = _host_identity()
    den_obs = DenObserver(status="not_run", os_name=host["os"])
    candidate_head = _git_head(REPO_ROOT)
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-c05-s{seed}-{uuid.uuid4().hex[:8]}"

    app = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    submission_id = ""
    barrier_error: dict[str, Any] | None = None
    assertions: dict[str, bool] = {
        "fixture_started_serving_503": False,
        "missing_grant_http_refused": False,
        "missing_grant_port_refused": False,
        "fixture_transitioned_to_204": False,
    }

    try:
        # Independently verify fixture serves 503 before release.
        import urllib.error
        import urllib.request

        try:
            urllib.request.urlopen(url, timeout=3)
        except urllib.error.HTTPError as err:
            assertions["fixture_started_serving_503"] = (err.code == 503)

        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        submission_id = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": submission_id, "text": "Run WW03 C05 readiness."})
        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        # Control 1: missing loopback grant for http_ready is refused with SANDBOX_TRY_LOOPBACK_CONNECT.
        _, http_res, http_parsed = driver.call_wait(
            [{"kind": "http_ready", "url": url, "method": "GET", "status_min": 200, "status_max": 299}],
            until_complete=False,
            timeout_ms=5000,
            reason="C05 missing grant http_ready control",
        )
        http_raw = json.dumps(http_res)
        assertions["missing_grant_http_refused"] = (
            (http_res or {}).get("outcome") == "rejected" or "SANDBOX_TRY_LOOPBACK_CONNECT" in http_raw
        )
        source_obs.record_event("http_missing_grant", outcome=(http_res or {}).get("outcome"), parsed=http_parsed)

        # Control 2: missing loopback grant for port_ready is refused with SANDBOX_TRY_LOOPBACK_CONNECT.
        _, port_res, port_parsed = driver.call_wait(
            [{"kind": "port_ready", "host": "127.0.0.1", "port": fixture.port}],
            until_complete=False,
            timeout_ms=5000,
            reason="C05 missing grant port_ready control",
        )
        port_raw = json.dumps(port_res)
        assertions["missing_grant_port_refused"] = (
            (port_res or {}).get("outcome") == "rejected" or "SANDBOX_TRY_LOOPBACK_CONNECT" in port_raw
        )
        source_obs.record_event("port_missing_grant", outcome=(port_res or {}).get("outcome"), parsed=port_parsed)

        # Transition fixture from 503 to 204 and verify both GET and HEAD return 204.
        fixture.release()
        with urllib.request.urlopen(urllib.request.Request(url, method="GET"), timeout=3) as r_get:
            get_code = r_get.status
        with urllib.request.urlopen(urllib.request.Request(url, method="HEAD"), timeout=3) as r_head:
            head_code = r_head.status
        assertions["fixture_transitioned_to_204"] = (get_code == 204 and head_code == 204)
        source_obs.record_event("fixture_released_204", get_status=get_code, head_status=head_code)

        next_req = driver.await_pending("c05:finish")
        rendered_obs.record_pending(next_req)
        driver.finish_turn(next_req, "C05 HTTP/port readiness controls verified.")
        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        fixture.close()
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    proc = getattr(app, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)
    admission_obs.load_sse_jsonl(sse_path)
    admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    source_obs.set_facts(fixture.source_facts())
    return _build_bundle(
        slot_id="C05-http-port-readiness",
        run_id=run_id,
        candidate_head=candidate_head,
        host=host,
        seed=seed,
        elapsed_ms=time.monotonic() * 1000.0 - started,
        source_obs=source_obs,
        storage_snap=storage_snap,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        correlation={"session_id": session_id, "workspace_id": project_id, "clock_domain": "system"},
        assertions=assertions,
        barrier_error=barrier_error,
    )


class LiveRefusalBarrier(ProcessBarrier):
    """ProcessBarrier that stays live, triggers real confined refusals (unix bind + outside write + floor write), then waits for release."""

    def __init__(
        self,
        root_dir: pathlib.Path | str,
        *,
        refusal_target: pathlib.Path | str,
        outside_target: pathlib.Path | str,
        exit_code: int = 17,
        marker: str = "ww03-c06-live-refusal",
        payload: str | None = None,
        max_script_wait_s: int = 90,
    ) -> None:
        super().__init__(root_dir, exit_code=exit_code, marker=marker, payload=payload, max_script_wait_s=max_script_wait_s)
        self.refusal_target = pathlib.Path(refusal_target).resolve()
        self.outside_target = pathlib.Path(outside_target).resolve()
        self.trigger_refusal_path = self.root_dir / "trigger_refusal"
        self.refusal_attempted_path = self.root_dir / "refusal_attempted"
        self.ping_path = self.root_dir / "ping"
        self.pong_path = self.root_dir / "pong"

    def _ensure_script(self) -> pathlib.Path:
        script_path = self.root_dir / "run_live_refusal_barrier.py"
        body = (
            "import json, os, pathlib, socket, subprocess, sys, time\n"
            "root = pathlib.Path(__file__).resolve().parent\n"
            "reached = root / 'reached'\n"
            "reached_tmp = root / 'reached.tmp'\n"
            "trigger = root / 'trigger_refusal'\n"
            "attempted = root / 'refusal_attempted'\n"
            "attempted_tmp = root / 'refusal_attempted.tmp'\n"
            "ping = root / 'ping'\n"
            "pong = root / 'pong'\n"
            "pong_tmp = root / 'pong.tmp'\n"
            "release = root / 'release'\n"
            f"floor_target = pathlib.Path({str(self.refusal_target)!r})\n"
            f"outside_target = pathlib.Path({str(self.outside_target)!r})\n"
            f"marker = {self.marker!r}\n"
            f"payload = {self.payload!r}\n"
            f"exit_code = {self.exit_code}\n"
            f"max_wait = {self.max_script_wait_s}\n"
            "def answer_ping():\n"
            "    if ping.is_file():\n"
            "        token = ping.read_text(encoding='utf-8', errors='replace').strip()\n"
            "        pong_tmp.write_text(json.dumps({'token': token, 'pid': os.getpid(), 'ts_ns': time.time_ns()}), encoding='utf-8')\n"
            "        os.replace(pong_tmp, pong)\n"
            "reached_tmp.write_text(json.dumps({'marker': marker, 'pid': os.getpid(), 'ts_ns': time.time_ns()}), encoding='utf-8')\n"
            "os.replace(reached_tmp, reached)\n"
            "deadline = time.monotonic() + max_wait\n"
            "while not trigger.is_file():\n"
            "    answer_ping()\n"
            "    if time.monotonic() >= deadline:\n"
            "        sys.exit(124)\n"
            "    time.sleep(0.05)\n"
            "attempts = {}\n"
            "try:\n"
            "    orig_cwd = os.getcwd()\n"
            "    os.chdir(str(root))\n"
            "    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)\n"
            "    try:\n"
            "        s.bind('c06.sock')\n"
            "        attempts['unix_bind'] = 'bound'\n"
            "    finally:\n"
            "        s.close()\n"
            "        os.chdir(orig_cwd)\n"
            "except OSError as exc:\n"
            "    attempts['unix_bind'] = f'{type(exc).__name__}:{exc.errno}:{exc}'\n"
            "for label, tgt in (('outside_write', outside_target), ('floor_write', floor_target)):\n"
            "    try:\n"
            "        with open(tgt, 'a', encoding='utf-8') as fh:\n"
            "            fh.write('unexpected-write\\n')\n"
            "        attempts[label] = 'wrote'\n"
            "    except OSError as exc:\n"
            "        attempts[label] = f'{type(exc).__name__}:{exc.errno}:{exc}'\n"
            "for label, tgt in (('touch_outside', outside_target), ('touch_floor', floor_target)):\n"
            "    try:\n"
            "        tp = subprocess.run(['/usr/bin/touch', str(tgt)], capture_output=True, text=True, check=False)\n"
            "        attempts[label] = {'rc': tp.returncode, 'stderr': (tp.stderr or '').strip()}\n"
            "    except OSError as exc:\n"
            "        attempts[label] = f'{type(exc).__name__}:{exc.errno}:{exc}'\n"
            "attempted_tmp.write_text(json.dumps({'attempts': attempts, 'pid': os.getpid(), 'ts_ns': time.time_ns()}), encoding='utf-8')\n"
            "os.replace(attempted_tmp, attempted)\n"
            "while not release.is_file():\n"
            "    answer_ping()\n"
            "    if time.monotonic() >= deadline:\n"
            "        sys.exit(124)\n"
            "    time.sleep(0.05)\n"
            "sys.stdout.write(payload)\n"
            "sys.stdout.flush()\n"
            "sys.exit(exit_code)\n"
        )
        script_path.write_text(body, encoding="utf-8")
        return script_path

    def ping_alive(self, timeout_s: float = 5.0) -> bool:
        """Independently prove the exact sandboxed child process is still alive (works across PID namespaces)."""
        if not self.is_reached() or self.release_path.is_file():
            return False
        token = uuid.uuid4().hex
        tmp = self.ping_path.with_suffix(".tmp")
        tmp.write_text(token, encoding="utf-8")
        os.replace(tmp, self.ping_path)
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            if self.pong_path.is_file():
                try:
                    data = json.loads(self.pong_path.read_text(encoding="utf-8", errors="replace"))
                    if data.get("token") == token:
                        return True
                except ValueError:
                    pass
            time.sleep(0.05)
        return False

    def trigger_refusal(self) -> dict[str, Any]:
        if not self.is_reached():
            raise RuntimeError("cannot trigger refusal before `reached` sentinel is verified")
        tmp = self.trigger_refusal_path.with_suffix(".tmp")
        ts = time.time_ns()
        tmp.write_text(json.dumps({"marker": self.marker, "triggered_at_ns": ts}), encoding="utf-8")
        os.replace(tmp, self.trigger_refusal_path)
        return {"triggered": True, "triggered_at_ns": ts}

    def wait_refusal_attempted(self, timeout_s: float = 15.0, *, dump_state: Any = None) -> dict[str, Any]:
        guard = TimerGuard(timeout_s, phase="c06.wait_refusal_attempted", verdict="incomplete", dump_state=dump_state)
        guard.poll(lambda: self.refusal_attempted_path.is_file(), detail="refusal_attempted sentinel not written")
        raw = self.refusal_attempted_path.read_text(encoding="utf-8", errors="replace").strip()
        try:
            return json.loads(raw)
        except ValueError:
            return {"raw": raw}


def run_slot_c06(out_dir: pathlib.Path, *, engine_bin: pathlib.Path | None, seed: int, timeout_s: float) -> dict[str, Any]:
    """C06-refusal-while-live: real expected-refusal fixture while live, independent live check, then distinct exit 17 + checker ordering."""
    started = time.monotonic() * 1000.0
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW03 C06 refusal-while-live fixture\n", encoding="utf-8")

    # Floor-refused target (AGENTS.md) and outside-workspace target + Unix bind inside LiveRefusalBarrier.
    refusal_target = project_dir / "AGENTS.md"
    outside_target = out_path / "outside-escape.txt"
    barrier = LiveRefusalBarrier(
        project_dir / ".ww03-c06-live",
        refusal_target=refusal_target,
        outside_target=outside_target,
        exit_code=17,
        marker=f"ww03-c06-s{seed}",
    )
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    host = _host_identity()
    den_obs = DenObserver(status="not_run", os_name=host["os"])
    candidate_head = _git_head(REPO_ROOT)
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-c06-s{seed}-{uuid.uuid4().hex[:8]}"

    app = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    submission_id = ""
    handle = ""
    barrier_error: dict[str, Any] | None = None
    report_pub_fact: dict[str, Any] | None = None
    report_published_ts_ns: int | None = None
    refusal_attempt_info: dict[str, Any] = {}
    terminal_report_text = ""
    terminal_exit_code: int | None = None
    assertions: dict[str, bool] = {
        "live_command_reached_barrier": False,
        "parked_wait_before_refusal": False,
        "typed_refusal_received_while_live": False,
        "job_still_running_after_refusal": False,
        "distinct_terminal_exit_17_no_replay": False,
        "checker_publication_before_delivery_pass": False,
    }

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3, 4]})
        submission_id = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": submission_id, "text": "Run WW03 C06 refusal while live."})
        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        # 1. Launch live refusal barrier and confirm running via active ping/pong.
        _, _, handle = driver.call_command(barrier.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C06 live refusal job")
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        assertions["live_command_reached_barrier"] = barrier.ping_alive(timeout_s=5.0)

        # 2. Park wait BEFORE triggering expected refusal.
        driver.call_wait([{"kind": "process_done", "handles": [handle]}], until_complete=True, timeout_ms=int(timeout_s * 1000), reason="C06 wait before refusal")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)
        assertions["parked_wait_before_refusal"] = True

        # 3. Trigger real floor/outside/unix refusal while command stays live.
        trig_info = barrier.trigger_refusal()
        source_obs.record_event("refusal_triggered", handle=handle, **trig_info)
        refusal_attempt_info = barrier.wait_refusal_attempted(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("refusal_attempted", handle=handle, **refusal_attempt_info)

        # 4. Observe resumed turn carrying typed refusal evidence and independently confirm job is STILL running via active ping/pong.
        refusal_req = driver.await_pending("c06:refusal_resumed")
        refusal_text, refusal_handle = _extract_refusal_from_pending(refusal_req, expected_handle=handle)
        refusal_lower = refusal_text.lower()
        typed_refusal = (
            bool(refusal_text)
            and (
                "sandbox_refusal" in refusal_lower
                or "unshown_refusals" in refusal_lower
                or "state=running" in refusal_lower
                or "refused" in refusal_lower
                or "network-bind" in refusal_lower
                or "file-write" in refusal_lower
                or "agents.md" in refusal_lower
                or "outside-escape" in refusal_lower
            )
            and "termination=exited" not in refusal_lower
        )
        still_alive = barrier.ping_alive(timeout_s=5.0)
        assertions["typed_refusal_received_while_live"] = typed_refusal
        assertions["job_still_running_after_refusal"] = still_alive
        source_obs.record_event(
            "refusal_observed_while_live",
            handle=refusal_handle or handle,
            typed_refusal=typed_refusal,
            still_alive=still_alive,
            refusal_excerpt=refusal_text[:300],
        )

        # 5. Re-park on the same still-live handle, then release barrier for distinct terminal exit 17.
        driver.call_wait([{"kind": "process_done", "handles": [handle]}], until_complete=True, timeout_ms=int(timeout_s * 1000), reason="C06 wait for terminal exit")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        rel_info = barrier.release()
        source_obs.record_event("barrier_released", handle=handle, **rel_info)

        resumed = driver.await_pending("c06:terminal_resumed")
        terminal_report_text, terminal_exit_code, obs_h = _extract_terminal_report_for_handle(resumed, handle)

        storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        prod_ids, prod_refs, _ = _observe_product_identities(storage_now)
        report_pub_fact = _observe_report_publication(app_dir / "sidecar.log", session_id=session_id, handle=handle)
        if report_pub_fact is not None:
            source_obs.record_event("report_published", **report_pub_fact)
            report_published_ts_ns = report_pub_fact.get("published_at_ns")

        rendered_obs.record_pending(
            resumed,
            presentation_kind="new_model_presentation",
            session_id=session_id,
            handle=obs_h or handle,
            invocation_id=prod_ids["invocation_id"],
            occurrence_id=prod_ids["occurrence_id"],
            claim_id=prod_ids["claim_id"],
            exit_code=terminal_exit_code,
            report=terminal_report_text,
            report_readable=bool(terminal_report_text),
            evidence_refs={"report": "rendered_input:pending.messages", **prod_refs},
        )
        assertions["distinct_terminal_exit_17_no_replay"] = (
            (obs_h == handle)
            and (terminal_exit_code == 17)
            and ("termination=exited" in terminal_report_text)
            and (terminal_report_text != refusal_text)
        )
        source_obs.record_event("c06_terminal_delivered", handle=obs_h, exit_code=terminal_exit_code, pub_fact=report_pub_fact)

        driver.finish_turn(resumed, "C06 live refusal and distinct terminal receipt verified.")
        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if barrier.is_reached() and not barrier.trigger_refusal_path.is_file():
            try:
                barrier.trigger_refusal()
            except Exception:
                pass
        if barrier.is_reached() and not barrier.release_path.is_file():
            try:
                barrier.release()
            except Exception:
                pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    proc = getattr(app, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)
    admission_obs.load_sse_jsonl(sse_path)
    admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    prod_ids, prod_refs, _ = _observe_product_identities(storage_snap)
    source_obs.set_facts({
        **barrier.source_facts(),
        "refusal_target": str(refusal_target),
        "refusal_attempt": refusal_attempt_info,
        "report_publication": report_pub_fact,
        "report_published_ts_ns": report_published_ts_ns,
        "handle": handle,
        "product_identities": prod_ids,
        "product_identity_refs": prod_refs,
    })

    expectations = {
        "expected_outcome": "delivered",
        "owed_recipients": [f"session:{session_id}"] if session_id else [],
        "owed_occurrences": [prod_ids["occurrence_id"]] if prod_ids.get("occurrence_id") else [],
        "expected_invocation_id": prod_ids.get("invocation_id") or "",
        "expected_handle": handle,
        "expected_exit_code": 17,
        "require_zero_armed_leases": True,
    }
    bundle = _build_bundle(
        slot_id="C06-refusal-while-live",
        run_id=run_id,
        candidate_head=candidate_head,
        host=host,
        seed=seed,
        elapsed_ms=time.monotonic() * 1000.0 - started,
        source_obs=source_obs,
        storage_snap=storage_snap,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        correlation={
            "session_id": session_id,
            "workspace_id": project_id,
            "handle": handle,
            "invocation_id": prod_ids.get("invocation_id") or "",
            "occurrence_id": prod_ids.get("occurrence_id") or "",
            "claim_id": prod_ids.get("claim_id") or "",
            "clock_domain": "system",
        },
        assertions=assertions,
        barrier_error=barrier_error,
        expectations=expectations,
    )
    verdict = bundle.get("checker_verdict") or {}
    defects = verdict.get("defects") or []
    no_pub_order_defect = not any(d.get("code") == "terminal_before_report" for d in defects)
    bundle["assertions"]["checker_publication_before_delivery_pass"] = (
        verdict.get("verdict") == "pass" and no_pub_order_defect
    )
    all_ok = barrier_error is None and all(bool(v) for v in bundle["assertions"].values())
    bundle["status"] = "passed" if all_ok else ("incomplete" if barrier_error else "failed")
    return bundle


def _resolve_engine_root(engine_bin: pathlib.Path | None) -> pathlib.Path | None:
    """Locate an engine-root containing gitengine/bin/git for bundled git tests."""
    candidates: list[pathlib.Path] = []
    if env_root := os.environ.get("LYCAON_ENGINE_ROOT", "").strip():
        candidates.append(pathlib.Path(env_root))
    candidates.extend([
        REPO_ROOT / "lycaon-den/src-tauri/engine-root",
        REPO_ROOT / "e2e-go-cache",
        REPO_ROOT.parent / "e2e-go-cache",
        pathlib.Path.home() / "e2e-go-cache",
    ])
    if engine_bin is not None:
        eb = engine_bin.resolve()
        candidates.extend([
            eb.parent / "engine-root",
            eb.parent,
            eb.parent / "e2e-go-cache",
            eb.parent.parent / "engine-root",
            eb.parent.parent / "e2e-go-cache",
            eb.parent.parent / "lycaon-den/src-tauri/engine-root",
        ])
    for cand in candidates:
        if (cand / "gitengine" / "bin" / "git").is_file():
            return cand
    return None


def _ensure_engine_root(engine_bin: pathlib.Path | None, timeout_s: float) -> pathlib.Path | None:
    """Resolve an existing engine-root with gitengine/bin/git, or stage via scripts/gitengine-ensure.sh."""
    found = _resolve_engine_root(engine_bin)
    if found is not None:
        return found
    ensure_script = REPO_ROOT / "scripts/gitengine-ensure.sh"
    if ensure_script.is_file():
        subprocess.run(
            ["bash", str(ensure_script)],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=max(120.0, timeout_s * 4.0),
            check=False,
        )
    found = _resolve_engine_root(engine_bin)
    if found is not None:
        return found
    default_root = REPO_ROOT / "lycaon-den/src-tauri/engine-root"
    return default_root if default_root.is_dir() else None


def run_slot_c06_git_ssh(out_dir: pathlib.Path, *, engine_bin: pathlib.Path | None, seed: int, timeout_s: float) -> dict[str, Any]:
    """C06-git-ssh-transport-refusal: invoke real #2708 Git SSH transport refusal & receipt parity fixture."""
    started = time.monotonic() * 1000.0
    out_path = out_dir.resolve()
    out_path.mkdir(parents=True, exist_ok=True)
    host = _host_identity()
    candidate_head = _git_head(REPO_ROOT)
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-c06-git-ssh-s{seed}-{uuid.uuid4().hex[:8]}"

    cmd = [
        "./task",
        "test:digest",
        "--",
        "-run",
        "^TestGitSSHTransport(DeliveryParityAndReceiptReopen|SetupDialUnknownAndMultiRemoteIsolation)$",
        "./test/wiring/commands",
    ]
    env = dict(os.environ)
    engine_root = _ensure_engine_root(engine_bin, timeout_s)
    if engine_root is not None:
        env["LYCAON_ENGINE_ROOT"] = str(engine_root)
    timed_out = False
    try:
        proc = subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            env=env,
            capture_output=True,
            text=True,
            timeout=max(300.0, timeout_s * 10.0),
        )
        returncode = proc.returncode
        stdout_text = proc.stdout or ""
        stderr_text = proc.stderr or ""
    except subprocess.TimeoutExpired as exc:
        timed_out = True
        returncode = -1
        stdout_text = (exc.stdout.decode("utf-8", errors="replace") if isinstance(exc.stdout, bytes) else (exc.stdout or ""))
        stderr_text = (exc.stderr.decode("utf-8", errors="replace") if isinstance(exc.stderr, bytes) else (exc.stderr or "")) + f"\nTimeoutExpired after {exc.timeout}s\n"
    log_path = out_path / "git-ssh-transport-e2e.log"
    combined = stdout_text + ("\n--- STDERR ---\n" + stderr_text if stderr_text else "")
    log_path.write_text(combined, encoding="utf-8")
    log_sha = hashlib.sha256(combined.encode("utf-8")).hexdigest()

    combined_low = combined.lower()
    digest_passed = (returncode == 0) and (
        "0 failed" in combined_low
        or "pass" in combined_low
        or "passed" in combined_low
    )
    assertions = {
        "exit_zero": returncode == 0,
        "task_test_digest_passed": digest_passed,
    }
    return {
        "schema_version": SCHEMA_VERSION,
        "suite": "WW03",
        "slot_id": "C06-git-ssh-transport-refusal",
        "case_id": "C06-git-ssh-transport-refusal",
        "run_id": run_id,
        "lane": "sidecar",
        "clock_domain": "system",
        "candidate_head": candidate_head,
        "host": host,
        "seed": seed,
        "elapsed_ms": round(time.monotonic() * 1000.0 - started, 2),
        "command": cmd,
        "lycaon_engine_root": str(engine_root) if engine_root is not None else "",
        "returncode": returncode,
        "timed_out": timed_out,
        "log_path": str(log_path),
        "log_sha256": log_sha,
        "assertions": assertions,
        "status": "passed" if all(assertions.values()) else ("incomplete" if timed_out else "failed"),
    }


SLOT_RUNNERS = {
    "C02-eligibility-alternatives": run_slot_c02,
    "C03-until-complete-watch-match": run_slot_c03,
    "C05-http-port-readiness": run_slot_c05,
    "C06-refusal-while-live": run_slot_c06,
    "C06-git-ssh-transport-refusal": run_slot_c06_git_ssh,
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="suite_ww03.py",
        description="WW03 live sidecar suite (C02, C03, C05, C06) writing evidence bundles and ww03-summary.json.",
    )
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to built lycaon-dev binary.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for evidence bundles and ww03-summary.json.")
    parser.add_argument("--slots", nargs="*", choices=WW03_SIDECAR_SLOTS, default=list(WW03_SIDECAR_SLOTS), help="Subset of WW03 sidecar slots to run.")
    parser.add_argument("--seed", type=int, default=1, help="Deterministic fixture seed (default: 1).")
    parser.add_argument("--timeout", type=float, default=30.0, help="Per-slot timer guard in seconds (default: 30).")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    out_root = args.out.resolve()
    out_root.mkdir(parents=True, exist_ok=True)
    started = time.monotonic() * 1000.0

    slot_entries: list[dict[str, Any]] = []
    all_passed = True
    for slot_id in args.slots:
        slot_dir = out_root / slot_id
        bundle = SLOT_RUNNERS[slot_id](slot_dir, engine_bin=args.engine, seed=args.seed, timeout_s=args.timeout)
        bundle_path = slot_dir / "evidence-bundle.json"
        bundle_sha = _write_json(bundle_path, bundle)
        passed = bundle.get("status") == "passed"
        if not passed:
            all_passed = False
        slot_entries.append({
            "slot_id": slot_id,
            "run_id": bundle["run_id"],
            "status": bundle["status"],
            "assertions": bundle["assertions"],
            "bundle_path": str(bundle_path),
            "bundle_sha256": bundle_sha,
            "elapsed_ms": bundle["elapsed_ms"],
        })

    summary: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "suite": "WW03",
        "candidate_head": _git_head(REPO_ROOT),
        "host": _host_identity(),
        "engine": str(args.engine.resolve()),
        "engine_sha256": _file_sha256(args.engine.resolve()) if args.engine.resolve().is_file() else "",
        "seed": args.seed,
        "all_passed": all_passed,
        "elapsed_ms": round(time.monotonic() * 1000.0 - started, 2),
        "slots": slot_entries,
    }
    summary["summary_sha256"] = canonical_sha256(summary)
    summary_path = out_root / "ww03-summary.json"
    _write_json(summary_path, summary)
    print(json.dumps({"summary_path": str(summary_path), "all_passed": all_passed, "summary_sha256": summary["summary_sha256"]}, indent=2, sort_keys=True))
    return 0 if all_passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
