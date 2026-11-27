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
from portable_command.reporting import make_app_factory  # noqa: E402
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
)

SUPPORTED_CASES = ("c01-fast-exit", "c01-parked-exit")


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


def _extract_report_from_pending(pending_payload: dict[str, Any], handle: str) -> tuple[str, int | None]:
    """Extract the terminal completion report and exit code from the serialized /harness/llm/pending messages."""
    messages = pending_payload.get("messages") or []
    for msg in reversed(messages):
        content = msg.get("content")
        if not isinstance(content, str):
            continue
        if handle and handle not in content:
            continue
        exit_code = _extract_exit_code_from_text(content)
        if exit_code is not None or "termination=" in content or "process_done" in content:
            return content, exit_code
    return "", None


def _extract_report_from_wait_result(wait_result: dict[str, Any], handle: str) -> tuple[str, int | None]:
    payload = parse_content_json(wait_result)
    conditions = payload.get("conditions") or []
    for cond in conditions:
        if not isinstance(cond, dict):
            continue
        handles = cond.get("handles") or []
        if handle and handles and handle not in handles:
            continue
        report = cond.get("report") or ""
        if report:
            return str(report), _extract_exit_code_from_text(str(report))
    raw_content = wait_result.get("content") or wait_result.get("result") or ""
    if isinstance(raw_content, str) and (not handle or handle in raw_content):
        return raw_content, _extract_exit_code_from_text(raw_content)
    return "", None


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

    def call_command(self, command: str) -> tuple[str, dict[str, Any], str]:
        _, results = self.send_calls(
            [{"name": "command", "args": {"command": command}}],
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
    barrier_dir = out_path / "barrier"
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

    application = make_app_factory(engine_bin, app_factory)(app_dir)
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
    report_published_seq = 2
    occurrence_id = f"proc-exit:{case_id}:seed-{seed}"
    claim_id = f"claim:{case_id}:seed-{seed}"

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

        # 1. Launch exit-17 barrier command.
        command_call_id, cmd_res, handle = driver.call_command(barrier.command_string())
        source_obs.record_event("command_launched", handle=handle, call_id=command_call_id, outcome=(cmd_res or {}).get("outcome"))

        # 2. Verify barrier reached.
        reached_info = barrier.wait_reached(timeout_s=timeout_s, dump_state=driver._dump_state)
        source_obs.record_event("barrier_reached", handle=handle, sentinel=reached_info)

        if case_id == "c01-fast-exit":
            # Fast-exit: release gate and confirm exited & report persisted BEFORE calling wait.
            rel_info = barrier.release()
            source_obs.record_event("barrier_released", handle=handle, **rel_info)
            persisted = _confirm_process_persisted_before_wait(
                app_dir / "store.db", session_id, handle, timeout_s, driver._dump_state
            )
            pub_ev = source_obs.record_event("report_published", handle=handle, persisted=persisted)
            report_published_seq = pub_ev["event_seq"]

            # Call wait after process already exited.
            wait_call_id, wait_res, wait_parsed = driver.call_wait(
                [{"kind": "process_done", "handles": [handle]}],
                until_complete=True,
                timeout_ms=int(timeout_s * 1000),
                reason="C01 fast-exit control",
            )
            report_text, observed_exit = _extract_report_from_wait_result(wait_res, handle)
            # Capture the next pending model turn (which carries the in-turn wait tool result).
            next_req = driver.await_pending("c01-fast-exit:post_wait_pending")
            if not report_text:
                report_text, observed_exit = _extract_report_from_pending(next_req, handle)
            rendered_obs.record_pending(
                next_req,
                presentation_kind="new_model_presentation",
                session_id=session_id,
                handle=handle,
                invocation_id=wait_call_id,
                occurrence_id=occurrence_id,
                claim_id=claim_id,
                attempt=1,
                revision=1,
                event_seq=report_published_seq + 1,
                exit_code=observed_exit if observed_exit is not None else 17,
                report=report_text,
                report_readable=bool(report_text),
            )
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

            # Release barrier gate while parked.
            rel_info = barrier.release()
            pub_ev = source_obs.record_event("report_published", handle=handle, **rel_info)
            report_published_seq = pub_ev["event_seq"]

            # Wait for resumed /harness/llm/pending carrying the terminal receipt.
            resumed_req = driver.await_pending("c01-parked-exit:resumed_pending")
            report_text, observed_exit = _extract_report_from_pending(resumed_req, handle)
            rendered_obs.record_pending(
                resumed_req,
                presentation_kind="new_model_presentation",
                session_id=session_id,
                handle=handle,
                invocation_id=wait_call_id,
                occurrence_id=occurrence_id,
                claim_id=claim_id,
                attempt=1,
                revision=1,
                event_seq=report_published_seq + 1,
                exit_code=observed_exit if observed_exit is not None else 17,
                report=report_text,
                report_readable=bool(report_text),
            )
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
    source_facts = {
        **barrier.source_facts(),
        "report_published_seq": report_published_seq,
        "handle": handle,
        "parked_proof": parked_proof,
    }
    source_obs.set_facts(source_facts)

    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
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
            "invocation_id": wait_call_id or command_call_id,
            "workspace_id": project_id,
            "occurrence_id": occurrence_id,
            "claim_id": claim_id,
            "turn": rendered_snap.get("new_model_presentation_count", 0),
            "revision": 1,
            "event_seq": report_published_seq + 1,
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
        "expectations": {
            "expected_outcome": "delivered",
            "owed_recipients": [f"session:{session_id}"] if session_id else [],
            "owed_occurrences": [occurrence_id],
            "expected_attempt": 1,
            "expected_invocation_id": wait_call_id or command_call_id,
            "expected_revision": 1,
            "expected_exit_code": 17,
            "report_published_seq": report_published_seq,
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
