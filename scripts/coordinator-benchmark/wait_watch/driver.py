"""Sidecar driver and replay runner for wait/watch bug-bash cases (extending smoke.py & portable_command)."""
from __future__ import annotations

import contextlib
import hashlib
import json
import os
import pathlib
import platform
import re
import socket
import sqlite3
import subprocess
import sys
import time
import uuid
from typing import Any, Callable

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

import smoke  # noqa: E402
import smoke_workers  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.handle_await import parse_content_json  # noqa: E402
from portable_command.harness import (  # noqa: E402
    SSECollector,
    create_isolated_session,
    wait_tool_result,
)
from portable_command.provenance import resolve_engine_binary  # noqa: E402
from progress import save  # noqa: E402

from wait_watch.barriers import BarrierTimeoutError, ProcessBarrier, TimerGuard  # noqa: E402
from wait_watch.checker import SCHEMA_VERSION, check_bundle  # noqa: E402
from wait_watch.observers import (  # noqa: E402
    AdmissionObserver,
    DenObserver,
    RenderedInputObserver,
    SourceObserver,
    StorageObserver,
    canonical_sha256,
    correlate_observers,
    parse_sidecar_phase_records,
)

SUPPORTED_CASES = ("c01-fast-exit", "c01-parked-exit")


def _make_wait_watch_app_factory(
    engine_bin: pathlib.Path | str | None,
    base_factory: Callable[..., Any],
) -> Callable[[pathlib.Path], Any]:
    """Honor --engine / PW_ENGINE_BIN for smoke.Application and its subclasses (e.g. WaitWatchApplication)."""
    if not (isinstance(base_factory, type) and issubclass(base_factory, smoke.Application)):
        return base_factory
    binary, _ = resolve_engine_binary(REPO_ROOT, engine_bin)

    def factory(directory: pathlib.Path) -> Any:
        orig = smoke.engine_path
        smoke.engine_path = lambda _root: binary
        try:
            return base_factory(directory)
        finally:
            smoke.engine_path = orig

    return factory


class WaitWatchApplication(smoke.Application):
    """Thin extension of smoke.Application for wait/watch sidecar runs."""

    def dump_diagnostics(self, session_id: str | None, sse_path: pathlib.Path | None = None) -> dict[str, Any]:
        diag: dict[str, Any] = {"session_id": session_id or ""}
        try:
            diag["pending_llm"] = self.request("/harness/llm/pending") if self.manual else {}
        except Exception as exc:
            diag["pending_llm_error"] = str(exc)
        try:
            diag["storage"] = StorageObserver(self.directory / "store.db").snapshot(session_id)
        except Exception as exc:
            diag["storage_error"] = str(exc)
        if sse_path is not None and sse_path.is_file():
            lines = sse_path.read_text(encoding="utf-8", errors="replace").splitlines()
            diag["sse_tail"] = lines[-20:]
        return diag


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


def _extract_handle(tool_result: dict[str, Any]) -> str:
    payload = parse_content_json(tool_result)
    bg_jobs = payload.get("background_jobs")
    if isinstance(bg_jobs, list):
        for job in bg_jobs:
            if isinstance(job, dict) and isinstance(job.get("handle"), str) and job["handle"].strip():
                return job["handle"].strip()
    for key in ("handle", "job_handle"):
        val = payload.get(key)
        if isinstance(val, str) and val.strip():
            return val.strip()
    data = tool_result.get("data")
    if isinstance(data, dict) and isinstance(data.get("handle"), str) and data["handle"].strip():
        return data["handle"].strip()
    if isinstance(tool_result.get("handle"), str) and tool_result["handle"].strip():
        return tool_result["handle"].strip()
    return ""


def _extract_exit_code_from_text(text: str) -> int | None:
    m = re.search(r"\bexit_code=(-?\d+)\b", text)
    if m:
        return int(m.group(1))
    m2 = re.search(r'"exit_code"\s*:\s*(-?\d+)', text)
    if m2:
        return int(m2.group(1))
    return None


def _extract_handle_from_text(text: str) -> str:
    m = re.search(r"\bhandle=([^\s,;\]]+)", text)
    if m:
        return m.group(1).strip()
    m2 = re.search(r'"handle"\s*:\s*"([^"]+)"', text)
    if m2:
        return m2.group(1).strip()
    return ""


def _extract_report_from_pending(pending_payload: dict[str, Any], expected_handle: str = "") -> tuple[str, int | None, str]:
    """Extract the terminal completion report, exit code, and handle actually observed in /harness/llm/pending."""
    messages = pending_payload.get("messages") or []
    for msg in reversed(messages):
        content = msg.get("content")
        if not isinstance(content, str):
            continue
        obs_handle = _extract_handle_from_text(content)
        exit_code = _extract_exit_code_from_text(content)
        if obs_handle or exit_code is not None or "termination=" in content or "process_done" in content:
            if expected_handle and not obs_handle and expected_handle in content:
                obs_handle = expected_handle
            return content, exit_code, obs_handle
    return "", None, ""


def _extract_report_from_wait_result(wait_result: dict[str, Any]) -> tuple[str, int | None, str]:
    payload = parse_content_json(wait_result)
    conditions = payload.get("conditions") or []
    for cond in conditions:
        if not isinstance(cond, dict):
            continue
        handles = cond.get("handles") or []
        obs_handle = str(handles[0]) if handles else ""
        report = cond.get("report") or ""
        if report:
            if not obs_handle:
                obs_handle = _extract_handle_from_text(str(report))
            return str(report), _extract_exit_code_from_text(str(report)), obs_handle
    raw_content = wait_result.get("content") or wait_result.get("result") or ""
    if isinstance(raw_content, str) and raw_content.strip():
        return raw_content, _extract_exit_code_from_text(raw_content), _extract_handle_from_text(raw_content)
    return "", None, ""


def _observe_product_identities(storage_snap: dict[str, Any]) -> tuple[dict[str, Any], dict[str, str], list[str]]:
    """Correlate actual product-exposed IDs from SQLite records (never fabricate expected IDs)."""
    tables = storage_snap.get("tables") or {}
    ids: dict[str, Any] = {
        "invocation_id": "",
        "occurrence_id": "",
        "claim_id": "",
        "program_id": "",
        "lease_id": "",
        "exit_code": None,
    }
    refs: dict[str, str] = {}
    limitations: list[str] = []

    programs = tables.get("invocation_programs") or []
    if programs:
        prog = programs[-1]
        if prog.get("id"):
            ids["program_id"] = str(prog["id"])
            refs["program_id"] = f"storage:invocation_programs:{prog['id']}"
        if isinstance(prog.get("status_code"), int):
            ids["exit_code"] = prog["status_code"]
            refs["storage_exit_code"] = f"storage:invocation_programs:{prog.get('id', '')}.status_code"

    leases = tables.get("wait_leases") or []
    if leases:
        lease = leases[-1]
        if lease.get("id"):
            ids["lease_id"] = str(lease["id"])
            refs["lease_id"] = f"storage:wait_leases:{lease['id']}"
        if lease.get("tool_call_id"):
            ids["invocation_id"] = str(lease["tool_call_id"])
            refs["invocation_id"] = f"storage:wait_leases:{lease.get('id', '')}.tool_call_id"

    if not ids["invocation_id"]:
        receipts = [r for r in (tables.get("invocation_receipts") or []) if r.get("tool_name") == "wait"]
        if receipts and receipts[-1].get("tool_call_id"):
            ids["invocation_id"] = str(receipts[-1]["tool_call_id"])
            refs["invocation_id"] = f"storage:invocation_receipts:{receipts[-1].get('id', '')}.tool_call_id"

    matches = tables.get("watch_matches") or []
    if matches and matches[-1].get("source_occurrence_id"):
        ids["occurrence_id"] = str(matches[-1]["source_occurrence_id"])
        refs["occurrence_id"] = f"storage:watch_matches:{matches[-1].get('match_id', '')}.source_occurrence_id"
    elif ids["program_id"]:
        # Direct wait(process_done) without a watch registration uses the program record as occurrence anchor.
        ids["occurrence_id"] = ids["program_id"]
        refs["occurrence_id"] = f"storage:invocation_programs:{ids['program_id']}"
    else:
        limitations.append("product_occurrence_id_not_exposed_in_storage")

    claims = tables.get("watch_presentation_claims") or []
    if claims and claims[-1].get("claim_id"):
        ids["claim_id"] = str(claims[-1]["claim_id"])
        refs["claim_id"] = f"storage:watch_presentation_claims:{claims[-1]['claim_id']}"
    elif ids["lease_id"]:
        ids["claim_id"] = ids["lease_id"]
        refs["claim_id"] = f"storage:wait_leases:{ids['lease_id']}"
    else:
        limitations.append("product_claim_id_not_exposed_in_storage")

    return ids, refs, limitations


def _observe_report_publication(
    sidecar_log: pathlib.Path | str,
    *,
    session_id: str,
    handle: str,
) -> dict[str, Any] | None:
    """Independently observe the authoritative `phase=process_report_published` record from sidecar.log.

    Never accepts a generic terminal program row, resolved lease, or generic finished SSE frame
    as report publication. Returns None if the authoritative phase record or its orderable timestamp
    is absent (causing the checker to emit verdict='unknown').
    """
    if not session_id or not handle:
        return None
    records = parse_sidecar_phase_records(sidecar_log, session_id=session_id, handle=handle)
    pub_records = [r for r in records if r.get("phase") == "process_report_published"]
    if not pub_records:
        return None
    rec = pub_records[-1]
    if rec.get("recorded_at_ns") is None:
        return None
    return {
        "observed": True,
        "phase": rec["phase"],
        "session_id": rec["session_id"],
        "handle": rec["handle"],
        "evidence_ref": rec["evidence_ref"],
        "line_no": rec["line_no"],
        "recorded_at": rec["recorded_at"],
        "published_at_ns": rec["recorded_at_ns"],
    }


class WaitWatchDriver:
    """Drives command, wait, watch, and watch_control tool calls while recording all 5 observers."""

    def __init__(
        self,
        application: Any,
        session_id: str,
        *,
        rendered_observer: RenderedInputObserver,
        source_observer: SourceObserver,
        timeout_s: float = 20.0,
        sse_path: pathlib.Path | None = None,
    ) -> None:
        self.app = application
        self.session_id = session_id
        self.rendered = rendered_observer
        self.source = source_observer
        self.timeout_s = float(timeout_s)
        self.sse_path = sse_path
        self.tool_history: list[dict[str, Any]] = []
        self._event_seq = 0

    def _next_seq(self) -> int:
        self._event_seq += 1
        return self._event_seq

    def _dump_state(self) -> dict[str, Any]:
        if hasattr(self.app, "dump_diagnostics"):
            return self.app.dump_diagnostics(self.session_id, self.sse_path)
        return {"session_id": self.session_id}

    def await_pending(self, phase: str) -> dict[str, Any]:
        guard = TimerGuard(
            self.timeout_s,
            phase=phase,
            verdict="incomplete",
            dump_state=self._dump_state,
        )
        return guard.poll(lambda: smoke_workers.pending(self.app, self.session_id), detail=phase)

    def send_calls(
        self,
        calls: list[dict[str, Any]],
        *,
        phase: str = "tool_calls",
        handle: str = "",
        occurrence_id: str = "",
        claim_id: str = "",
        record_presentation: bool = False,
    ) -> tuple[dict[str, Any], list[tuple[str, dict[str, Any]]]]:
        """Capture /harness/llm/pending before responding, auto-load missing tools, and await tool results."""
        req = self.await_pending(f"{phase}:pending")
        names = [c["name"] for c in calls]
        missing = sorted({n for n in names if n not in (req.get("tools") or ())})
        if missing and "request_tools" in (req.get("tools") or ()):
            load_id = str(uuid.uuid4())
            self.app.request(
                "/harness/llm/respond",
                {
                    "id": req["id"],
                    "tool_calls": [
                        {"id": load_id, "name": "request_tools", "args": {"need": ", ".join(missing)}}
                    ],
                },
            )
            wait_tool_result(self.app, self.session_id, load_id)
            req = self.await_pending(f"{phase}:loaded_tools")

        if record_presentation:
            self.rendered.record_pending(
                req,
                presentation_kind="new_model_presentation",
                session_id=self.session_id,
                handle=handle,
                occurrence_id=occurrence_id,
                claim_id=claim_id,
                event_seq=self._next_seq(),
            )

        ids = [str(uuid.uuid4()) for _ in calls]
        self.app.request(
            "/harness/llm/respond",
            {
                "id": req["id"],
                "tool_calls": [{"id": i, **c} for i, c in zip(ids, calls)],
            },
        )
        results = [(i, wait_tool_result(self.app, self.session_id, i)) for i in ids]
        for (cid, res), call in zip(results, calls):
            self.tool_history.append({
                "call_id": cid,
                "tool": call["name"],
                "args": call.get("args") or {},
                "outcome": (res or {}).get("outcome"),
                "parsed": parse_content_json(res),
            })
        return req, results

    def call_command(self, command: str, *, progress_update: str | None = None) -> tuple[str, dict[str, Any], str]:
        args: dict[str, Any] = {"command": command}
        if progress_update:
            args["progress_update"] = {"content": progress_update}
        _, results = self.send_calls(
            [{"name": "command", "args": args}],
            phase="command",
        )
        cid, res = results[0]
        return cid, res, _extract_handle(res)

    def call_wait(
        self,
        conditions: list[dict[str, Any]],
        *,
        until_complete: bool = True,
        timeout_ms: int = 30000,
        reason: str = "Wait for fixture condition",
    ) -> tuple[str, dict[str, Any], dict[str, Any]]:
        args: dict[str, Any] = {
            "conditions": conditions,
            "until_complete": until_complete,
            "timeout_ms": timeout_ms,
            "reason": reason,
        }
        _, results = self.send_calls([{"name": "wait", "args": args}], phase="wait")
        cid, res = results[0]
        return cid, res, parse_content_json(res)

    def call_watch(self, args: dict[str, Any]) -> tuple[str, dict[str, Any], dict[str, Any]]:
        _, results = self.send_calls([{"name": "watch", "args": args}], phase="watch")
        cid, res = results[0]
        return cid, res, parse_content_json(res)

    def call_watch_control(self, args: dict[str, Any]) -> tuple[str, dict[str, Any], dict[str, Any]]:
        _, results = self.send_calls([{"name": "watch_control", "args": args}], phase="watch_control")
        cid, res = results[0]
        return cid, res, parse_content_json(res)

    def finish_turn(self, pending_req: dict[str, Any], text: str = "Observed terminal receipt.") -> None:
        self.app.request("/harness/llm/respond", {"id": pending_req["id"], "content": text})


def _confirm_process_persisted_before_wait(
    db_path: pathlib.Path,
    session_id: str,
    handle: str,
    timeout_s: float,
    dump_state: Callable[[], dict[str, Any]],
) -> dict[str, Any]:
    """Wait until the barrier command has exited and its report/program row is persisted in SQLite."""
    guard = TimerGuard(
        timeout_s,
        phase="c01.confirm_exited_before_wait",
        verdict="incomplete",
        dump_state=dump_state,
    )

    def check_db() -> dict[str, Any] | None:
        snap = StorageObserver(db_path).snapshot(session_id)
        progs = snap.get("tables", {}).get("invocation_programs") or []
        for p in progs:
            state = p.get("state") or ""
            status_code = p.get("status_code")
            if state in {"completed", "failed", "exited"} or status_code == 17:
                return {"program": p, "storage_counts": snap.get("counts", {})}
        # Fallback: check if invocation_receipts has a completed/failed receipt for the command.
        receipts = snap.get("tables", {}).get("invocation_receipts") or []
        for r in receipts:
            if r.get("tool_name") == "command" and r.get("status") in {"completed", "failed"}:
                return {"receipt": r, "storage_counts": snap.get("counts", {})}
        return None

    return guard.poll(check_db, detail=f"process {handle} exit not persisted before wait")


def _confirm_parked_lease(
    db_path: pathlib.Path,
    sse_path: pathlib.Path,
    session_id: str,
    timeout_s: float,
    dump_state: Callable[[], dict[str, Any]],
) -> dict[str, Any]:
    """Confirm parked state via independent storage (wait_leases status='armed') or SSE wait-lease frame."""
    guard = TimerGuard(
        timeout_s,
        phase="c01.confirm_parked_lease",
        verdict="incomplete",
        dump_state=dump_state,
    )

    def check_parked() -> dict[str, Any] | None:
        snap = StorageObserver(db_path).snapshot(session_id)
        leases = snap.get("tables", {}).get("wait_leases") or []
        armed = [l for l in leases if l.get("status") == "armed"]
        if armed:
            return {"source": "storage", "lease": armed[-1]}
        if sse_path.is_file():
            for line in sse_path.read_text(encoding="utf-8", errors="replace").splitlines():
                if "wait_lease" in line and '"active":true' in line.replace(" ", ""):
                    return {"source": "sse", "frame": line}
        return None

    return guard.poll(check_parked, detail="armed wait_lease not observed in storage or SSE")


def run_case(
    case_id: str,
    out_dir: pathlib.Path | str,
    *,
    engine_bin: pathlib.Path | str | None = None,
    seed: int = 1,
    timeout_s: float = 25.0,
    export_path: pathlib.Path | str | None = None,
    app_factory: Callable[..., Any] = WaitWatchApplication,
    sse_factory: Callable[..., Any] = SSECollector,
    replayed_from: dict[str, Any] | None = None,
) -> dict[str, Any]:
    """Run a sidecar wait/watch control case and emit a versioned, replayable evidence bundle."""
    if case_id not in SUPPORTED_CASES:
        raise ValueError(f"unsupported case_id {case_id!r}; expected one of {SUPPORTED_CASES}")

    out_path = pathlib.Path(out_dir).resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    # Barrier files MUST live inside the session workspace write root.
    barrier_dir = project_dir / ".ww02-barrier"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text(f"# Wait/Watch fixture {case_id} seed={seed}\n", encoding="utf-8")

    started_ms = time.monotonic() * 1000.0
    candidate_head = _git_head(REPO_ROOT)
    host = _host_identity()
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-{case_id}-s{seed}-{uuid.uuid4().hex[:8]}"

    marker = f"ww02-{case_id}-seed-{seed}"
    expected_payload = f"{marker}:stdout:ok\n"
    barrier = ProcessBarrier(barrier_dir, exit_code=17, marker=marker, payload=expected_payload)

    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=host["os"])

    application = _make_wait_watch_app_factory(engine_bin, app_factory)(app_dir)
    sse_path = raw_dir / "events.jsonl"
    sse = None
    session_id = ""
    project_id = ""
    submission_id = ""
    handle = ""
    command_call_id = ""
    wait_call_id = ""
    parked_proof: dict[str, Any] = {}
    barrier_error: dict[str, Any] | None = None
    report_pub_fact: dict[str, Any] | None = None
    report_published_ts_ns: int | None = None
    instrumentation_gaps: list[str] = []
    identity_limitations: list[str] = []

    try:
        application.ready()
        sse = sse_factory(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(application, project_dir, {"profile": "implement", "turns": [1, 2, 3]})
        submission_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": submission_id, "text": f"Run wait/watch control {case_id}."},
        )

        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        # 1. Launch exit-17 barrier command in background inside workspace (with progress_update for build posture).
        cmd_str = barrier.command_string(relative_to=project_dir, background=True)
        command_call_id, cmd_res, handle = driver.call_command(
            cmd_str,
            progress_update=f"- [ ] Run wait/watch control {case_id}",
        )
        cmd_outcome = (cmd_res or {}).get("outcome")
        source_obs.record_event("command_launched", handle=handle, call_id=command_call_id, outcome=cmd_outcome)
        if cmd_outcome == "rejected":
            raise BarrierTimeoutError(
                f"command tool rejected barrier launch: {json.dumps(cmd_res)[:500]}",
                phase="command.rejected",
                verdict="incomplete",
                diagnostics={"command_result": cmd_res, **driver._dump_state()},
            )

        # 2. Verify barrier reached.
        reached_info = barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("barrier_reached", handle=handle, sentinel=reached_info)

        if case_id == "c01-fast-exit":
            # Fast-exit: release gate (distinct event from exit/publication).
            rel_info = barrier.release()
            source_obs.record_event("barrier_released", handle=handle, **rel_info)
            child_exited = barrier.wait_child_exited(timeout_s=min(10.0, timeout_s))
            if child_exited:
                source_obs.record_event("process_exited", handle=handle, pid=barrier.child_pid)

            # Independently wait for authoritative `phase=process_report_published` in sidecar.log BEFORE calling wait.
            sidecar_log = app_dir / "sidecar.log"
            pub_guard = TimerGuard(
                min(10.0, timeout_s),
                phase="c01.wait_report_published_phase",
                verdict="incomplete",
                dump_state=driver._dump_state,
            )
            try:
                report_pub_fact = pub_guard.poll(
                    lambda: _observe_report_publication(sidecar_log, session_id=session_id, handle=handle),
                    detail=f"phase=process_report_published for {session_id}/{handle}",
                )
            except BarrierTimeoutError:
                report_pub_fact = None

            if report_pub_fact is not None:
                source_obs.record_event("report_published", **report_pub_fact)
                report_published_ts_ns = report_pub_fact.get("published_at_ns")
            else:
                instrumentation_gaps.append("missing_report_publication_after_release")

            # Call wait after process already exited.
            wait_call_id, wait_res, wait_parsed = driver.call_wait(
                [{"kind": "process_done", "handles": [handle]}],
                until_complete=True,
                timeout_ms=int(timeout_s * 1000),
                reason="C01 fast-exit control",
            )
            # Capture the next pending model turn (which carries the in-turn wait tool result).
            next_req = driver.await_pending("c01-fast-exit:post_wait_pending")
            report_text, observed_exit, observed_handle = _extract_report_from_pending(next_req, expected_handle=handle)
            report_ref = "rendered_input:pending.messages"
            if not report_text:
                w_report, w_exit, w_handle = _extract_report_from_wait_result(wait_res)
                if w_report:
                    report_text = w_report
                    report_ref = f"tool_result:{wait_call_id}"
                if observed_exit is None:
                    observed_exit = w_exit
                if not observed_handle:
                    observed_handle = w_handle

            storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
            prod_ids, prod_refs, prod_lims = _observe_product_identities(storage_now)
            identity_limitations.extend(prod_lims)
            ev_refs = {
                "session_id": "rendered_input:pending.session_id",
                "report": report_ref,
                **prod_refs,
            }
            if observed_handle:
                ev_refs["handle"] = report_ref
            if observed_exit is not None:
                ev_refs["exit_code"] = report_ref
            rendered_obs.record_pending(
                next_req,
                presentation_kind="new_model_presentation",
                handle=observed_handle,
                invocation_id=prod_ids["invocation_id"],
                occurrence_id=prod_ids["occurrence_id"],
                claim_id=prod_ids["claim_id"],
                exit_code=observed_exit,
                report=report_text,
                report_readable=bool(report_text),
                evidence_refs=ev_refs,
            )
            source_obs.record_event("receipt_delivered", handle=observed_handle, request_id=next_req.get("id", ""))
            driver.finish_turn(next_req, f"Fast exit verified for {handle}.")

        elif case_id == "c01-parked-exit":
            # Parked-exit: call wait BEFORE releasing gate.
            wait_call_id, wait_res, wait_parsed = driver.call_wait(
                [{"kind": "process_done", "handles": [handle]}],
                until_complete=True,
                timeout_ms=int(timeout_s * 1000),
                reason="C01 parked-exit control",
            )
            source_obs.record_event(
                "wait_called",
                handle=handle,
                call_id=wait_call_id,
                wait_status=wait_parsed.get("status"),
            )
            # Confirm parked state via independent storage/SSE wait-lease observation.
            parked_proof = _confirm_parked_lease(
                app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state
            )
            source_obs.record_event("parked_lease_confirmed", handle=handle, proof=parked_proof)

            # Release barrier gate while parked (distinct from process exit and report publication!).
            rel_info = barrier.release()
            source_obs.record_event("barrier_released", handle=handle, **rel_info)

            # Wait for resumed /harness/llm/pending carrying the terminal receipt.
            resumed_req = driver.await_pending("c01-parked-exit:resumed_pending")
            report_text, observed_exit, observed_handle = _extract_report_from_pending(resumed_req, expected_handle=handle)

            # Independently observe authoritative `phase=process_report_published` from sidecar.log.
            storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
            sidecar_log = app_dir / "sidecar.log"
            report_pub_fact = _observe_report_publication(sidecar_log, session_id=session_id, handle=handle)
            if report_pub_fact is not None:
                source_obs.record_event("report_published", **report_pub_fact)
                report_published_ts_ns = report_pub_fact.get("published_at_ns")
            else:
                instrumentation_gaps.append("missing_report_publication_after_release")

            prod_ids, prod_refs, prod_lims = _observe_product_identities(storage_now)
            identity_limitations.extend(prod_lims)
            ev_refs = {
                "session_id": "rendered_input:pending.session_id",
                "report": "rendered_input:pending.messages",
                **prod_refs,
            }
            if observed_handle:
                ev_refs["handle"] = "rendered_input:pending.messages"
            if observed_exit is not None:
                ev_refs["exit_code"] = "rendered_input:pending.messages"
            rendered_obs.record_pending(
                resumed_req,
                presentation_kind="new_model_presentation",
                handle=observed_handle,
                invocation_id=prod_ids["invocation_id"],
                occurrence_id=prod_ids["occurrence_id"],
                claim_id=prod_ids["claim_id"],
                exit_code=observed_exit,
                report=report_text,
                report_readable=bool(report_text),
                evidence_refs=ev_refs,
            )
            source_obs.record_event("receipt_delivered", handle=observed_handle, request_id=resumed_req.get("id", ""))
            driver.finish_turn(resumed_req, f"Parked exit verified for {handle}.")

        admission_obs.capture_http(
            application,
            session_id=session_id,
            project_id=project_id,
            submission_ids=[submission_id] if submission_id else [],
        )
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
    admission_obs.load_sse_jsonl(sse_path)
    scoped_phases = admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)

    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    final_prod_ids, final_prod_refs, _ = _observe_product_identities(storage_snap)
    source_facts = {
        **barrier.source_facts(),
        "report_publication": report_pub_fact,
        "report_published_ts_ns": report_published_ts_ns,
        "phase_records": scoped_phases,
        "handle": handle,
        "parked_proof": parked_proof,
        "product_identities": final_prod_ids,
        "product_identity_refs": final_prod_refs,
        "identity_limitations": list(dict.fromkeys(identity_limitations)),
    }
    source_obs.set_facts(source_facts)

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
            "handle": handle,
            "invocation_id": final_prod_ids.get("invocation_id") or wait_call_id or command_call_id,
            "workspace_id": project_id,
            "occurrence_id": final_prod_ids.get("occurrence_id") or "",
            "claim_id": final_prod_ids.get("claim_id") or "",
            "turn": rendered_snap.get("new_model_presentation_count", 0),
            "revision": 1,
            "event_seq": len(source_obs.events),
            "clock_domain": "system",
        },
    )

    replay_inputs = {
        "case_id": case_id,
        "seed": seed,
        "exit_code": 17,
        "marker": marker,
        "expected_payload": expected_payload,
        "expected_payload_sha256": barrier.payload_sha256,
        "timeout_s": timeout_s,
    }
    elapsed_ms = round(time.monotonic() * 1000.0 - started_ms, 2)
    bundle_path = out_path / "evidence-bundle.json"
    replay_command = f"python3 -m wait_watch.cli --replay {bundle_path} --out <fresh-out-dir>"

    owed_occurrences = [final_prod_ids["occurrence_id"]] if final_prod_ids.get("occurrence_id") else []
    bundle: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "candidate_head": candidate_head,
        "host": host,
        "lane": "sidecar",
        "clock_domain": "system",
        "case_id": case_id,
        "fixture": {
            "kind": "process_file_gate",
            "exit_code": 17,
            "marker": marker,
            "expected_payload_sha256": barrier.payload_sha256,
        },
        "seed": seed,
        "elapsed_ms": elapsed_ms,
        "barriers": {
            "process_barrier": barrier.source_facts(),
            "timeout_abort": barrier_error,
        },
        "instrumentation_gaps": list(dict.fromkeys(instrumentation_gaps)),
        "expectations": {
            "expected_outcome": "delivered",
            "owed_recipients": [f"session:{session_id}"] if session_id else [],
            "owed_occurrences": owed_occurrences,
            "expected_handle": handle,
            "expected_invocation_id": wait_call_id or command_call_id,
            "expected_exit_code": 17,
            "require_publication_order": True,
            "require_zero_armed_leases": True,
        },
        "observers": observers,
        "replay_inputs": replay_inputs,
        "replay_command": replay_command,
    }
    if replayed_from is not None:
        bundle["replayed_from"] = replayed_from

    if barrier_error is not None:
        bundle["checker_verdict"] = {
            "schema_version": SCHEMA_VERSION,
            "verdict": barrier_error["verdict"],
            "defects": [],
            "defect_codes": [],
            "missing_instrumentation": [],
            "barrier_timeout": barrier_error,
            "diagnostics": {"elapsed_ms_diagnostic_only": elapsed_ms},
        }
    else:
        bundle["checker_verdict"] = check_bundle(bundle)

    bundle["sha256_digests"] = {
        "source": source_snap["sha256"],
        "storage": storage_snap.get("sha256", ""),
        "admission": admission_snap["sha256"],
        "rendered_input": rendered_snap["sha256"],
        "den": den_snap["sha256"],
        "replay_inputs": canonical_sha256(replay_inputs),
    }

    save(bundle_path, bundle)
    if export_path is not None:
        exp = pathlib.Path(export_path).resolve()
        exp.parent.mkdir(parents=True, exist_ok=True)
        save(exp, bundle)
    return bundle


def replay_bundle(
    bundle_path: pathlib.Path | str,
    out_dir: pathlib.Path | str,
    *,
    engine_bin: pathlib.Path | str | None = None,
    app_factory: Callable[..., Any] = WaitWatchApplication,
    sse_factory: Callable[..., Any] = SSECollector,
) -> dict[str, Any]:
    """Validate a retained evidence bundle and replay its case on a fresh profile."""
    src_path = pathlib.Path(bundle_path).resolve()
    raw = json.loads(src_path.read_text(encoding="utf-8"))
    if raw.get("schema_version") != SCHEMA_VERSION:
        raise ValueError(f"unsupported evidence bundle schema_version: {raw.get('schema_version')!r}")
    replay_inputs = raw.get("replay_inputs")
    if not isinstance(replay_inputs, dict) or not replay_inputs.get("case_id"):
        raise ValueError("evidence bundle missing valid replay_inputs")
    expected_digest = (raw.get("sha256_digests") or {}).get("replay_inputs")
    if expected_digest and canonical_sha256(replay_inputs) != expected_digest:
        raise ValueError("replay_inputs sha256 digest mismatch")

    return run_case(
        replay_inputs["case_id"],
        out_dir,
        engine_bin=engine_bin,
        seed=int(replay_inputs.get("seed", 1)),
        timeout_s=float(replay_inputs.get("timeout_s", 25.0)),
        app_factory=app_factory,
        sse_factory=sse_factory,
        replayed_from={
            "source_run_id": raw.get("run_id", ""),
            "source_candidate_head": raw.get("candidate_head", ""),
            "source_bundle_sha256": hashlib.sha256(src_path.read_bytes()).hexdigest(),
        },
    )
