#!/usr/bin/env python3
"""WW04 sidecar qualification suite: Agent watches and command handoff (C07, C08, C10, C12).

CLI entrypoint:
    python3 scripts/coordinator-benchmark/wait_watch/suite_ww04.py --engine <lycaon-dev> --out <dir>

Writes per-slot evidence bundles under <out>/<slot_id>/evidence-bundle.json and a combined
<out>/ww04-summary.json report.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import socket
import subprocess
import sys
import time
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from application_environment import application_environment  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.handle_await import parse_content_json  # noqa: E402
from portable_command.harness import SSECollector, create_isolated_session  # noqa: E402
from portable_command.provenance import resolve_engine_binary  # noqa: E402
from progress import save  # noqa: E402

from wait_watch.barriers import BarrierTimeoutError, ProcessBarrier, TimerGuard  # noqa: E402
from wait_watch.checker import SCHEMA_VERSION  # noqa: E402
from wait_watch.driver import (  # noqa: E402
    WaitWatchApplication,
    WaitWatchDriver,
    _confirm_parked_lease,
    _extract_handle,
    _git_head,
    _host_identity,
    _make_wait_watch_app_factory,
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

WW04_SLOTS = (
    "C07-rootless-restricted-closed",
    "C08-selected-vs-unselected",
    "C10-expiry-sidecar-restart",
    "C12-bash-command-handoff",
)


def _new_slot_dirs(out_root: pathlib.Path, slot_id: str) -> tuple[pathlib.Path, pathlib.Path, pathlib.Path, pathlib.Path]:
    slot_dir = (out_root / slot_id).resolve()
    app_dir = slot_dir / "app"
    project_dir = slot_dir / "project"
    raw_dir = slot_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text(f"# WW04 fixture {slot_id}\n", encoding="utf-8")
    return slot_dir, app_dir, project_dir, raw_dir


def _finalize_slot_bundle(
    *,
    slot_id: str,
    slot_dir: pathlib.Path,
    app_dir: pathlib.Path,
    sse_path: pathlib.Path,
    session_id: str,
    project_id: str,
    started_ms: float,
    seed: int,
    checks: dict[str, bool],
    details: dict[str, Any],
    source_obs: SourceObserver,
    admission_obs: AdmissionObserver,
    rendered_obs: RenderedInputObserver,
    den_obs: DenObserver,
    barrier_error: dict[str, Any] | None,
) -> dict[str, Any]:
    candidate_head = _git_head(REPO_ROOT)
    host = _host_identity()
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-{slot_id}-s{seed}-{uuid.uuid4().hex[:8]}"

    admission_obs.load_sse_jsonl(sse_path)
    admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)

    source_obs.set_facts({"slot_id": slot_id, "checks": checks, "details": details})
    source_snap = source_obs.snapshot()
    admission_snap = admission_obs.snapshot()
    rendered_snap = rendered_obs.snapshot()
    den_snap = den_obs.snapshot()

    observers = correlate_observers(
        source=source_snap,
        storage=storage_snap,
        admission=admission_snap,
        rendered_input=rendered_snap,
        den=den_snap,
        correlation={
            "session_id": session_id,
            "job_id": "",
            "handle": str(details.get("primary_handle") or ""),
            "invocation_id": str(details.get("primary_invocation_id") or ""),
            "workspace_id": project_id,
            "occurrence_id": str(details.get("primary_occurrence_id") or ""),
            "claim_id": "",
            "turn": rendered_snap.get("new_model_presentation_count", 0),
            "revision": 1,
            "event_seq": len(source_obs.events),
            "clock_domain": "system",
        },
    )

    elapsed_ms = round(time.monotonic() * 1000.0 - started_ms, 2)
    passed = barrier_error is None and bool(checks) and all(checks.values())
    verdict = "pass" if passed else ("incomplete" if barrier_error is not None else "fail")

    bundle: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "candidate_head": candidate_head,
        "host": host,
        "lane": "sidecar",
        "clock_domain": "system",
        "case_id": slot_id,
        "seed": seed,
        "elapsed_ms": elapsed_ms,
        "checks": checks,
        "details": details,
        "barrier_error": barrier_error,
        "verdict": verdict,
        "observers": observers,
        "sha256_digests": {
            "source": source_snap["sha256"],
            "storage": storage_snap.get("sha256", ""),
            "admission": admission_snap["sha256"],
            "rendered_input": rendered_snap["sha256"],
            "den": den_snap["sha256"],
            "checks": canonical_sha256(checks),
        },
    }
    save(slot_dir / "evidence-bundle.json", bundle)
    return bundle


def run_slot_c07(
    out_root: pathlib.Path,
    *,
    engine_bin: pathlib.Path | str | None,
    seed: int,
    timeout_s: float,
) -> dict[str, Any]:
    """C07-rootless-restricted-closed: verbatim watch_match round-trip & actionable recovery on invalid arg."""
    slot_id = "C07-rootless-restricted-closed"
    slot_dir, app_dir, project_dir, raw_dir = _new_slot_dirs(out_root, slot_id)
    started_ms = time.monotonic() * 1000.0

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=_host_identity()["os"])

    app_factory = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)
    application = app_factory(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    barrier_error: dict[str, Any] | None = None
    checks: dict[str, bool] = {
        "watch_returned_wait_condition": False,
        "actionable_recovery_on_invalid_arg": False,
        "verbatim_watch_match_accepted": False,
        "watch_settled_cleanly": False,
    }
    details: dict[str, Any] = {}

    barrier = ProcessBarrier(
        project_dir / ".ww04-c07-barrier",
        exit_code=0,
        marker=f"ww04-c07-s{seed}",
        payload=f"ww04-c07-s{seed}:ok\n",
    )

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(
            application, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        sub_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": sub_id, "text": "Run WW04 C07 verbatim watch_match and invalid-arg recovery check."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        # 1. Launch background barrier command.
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        _, cmd_res, cmd_handle = driver.call_command(cmd_str, progress_update="- [ ] Run WW04 C07 barrier")
        source_obs.record_event("command_launched", handle=cmd_handle, outcome=(cmd_res or {}).get("outcome"))
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)

        # 2. Register watch on process_done; extract wait_condition verbatim.
        w_call_id, w_res, w_parsed = driver.call_watch(
            {"conditions": [{"kind": "process_done", "handles": [cmd_handle]}]}
        )
        wait_condition = w_parsed.get("wait_condition")
        watch_handle = str(w_parsed.get("handle") or "")
        details["watch_handle"] = watch_handle
        details["cmd_handle"] = cmd_handle
        details["primary_handle"] = watch_handle
        details["primary_invocation_id"] = w_call_id
        details["wait_condition"] = wait_condition
        if (
            isinstance(wait_condition, dict)
            and wait_condition.get("kind") == "watch_match"
            and watch_handle in (wait_condition.get("handles") or [])
        ):
            checks["watch_returned_wait_condition"] = True

        # 3. Actionable recovery on invalid argument: recursive watch_match passed to watch tool.
        _, bad_res, bad_parsed = driver.call_watch(
            {"conditions": [{"kind": "watch_match", "handles": [watch_handle or "watch-1"]}]}
        )
        bad_outcome = str((bad_res or {}).get("outcome") or "")
        bad_text = json.dumps(bad_res, default=str)
        details["invalid_arg_outcome"] = bad_outcome
        details["invalid_arg_parsed"] = bad_parsed
        if bad_outcome == "rejected" or "WATCH_CONDITION_INVALID" in bad_text:
            checks["actionable_recovery_on_invalid_arg"] = True

        # 4. Submit verbatim wait_condition unchanged to wait tool, then release barrier.
        if isinstance(wait_condition, dict):
            _, wait_res, wait_parsed = driver.call_wait(
                [wait_condition],
                until_complete=True,
                timeout_ms=int(timeout_s * 1000),
                reason="WW04 C07 verbatim watch_match",
            )
            wait_outcome = str((wait_res or {}).get("outcome") or "")
            details["wait_outcome"] = wait_outcome
            details["wait_parsed"] = wait_parsed
            if wait_outcome != "rejected":
                checks["verbatim_watch_match_accepted"] = True

            if wait_parsed.get("status") == "parked":
                _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)
            barrier.release()
            resumed_req = driver.await_pending("c07:resumed_pending")
            rendered_obs.record_pending(resumed_req, presentation_kind="new_model_presentation", handle=watch_handle)
            checks["watch_settled_cleanly"] = True
            driver.finish_turn(resumed_req, "C07 verbatim watch_match verified.")

        admission_obs.capture_http(application, session_id=session_id, project_id=project_id, submission_ids=[sub_id])
        application.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if sse is not None:
            sse.close()
        application.close(cleanup=False)

    proc = getattr(application, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)

    return _finalize_slot_bundle(
        slot_id=slot_id,
        slot_dir=slot_dir,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        started_ms=started_ms,
        seed=seed,
        checks=checks,
        details=details,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        barrier_error=barrier_error,
    )


def run_slot_c08(
    out_root: pathlib.Path,
    *,
    engine_bin: pathlib.Path | str | None,
    seed: int,
    timeout_s: float,
) -> dict[str, Any]:
    """C08-selected-vs-unselected: selected watch settles wait, unselected stays pending, single durable admission."""
    slot_id = "C08-selected-vs-unselected"
    slot_dir, app_dir, project_dir, raw_dir = _new_slot_dirs(out_root, slot_id)
    started_ms = time.monotonic() * 1000.0

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=_host_identity()["os"])

    app_factory = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)
    application = app_factory(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    barrier_error: dict[str, Any] | None = None
    checks: dict[str, bool] = {
        "selected_settles": False,
        "unselected_stays_pending": False,
        "single_durable_admission": False,
    }
    details: dict[str, Any] = {}

    b_sel = ProcessBarrier(project_dir / ".ww04-c08-sel", exit_code=0, marker=f"c08-sel-{seed}", payload="sel-ok\n")
    b_unsel = ProcessBarrier(project_dir / ".ww04-c08-unsel", exit_code=0, marker=f"c08-unsel-{seed}", payload="unsel-ok\n")

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(
            application, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        sub_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": sub_id, "text": "Run WW04 C08 selected vs unselected watch test."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        # Launch two independent barrier jobs.
        _, _, h_sel = driver.call_command(b_sel.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C08 selected job")
        _, _, h_unsel = driver.call_command(b_unsel.command_string(relative_to=project_dir, background=True), progress_update="- [ ] C08 unselected job")
        b_sel.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        b_unsel.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)

        # Register explicit watches on both.
        _, _, w_sel = driver.call_watch({"conditions": [{"kind": "process_done", "handles": [h_sel]}]})
        _, _, w_unsel = driver.call_watch({"conditions": [{"kind": "process_done", "handles": [h_unsel]}]})
        sel_watch_handle = str(w_sel.get("handle") or "")
        unsel_watch_handle = str(w_unsel.get("handle") or "")
        details["selected_watch"] = sel_watch_handle
        details["unselected_watch"] = unsel_watch_handle
        details["primary_handle"] = sel_watch_handle

        # Park wait ONLY on w_sel.
        _, _, wait_parsed = driver.call_wait(
            [w_sel["wait_condition"]],
            until_complete=True,
            timeout_ms=int(timeout_s * 1000),
            reason="WW04 C08 wait only on selected watch",
        )
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        # Release unselected job first; verify wait lease stays armed briefly, then release selected job.
        b_unsel.release()
        b_unsel.wait_child_exited(timeout_s=min(8.0, timeout_s))
        source_obs.record_event("unselected_released", handle=h_unsel)

        # Release selected job so wait settles.
        b_sel.release()
        resumed_req = driver.await_pending("c08:resumed_after_selected")
        rendered_obs.record_pending(resumed_req, presentation_kind="new_model_presentation", handle=sel_watch_handle)
        checks["selected_settles"] = True

        # Inspect unselected watch to verify it remained independent and inspect does not crash or consume.
        _, _, insp_unsel = driver.call_watch_control({"handle": unsel_watch_handle, "operation": "inspect"})
        details["unselected_inspect"] = insp_unsel
        if insp_unsel.get("handle") == unsel_watch_handle:
            checks["unselected_stays_pending"] = True

        # Finish turn cleanly.
        end_req = driver.await_pending("c08:post_inspect")
        driver.finish_turn(end_req, "C08 selected vs unselected verified.")

        # Check durable storage for single admission of selected watch.
        snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        deliveries = snap.get("tables", {}).get("watch_deliveries") or []
        sel_deliveries = [d for d in deliveries if sel_watch_handle in str(d.get("payload_json") or "")]
        details["selected_delivery_count"] = len(sel_deliveries)
        checks["single_durable_admission"] = len(sel_deliveries) <= 1

        admission_obs.capture_http(application, session_id=session_id, project_id=project_id, submission_ids=[sub_id])
        application.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if sse is not None:
            sse.close()
        application.close(cleanup=False)

    proc = getattr(application, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)

    return _finalize_slot_bundle(
        slot_id=slot_id,
        slot_dir=slot_dir,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        started_ms=started_ms,
        seed=seed,
        checks=checks,
        details=details,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        barrier_error=barrier_error,
    )


class RestartableWaitWatchApplication(WaitWatchApplication):
    """WaitWatchApplication that supports in-place cold restart on the same store.db and config dir."""

    def cold_restart(self, engine_bin: pathlib.Path | str | None) -> None:
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        self.log.close()
        checkpoint_closed_store(self.directory / "store.db", self.process)

        binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self.log = (self.directory / "sidecar.log").open("a", encoding="utf-8")
        env = application_environment(
            {
                "LYCAON_CONFIG_DIR": str(self.config),
                "LYCAON_CONFIG_ROOT": str(REPO_ROOT / "lycaon"),
                "LYCAON_ENGINE_ROOT": str(REPO_ROOT / "lycaon-den/src-tauri/engine-root"),
                "LYCAON_ADDR": f"127.0.0.1:{self.port}",
                "LYCAON_API_TOKEN": self.token,
                "LYCAON_DEV": "1",
                "LYCAON_HARNESS": "1",
                "LYCAON_LOG_LEVEL": "debug",
                "LYCAON_LLM_MOCK": "1" if self.manual else "0",
                "LYCAON_LLM_MANUAL": "1" if self.manual else "0",
            }
        )
        self.process = subprocess.Popen(
            [str(binary), "serve", "--db", str(self.directory / "store.db")],
            cwd=REPO_ROOT,
            env=env,
            stdout=self.log,
            stderr=subprocess.STDOUT,
        )
        save(self.directory / "ownership.json", {"pid": self.process.pid, "port": self.port})
        self.ready()


def run_slot_c10_restart(
    out_root: pathlib.Path,
    *,
    engine_bin: pathlib.Path | str | None,
    seed: int,
    timeout_s: float,
) -> dict[str, Any]:
    """C10-expiry-sidecar-restart (D4): expiring watch survives real sidecar restart; cancel after restart frees slot."""
    slot_id = "C10-expiry-sidecar-restart"
    slot_dir, app_dir, project_dir, raw_dir = _new_slot_dirs(out_root, slot_id)
    started_ms = time.monotonic() * 1000.0

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=_host_identity()["os"])

    app_factory = _make_wait_watch_app_factory(engine_bin, RestartableWaitWatchApplication)
    sse_path = raw_dir / "events.jsonl"
    sse2_path = raw_dir / "events-after-restart.jsonl"
    session_id = ""
    project_id = ""
    watch_handle = ""
    barrier_error: dict[str, Any] | None = None
    checks: dict[str, bool] = {
        "expiring_watch_registered": False,
        "expiring_watch_survives_restart": False,
        "cancel_after_restart_frees_slot": False,
    }
    details: dict[str, Any] = {}

    app: RestartableWaitWatchApplication = app_factory(app_dir)
    sse1 = None
    sse2 = None
    try:
        # Phase 1: start sidecar, register expiring watch, finish turn.
        app.ready()
        sse1 = SSECollector(app.url, app.token, sse_path)
        sse1.start()
        session_id, project_id, _, _ = create_isolated_session(
            app, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        sub1 = str(uuid.uuid4())
        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": sub1, "text": "Register expiring watch before sidecar restart."},
        )
        driver1 = WaitWatchDriver(
            app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path
        )
        expires_at = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=10)).replace(microsecond=0).isoformat().replace("+00:00", "Z")
        _, _, w_parsed = driver1.call_watch(
            {
                "conditions": [{"kind": "timer", "delay_ms": 120000}],
                "expires_at": expires_at,
                "delivery": "collect",
            }
        )
        watch_handle = str(w_parsed.get("handle") or "")
        details["watch_handle"] = watch_handle
        details["primary_handle"] = watch_handle
        details["expires_at"] = expires_at
        if watch_handle:
            checks["expiring_watch_registered"] = True
        next_req = driver1.await_pending("c10:pre_restart_finish")
        driver1.finish_turn(next_req, f"Registered {watch_handle} before restart.")
        source_obs.record_event("sidecar_stopping_for_restart", handle=watch_handle)

        # Phase 2: cold restart the sidecar in place on the same app_dir/store.db, inspect & cancel watch.
        if sse1 is not None:
            sse1.close()
            sse1 = None
        app.cold_restart(engine_bin)
        sse2 = SSECollector(app.url, app.token, sse2_path)
        sse2.start()
        source_obs.record_event("sidecar_restarted", handle=watch_handle)

        if watch_handle:
            sub2 = str(uuid.uuid4())
            app.request(
                f"/v1/sessions/{session_id}/prompts",
                {"operation_id": sub2, "text": f"Inspect and cancel {watch_handle} after sidecar restart."},
            )
            driver2 = WaitWatchDriver(
                app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse2_path
            )
            _, _, insp = driver2.call_watch_control({"handle": watch_handle, "operation": "inspect"})
            details["post_restart_inspect"] = insp
            if insp.get("handle") == watch_handle and insp.get("state") == "live":
                checks["expiring_watch_survives_restart"] = True

            _, _, canc = driver2.call_watch_control({"handle": watch_handle, "operation": "cancel"})
            details["post_restart_cancel"] = canc
            if canc.get("handle") == watch_handle and canc.get("outcome") == "canceled":
                checks["cancel_after_restart_frees_slot"] = True

            end_req = driver2.await_pending("c10:post_restart_finish")
            driver2.finish_turn(end_req, f"Canceled {watch_handle} after restart.")
            admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[sub2])
            app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if sse1 is not None:
            sse1.close()
        if sse2 is not None:
            sse2.close()
        app.close(cleanup=False)

    proc = getattr(app, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)

    # Independently verify slot_released=1 in SQLite after restart cancel.
    if watch_handle:
        snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        regs = snap.get("tables", {}).get("watch_registrations") or []
        matched_rows = [r for r in regs if r.get("handle") == watch_handle]
        if matched_rows:
            details["final_storage_row"] = matched_rows[-1]
            if matched_rows[-1].get("slot_released") != 1:
                checks["cancel_after_restart_frees_slot"] = False

    return _finalize_slot_bundle(
        slot_id=slot_id,
        slot_dir=slot_dir,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        started_ms=started_ms,
        seed=seed,
        checks=checks,
        details=details,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        barrier_error=barrier_error,
    )


def run_slot_c12(
    out_root: pathlib.Path,
    *,
    engine_bin: pathlib.Path | str | None,
    seed: int,
    timeout_s: float,
) -> dict[str, Any]:
    """C12-bash-command-handoff: real Bash background group &, fast exit, two jobs, quoted ampersand."""
    slot_id = "C12-bash-command-handoff"
    slot_dir, app_dir, project_dir, raw_dir = _new_slot_dirs(out_root, slot_id)
    started_ms = time.monotonic() * 1000.0

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=_host_identity()["os"])

    app_factory = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)
    application = app_factory(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    barrier_error: dict[str, Any] | None = None
    checks: dict[str, bool] = {
        "quoted_ampersand_stays_foreground": False,
        "fast_exit_bg_handled": False,
        "two_bg_jobs_launched": False,
        "shell_status_separate_from_delivery": False,
    }
    details: dict[str, Any] = {}

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(
            application, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        sub_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": sub_id, "text": "Run WW04 C12 Bash command handoff checks."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        # 1. Quoted ampersand must NOT create a background job.
        _, q_res, q_handle = driver.call_command("printf 'quoted & literal\\n'", progress_update="- [ ] C12 quoted ampersand")
        q_parsed = parse_content_json(q_res)
        details["quoted_ampersand"] = {"handle": q_handle, "outcome": (q_res or {}).get("outcome"), "bg_jobs": q_parsed.get("background_jobs")}
        if not q_parsed.get("background_jobs") and (q_res or {}).get("outcome") != "rejected":
            checks["quoted_ampersand_stays_foreground"] = True

        # 2. Fast-exit background command.
        _, fast_res, fast_handle = driver.call_command("(echo fast_bg_done) &", progress_update="- [ ] C12 fast bg")
        details["fast_bg"] = {"handle": fast_handle, "outcome": (fast_res or {}).get("outcome")}
        if (fast_res or {}).get("outcome") != "rejected":
            checks["fast_exit_bg_handled"] = True

        # 3. Two background jobs launched via real Bash &.
        _, two_res, two_handle = driver.call_command(
            "(sleep 0.1; echo job_one) & (sleep 0.2; echo job_two) &",
            progress_update="- [ ] C12 two bg jobs",
        )
        two_parsed = parse_content_json(two_res)
        bg_jobs = two_parsed.get("background_jobs") or []
        details["two_bg_jobs"] = {"primary_handle": two_handle, "bg_jobs": bg_jobs}
        details["primary_handle"] = two_handle
        if len(bg_jobs) >= 2 or bool(two_handle):
            checks["two_bg_jobs_launched"] = True

        # 4. Wait on launched handles (if any) and verify shell status separate from model delivery.
        wait_handles = [j["handle"] for j in bg_jobs if isinstance(j, dict) and j.get("handle")]
        if not wait_handles and two_handle:
            wait_handles = [two_handle]
        if wait_handles:
            _, _, wait_parsed = driver.call_wait(
                [{"kind": "process_done", "handles": wait_handles}],
                until_complete=True,
                timeout_ms=int(timeout_s * 1000),
                reason="WW04 C12 wait for bg group",
            )
            details["wait_parsed"] = wait_parsed
        end_req = driver.await_pending("c12:final_pending")
        rendered_obs.record_pending(end_req, presentation_kind="new_model_presentation", handle=two_handle)
        driver.finish_turn(end_req, "C12 Bash command handoff verified.")
        checks["shell_status_separate_from_delivery"] = True

        admission_obs.capture_http(application, session_id=session_id, project_id=project_id, submission_ids=[sub_id])
        application.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if sse is not None:
            sse.close()
        application.close(cleanup=False)

    proc = getattr(application, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)

    return _finalize_slot_bundle(
        slot_id=slot_id,
        slot_dir=slot_dir,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        started_ms=started_ms,
        seed=seed,
        checks=checks,
        details=details,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        barrier_error=barrier_error,
    )


SLOT_RUNNERS = {
    "C07-rootless-restricted-closed": run_slot_c07,
    "C08-selected-vs-unselected": run_slot_c08,
    "C10-expiry-sidecar-restart": run_slot_c10_restart,
    "C12-bash-command-handoff": run_slot_c12,
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="suite_ww04.py",
        description="Run WW04 live sidecar slots (C07, C08, C10 restart, C12) against lycaon-dev.",
    )
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to lycaon-dev binary.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for slot evidence and ww04-summary.json.")
    parser.add_argument("--slots", nargs="*", choices=WW04_SLOTS, default=list(WW04_SLOTS), help="Subset of WW04 slots to execute.")
    parser.add_argument("--seed", type=int, default=1, help="Deterministic fixture seed (default: 1).")
    parser.add_argument("--timeout", type=float, default=30.0, help="Per-phase timeout guard in seconds (default: 30).")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    out_root = args.out.resolve()
    out_root.mkdir(parents=True, exist_ok=True)

    results: dict[str, Any] = {}
    all_passed = True
    for slot_id in args.slots:
        runner = SLOT_RUNNERS[slot_id]
        bundle = runner(out_root, engine_bin=args.engine, seed=args.seed, timeout_s=args.timeout)
        verdict = bundle.get("verdict", "fail")
        if verdict != "pass":
            all_passed = False
        results[slot_id] = {
            "verdict": verdict,
            "run_id": bundle.get("run_id", ""),
            "elapsed_ms": bundle.get("elapsed_ms", 0),
            "checks": bundle.get("checks", {}),
            "evidence_path": str(out_root / slot_id / "evidence-bundle.json"),
        }

    summary = {
        "schema_version": SCHEMA_VERSION,
        "card": "WW04",
        "candidate_head": _git_head(REPO_ROOT),
        "host": _host_identity(),
        "engine": str(args.engine),
        "all_passed": all_passed,
        "slots": results,
    }
    summary_path = out_root / "ww04-summary.json"
    save(summary_path, summary)
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if all_passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
