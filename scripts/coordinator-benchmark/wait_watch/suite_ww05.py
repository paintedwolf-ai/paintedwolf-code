#!/usr/bin/env python3
"""WW05 sidecar lane suite (C14, C15, C16, C17 restart) against a live lycaon-dev engine."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import shutil
import subprocess
import sys
import time
import uuid
from typing import Any, Callable

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.harness import SSECollector, create_isolated_session  # noqa: E402

from wait_watch.barriers import (  # noqa: E402
    BarrierTimeoutError,
    GitRefWriterFixture,
    TimerGuard,
)
from wait_watch.checker import SCHEMA_VERSION, check_bundle  # noqa: E402
from wait_watch.driver import (  # noqa: E402
    WaitWatchApplication,
    WaitWatchDriver,
    _confirm_parked_lease,
    _git_head,
    _host_identity,
    _make_wait_watch_app_factory,
    _observe_product_identities,
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

WW05_SLOTS = (
    "C14-cursor-burst-wrap",
    "C15-file-attribution-include-own",
    "C16-git-loose-packed-worktree",
    "C17-timer-sidecar-restart",
)


def _write_json(path: pathlib.Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _resolve_root_label(application: Any, project_id: str, project_dir: pathlib.Path) -> str:
    """Query /v1/projects/<id> to discover the exact attached root label for file_change / git_ref."""
    try:
        proj = application.request(f"/v1/projects/{project_id}")
        roots = proj.get("roots") or []
        for root in roots:
            if not isinstance(root, dict):
                continue
            label = str(root.get("label") or "").strip().lstrip("@")
            if label:
                return label
            rid = str(root.get("id") or "").strip()
            if rid:
                return rid
    except Exception:
        pass
    return project_dir.name


def _extract_watch_match_text(pending_payload: dict[str, Any]) -> str:
    messages = pending_payload.get("messages") or []
    for msg in reversed(messages):
        content = msg.get("content")
        if isinstance(content, str) and content.strip():
            return content
    return ""


def _parse_watch_match_rows(storage_snap: dict[str, Any], watch_id: str = "") -> list[dict[str, Any]]:
    tables = storage_snap.get("tables") or {}
    rows = tables.get("watch_matches") or []
    out: list[dict[str, Any]] = []
    for row in rows:
        if not isinstance(row, dict):
            continue
        if watch_id and row.get("watch_id") != watch_id:
            continue
        ev_raw = row.get("evidence_json")
        ev: dict[str, Any] = {}
        if isinstance(ev_raw, str) and ev_raw.strip():
            try:
                parsed = json.loads(ev_raw)
                if isinstance(parsed, dict):
                    ev = parsed
            except ValueError:
                pass
        out.append({**row, "evidence": ev})
    return out


def _finalize_slot_bundle(
    *,
    slot_id: str,
    out_path: pathlib.Path,
    app_dir: pathlib.Path,
    sse_path: pathlib.Path,
    session_id: str,
    project_id: str,
    handle: str,
    started_ms: float,
    source_obs: SourceObserver,
    admission_obs: AdmissionObserver,
    rendered_obs: RenderedInputObserver,
    den_obs: DenObserver,
    source_facts: dict[str, Any],
    extra_assertions: dict[str, Any],
    barrier_error: dict[str, Any] | None,
    instrumentation_gaps: list[str],
    identity_limitations: list[str],
    owed_occurrences: list[str],
) -> dict[str, Any]:
    admission_obs.load_sse_jsonl(sse_path)
    scoped_phases = admission_obs.load_phase_records(app_dir / "sidecar.log", session_id=session_id or None)
    storage_snap = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    final_prod_ids, final_prod_refs, _ = _observe_product_identities(storage_snap)

    source_obs.set_facts({
        **source_facts,
        "phase_records": scoped_phases,
        "handle": handle,
        "product_identities": final_prod_ids,
        "product_identity_refs": final_prod_refs,
        "identity_limitations": list(dict.fromkeys(identity_limitations)),
        "slot_assertions": extra_assertions,
    })

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
            "invocation_id": final_prod_ids.get("invocation_id") or "",
            "workspace_id": project_id,
            "occurrence_id": final_prod_ids.get("occurrence_id") or "",
            "claim_id": final_prod_ids.get("claim_id") or "",
            "turn": rendered_snap.get("new_model_presentation_count", 0),
            "revision": 1,
            "event_seq": len(source_obs.events),
            "clock_domain": "system",
        },
    )

    candidate_head = _git_head(REPO_ROOT)
    host = _host_identity()
    run_id = f"{candidate_head[:12]}-{host['os'].lower()}-{slot_id}-{uuid.uuid4().hex[:8]}"
    elapsed_ms = round(time.monotonic() * 1000.0 - started_ms, 2)

    bundle: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id,
        "candidate_head": candidate_head,
        "host": host,
        "lane": "sidecar",
        "clock_domain": "system",
        "case_id": slot_id,
        "elapsed_ms": elapsed_ms,
        "expectations": {
            "expected_outcome": "delivered",
            "owed_recipients": [f"session:{session_id}"] if session_id else [],
            "owed_occurrences": owed_occurrences,
            "expected_handle": handle,
        },
        "observers": observers,
        "slot_assertions": extra_assertions,
        "instrumentation_gaps": list(dict.fromkeys(instrumentation_gaps)),
        "barrier_error": barrier_error,
        "sha256_digests": {
            "source": canonical_sha256(source_snap),
            "storage": canonical_sha256(storage_snap),
            "admission": canonical_sha256(admission_snap),
            "rendered_input": canonical_sha256(rendered_snap),
            "den": canonical_sha256(den_snap),
        },
    }

    checker_verdict = check_bundle(bundle)
    all_slot_ok = all(bool(v) for v in extra_assertions.values()) if extra_assertions else False
    if barrier_error is not None:
        checker_verdict["verdict"] = barrier_error.get("verdict", "incomplete")
    elif checker_verdict["verdict"] == "pass" and not all_slot_ok:
        checker_verdict["verdict"] = "fail"
        checker_verdict["defects"].append({
            "code": "ww05_slot_assertion_failed",
            "detail": f"failed assertions: {[k for k, v in extra_assertions.items() if not v]}",
        })
    bundle["checker_verdict"] = checker_verdict
    _write_json(out_path / "evidence-bundle.json", bundle)
    return bundle


def run_c14_cursor_burst_wrap(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float,
    app_factory: Callable[..., Any] = WaitWatchApplication,
) -> dict[str, Any]:
    """C14-cursor-burst-wrap: real process output burst & cursor catch-up against live lycaon-dev."""
    slot_id = "C14-cursor-burst-wrap"
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    barrier_dir = project_dir / ".ww05-c14"
    barrier_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)

    pre_reg_line = "ww05-c14-pre-reg-0\n"
    burst_lines = [f"ww05-c14-burst-{i:02d}\n" for i in range(1, 17)]
    burst_payload = "".join(burst_lines) + ("B" * 256) + "\n"

    script_path = barrier_dir / "burst_writer.py"
    script_path.write_text(
        "import json, os, pathlib, sys, time\n"
        "root = pathlib.Path(__file__).resolve().parent\n"
        f"sys.stdout.write({pre_reg_line!r})\n"
        "sys.stdout.flush()\n"
        "(root / 'reached.tmp').write_text(json.dumps({'pid': os.getpid(), 'ts_ns': time.time_ns()}), encoding='utf-8')\n"
        "os.replace(root / 'reached.tmp', root / 'reached')\n"
        "deadline = time.monotonic() + 60\n"
        "while not (root / 'release').is_file():\n"
        "    if time.monotonic() >= deadline: sys.exit(124)\n"
        "    time.sleep(0.05)\n"
        f"sys.stdout.write({burst_payload!r})\n"
        "sys.stdout.flush()\n"
        "(root / 'burst_done.tmp').write_text('ok', encoding='utf-8')\n"
        "os.replace(root / 'burst_done.tmp', root / 'burst_done')\n"
        "while not (root / 'exit_gate').is_file():\n"
        "    if time.monotonic() >= deadline: sys.exit(0)\n"
        "    time.sleep(0.05)\n"
        "sys.exit(0)\n",
        encoding="utf-8",
    )

    started_ms = time.monotonic() * 1000.0
    host = _host_identity()
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
    cmd_handle = ""
    watch_id = ""
    catchup_pending = 0
    barrier_error: dict[str, Any] | None = None
    instrumentation_gaps: list[str] = []
    identity_limitations: list[str] = []
    owed_occurrences: list[str] = []

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(
            application, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        submission_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": submission_id, "text": "Run WW05 C14 process_output burst and cursor catch-up."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        _, cmd_res, cmd_handle = driver.call_command(
            "python3 -B .ww05-c14/burst_writer.py &",
            progress_update="- [ ] Launch C14 burst writer",
        )
        source_obs.record_event("command_launched", handle=cmd_handle, outcome=(cmd_res or {}).get("outcome"))

        TimerGuard(timeout_s, phase="c14.wait_reached", dump_state=driver._dump_state).poll(
            lambda: (barrier_dir / "reached").is_file(),
            detail="burst_writer reached sentinel not written",
        )
        source_obs.record_event("pre_reg_written", handle=cmd_handle, bytes=len(pre_reg_line))

        # 1. Register cursor=0 catch-up watch (must immediately catch up pre-reg bytes).
        _, _, catchup_parsed = driver.call_watch({
            "conditions": [{"kind": "process_output", "handle": cmd_handle, "cursor": 0}],
        })
        catchup_pending = int(catchup_parsed.get("pending_matches") or 0)
        source_obs.record_event("catchup_watch_registered", handle=catchup_parsed.get("handle"), pending=catchup_pending)

        # 2. Register live repeating process_output watch starting at current cursor, then park on it.
        _, _, live_parsed = driver.call_watch({
            "conditions": [{"kind": "process_output", "handle": cmd_handle}],
            "repeat": True,
        })
        watch_id = str(live_parsed.get("watch_id") or "")
        wait_cond = live_parsed.get("wait_condition") or {"kind": "watch_match", "handles": [live_parsed.get("handle", "")]}
        source_obs.record_event("live_output_watch_registered", watch_id=watch_id, handle=live_parsed.get("handle"))

        driver.call_wait([wait_cond], until_complete=False, timeout_ms=int(timeout_s * 1000), reason="C14 output burst")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        # Release burst gate and wait for resumed pending model turn.
        (barrier_dir / "release").write_text("go\n", encoding="utf-8")
        source_obs.record_event("burst_released", bytes=len(burst_payload))

        resumed_req = driver.await_pending("c14:resumed_pending")
        report_text = _extract_watch_match_text(resumed_req)
        storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        prod_ids, prod_refs, prod_lims = _observe_product_identities(storage_now)
        identity_limitations.extend(prod_lims)
        if prod_ids.get("occurrence_id"):
            owed_occurrences.append(prod_ids["occurrence_id"])

        rendered_obs.record_pending(
            resumed_req,
            presentation_kind="new_model_presentation",
            handle=cmd_handle,
            invocation_id=prod_ids["invocation_id"],
            occurrence_id=prod_ids["occurrence_id"],
            claim_id=prod_ids["claim_id"],
            report=report_text,
            report_readable=bool(report_text),
            evidence_refs={"session_id": "rendered_input:pending.session_id", **prod_refs},
        )
        driver.finish_turn(resumed_req, "Observed C14 process_output burst.")
        (barrier_dir / "exit_gate").write_text("exit\n", encoding="utf-8")

        admission_obs.capture_http(application, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
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

    final_storage = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    live_matches = _parse_watch_match_rows(final_storage, watch_id=watch_id)
    has_burst_range = any(
        isinstance(m["evidence"].get("end_cursor"), (int, float))
        and m["evidence"]["end_cursor"] > len(pre_reg_line)
        for m in live_matches
    )

    return _finalize_slot_bundle(
        slot_id=slot_id,
        out_path=out_path,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        handle=cmd_handle,
        started_ms=started_ms,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        source_facts={
            "pre_reg_bytes": len(pre_reg_line),
            "burst_bytes": len(burst_payload),
            "catchup_pending_matches": catchup_pending,
            "live_match_count": len(live_matches),
        },
        extra_assertions={
            "catchup_matched_pre_reg": catchup_pending >= 1,
            "burst_match_recorded": len(live_matches) >= 1,
            "cursor_advanced_past_pre_reg": has_burst_range,
        },
        barrier_error=barrier_error,
        instrumentation_gaps=instrumentation_gaps,
        identity_limitations=identity_limitations,
        owed_occurrences=owed_occurrences,
    )


def run_c15_file_attribution(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float,
    app_factory: Callable[..., Any] = WaitWatchApplication,
) -> dict[str, Any]:
    """C15-file-attribution-include-own: real workspace file write with external writer & file_change watch."""
    slot_id = "C15-file-attribution-include-own"
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)

    watched_file = project_dir / "watched.txt"
    watched_file.write_text("initial-v1\n", encoding="utf-8")

    started_ms = time.monotonic() * 1000.0
    host = _host_identity()
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
    watch_handle = ""
    watch_id = ""
    external_payload = "external-writer-v2\n"
    barrier_error: dict[str, Any] | None = None
    instrumentation_gaps: list[str] = []
    identity_limitations: list[str] = []
    owed_occurrences: list[str] = []

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(
            application, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        root_label = _resolve_root_label(application, project_id, project_dir)
        submission_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": submission_id, "text": "Run WW05 C15 file_change external writer observation."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        _, _, watch_parsed = driver.call_watch({
            "conditions": [{"kind": "file_change", "root": root_label, "path": "watched.txt"}],
            "repeat": True,
        })
        watch_handle = str(watch_parsed.get("handle") or "")
        watch_id = str(watch_parsed.get("watch_id") or "")
        wait_cond = watch_parsed.get("wait_condition") or {"kind": "watch_match", "handles": [watch_handle]}
        source_obs.record_event("file_watch_registered", watch_id=watch_id, handle=watch_handle, root=root_label)

        driver.call_wait([wait_cond], until_complete=False, timeout_ms=int(timeout_s * 1000), reason="C15 external file write")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        # Perform independent external write on disk.
        watched_file.write_text(external_payload, encoding="utf-8")
        source_obs.record_event(
            "external_file_written",
            path="watched.txt",
            sha256=hashlib.sha256(external_payload.encode("utf-8")).hexdigest(),
        )

        resumed_req = driver.await_pending("c15:resumed_pending")
        report_text = _extract_watch_match_text(resumed_req)
        storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        prod_ids, prod_refs, prod_lims = _observe_product_identities(storage_now)
        identity_limitations.extend(prod_lims)
        if prod_ids.get("occurrence_id"):
            owed_occurrences.append(prod_ids["occurrence_id"])

        rendered_obs.record_pending(
            resumed_req,
            presentation_kind="new_model_presentation",
            handle=watch_handle,
            invocation_id=prod_ids["invocation_id"],
            occurrence_id=prod_ids["occurrence_id"],
            claim_id=prod_ids["claim_id"],
            report=report_text,
            report_readable=bool(report_text),
            evidence_refs={"session_id": "rendered_input:pending.session_id", **prod_refs},
        )
        driver.finish_turn(resumed_req, "Observed C15 external file_change.")
        admission_obs.capture_http(application, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
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

    final_storage = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    file_matches = _parse_watch_match_rows(final_storage, watch_id=watch_id)
    matched_watched_txt = any(m["evidence"].get("path") == "watched.txt" for m in file_matches)
    has_actor_label = any(bool(m["evidence"].get("actor_class")) for m in file_matches)

    return _finalize_slot_bundle(
        slot_id=slot_id,
        out_path=out_path,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        handle=watch_handle,
        started_ms=started_ms,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        source_facts={
            "path": "watched.txt",
            "expected_sha256": hashlib.sha256(external_payload.encode("utf-8")).hexdigest(),
            "match_count": len(file_matches),
        },
        extra_assertions={
            "file_change_matched": len(file_matches) >= 1,
            "matched_exact_path": matched_watched_txt,
            "attribution_label_present": has_actor_label,
        },
        barrier_error=barrier_error,
        instrumentation_gaps=instrumentation_gaps,
        identity_limitations=identity_limitations,
        owed_occurrences=owed_occurrences,
    )


def run_c16_git_ref(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float,
    app_factory: Callable[..., Any] = WaitWatchApplication,
) -> dict[str, Any]:
    """C16-git-loose-packed-worktree: real GitRefWriterFixture advancing a ref on an isolated repo."""
    slot_id = "C16-git-loose-packed-worktree"
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    raw_dir.mkdir(parents=True, exist_ok=True)

    git_fixture = GitRefWriterFixture(project_dir, branch="main")
    oid1 = git_fixture.init()
    oid2 = ""

    started_ms = time.monotonic() * 1000.0
    host = _host_identity()
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
    watch_handle = ""
    watch_id = ""
    barrier_error: dict[str, Any] | None = None
    instrumentation_gaps: list[str] = []
    identity_limitations: list[str] = []
    owed_occurrences: list[str] = []

    try:
        application.ready()
        sse = SSECollector(application.url, application.token, sse_path)
        sse.start()
        session_id, project_id, _, _ = create_isolated_session(
            application, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        root_label = _resolve_root_label(application, project_id, project_dir)
        submission_id = str(uuid.uuid4())
        application.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": submission_id, "text": "Run WW05 C16 git_ref watch on refs/heads/main."},
        )
        driver = WaitWatchDriver(
            application,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse_path,
        )

        _, _, watch_parsed = driver.call_watch({
            "conditions": [{"kind": "git_ref", "root": root_label, "ref": "refs/heads/main"}],
            "repeat": True,
        })
        watch_handle = str(watch_parsed.get("handle") or "")
        watch_id = str(watch_parsed.get("watch_id") or "")
        wait_cond = watch_parsed.get("wait_condition") or {"kind": "watch_match", "handles": [watch_handle]}
        source_obs.record_event("git_watch_registered", watch_id=watch_id, handle=watch_handle, baseline_oid=oid1)

        driver.call_wait([wait_cond], until_complete=False, timeout_ms=int(timeout_s * 1000), reason="C16 git ref advance")
        _confirm_parked_lease(app_dir / "store.db", sse_path, session_id, timeout_s, driver._dump_state)

        rec = git_fixture.release(message="ww05 c16 advance main")
        oid2 = str(rec["oid"])
        source_obs.record_event("git_ref_advanced", ref="refs/heads/main", before_oid=oid1, after_oid=oid2)

        resumed_req = driver.await_pending("c16:resumed_pending")
        report_text = _extract_watch_match_text(resumed_req)
        storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        prod_ids, prod_refs, prod_lims = _observe_product_identities(storage_now)
        identity_limitations.extend(prod_lims)
        if prod_ids.get("occurrence_id"):
            owed_occurrences.append(prod_ids["occurrence_id"])

        rendered_obs.record_pending(
            resumed_req,
            presentation_kind="new_model_presentation",
            handle=watch_handle,
            invocation_id=prod_ids["invocation_id"],
            occurrence_id=prod_ids["occurrence_id"],
            claim_id=prod_ids["claim_id"],
            report=report_text,
            report_readable=bool(report_text),
            evidence_refs={"session_id": "rendered_input:pending.session_id", **prod_refs},
        )
        driver.finish_turn(resumed_req, f"Observed C16 git_ref advance to {oid2}.")
        admission_obs.capture_http(application, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
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

    independent_oid = subprocess.run(
        ["git", "rev-parse", "refs/heads/main"],
        cwd=project_dir,
        capture_output=True,
        text=True,
        check=True,
    ).stdout.strip()
    final_storage = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    git_matches = _parse_watch_match_rows(final_storage, watch_id=watch_id)
    oid_parity = any(
        m["evidence"].get("before_oid") == oid1 and m["evidence"].get("after_oid") == independent_oid
        for m in git_matches
    )

    return _finalize_slot_bundle(
        slot_id=slot_id,
        out_path=out_path,
        app_dir=app_dir,
        sse_path=sse_path,
        session_id=session_id,
        project_id=project_id,
        handle=watch_handle,
        started_ms=started_ms,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        source_facts={
            **git_fixture.source_facts(),
            "independent_git_head": independent_oid,
            "match_count": len(git_matches),
        },
        extra_assertions={
            "git_ref_matched": len(git_matches) >= 1,
            "old_new_oids_match_git": oid_parity,
        },
        barrier_error=barrier_error,
        instrumentation_gaps=instrumentation_gaps,
        identity_limitations=identity_limitations,
        owed_occurrences=owed_occurrences,
    )


def run_c17_timer_sidecar_restart(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float,
    app_factory: Callable[..., Any] = WaitWatchApplication,
) -> dict[str, Any]:
    """C17-timer-sidecar-restart (D4 representative sidecar restart): arm short timer, restart sidecar, fire once."""
    slot_id = "C17-timer-sidecar-restart"
    out_path = out_dir.resolve()
    app_dir = out_path / "app"
    project_dir = out_path / "project"
    raw_dir = out_path / "raw"
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("# WW05 C17 timer restart fixture\n", encoding="utf-8")

    started_ms = time.monotonic() * 1000.0
    host = _host_identity()
    source_obs = SourceObserver(clock_domain="system")
    admission_obs = AdmissionObserver(clock_domain="system")
    rendered_obs = RenderedInputObserver(clock_domain="system")
    den_obs = DenObserver(status="not_run", os_name=host["os"])

    make_app = _make_wait_watch_app_factory(engine_bin, app_factory)
    app1 = make_app(app_dir)
    app2 = None
    sse1 = None
    sse2 = None
    sse1_path = raw_dir / "events-pre-restart.jsonl"
    sse2_path = raw_dir / "events.jsonl"
    session_id = ""
    project_id = ""
    submission_id = ""
    watch_handle = ""
    watch_id = ""
    pre_pid = 0
    post_pid = 0
    barrier_error: dict[str, Any] | None = None
    instrumentation_gaps: list[str] = []
    identity_limitations: list[str] = []
    owed_occurrences: list[str] = []

    try:
        # Phase 1: Arm a 4000ms ambient wake timer watch and finish turn cleanly.
        app1.ready()
        pre_pid = getattr(getattr(app1, "process", None), "pid", 0) or 0
        sse1 = SSECollector(app1.url, app1.token, sse1_path)
        sse1.start()
        session_id, project_id, _, _ = create_isolated_session(
            app1, project_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        submission_id = str(uuid.uuid4())
        app1.request(
            f"/v1/sessions/{session_id}/prompts",
            {"operation_id": submission_id, "text": "Arm WW05 C17 timer watch before sidecar restart."},
        )
        driver1 = WaitWatchDriver(
            app1,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse1_path,
        )

        _, _, watch_parsed = driver1.call_watch({
            "conditions": [{"kind": "timer", "delay_ms": 4000}],
            "delivery": "wake",
        })
        watch_handle = str(watch_parsed.get("handle") or "")
        watch_id = str(watch_parsed.get("watch_id") or "")
        source_obs.record_event("timer_watch_armed", watch_id=watch_id, handle=watch_handle, delay_ms=4000, pre_pid=pre_pid)

        post_watch_req = driver1.await_pending("c17:finish_before_restart")
        driver1.finish_turn(post_watch_req, "Timer watch armed; ready for sidecar restart.")
        time.sleep(0.3)

        # Phase 2: Stop sidecar cleanly and restart a fresh engine process on the same store.db.
        sse1.close()
        sse1 = None
        app1.close(cleanup=False)
        proc1 = getattr(app1, "process", None)
        if proc1 is not None:
            checkpoint_closed_store(app_dir / "store.db", proc1)
        if (app_dir / "sidecar.log").is_file():
            shutil.copyfile(app_dir / "sidecar.log", raw_dir / "sidecar-pre-restart.log")
        source_obs.record_event("sidecar_stopped", pre_pid=pre_pid)

        app2 = make_app(app_dir)
        app2.ready()
        post_pid = getattr(getattr(app2, "process", None), "pid", 0) or 0
        sse2 = SSECollector(app2.url, app2.token, sse2_path)
        sse2.start()
        source_obs.record_event("sidecar_restarted", pre_pid=pre_pid, post_pid=post_pid)

        driver2 = WaitWatchDriver(
            app2,
            session_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
            sse_path=sse2_path,
        )

        # Wait for the recovered timer to fire and wake the idle session once.
        wake_req = driver2.await_pending("c17:post_restart_timer_wake")
        report_text = _extract_watch_match_text(wake_req)
        storage_now = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id)
        prod_ids, prod_refs, prod_lims = _observe_product_identities(storage_now)
        identity_limitations.extend(prod_lims)
        if prod_ids.get("occurrence_id"):
            owed_occurrences.append(prod_ids["occurrence_id"])

        rendered_obs.record_pending(
            wake_req,
            presentation_kind="new_model_presentation",
            handle=watch_handle,
            invocation_id=prod_ids["invocation_id"],
            occurrence_id=prod_ids["occurrence_id"],
            claim_id=prod_ids["claim_id"],
            report=report_text,
            report_readable=bool(report_text),
            evidence_refs={"session_id": "rendered_input:pending.session_id", **prod_refs},
        )
        driver2.finish_turn(wake_req, "Observed C17 timer wake after sidecar restart.")
        admission_obs.capture_http(app2, session_id=session_id, project_id=project_id, submission_ids=[submission_id])
        app2.request(f"/v1/sessions/{session_id}/abort", {})
    except BarrierTimeoutError as exc:
        barrier_error = exc.to_dict()
    finally:
        if sse1 is not None:
            sse1.close()
        if sse2 is not None:
            sse2.close()
        if app2 is not None:
            app2.close(cleanup=False)
        else:
            app1.close(cleanup=False)

    last_proc = getattr(app2 or app1, "process", None)
    if last_proc is not None:
        checkpoint_closed_store(app_dir / "store.db", last_proc)

    final_storage = StorageObserver(app_dir / "store.db", clock_domain="system").snapshot(session_id or None)
    timer_matches = _parse_watch_match_rows(final_storage, watch_id=watch_id)
    regs = [
        r for r in (final_storage.get("tables", {}).get("watch_registrations") or [])
        if r.get("watch_id") == watch_id
    ]
    completed_once = bool(regs and regs[-1].get("state") == "terminal" and regs[-1].get("terminal_outcome") == "completed")

    return _finalize_slot_bundle(
        slot_id=slot_id,
        out_path=out_path,
        app_dir=app_dir,
        sse_path=sse2_path,
        session_id=session_id,
        project_id=project_id,
        handle=watch_handle,
        started_ms=started_ms,
        source_obs=source_obs,
        admission_obs=admission_obs,
        rendered_obs=rendered_obs,
        den_obs=den_obs,
        source_facts={
            "pre_restart_pid": pre_pid,
            "post_restart_pid": post_pid,
            "distinct_pids": pre_pid > 0 and post_pid > 0 and pre_pid != post_pid,
            "timer_match_count": len(timer_matches),
            "terminal_completed": completed_once,
        },
        extra_assertions={
            "real_sidecar_restart": pre_pid > 0 and post_pid > 0 and pre_pid != post_pid,
            "timer_fired_exactly_once": len(timer_matches) == 1,
            "timer_completed_terminal": completed_once,
        },
        barrier_error=barrier_error,
        instrumentation_gaps=instrumentation_gaps,
        identity_limitations=identity_limitations,
        owed_occurrences=owed_occurrences,
    )


SLOT_RUNNERS: dict[str, Callable[..., dict[str, Any]]] = {
    "C14-cursor-burst-wrap": run_c14_cursor_burst_wrap,
    "C15-file-attribution-include-own": run_c15_file_attribution,
    "C16-git-loose-packed-worktree": run_c16_git_ref,
    "C17-timer-sidecar-restart": run_c17_timer_sidecar_restart,
}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="suite_ww05",
        description="Run WW05 sidecar qualification slots (C14, C15, C16, C17 restart) against lycaon-dev.",
    )
    parser.add_argument("--engine", type=pathlib.Path, required=True, help="Path to built lycaon-dev engine binary.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for per-slot evidence and ww05-summary.json.")
    parser.add_argument(
        "--slot",
        action="append",
        choices=WW05_SLOTS,
        default=None,
        help="Optional specific WW05 slot(s) to run (defaults to all 4 sidecar slots).",
    )
    parser.add_argument("--timeout", type=float, default=30.0, help="Explicit short timer guard per slot in seconds (default: 30).")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    out_root = args.out.resolve()
    out_root.mkdir(parents=True, exist_ok=True)
    slots_to_run = tuple(args.slot) if args.slot else WW05_SLOTS

    results: list[dict[str, Any]] = []
    all_passed = True
    for slot_id in slots_to_run:
        runner = SLOT_RUNNERS[slot_id]
        bundle = runner(out_root / slot_id, engine_bin=args.engine, timeout_s=args.timeout)
        verdict = (bundle.get("checker_verdict") or {}).get("verdict", "unknown")
        if verdict != "pass":
            all_passed = False
        results.append({
            "slot_id": slot_id,
            "run_id": bundle.get("run_id"),
            "verdict": verdict,
            "slot_assertions": bundle.get("slot_assertions") or {},
            "defects": (bundle.get("checker_verdict") or {}).get("defects") or [],
            "evidence_path": str(out_root / slot_id / "evidence-bundle.json"),
        })

    summary = {
        "schema_version": SCHEMA_VERSION,
        "card": "WW05",
        "candidate_head": _git_head(REPO_ROOT),
        "host": _host_identity(),
        "engine": str(args.engine),
        "all_passed": all_passed,
        "slots": results,
    }
    _write_json(out_root / "ww05-summary.json", summary)
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if all_passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
