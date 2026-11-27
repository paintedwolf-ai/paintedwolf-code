#!/usr/bin/env python3
"""WW03 live sidecar suite (C02, C03, C05, C06) extending wait_watch.driver."""
from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
import pathlib
import sys
import threading
import time
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.handle_await import parse_content_json  # noqa: E402
from portable_command.harness import SSECollector, create_isolated_session  # noqa: E402

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
    _extract_handle,
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
        _, _, handle_a = driver.call_command(b_primary.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C02 primary")
        b_primary.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        _, _, handle_b = driver.call_command(b_sibling.command_string(relative_to=project_dir, background=True))
        b_sibling.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("two_jobs_live", handle_a=handle_a, handle_b=handle_b)

        # Park on exact handle_a.
        driver.call_wait([{"kind": "process_done", "handles": [handle_a]}], until_complete=True, timeout_ms=int(timeout_s * 1000), reason="C02 exact handle A")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        # Release primary; sibling stays blocked at barrier.
        b_primary.release()
        resumed_a = driver.await_pending("c02:resumed_a")
        rep_a, exit_a, obs_ha = _extract_report_from_pending(resumed_a, expected_handle=handle_a)
        rendered_obs.record_pending(resumed_a, handle=obs_ha, exit_code=exit_a, report=rep_a, report_readable=bool(rep_a))
        sibling_alive = _pid_alive(b_sibling.child_pid) and not b_sibling.release_path.is_file()
        assertions["exact_handle_selected"] = (obs_ha == handle_a and exit_a == 17)
        assertions["sibling_remained_live"] = sibling_alive
        source_obs.record_event("primary_settled", handle=obs_ha, exit_code=exit_a, sibling_alive=sibling_alive)

        # Release stale sibling so it exits, then launch retry job and verify stale handle cannot settle retry.
        b_sibling.release()
        b_sibling.wait_child_exited(timeout_s=min(10.0, timeout_s))
        _, _, handle_retry = driver.call_command(b_retry.command_string(relative_to=project_dir, background=True))
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
        rep_r, exit_r, obs_hr = _extract_report_from_pending(resumed_retry, expected_handle=handle_retry)
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

        # Register a process_done watch and wait with until_complete + watch_match.
        _, _, watch_parsed = driver.call_watch({
            "condition": {"kind": "process_done", "handles": [proc_handle]},
            "reason": "C03 until_complete watch_match",
        })
        watch_handle = str(watch_parsed.get("handle") or watch_parsed.get("watch_handle") or watch_parsed.get("watch_id") or "")
        assertions["watch_registered"] = bool(watch_handle)
        source_obs.record_event("watch_registered", proc_handle=proc_handle, watch_handle=watch_handle, raw=watch_parsed)

        if watch_handle:
            _, _, wait_parsed = driver.call_wait(
                [{"kind": "watch_match", "handles": [watch_handle]}],
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

        # Cold restart of the sidecar against the same app_dir/store.db (D4).
        if sse is not None:
            sse.close()
            sse = None
        app.close(cleanup=False)
        proc1 = getattr(app, "process", None)
        if proc1 is not None:
            checkpoint_closed_store(app_dir / "store.db", proc1)

        app = app_factory(app_dir)
        app.ready()
        assertions["sidecar_restarted_cleanly"] = True
        source_obs.record_event("sidecar_restarted")

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


def run_slot_c06(out_dir: pathlib.Path, *, engine_bin: pathlib.Path | None, seed: int, timeout_s: float) -> dict[str, Any]:
    """C06-refusal-while-live: live command stays running across refusal/barrier, then releases distinct terminal exit."""
    started = time.monotonic() * 1000.0
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW03 C06 refusal-while-live fixture\n", encoding="utf-8")

    barrier = ProcessBarrier(project_dir / ".ww03-c06-live", exit_code=17, marker=f"ww03-c06-s{seed}")
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
    assertions: dict[str, bool] = {
        "live_command_reached_barrier": False,
        "parked_wait_armed": False,
        "report_published_before_delivery": False,
        "terminal_exit_17_delivered": False,
    }

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        submission_id = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": submission_id, "text": "Run WW03 C06 refusal while live."})
        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        _, _, handle = driver.call_command(barrier.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C06 live job")
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        assertions["live_command_reached_barrier"] = _pid_alive(barrier.child_pid)

        # Park on live job, then release terminal exit 17 and verify publication-before-delivery (#2713).
        driver.call_wait([{"kind": "process_done", "handles": [handle]}], until_complete=True, timeout_ms=int(timeout_s * 1000), reason="C06 live wait")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)
        assertions["parked_wait_armed"] = True

        barrier.release()
        resumed = driver.await_pending("c06:resumed")
        rep, exit_code, obs_h = _extract_report_from_pending(resumed, expected_handle=handle)
        rendered_obs.record_pending(resumed, handle=obs_h, exit_code=exit_code, report=rep, report_readable=bool(rep))

        pub_fact = _observe_report_publication(app_dir / "sidecar.log", session_id=session_id, handle=handle)
        assertions["report_published_before_delivery"] = pub_fact is not None
        assertions["terminal_exit_17_delivered"] = (obs_h == handle and exit_code == 17)
        source_obs.record_event("c06_terminal_delivered", handle=obs_h, exit_code=exit_code, pub_fact=pub_fact)

        driver.finish_turn(resumed, "C06 live command terminal receipt verified.")
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
    prod_ids, prod_refs, _ = _observe_product_identities(storage_snap)
    source_obs.set_facts({"barrier": barrier.source_facts(), "handle": handle, "product_identities": prod_ids, "product_identity_refs": prod_refs})
    return _build_bundle(
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
        correlation={"session_id": session_id, "workspace_id": project_id, "handle": handle, "clock_domain": "system"},
        assertions=assertions,
        barrier_error=barrier_error,
    )


SLOT_RUNNERS = {
    "C02-eligibility-alternatives": run_slot_c02,
    "C03-until-complete-watch-match": run_slot_c03,
    "C05-http-port-readiness": run_slot_c05,
    "C06-refusal-while-live": run_slot_c06,
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
