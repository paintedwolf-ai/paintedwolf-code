"""WW07 sidecar qualification suite for person-owned standing watches (C23–C26).

Exercises real `lycaon-dev` sidecar restarts per D4 across:
  - C23-draft-sidecar-restart: standing draft & dismissal survive restart; Keep after restart persists owner
  - C24-calendar-sidecar-restart: calendar standing watch survives restart
  - C25-sidecar-dispatch-restart: create, Keep, due run & idempotent dispatch across sidecar restart
  - C26-lifecycle-budget-sidecar-restart: paused state & spend budget survive restart; resume no catch-up

CLI entrypoint:
  python3 scripts/coordinator-benchmark/wait_watch/suite_ww07.py --engine <lycaon-dev> --out <dir>
"""
from __future__ import annotations

import argparse
import contextlib
import json
import pathlib
import platform
import secrets
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))
if str(REPO_ROOT / "scripts") not in sys.path:
    sys.path.insert(0, str(REPO_ROOT / "scripts"))

from application_environment import application_environment  # noqa: E402
from bootstrap_policy import open_request  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.harness import (  # noqa: E402
    SSECollector,
    create_isolated_session,
)
from portable_command.provenance import resolve_engine_binary  # noqa: E402
from progress import save  # noqa: E402

from wait_watch.barriers import TimerGuard  # noqa: E402
from wait_watch.checker import SCHEMA_VERSION  # noqa: E402
from wait_watch.driver import WaitWatchDriver  # noqa: E402
from wait_watch.observers import (  # noqa: E402
    AdmissionObserver,
    DenObserver,
    RenderedInputObserver,
    SourceObserver,
    StorageObserver,
    canonical_sha256,
)

ALL_SLOTS = (
    "C23-draft-sidecar-restart",
    "C24-calendar-sidecar-restart",
    "C25-sidecar-dispatch-restart",
    "C26-lifecycle-budget-sidecar-restart",
)


def _git_head(root: pathlib.Path = REPO_ROOT) -> str:
    try:
        proc = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=root,
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


def _pick_free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class RestartableSidecar:
    """Manages a `lycaon-dev serve` sidecar that can be cleanly stopped and restarted on the same store.db."""

    def __init__(self, directory: pathlib.Path, engine_bin: pathlib.Path) -> None:
        self.directory = directory
        self.directory.mkdir(parents=True, exist_ok=True)
        self.config = directory / "config"
        self.config.mkdir(exist_ok=True)
        self.engine_bin = engine_bin
        self.token = secrets.token_hex(24)
        self.manual = True
        self.port = 0
        self.url = ""
        self.process: subprocess.Popen[Any] | None = None
        self.log: Any = None
        self.restart_count = 0
        self.start()

    def start(self) -> None:
        self.port = _pick_free_port()
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
                "LYCAON_LLM_MOCK": "1",
                "LYCAON_LLM_MANUAL": "1",
            }
        )
        self.process = subprocess.Popen(
            [str(self.engine_bin), "serve", "--db", str(self.directory / "store.db")],
            cwd=REPO_ROOT,
            env=env,
            stdout=self.log,
            stderr=subprocess.STDOUT,
        )
        save(self.directory / "ownership.json", {"pid": self.process.pid, "port": self.port, "restarts": self.restart_count})

    def stop(self) -> subprocess.Popen[Any] | None:
        proc = self.process
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=10)
        self.process = None
        if self.log is not None:
            try:
                self.log.close()
            except Exception:
                pass
            self.log = None
        return proc

    def restart(self, *, timeout_s: float = 25.0) -> None:
        proc = self.stop()
        db_path = self.directory / "store.db"
        if proc is not None and db_path.is_file():
            checkpoint_closed_store(db_path, proc)
        self.restart_count += 1
        self.start()
        self.ready(timeout_s=timeout_s)

    def close(self) -> None:
        self.stop()

    def request_raw(
        self,
        path: str,
        body: Any = None,
        *,
        method: str | None = None,
    ) -> tuple[int, Any]:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        req = urllib.request.Request(
            self.url + path,
            data=data,
            headers={
                "Authorization": "Bearer " + self.token,
                "Content-Type": "application/json",
            },
            method=method or ("POST" if body is not None else "GET"),
        )
        try:
            with open_request(req) as resp:
                raw = resp.read().decode("utf-8", errors="replace").strip()
                payload = json.loads(raw) if raw else None
                return int(resp.status), payload
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace").strip()
            try:
                payload = json.loads(raw) if raw else None
            except json.JSONDecodeError:
                payload = {"raw": raw}
            return int(exc.code), payload

    def request(self, path: str, body: Any = None, missing: bool = False, method: str | None = None) -> Any:
        status, payload = self.request_raw(path, body, method=method)
        if status == 404 and missing:
            return None
        if status >= 400:
            raise RuntimeError(f"HTTP {status} {method or ('POST' if body is not None else 'GET')} {path}: {json.dumps(payload)[:500]}")
        return payload

    def wait_for(self, observe: Any, phase: str, session: str | None = None, timeout: float = 30.0) -> Any:
        deadline = time.monotonic() + timeout
        while True:
            if self.process is None or self.process.poll() is not None:
                raise RuntimeError(f"sidecar exited during {phase}")
            if time.monotonic() > deadline:
                raise RuntimeError(f"{phase}: timed out after {timeout}s")
            val = observe()
            if val:
                return val
            time.sleep(0.2)

    def ready(self, *, timeout_s: float = 30.0) -> None:
        def healthy() -> bool:
            try:
                status, _ = self.request_raw("/health", method="GET")
                return status == 200
            except OSError:
                return False

        self.wait_for(healthy, "sidecar_startup", timeout=timeout_s)
        self.request("/harness/llm/auto", {"enabled": False}, method="POST")

    def dump_diagnostics(self, session_id: str | None = None, sse_path: pathlib.Path | None = None) -> dict[str, Any]:
        diag: dict[str, Any] = {"session_id": session_id or "", "restart_count": self.restart_count}
        try:
            diag["storage"] = StorageObserver(self.directory / "store.db").snapshot(session_id)
        except Exception as exc:
            diag["storage_error"] = str(exc)
        if sse_path is not None and sse_path.is_file():
            diag["sse_tail"] = sse_path.read_text(encoding="utf-8", errors="replace").splitlines()[-20:]
        return diag


def _query_standing_rows(db_path: pathlib.Path, watch_id: str | None = None) -> dict[str, Any]:
    """Read-only SQLite audit of standing watch tables."""
    uri = db_path.resolve().as_uri() + "?mode=ro"
    out: dict[str, Any] = {}
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as conn:
        conn.row_factory = sqlite3.Row
        if watch_id:
            regs = conn.execute("SELECT * FROM watch_registrations WHERE watch_id = ?", (watch_id,)).fetchall()
            settings = conn.execute("SELECT * FROM standing_watch_settings WHERE watch_id = ?", (watch_id,)).fetchall()
            runs = conn.execute("SELECT * FROM watch_runs WHERE watch_id = ? ORDER BY created_at ASC", (watch_id,)).fetchall()
        else:
            regs = conn.execute("SELECT * FROM watch_registrations ORDER BY created_at ASC").fetchall()
            settings = conn.execute("SELECT * FROM standing_watch_settings").fetchall()
            runs = conn.execute("SELECT * FROM watch_runs ORDER BY created_at ASC").fetchall()
        out["watch_registrations"] = [dict(r) for r in regs]
        out["standing_watch_settings"] = [dict(r) for r in settings]
        out["watch_runs"] = [dict(r) for r in runs]
    return out


def _attach_project(app: RestartableSidecar, project_dir: pathlib.Path) -> str:
    project_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW07 Standing Watch Sidecar Fixture\n", encoding="utf-8")
    attached = app.request("/v1/projects", {"roots": [{"path": str(project_dir)}]}, method="POST")
    return str(attached["id"])


def _build_slot_bundle(
    *,
    slot_id: str,
    run_id: str,
    passed: bool,
    checks: dict[str, Any],
    source_obs: SourceObserver,
    storage_snap: dict[str, Any],
    admission_obs: AdmissionObserver,
    rendered_obs: RenderedInputObserver,
    error: str | None = None,
) -> dict[str, Any]:
    host = _host_identity()
    bundle: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "lane": "sidecar",
        "clock_domain": "system",
        "case_id": slot_id,
        "candidate_head": _git_head(REPO_ROOT),
        "host": host,
        "status": "passed" if passed else "failed",
        "error": error,
        "checks": checks,
        "expectations": {"expected_outcome": "pass", "slot_id": slot_id},
        "observers": {
            "source": source_obs.snapshot(),
            "storage": storage_snap,
            "admission": admission_obs.snapshot(),
            "rendered_input": rendered_obs.snapshot(),
            "den": DenObserver(status="not_run", os_name=host["os"]).snapshot(),
        },
        "sha256_digests": {},
    }
    bundle["sha256_digests"]["bundle_payload"] = canonical_sha256(bundle["checks"])
    return bundle


def run_c23_draft_sidecar_restart(
    out_dir: pathlib.Path, engine_bin: pathlib.Path, *, timeout_s: float
) -> dict[str, Any]:
    """C23-draft-sidecar-restart: standing draft & dismissal survive restart; Keep after restart persists owner."""
    slot_id = "C23-draft-sidecar-restart"
    app_dir = out_dir / "app"
    project_dir = out_dir / "project"
    raw_dir = out_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW07 C23 Draft Sidecar Fixture\n", encoding="utf-8")
    raw_dir.mkdir(parents=True, exist_ok=True)

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    checks: dict[str, Any] = {}
    app = RestartableSidecar(app_dir, engine_bin)
    sse: SSECollector | None = None
    session_id = ""

    try:
        app.ready(timeout_s=timeout_s)
        sse_path = raw_dir / "events.jsonl"
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()

        session_id, project_id, _, _ = create_isolated_session(
            app, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        sub_id = str(uuid.uuid4())
        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": sub_id, "text": "Propose two standing watch drafts."},
            method="POST",
        )
        driver = WaitWatchDriver(
            app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path
        )

        # 1. Agent proposes Draft A (to dismiss) and Draft B (to Keep after restart).
        _, _, draft_a = driver.call_watch(
            {
                "conditions": [{"kind": "timer", "calendar": {"days": ["weekdays"], "times": ["09:00"]}, "time_zone": "UTC"}],
                "repeat": True,
                "standing": {"name": "WW07 Draft A Dismiss", "prompt": "Check morning build."},
            }
        )
        handle_a = str(draft_a.get("handle") or "")
        if not handle_a or not draft_a.get("standing_draft"):
            raise RuntimeError(f"Draft A did not return standing_draft=true: {draft_a}")

        _, _, draft_b = driver.call_watch(
            {
                "conditions": [{"kind": "timer", "calendar": {"days": ["weekdays"], "times": ["11:00"]}, "time_zone": "UTC"}],
                "repeat": True,
                "standing": {"name": "WW07 Draft B Keep", "prompt": "Check midday status."},
            }
        )
        handle_b = str(draft_b.get("handle") or "")
        if not handle_b or not draft_b.get("standing_draft"):
            raise RuntimeError(f"Draft B did not return standing_draft=true: {draft_b}")

        # 2. Person dismisses Draft A before restart.
        dismiss_resp = app.request(f"/v1/sessions/{session_id}/watches/{handle_a}/cancel", method="POST")
        checks["draft_a_dismissed_before_restart"] = dismiss_resp.get("outcome") == "dismissed"
        source_obs.record_event("draft_a_dismissed", handle=handle_a, outcome=dismiss_resp.get("outcome"))

        # 3. Real sidecar restart (D4).
        sse.close()
        sse = None
        app.restart(timeout_s=timeout_s)
        source_obs.record_event("sidecar_restarted", restart_count=app.restart_count)

        # 4. After restart, Draft A remains dismissed and Draft B remains live & keepable.
        after_a = app.request(f"/v1/sessions/{session_id}/watches/{handle_a}", method="GET")
        after_b = app.request(f"/v1/sessions/{session_id}/watches/{handle_b}", method="GET")
        checks["dismissal_survived_restart"] = after_a.get("state") in {"canceled", "dismissed", "ended"} or after_a.get("outcome") == "dismissed"
        checks["draft_b_survived_restart"] = after_b.get("state") == "live"

        # 5. Keep Draft B after restart -> persists owner_person_id and standing watch.
        keep_resp = app.request(
            f"/v1/sessions/{session_id}/watches/{handle_b}/keep",
            {"allow_unpriced": True},
            method="POST",
        )
        sw = keep_resp.get("standing_watch") or {}
        watch_id = str(sw.get("watch_id") or "")
        owner_id = str(sw.get("owner_person_id") or "")
        checks["keep_after_restart_persisted_owner"] = bool(watch_id and owner_id and sw.get("state") == "enabled")
        checks["keep_after_restart_watch_id"] = watch_id
        checks["keep_after_restart_owner"] = owner_id

        # 6. Verify SQLite directly.
        db_rows = _query_standing_rows(app_dir / "store.db", watch_id)
        checks["sqlite_owner_matches"] = (
            len(db_rows["watch_registrations"]) == 1
            and db_rows["watch_registrations"][0].get("owner_kind") == "person"
            and db_rows["watch_registrations"][0].get("owner_person_id") == owner_id
        )

        passed = all(
            bool(checks[k])
            for k in (
                "draft_a_dismissed_before_restart",
                "dismissal_survived_restart",
                "draft_b_survived_restart",
                "keep_after_restart_persisted_owner",
                "sqlite_owner_matches",
            )
        )
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(session_id)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=passed,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
        )
    except Exception as exc:
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(session_id or None)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=False,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
            error=str(exc),
        )
    finally:
        if sse is not None:
            sse.close()
        app.close()


def run_c24_calendar_sidecar_restart(
    out_dir: pathlib.Path, engine_bin: pathlib.Path, *, timeout_s: float
) -> dict[str, Any]:
    """C24-calendar-sidecar-restart: calendar standing watch survives sidecar restart with schedule intact."""
    slot_id = "C24-calendar-sidecar-restart"
    app_dir = out_dir / "app"
    project_dir = out_dir / "project"
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    checks: dict[str, Any] = {}
    app = RestartableSidecar(app_dir, engine_bin)

    try:
        app.ready(timeout_s=timeout_s)
        project_id = _attach_project(app, project_dir)

        created = app.request(
            f"/v1/projects/{project_id}/watches",
            {
                "name": "WW07 Weekday Calendar",
                "prompt": "Review weekday commits.",
                "time_zone": "America/New_York",
                "allow_unpriced": True,
                "daily_run_cap": 6,
                "conditions": [
                    {
                        "kind": "timer",
                        "calendar": {"days": ["weekdays"], "times": ["09:00"]},
                    }
                ],
            },
            method="POST",
        )
        watch_id = str(created.get("watch_id") or "")
        next_runs_before = created.get("next_runs") or []
        checks["created_calendar_watch"] = bool(watch_id and created.get("state") == "enabled" and len(next_runs_before) >= 1)
        source_obs.record_event("calendar_watch_created", watch_id=watch_id, next_runs=next_runs_before)

        # Real sidecar restart (D4).
        app.restart(timeout_s=timeout_s)
        source_obs.record_event("sidecar_restarted", restart_count=app.restart_count)

        fetched = app.request(f"/v1/projects/{project_id}/watches/{watch_id}", method="GET")
        sw = fetched.get("standing_watch") or fetched
        next_runs_after = sw.get("next_runs") or []
        checks["calendar_survived_restart"] = (
            sw.get("watch_id") == watch_id
            and sw.get("state") == "enabled"
            and sw.get("time_zone") == "America/New_York"
            and next_runs_after == next_runs_before
        )
        db_rows = _query_standing_rows(app_dir / "store.db", watch_id)
        checks["sqlite_calendar_persisted"] = (
            len(db_rows["standing_watch_settings"]) == 1
            and db_rows["standing_watch_settings"][0].get("time_zone") == "America/New_York"
        )

        passed = all(
            bool(checks[k])
            for k in ("created_calendar_watch", "calendar_survived_restart", "sqlite_calendar_persisted")
        )
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(None)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=passed,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
        )
    except Exception as exc:
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(None)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=False,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
            error=str(exc),
        )
    finally:
        app.close()


def run_c25_sidecar_dispatch_restart(
    out_dir: pathlib.Path, engine_bin: pathlib.Path, *, timeout_s: float
) -> dict[str, Any]:
    """C25-sidecar-dispatch-restart: create, Keep, due/manual run & idempotent dispatch across sidecar restart."""
    slot_id = "C25-sidecar-dispatch-restart"
    app_dir = out_dir / "app"
    project_dir = out_dir / "project"
    raw_dir = out_dir / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW07 C23 Draft Sidecar Fixture\n", encoding="utf-8")
    raw_dir.mkdir(parents=True, exist_ok=True)
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    checks: dict[str, Any] = {}
    app = RestartableSidecar(app_dir, engine_bin)
    sse: SSECollector | None = None
    session_id = ""

    try:
        app.ready(timeout_s=timeout_s)
        sse_path = raw_dir / "events.jsonl"
        sse = SSECollector(app.url, app.token, sse_path)
        sse.start()

        session_id, project_id, _, _ = create_isolated_session(
            app, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        sub_id = str(uuid.uuid4())
        app.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": sub_id, "text": "Propose a standing watch for dispatch test."},
            method="POST",
        )
        driver = WaitWatchDriver(
            app, session_id, rendered_observer=rendered_obs, source_observer=source_obs, timeout_s=timeout_s, sse_path=sse_path
        )

        # 1. Agent proposes standing draft; person Keeps it.
        _, _, draft = driver.call_watch(
            {
                "conditions": [{"kind": "timer", "delay_ms": 30 * 60 * 1000}],
                "repeat": True,
                "standing": {"name": "WW07 Dispatch Watch", "prompt": "Report repo health."},
            }
        )
        handle = str(draft.get("handle") or "")
        keep_resp = app.request(
            f"/v1/sessions/{session_id}/watches/{handle}/keep",
            {"allow_unpriced": True},
            method="POST",
        )
        sw = keep_resp.get("standing_watch") or {}
        watch_id = str(sw.get("watch_id") or "")
        checks["kept_standing_watch"] = bool(watch_id and sw.get("state") == "enabled")

        # 2. Trigger a due/manual run via POST /v1/projects/{id}/watches/{watch_id}/run.
        run_resp = app.request(f"/v1/projects/{project_id}/watches/{watch_id}/run", method="POST")
        run_id = str(run_resp.get("run_id") or "")
        run_session_id = str(run_resp.get("session_id") or "")
        checks["run_dispatched"] = bool(run_id and run_session_id and run_resp.get("status") in {"running", "queued", "completed"})
        source_obs.record_event("run_dispatched", watch_id=watch_id, run_id=run_id, run_session_id=run_session_id)

        # 3. Restart sidecar while run is in-flight / just dispatched (D4).
        sse.close()
        sse = None
        app.restart(timeout_s=timeout_s)
        source_obs.record_event("sidecar_restarted", restart_count=app.restart_count)

        # 4. Verify idempotent dispatch: exactly 1 run row & single run chat after restart.
        runs_resp = app.request(f"/v1/projects/{project_id}/watches/{watch_id}/runs?limit=10", method="GET")
        runs = runs_resp.get("runs") or []
        checks["single_run_after_restart"] = len(runs) == 1 and runs[0].get("run_id") == run_id
        checks["single_run_chat_preserved"] = len(runs) == 1 and runs[0].get("session_id") == run_session_id

        db_rows = _query_standing_rows(app_dir / "store.db", watch_id)
        checks["sqlite_single_run_row"] = len(db_rows["watch_runs"]) == 1

        passed = all(
            bool(checks[k])
            for k in (
                "kept_standing_watch",
                "run_dispatched",
                "single_run_after_restart",
                "single_run_chat_preserved",
                "sqlite_single_run_row",
            )
        )
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(session_id)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=passed,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
        )
    except Exception as exc:
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(session_id or None)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=False,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
            error=str(exc),
        )
    finally:
        if sse is not None:
            sse.close()
        app.close()


def run_c26_lifecycle_budget_sidecar_restart(
    out_dir: pathlib.Path, engine_bin: pathlib.Path, *, timeout_s: float
) -> dict[str, Any]:
    """C26-lifecycle-budget-sidecar-restart: paused state & spend budget survive restart; resume no catch-up."""
    slot_id = "C26-lifecycle-budget-sidecar-restart"
    app_dir = out_dir / "app"
    project_dir = out_dir / "project"
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    checks: dict[str, Any] = {}
    app = RestartableSidecar(app_dir, engine_bin)

    try:
        app.ready(timeout_s=timeout_s)
        project_id = _attach_project(app, project_dir)

        created = app.request(
            f"/v1/projects/{project_id}/watches",
            {
                "name": "WW07 Lifecycle Budget Watch",
                "prompt": "Check spend budget lifecycle.",
                "run_spend_limit_nano_usd": 1_500_000_000,
                "daily_run_cap": 3,
                "allow_unpriced": True,
                "time_zone": "UTC",
                "conditions": [{"kind": "timer", "every_minutes": 15}],
            },
            method="POST",
        )
        watch_id = str(created.get("watch_id") or "")
        rev1 = int(created.get("revision") or 0)
        checks["created_with_budget"] = (
            bool(watch_id)
            and created.get("daily_run_cap") == 3
            and created.get("run_spend_limit_nano_usd") == 1_500_000_000
        )

        # Pause before restart.
        paused = app.request(
            f"/v1/projects/{project_id}/watches/{watch_id}/pause",
            {"revision": rev1},
            method="POST",
        )
        rev2 = int(paused.get("revision") or 0)
        checks["paused_before_restart"] = paused.get("state") == "paused" and rev2 == rev1 + 1

        # Real sidecar restart (D4).
        app.restart(timeout_s=timeout_s)
        source_obs.record_event("sidecar_restarted", restart_count=app.restart_count)

        # Verify paused state and spend budget survived restart.
        fetched = app.request(f"/v1/projects/{project_id}/watches/{watch_id}", method="GET")
        sw = fetched.get("standing_watch") or fetched
        checks["paused_state_survived_restart"] = sw.get("state") == "paused" and int(sw.get("revision") or 0) == rev2
        checks["spend_budget_survived_restart"] = (
            sw.get("daily_run_cap") == 3
            and sw.get("run_spend_limit_nano_usd") == 1_500_000_000
        )

        # Resume after restart -> transitions to enabled, zero catch-up runs dispatched.
        resumed = app.request(
            f"/v1/projects/{project_id}/watches/{watch_id}/resume",
            {"revision": rev2},
            method="POST",
        )
        checks["resumed_after_restart"] = resumed.get("state") == "enabled" and int(resumed.get("revision") or 0) == rev2 + 1

        runs_after = app.request(f"/v1/projects/{project_id}/watches/{watch_id}/runs?limit=10", method="GET")
        checks["resume_no_catchup"] = len(runs_after.get("runs") or []) == 0

        passed = all(
            bool(checks[k])
            for k in (
                "created_with_budget",
                "paused_before_restart",
                "paused_state_survived_restart",
                "spend_budget_survived_restart",
                "resumed_after_restart",
                "resume_no_catchup",
            )
        )
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(None)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=passed,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
        )
    except Exception as exc:
        storage_snap = StorageObserver(app_dir / "store.db").snapshot(None)
        return _build_slot_bundle(
            slot_id=slot_id,
            run_id=f"ww07-{slot_id}-{uuid.uuid4().hex[:8]}",
            passed=False,
            checks=checks,
            source_obs=source_obs,
            storage_snap=storage_snap,
            admission_obs=admission_obs,
            rendered_obs=rendered_obs,
            error=str(exc),
        )
    finally:
        app.close()


SLOT_RUNNERS = {
    "C23-draft-sidecar-restart": run_c23_draft_sidecar_restart,
    "C24-calendar-sidecar-restart": run_c24_calendar_sidecar_restart,
    "C25-sidecar-dispatch-restart": run_c25_sidecar_dispatch_restart,
    "C26-lifecycle-budget-sidecar-restart": run_c26_lifecycle_budget_sidecar_restart,
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="suite_ww07",
        description="WW07 sidecar qualification suite for person-owned standing watches (C23–C26).",
    )
    parser.add_argument("--engine", type=pathlib.Path, default=None, help="Path to built lycaon-dev binary.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for per-slot evidence and ww07-summary.json.")
    parser.add_argument(
        "--slots",
        nargs="+",
        choices=ALL_SLOTS,
        default=list(ALL_SLOTS),
        help="Subset of WW07 sidecar slots to execute.",
    )
    parser.add_argument("--timeout", type=float, default=30.0, help="Per-phase timeout in seconds (default: 30).")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    out_root = args.out.resolve()
    out_root.mkdir(parents=True, exist_ok=True)
    engine_bin, engine_meta = resolve_engine_binary(REPO_ROOT, args.engine)

    results: list[dict[str, Any]] = []
    all_passed = True
    for slot_id in args.slots:
        slot_dir = out_root / slot_id
        slot_dir.mkdir(parents=True, exist_ok=True)
        bundle = SLOT_RUNNERS[slot_id](slot_dir, engine_bin, timeout_s=args.timeout)
        evidence_path = slot_dir / "evidence.json"
        evidence_path.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        passed = bundle.get("status") == "passed"
        if not passed:
            all_passed = False
        results.append(
            {
                "slot_id": slot_id,
                "status": bundle.get("status"),
                "error": bundle.get("error"),
                "checks": bundle.get("checks"),
                "evidence_path": str(evidence_path),
            }
        )

    summary = {
        "schema_version": SCHEMA_VERSION,
        "card": "WW07",
        "candidate_head": _git_head(REPO_ROOT),
        "host": _host_identity(),
        "engine_bin": str(engine_bin),
        "engine_meta": engine_meta,
        "all_passed": all_passed,
        "slots": results,
    }
    summary_path = out_root / "ww07-summary.json"
    summary_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if all_passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
