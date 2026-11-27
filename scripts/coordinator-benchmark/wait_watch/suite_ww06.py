"""WW06 live sidecar qualification suite: C19 cold restart, C20 Stop fence + D5 observation, C21 budget restart."""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import platform
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))
if str(REPO_ROOT / "scripts") not in sys.path:
    sys.path.insert(0, str(REPO_ROOT / "scripts"))

import smoke  # noqa: E402
import smoke_workers  # noqa: E402
from application_environment import application_environment  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.harness import (  # noqa: E402
    SSECollector,
    create_isolated_session,
)
from portable_command.provenance import resolve_engine_binary  # noqa: E402
from progress import save  # noqa: E402

from wait_watch.barriers import BarrierTimeoutError, ProcessBarrier, TimerGuard  # noqa: E402
from wait_watch.driver import WaitWatchApplication, WaitWatchDriver, _confirm_parked_lease  # noqa: E402
from wait_watch.observers import (  # noqa: E402
    AdmissionObserver,
    DenObserver,
    RenderedInputObserver,
    SourceObserver,
    StorageObserver,
    canonical_sha256,
    correlate_observers,
)

WW06_SIDECAR_SLOTS = (
    "C19-cold-sidecar-restart",
    "C20-stop-fence-sessions",
    "C20-stop-command-observation",
    "C21-budget-sidecar-restart",
)


def _git_head() -> str:
    try:
        proc = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
            check=True,
        )
        return proc.stdout.strip()
    except Exception:
        return "unknown"


def _host_identity() -> dict[str, str]:
    return {
        "hostname": socket.gethostname(),
        "os": platform.system(),
        "release": platform.release(),
        "arch": platform.machine(),
        "python": platform.python_version(),
    }


def _write_json(path: pathlib.Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


class RestartableSidecarApplication(WaitWatchApplication):
    """WaitWatchApplication that can stop, checkpoint SQLite, and cold-restart on the exact same store.db."""

    def __init__(self, directory: pathlib.Path, engine_bin: pathlib.Path) -> None:
        self._engine_bin = pathlib.Path(engine_bin).resolve()
        self._restart_count = 0
        orig = smoke.engine_path
        smoke.engine_path = lambda _root: self._engine_bin
        try:
            super().__init__(directory)
        finally:
            smoke.engine_path = orig

    def cold_restart(self) -> dict[str, Any]:
        """Terminate the running sidecar, checkpoint store.db, and start a fresh process on the same DB."""
        old_pid = self.process.pid if self.process is not None else None
        self.close(cleanup=False)
        if self.process is not None:
            checkpoint_closed_store(self.directory / "store.db", self.process)

        self._restart_count += 1
        self.token = secrets.token_hex(24)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"
        log_path = self.directory / f"sidecar-restart-{self._restart_count}.log"
        self.log = log_path.open("w")
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
                "LYCAON_LLM_MOCK": "1",
                "LYCAON_LLM_MANUAL": "1",
            }
        )
        self.process = subprocess.Popen(
            [str(self._engine_bin), "serve", "--db", str(self.directory / "store.db")],
            cwd=REPO_ROOT,
            env=env,
            stdout=self.log,
            stderr=subprocess.STDOUT,
        )
        save(self.directory / "ownership.json", {"pid": self.process.pid, "port": self.port})
        self.ready()
        return {
            "restart_index": self._restart_count,
            "old_pid": old_pid,
            "new_pid": self.process.pid,
            "port": self.port,
            "log_path": str(log_path),
        }


def _poll_storage(
    db_path: pathlib.Path,
    session_id: str,
    predicate: Any,
    *,
    phase: str,
    timeout_s: float,
    dump_state: Any,
    detail: str,
) -> Any:
    guard = TimerGuard(timeout_s, phase=phase, verdict="incomplete", dump_state=dump_state)

    def _check() -> Any:
        snap = StorageObserver(db_path, clock_domain="system").snapshot(session_id)
        return predicate(snap)

    return guard.poll(_check, detail=detail)


def _wait_session_idle(
    app: RestartableSidecarApplication,
    session_id: str,
    *,
    phase: str,
    timeout_s: float,
    dump_state: Any,
) -> None:
    """Wait until every prompt_submission row for session_id in SQLite has settled before cold-restarting."""
    guard = TimerGuard(timeout_s, phase=phase, verdict="incomplete", dump_state=dump_state)
    terminal_statuses = {"complete", "completed", "failed", "interrupted", "canceled"}

    def _settled() -> bool:
        snap = StorageObserver(app.directory / "store.db", clock_domain="system").snapshot(session_id)
        subs = snap.get("tables", {}).get("prompt_submissions") or []
        if not subs:
            return True
        active = [
            s
            for s in subs
            if not s.get("completed_at") and str(s.get("status") or "").lower() not in terminal_statuses
        ]
        return len(active) == 0

    guard.poll(_settled, detail=f"prompt_submissions for session {session_id} did not settle before cold restart")


def _pid_alive(pid: int | None) -> bool | None:
    if not pid or pid <= 0:
        return None
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


def _run_c19_cold_sidecar_restart(engine_bin: pathlib.Path, slot_dir: pathlib.Path, timeout_s: float) -> dict[str, Any]:
    """C19-cold-sidecar-restart: arm watch + trigger match, cold-restart sidecar on same store.db, confirm exact single delivery."""
    app_dir = slot_dir / "app"
    project_dir = slot_dir / "project"
    barrier_dir = project_dir / ".ww06-c19-barrier"
    raw_dir = slot_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW06 C19 cold sidecar restart fixture\n", encoding="utf-8")

    marker = f"ww06-c19-{uuid.uuid4().hex[:8]}"
    barrier = ProcessBarrier(barrier_dir, exit_code=17, marker=marker, payload=f"{marker}:done\n")
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=platform.system())

    app = RestartableSidecarApplication(app_dir, engine_bin)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    watch_handle = ""
    watch_id = ""
    cmd_handle = ""
    restart_info: dict[str, Any] = {}
    errors: list[str] = []
    checks: dict[str, Any] = {}

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        sub1 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub1, "text": "WW06 C19 cold restart turn 1."})

        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        # 1. Launch background barrier command.
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        _, cmd_res, cmd_handle = driver.call_command(cmd_str, progress_update="- [ ] WW06 C19 cold restart")
        source_obs.record_event("command_launched", handle=cmd_handle, outcome=(cmd_res or {}).get("outcome"))
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("barrier_reached", handle=cmd_handle, pid=barrier.child_pid)

        # 2. Register explicit collect watch on process_done for cmd_handle, then finish turn 1 cleanly.
        _, watch_res, watch_parsed = driver.call_watch(
            {
                "conditions": [{"kind": "process_done", "handles": [cmd_handle]}],
                "delivery": "collect",
            }
        )
        watch_handle = str(watch_parsed.get("handle") or "")
        watch_id = str(watch_parsed.get("watch_id") or "")
        source_obs.record_event("watch_registered", watch_handle=watch_handle, watch_id=watch_id, outcome=(watch_res or {}).get("outcome"))
        if not watch_handle:
            errors.append(f"watch registration did not return handle: {watch_parsed}")

        post_watch_req = driver.await_pending("c19:finish_turn1")
        driver.finish_turn(post_watch_req, f"Armed watch {watch_handle} for {cmd_handle}.")

        # 3. Release barrier while session is idle so match commits to SQLite (pending) before restart.
        rel_info = barrier.release()
        source_obs.record_event("barrier_released", handle=cmd_handle, **rel_info)
        barrier.wait_child_exited(timeout_s=min(10.0, timeout_s))

        pending_match = _poll_storage(
            app_dir / "store.db",
            session_id,
            lambda snap: next(
                (
                    m
                    for m in (snap.get("tables", {}).get("watch_matches") or [])
                    if (not watch_id or m.get("watch_id") == watch_id) and m.get("reservation_state") == "pending"
                ),
                None,
            ),
            phase="c19.wait_pending_match_before_restart",
            timeout_s=timeout_s,
            dump_state=driver._dump_state,
            detail=f"pending watch_match for {watch_handle}/{watch_id} not persisted before restart",
        )
        source_obs.record_event("pending_match_persisted", match_id=pending_match.get("match_id"))
        checks["pending_match_before_restart"] = True

        # 4. Cold-restart sidecar on the exact same store.db.
        if sse is not None:
            sse.close()
            sse = None
        restart_info = app.cold_restart()
        source_obs.record_event("sidecar_cold_restarted", **restart_info)
        sse_after_path = raw_dir / "events-after-restart.jsonl"
        sse = SSECollector(app.url, app.token, sse_after_path)
        sse.start()

        # 5. Send a follow-up user turn after cold restart; observe delivered watch match in /harness/llm/pending.
        sub2 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub2, "text": "WW06 C19 collect after cold restart."})
        driver2 = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_after_path)
        resumed_req = driver2.await_pending("c19:post_restart_pending")
        rendered_obs.record_pending(resumed_req, presentation_kind="new_model_presentation", session_id=session_id, handle=watch_handle)

        # Check whether the watch observation was delivered in the resumed turn prompt.
        prompt_text = "\n".join(str(m.get("content") or "") for m in (resumed_req.get("messages") or []))
        delivered_in_prompt = (watch_handle in prompt_text) or ("Watch observations (receipt " in prompt_text) or (cmd_handle in prompt_text)
        checks["delivered_in_post_restart_prompt"] = delivered_in_prompt
        if not delivered_in_prompt:
            errors.append(f"post-restart model prompt missing watch observation for {watch_handle}/{cmd_handle}")

        driver2.finish_turn(resumed_req, "Confirmed single delivery after cold restart.")

        # Wait for delivery to commit in SQLite.
        delivered_match = _poll_storage(
            app_dir / "store.db",
            session_id,
            lambda snap: next(
                (
                    m
                    for m in (snap.get("tables", {}).get("watch_matches") or [])
                    if m.get("match_id") == pending_match.get("match_id") and m.get("reservation_state") == "delivered"
                ),
                None,
            ),
            phase="c19.wait_match_delivered_after_restart",
            timeout_s=timeout_s,
            dump_state=driver2._dump_state,
            detail="watch_match did not transition to delivered after cold restart",
        )
        checks["match_delivered_in_sqlite"] = delivered_match is not None

        # Send one more turn to confirm zero duplicate presentation.
        sub3 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub3, "text": "WW06 C19 verify zero duplicate."})
        third_req = driver2.await_pending("c19:verify_no_duplicate")
        rendered_obs.record_pending(third_req, presentation_kind="new_model_presentation", session_id=session_id, handle=watch_handle)
        third_last_msgs = "\n".join(str(m.get("content") or "") for m in (third_req.get("messages") or [])[-2:])
        duplicate_present = "Watch observations (receipt " in third_last_msgs and watch_handle in third_last_msgs
        checks["zero_duplicate_presentation"] = not duplicate_present
        if duplicate_present:
            errors.append("duplicate watch observation presented on subsequent turn after cold restart")
        driver2.finish_turn(third_req, "Zero duplicate confirmed.")

        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[sub1, sub2, sub3])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        errors.append(f"barrier_timeout:{exc.phase}:{exc}")
    except Exception as exc:
        errors.append(f"exception:{type(exc).__name__}:{exc}")
    finally:
        if barrier.is_reached() and not barrier.release_path.is_file():
            try:
                barrier.release()
            except Exception:
                pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    if app.process is not None:
        checkpoint_closed_store(app_dir / "store.db", app.process)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    deliveries = [
        d
        for d in (storage_snap.get("tables", {}).get("watch_deliveries") or [])
        if d.get("state") == "committed" and d.get("destination_kind") != "final_receipt"
    ]
    checks["committed_delivery_count"] = len(deliveries)
    if len(deliveries) != 1:
        errors.append(f"expected exactly 1 committed watch delivery across cold restart, got {len(deliveries)}")

    source_obs.set_facts({**barrier.source_facts(), "watch_handle": watch_handle, "watch_id": watch_id, "restart": restart_info})
    observers = correlate_observers(
        source=source_obs.snapshot(),
        storage=storage_snap,
        admission=admission_obs.snapshot(),
        rendered_input=rendered_obs.snapshot(),
        den=den_obs.snapshot(),
        correlation={"session_id": session_id, "handle": watch_handle, "workspace_id": project_id, "clock_domain": "system"},
    )
    verdict = "pass" if not errors else "fail"
    bundle = {
        "schema_version": 1,
        "slot_id": "C19-cold-sidecar-restart",
        "family": "C19",
        "lane": "sidecar",
        "verdict": verdict,
        "checks": checks,
        "errors": errors,
        "observers": observers,
    }
    _write_json(slot_dir / "evidence.json", bundle)
    return bundle


def _run_c20_stop_fence_sessions(engine_bin: pathlib.Path, slot_dir: pathlib.Path, timeout_s: float) -> dict[str, Any]:
    """C20-stop-fence-sessions: real Stop API during parked/active watch -> zero replacement turns, retained evidence, no implicit rearm (D5)."""
    app_dir = slot_dir / "app"
    project_dir = slot_dir / "project"
    barrier_dir = project_dir / ".ww06-c20-fence-barrier"
    raw_dir = slot_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW06 C20 Stop fence fixture\n", encoding="utf-8")

    marker = f"ww06-c20-{uuid.uuid4().hex[:8]}"
    barrier = ProcessBarrier(barrier_dir, exit_code=0, marker=marker, payload=f"{marker}:ok\n")
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=platform.system())

    app = RestartableSidecarApplication(app_dir, engine_bin)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    watch_handle = ""
    watch_id = ""
    cmd_handle = ""
    checks: dict[str, Any] = {}
    errors: list[str] = []

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        sub1 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub1, "text": "WW06 C20 arm watch and park."})

        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        # 1. Launch background barrier command.
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        _, _, cmd_handle = driver.call_command(cmd_str, progress_update="- [ ] WW06 C20 Stop fence")
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)

        # 2. Register explicit wake watch on process_done and park wait on watch_match.
        _, _, watch_parsed = driver.call_watch(
            {
                "conditions": [{"kind": "process_done", "handles": [cmd_handle]}],
                "delivery": "wake",
            }
        )
        watch_handle = str(watch_parsed.get("handle") or "")
        watch_id = str(watch_parsed.get("watch_id") or "")
        wait_cond = watch_parsed.get("wait_condition") or {"kind": "watch_match", "handles": [watch_handle]}
        driver.call_wait([wait_cond], until_complete=False, timeout_ms=60000, reason="WW06 C20 park before Stop")

        parked_proof = _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)
        checks["parked_before_stop"] = bool(parked_proof)
        source_obs.record_event("parked_confirmed", proof=parked_proof)

        # 3. Call real Stop API: POST /v1/sessions/{session_id}/abort.
        stop_resp = app.request(f"/v1/sessions/{session_id}/abort", {})
        checks["stop_acknowledged"] = stop_resp is not None
        source_obs.record_event("stop_api_called", response=stop_resp)

        # 4. Verify watch canceled and armed lease canceled in SQLite.
        canceled_reg = _poll_storage(
            app_dir / "store.db",
            session_id,
            lambda snap: next(
                (
                    r
                    for r in (snap.get("tables", {}).get("watch_registrations") or [])
                    if r.get("watch_id") == watch_id and r.get("state") == "terminal" and r.get("terminal_outcome") == "canceled"
                ),
                None,
            ),
            phase="c20.wait_watch_canceled",
            timeout_s=timeout_s,
            dump_state=driver._dump_state,
            detail=f"watch {watch_handle} not canceled after Stop",
        )
        checks["watch_canceled_after_stop"] = canceled_reg is not None
        checks["evidence_retained_final_receipt"] = bool((canceled_reg or {}).get("final_receipt_id"))

        # 5. Release barrier AFTER Stop -> verify zero watch-originated replacement turns.
        barrier.release()
        barrier.wait_child_exited(timeout_s=min(8.0, timeout_s))
        time.sleep(1.5)
        pending_after_stop = smoke_workers.pending(app, session_id)
        checks["zero_watch_replacement_turns"] = pending_after_stop is None
        if pending_after_stop is not None:
            errors.append(f"unexpected watch-originated replacement turn after Stop: {pending_after_stop.get('id')}")

        # 6. Send a next user turn -> verify canceled watch does NOT implicitly rearm.
        sub2 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub2, "text": "WW06 C20 next user message after Stop."})
        next_req = driver.await_pending("c20:next_user_turn_after_stop")
        rendered_obs.record_pending(next_req, presentation_kind="new_model_presentation", session_id=session_id, handle=watch_handle)
        driver.finish_turn(next_req, "Next user turn completed without implicit rearm.")

        snap_after_user = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        live_after_user = [
            r for r in (snap_after_user.get("tables", {}).get("watch_registrations") or []) if r.get("state") == "live"
        ]
        checks["no_implicit_rearm_on_next_user_turn"] = len(live_after_user) == 0
        if live_after_user:
            errors.append(f"watch implicitly rearmed on next user message: {live_after_user}")

        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[sub1, sub2])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        errors.append(f"barrier_timeout:{exc.phase}:{exc}")
    except Exception as exc:
        errors.append(f"exception:{type(exc).__name__}:{exc}")
    finally:
        if barrier.is_reached() and not barrier.release_path.is_file():
            try:
                barrier.release()
            except Exception:
                pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    if app.process is not None:
        checkpoint_closed_store(app_dir / "store.db", app.process)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    source_obs.set_facts({**barrier.source_facts(), "watch_handle": watch_handle, "watch_id": watch_id})
    observers = correlate_observers(
        source=source_obs.snapshot(),
        storage=storage_snap,
        admission=admission_obs.snapshot(),
        rendered_input=rendered_obs.snapshot(),
        den=den_obs.snapshot(),
        correlation={"session_id": session_id, "handle": watch_handle, "workspace_id": project_id, "clock_domain": "system"},
    )
    verdict = "pass" if not errors and all(bool(v) for v in checks.values()) else "fail"
    bundle = {
        "schema_version": 1,
        "slot_id": "C20-stop-fence-sessions",
        "family": "C20",
        "lane": "sidecar",
        "d5_scope": "session_tree_stop_fence_only",
        "verdict": verdict,
        "checks": checks,
        "errors": errors,
        "observers": observers,
    }
    _write_json(slot_dir / "evidence.json", bundle)
    return bundle


def _run_c20_stop_command_observation(engine_bin: pathlib.Path, slot_dir: pathlib.Path, timeout_s: float) -> dict[str, Any]:
    """C20-stop-command-observation: record unjoined command outcome under Stop as observation-only linked to paintedwolf-portable-command-stop (D5)."""
    app_dir = slot_dir / "app"
    project_dir = slot_dir / "project"
    barrier_dir = project_dir / ".ww06-c20-cmd-obs-barrier"
    raw_dir = slot_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW06 C20 unjoined command observation fixture (D5)\n", encoding="utf-8")

    marker = f"ww06-c20-obs-{uuid.uuid4().hex[:8]}"
    barrier = ProcessBarrier(barrier_dir, exit_code=0, marker=marker, payload=f"{marker}:obs\n")
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=platform.system())

    app = RestartableSidecarApplication(app_dir, engine_bin)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    cmd_handle = ""
    observation: dict[str, Any] = {
        "observation_only": True,
        "d5_policy": "C20 grades the session-tree Stop fence only; continued-program Stop contract belongs to ST01-ST03.",
        "linked_milestone": "paintedwolf-portable-command-stop",
    }
    errors: list[str] = []

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2]})
        sub1 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub1, "text": "WW06 C20 unjoined command observation."})

        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        _, cmd_res, cmd_handle = driver.call_command(cmd_str, progress_update="- [ ] WW06 C20 unjoined command")
        reached = barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        observation["command_handle"] = cmd_handle
        observation["command_launch_outcome"] = (cmd_res or {}).get("outcome")
        observation["child_pid"] = barrier.child_pid
        observation["pid_alive_before_stop"] = _pid_alive(barrier.child_pid)
        source_obs.record_event("unjoined_command_reached", handle=cmd_handle, sentinel=reached)

        # Issue real Stop API call while unjoined command is still running.
        stop_resp = app.request(f"/v1/sessions/{session_id}/abort", {})
        observation["stop_acknowledged"] = stop_resp is not None
        time.sleep(1.0)
        observation["pid_alive_after_stop"] = _pid_alive(barrier.child_pid)

        snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        progs = snap.get("tables", {}).get("invocation_programs") or []
        observation["invocation_program_states_after_stop"] = [
            {"id": p.get("id"), "state": p.get("state"), "status_code": p.get("status_code")} for p in progs
        ]
        derived_watches = [
            {"handle": r.get("handle"), "state": r.get("state"), "outcome": r.get("terminal_outcome")}
            for r in (snap.get("tables", {}).get("watch_registrations") or [])
        ]
        observation["watch_states_after_stop"] = derived_watches
        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[sub1])
    except BarrierTimeoutError as exc:
        errors.append(f"barrier_timeout:{exc.phase}:{exc}")
    except Exception as exc:
        errors.append(f"exception:{type(exc).__name__}:{exc}")
    finally:
        if barrier.is_reached() and not barrier.release_path.is_file():
            try:
                barrier.release()
                barrier.wait_child_exited(timeout_s=5.0)
            except Exception:
                pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    if app.process is not None:
        checkpoint_closed_store(app_dir / "store.db", app.process)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    source_obs.set_facts({**barrier.source_facts(), "observation": observation})
    observers = correlate_observers(
        source=source_obs.snapshot(),
        storage=storage_snap,
        admission=admission_obs.snapshot(),
        rendered_input=rendered_obs.snapshot(),
        den=den_obs.snapshot(),
        correlation={"session_id": session_id, "handle": cmd_handle, "workspace_id": project_id, "clock_domain": "system"},
    )
    # Per D5, command outcome under Stop carries no product fail verdict; only harness execution errors fail.
    verdict = "pass" if not errors else "fail"
    bundle = {
        "schema_version": 1,
        "slot_id": "C20-stop-command-observation",
        "family": "C20",
        "lane": "sidecar",
        "observation_only": True,
        "linked_milestone": "paintedwolf-portable-command-stop",
        "verdict": verdict,
        "observation": observation,
        "errors": errors,
        "observers": observers,
    }
    _write_json(slot_dir / "evidence.json", bundle)
    return bundle


def _run_c21_budget_sidecar_restart(engine_bin: pathlib.Path, slot_dir: pathlib.Path, timeout_s: float) -> dict[str, Any]:
    """C21-budget-sidecar-restart (D4 representative sidecar restart): slot cap/reuse & durable hourly wake budget survive cold restart."""
    app_dir = slot_dir / "app"
    project_dir = slot_dir / "project"
    barrier_dir = project_dir / ".ww06-c21-wake-barrier"
    raw_dir = slot_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW06 C21 budget & slot reuse restart fixture\n", encoding="utf-8")

    marker = f"ww06-c21-{uuid.uuid4().hex[:8]}"
    barrier = ProcessBarrier(barrier_dir, exit_code=0, marker=marker, payload=f"{marker}:wake\n")
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=platform.system())

    app = RestartableSidecarApplication(app_dir, engine_bin)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    restart_info: dict[str, Any] = {}
    checks: dict[str, Any] = {}
    errors: list[str] = []

    try:
        app.ready()
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(app, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        sub1 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub1, "text": "WW06 C21 budget and capacity setup."})

        driver = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path)

        # 1. Launch a deterministic ProcessBarrier command, arm a delivery=wake watch on it,
        #    settle turn 1 to idle, and ONLY THEN release the barrier so the ambient wake is
        #    guaranteed to fire while idle on both fast and slow hosts (zero wall-clock timer race).
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        _, _, wake_cmd_handle = driver.call_command(cmd_str, progress_update="- [ ] WW06 C21 wake budget setup")
        barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("c21_wake_barrier_reached", handle=wake_cmd_handle, pid=barrier.child_pid)

        _, _, fast_wake = driver.call_watch(
            {
                "conditions": [{"kind": "process_done", "handles": [wake_cmd_handle]}],
                "delivery": "wake",
            }
        )
        fast_handle = str(fast_wake.get("handle") or "")
        turn1_end = driver.await_pending("c21:finish_turn1")
        driver.finish_turn(turn1_end, f"Armed wake watch {fast_handle} for {wake_cmd_handle}.")
        _wait_session_idle(
            app,
            session_id,
            phase="c21.wait_idle_before_wake_release",
            timeout_s=timeout_s,
            dump_state=driver._dump_state,
        )

        # Release barrier now that session is idle -> ambient wake turn admits deterministically.
        rel_info = barrier.release()
        source_obs.record_event("c21_wake_barrier_released", handle=wake_cmd_handle, **rel_info)
        barrier.wait_child_exited(timeout_s=min(10.0, timeout_s))

        wake_req = driver.await_pending("c21:ambient_wake_turn")
        rendered_obs.record_pending(wake_req, presentation_kind="new_model_presentation", session_id=session_id, handle=fast_handle)

        # 2. Fill all 8 live explicit watch slots with long-lived timer watches.
        live_handles: list[str] = []
        for idx in range(8):
            _, w_res, w_parsed = driver.call_watch(
                {
                    "conditions": [{"kind": "timer", "delay_ms": 3600000 + idx * 1000}],
                    "delivery": "collect",
                }
            )
            h = str(w_parsed.get("handle") or "")
            if not h:
                errors.append(f"failed registering live watch {idx + 1}/8: {w_res}")
                break
            live_handles.append(h)

        # 3. Attempt 9th live watch -> must be rejected by capacity cap.
        _, ninth_res, ninth_parsed = driver.call_watch(
            {
                "conditions": [{"kind": "timer", "delay_ms": 3700000}],
                "delivery": "collect",
            }
        )
        ninth_rejected = (ninth_res or {}).get("outcome") == "rejected" or not ninth_parsed.get("handle")
        checks["ninth_rejected_before_restart"] = ninth_rejected
        if not ninth_rejected:
            errors.append(f"9th live watch was not rejected before restart: {ninth_res}")

        turn2_end = driver.await_pending("c21:finish_turn2")
        driver.finish_turn(turn2_end, "Capacity filled before cold restart.")
        _wait_session_idle(
            app,
            session_id,
            phase="c21.wait_idle_before_restart",
            timeout_s=timeout_s,
            dump_state=driver._dump_state,
        )

        # Check durable wake_admissions_json before restart.
        snap_before = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        sess_rows_before = snap_before.get("tables", {}).get("watch_session_state") or []
        admissions_before = json.loads((sess_rows_before[0].get("wake_admissions_json") or "[]")) if sess_rows_before else []
        checks["wake_budget_recorded_before_restart"] = len(admissions_before) >= 1
        if len(admissions_before) < 1:
            errors.append(f"expected >= 1 wake admission before restart, got {admissions_before}")

        # 4. Cold-restart sidecar on the exact same store.db.
        if sse is not None:
            sse.close()
            sse = None
        restart_info = app.cold_restart()
        source_obs.record_event("sidecar_cold_restarted", **restart_info)
        sse_after_path = raw_dir / "events-after-restart.jsonl"
        sse = SSECollector(app.url, app.token, sse_after_path)
        sse.start()

        # Verify durable hourly budget survived cold restart.
        snap_after = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        sess_rows_after = snap_after.get("tables", {}).get("watch_session_state") or []
        admissions_after = json.loads((sess_rows_after[0].get("wake_admissions_json") or "[]")) if sess_rows_after else []
        checks["durable_hourly_budget_survived_restart"] = len(admissions_after) == len(admissions_before) and len(admissions_after) >= 1
        if not checks["durable_hourly_budget_survived_restart"]:
            errors.append(f"wake_admissions_json mismatch across restart: before={admissions_before} after={admissions_after}")

        # 5. After restart, confirm 9th is still rejected, then cancel 1 and immediately reuse the slot.
        sub3 = str(uuid.uuid4())
        app.request(f"/v1/sessions/{session_id}/prompts", {"operation_id": sub3, "text": "WW06 C21 verify cap and reuse after restart."})
        driver2 = WaitWatchDriver(app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_after_path)

        _, still_full_res, still_full_parsed = driver2.call_watch(
            {
                "conditions": [{"kind": "timer", "delay_ms": 3750000}],
                "delivery": "collect",
            }
        )
        checks["cap_preserved_after_restart"] = (still_full_res or {}).get("outcome") == "rejected" or not still_full_parsed.get("handle")
        if not checks["cap_preserved_after_restart"]:
            errors.append(f"live watch cap not preserved after restart: {still_full_res}")

        if live_handles:
            _, cancel_res, cancel_parsed = driver2.call_watch_control({"handle": live_handles[0], "operation": "cancel"})
            checks["cancel_after_restart_ok"] = (cancel_res or {}).get("outcome") != "rejected" and cancel_parsed.get("outcome") == "canceled"
            _, reuse_res, reuse_parsed = driver2.call_watch(
                {
                    "conditions": [{"kind": "timer", "delay_ms": 3800000}],
                    "delivery": "collect",
                }
            )
            checks["slot_reuse_after_restart"] = bool(reuse_parsed.get("handle")) and (reuse_res or {}).get("outcome") != "rejected"
            if not checks["slot_reuse_after_restart"]:
                errors.append(f"slot reuse failed after cancel+restart: {reuse_res}")

        turn3_end = driver2.await_pending("c21:finish_turn3")
        driver2.finish_turn(turn3_end, "Verified budget persistence and slot reuse after restart.")
        _wait_session_idle(
            app,
            session_id,
            phase="c21.wait_idle_after_turn3",
            timeout_s=timeout_s,
            dump_state=driver2._dump_state,
        )

        admission_obs.capture_http(app, session_id=session_id, project_id=project_id, submission_ids=[sub1, sub3])
        app.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        errors.append(f"barrier_timeout:{exc.phase}:{exc}")
    except Exception as exc:
        errors.append(f"exception:{type(exc).__name__}:{exc}")
    finally:
        if barrier.is_reached() and not barrier.release_path.is_file():
            try:
                barrier.release()
            except Exception:
                pass
        if sse is not None:
            sse.close()
        app.close(cleanup=False)

    if app.process is not None:
        checkpoint_closed_store(app_dir / "store.db", app.process)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    source_obs.set_facts({**barrier.source_facts(), "restart": restart_info, "checks": checks})
    observers = correlate_observers(
        source=source_obs.snapshot(),
        storage=storage_snap,
        admission=admission_obs.snapshot(),
        rendered_input=rendered_obs.snapshot(),
        den=den_obs.snapshot(),
        correlation={"session_id": session_id, "workspace_id": project_id, "clock_domain": "system"},
    )
    verdict = "pass" if not errors and all(bool(v) for v in checks.values()) else "fail"
    bundle = {
        "schema_version": 1,
        "slot_id": "C21-budget-sidecar-restart",
        "family": "C21",
        "lane": "sidecar",
        "verdict": verdict,
        "checks": checks,
        "errors": errors,
        "observers": observers,
    }
    _write_json(slot_dir / "evidence.json", bundle)
    return bundle


SLOT_RUNNERS = {
    "C19-cold-sidecar-restart": _run_c19_cold_sidecar_restart,
    "C20-stop-fence-sessions": _run_c20_stop_fence_sessions,
    "C20-stop-command-observation": _run_c20_stop_command_observation,
    "C21-budget-sidecar-restart": _run_c21_budget_sidecar_restart,
}


def run_suite(
    *,
    engine: pathlib.Path | str | None,
    out_dir: pathlib.Path | str,
    slots: list[str] | tuple[str, ...] = WW06_SIDECAR_SLOTS,
    timeout_s: float = 30.0,
) -> dict[str, Any]:
    out_path = pathlib.Path(out_dir).resolve()
    out_path.mkdir(parents=True, exist_ok=True)
    engine_bin, engine_meta = resolve_engine_binary(REPO_ROOT, engine)

    started_ms = time.monotonic() * 1000.0
    slot_results: dict[str, dict[str, Any]] = {}
    for slot_id in slots:
        runner = SLOT_RUNNERS[slot_id]
        slot_out = out_path / slot_id
        slot_out.mkdir(parents=True, exist_ok=True)
        res = runner(engine_bin, slot_out, timeout_s)
        slot_results[slot_id] = {
            "slot_id": slot_id,
            "family": res.get("family"),
            "lane": res.get("lane"),
            "verdict": res.get("verdict"),
            "observation_only": bool(res.get("observation_only", False)),
            "linked_milestone": res.get("linked_milestone", ""),
            "checks": res.get("checks", {}),
            "observation": res.get("observation"),
            "errors": res.get("errors", []),
            "evidence_path": str(slot_out / "evidence.json"),
        }

    all_passed = all(r["verdict"] == "pass" for r in slot_results.values())
    summary = {
        "schema_version": 1,
        "card": "WW06",
        "candidate_head": _git_head(),
        "host": _host_identity(),
        "engine_bin": str(engine_bin),
        "engine_meta": engine_meta,
        "elapsed_ms": round(time.monotonic() * 1000.0 - started_ms, 2),
        "all_passed": all_passed,
        "slots": slot_results,
    }
    summary["sha256"] = canonical_sha256(summary)
    _write_json(out_path / "ww06-summary.json", summary)
    return summary


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="suite_ww06",
        description="Run WW06 live sidecar slots (C19 cold restart, C20 Stop fence + D5 observation, C21 budget restart).",
    )
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to built lycaon-dev engine binary.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for per-slot evidence and ww06-summary.json.")
    parser.add_argument("--slot", action="append", choices=WW06_SIDECAR_SLOTS, help="Optional subset of WW06 sidecar slots to run.")
    parser.add_argument("--timeout", type=float, default=30.0, help="Explicit short timer guard per phase in seconds (default: 30).")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    slots = args.slot if args.slot else list(WW06_SIDECAR_SLOTS)
    summary = run_suite(engine=args.engine, out_dir=args.out, slots=slots, timeout_s=args.timeout)
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
