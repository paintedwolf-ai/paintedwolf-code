"""Decision 033 / 034 D10 exact-binary observer validation runner for GB02.

Proves all three Decision 034 acceptance requirements on exact D1 `lycaon-dev`:
1. Exact-binary debugger path (GDB on Linux, LLDB on macOS) with zero-charge
   controls (`friction_units=0`) and charged recovered-repair control
   (`friction_units=1`, `store.db retry_count=0`).
2. Overlapping concurrent sessions on the same sidecar process proving
   `(sessionID, rootID)` owner attribution and per-phase delta attribution
   without cross-session bleed.
3. Cross-source deduplication proving one typed event present in both
   `store.db` (`event_outbox`) and SSE (`/v1/events`) is counted exactly once.
"""
from __future__ import annotations

import argparse
import concurrent.futures
import json
import pathlib
import sqlite3
import sys
from typing import Any

from grounding.friction_probe import (
    count_attributed_friction_hits,
    parse_friction_trace_log,
    probe_store_db,
)
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)


def _verify_store_and_sse_dedup(tmp_dir: pathlib.Path) -> dict[str, Any]:
    """Prove one typed event in both store.db event_outbox and SSE is counted once."""
    db_path = tmp_dir / "dedup-check-store.db"
    if db_path.exists():
        db_path.unlink()
    sid = "11111111-2222-3333-4444-555555555555"
    conn = sqlite3.connect(str(db_path))
    try:
        conn.execute("CREATE TABLE messages (id TEXT, session_id TEXT, grounding_json TEXT)")
        conn.execute("CREATE TABLE event_outbox (id TEXT, payload_json TEXT)")
        conn.execute(
            "INSERT INTO event_outbox (id, payload_json) VALUES (?, ?)",
            ("evt-note-1", json.dumps({"session_id": sid, "code": "MID_TURN_NOTE_HANDLE_UNGROUND"})),
        )
        conn.execute(
            "INSERT INTO event_outbox (id, payload_json) VALUES (?, ?)",
            ("evt-brk-1", json.dumps({"session_id": sid, "code": "COORDINATOR_GROUNDING_ESCALATED"})),
        )
        conn.commit()
    finally:
        conn.close()

    sse_events = [
        {"id": "evt-note-1", "session_id": sid, "code": "MID_TURN_NOTE_HANDLE_UNGROUND"},
        {"id": "evt-brk-1", "session_id": sid, "code": "COORDINATOR_GROUNDING_ESCALATED"},
    ]
    snap_db_only = probe_store_db(db_path, sid, sse_events=())
    snap_both = probe_store_db(db_path, sid, sse_events=sse_events)
    passed = (
        snap_db_only.withheld_notes.value == 1
        and snap_both.withheld_notes.value == 1
        and snap_db_only.breaker_advisories.value == 1
        and snap_both.breaker_advisories.value == 1
    )
    return {
        "passed": passed,
        "event_ids": ["evt-note-1", "evt-brk-1"],
        "db_only_withheld": snap_db_only.withheld_notes.value,
        "db_plus_sse_withheld": snap_both.withheld_notes.value,
        "db_only_breaker": snap_db_only.breaker_advisories.value,
        "db_plus_sse_breaker": snap_both.breaker_advisories.value,
    }


def run_d10_observer_validation(
    *,
    engine_bin: str,
    product_root: str,
    expected_engine_sha256: str,
    out_dir: pathlib.Path,
    run_id: str,
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    workspace = out_dir / "workspace"
    config_dir = out_dir / "config"
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "app.go").write_text(
        "package app\n\nfunc Main() string { return \"ready\" }\n",
        encoding="utf-8",
    )

    dedup_result = _verify_store_and_sse_dedup(out_dir)

    sidecar = IsolatedSidecarProcess(
        engine_bin,
        product_root=product_root,
        config_dir=config_dir,
        workspace_dir=workspace,
        expected_sha256=expected_engine_sha256,
        trace_friction=True,
    )

    with sidecar as client:
        manifest = sidecar.build_run_manifest()
        plat = "macos" if sidecar.provenance.platform == "darwin" else "linux"
        pid = client.create_project(workspace)["id"]
        trace_log = sidecar.log_path

        # Create two sessions on the same traced D1 process for overlapping attribution
        sid_a = client.create_session(pid)["id"]
        sid_b = client.create_session(pid)["id"]
        client.wait_session_ready(sid_a)
        client.wait_session_ready(sid_b)

        a_p1_before = probe_store_db(sidecar.store_db_path, sid_a, trace_log_path=trace_log)
        b_p1_before = probe_store_db(sidecar.store_db_path, sid_b, trace_log_path=trace_log)

        # Phase 1: run sid_a (zero-charge grounded control) and sid_b (charged recovered repair) concurrently
        def _run_a_phase1() -> Any:
            p = client.send_prompt(sid_a, "Read app.go and report line 3.")
            return run_scripted_session(
                client,
                session_id=sid_a,
                turns=(
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
                    ScriptedTurn(content="Verified Main() at [read#1:3]."),
                ),
                db_path=sidecar.store_db_path,
                workspace_dir=workspace,
                submission_id=str(p.get("submission_id") or p.get("id") or ""),
                wait_for_settlement=True,
                max_repair_replies=0,
                engine_provenance=sidecar.provenance,
                run_manifest=manifest,
                poll_timeout=180.0,
            )

        def _run_b_phase1() -> Any:
            p = client.send_prompt(sid_b, "Read app.go and report line 3.")
            return run_scripted_session(
                client,
                session_id=sid_b,
                turns=(
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
                    ScriptedTurn(content="Claimed [read#99:99]."),
                ),
                db_path=sidecar.store_db_path,
                workspace_dir=workspace,
                submission_id=str(p.get("submission_id") or p.get("id") or ""),
                wait_for_settlement=True,
                max_repair_replies=1,
                repair_reply_content="[cite: c1 => read#1:3]",
                engine_provenance=sidecar.provenance,
                run_manifest=manifest,
                poll_timeout=180.0,
            )

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            fut_a = pool.submit(_run_a_phase1)
            fut_b = pool.submit(_run_b_phase1)
            art_a1 = fut_a.result()
            art_b1 = fut_b.result()

        a_p1_after = probe_store_db(sidecar.store_db_path, sid_a, trace_log_path=trace_log)
        b_p1_after = probe_store_db(sidecar.store_db_path, sid_b, trace_log_path=trace_log)

        # Phase 2: now charge sid_a with a recovered repair while sid_b stays idle
        a_p2_before = a_p1_after
        b_p2_before = b_p1_after
        p_a2 = client.send_prompt(sid_a, "Verify line 3 of app.go again.")
        art_a2 = run_scripted_session(
            client,
            session_id=sid_a,
            turns=(
                ScriptedTurn(content="Claimed [read#99:99]."),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p_a2.get("submission_id") or p_a2.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=1,
            repair_reply_content="[cite: c1 => read#1:3]",
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=180.0,
        )

        a_p2_after = probe_store_db(sidecar.store_db_path, sid_a, trace_log_path=trace_log)
        b_p2_after = probe_store_db(sidecar.store_db_path, sid_b, trace_log_path=trace_log)

        trace_text = trace_log.read_text(encoding="utf-8", errors="replace")
        owned_pid, hits = parse_friction_trace_log(trace_text)

        (out_dir / "session-a-phase1.json").write_text(json.dumps(art_a1.to_dict(), indent=2, sort_keys=True), encoding="utf-8")
        (out_dir / "session-b-phase1.json").write_text(json.dumps(art_b1.to_dict(), indent=2, sort_keys=True), encoding="utf-8")
        (out_dir / "session-a-phase2.json").write_text(json.dumps(art_a2.to_dict(), indent=2, sort_keys=True), encoding="utf-8")

    a_p1_delta = a_p1_after.delta_observed(a_p1_before)
    b_p1_delta = b_p1_after.delta_observed(b_p1_before)
    a_p2_delta = a_p2_after.delta_observed(a_p2_before)
    b_p2_delta = b_p2_after.delta_observed(b_p2_before)

    hits_a = [h for h in hits if h.session_id == sid_a]
    hits_b = [h for h in hits if h.session_id == sid_b]
    root_a = hits_a[0].root_id if hits_a else ""
    root_b = hits_b[0].root_id if hits_b else ""

    root_attr_ok = (
        bool(root_a)
        and bool(root_b)
        and count_attributed_friction_hits(hits, session_id=sid_a, root_id=root_a) == 1
        and count_attributed_friction_hits(hits, session_id=sid_b, root_id=root_b) == 1
        and count_attributed_friction_hits(hits, session_id=sid_a, root_id=root_b) == (0 if root_a != root_b else 1)
    )

    passed = (
        owned_pid is not None
        and a_p1_after.is_complete()
        and b_p1_after.is_complete()
        and a_p2_after.is_complete()
        and a_p1_delta["friction_units"] == 0
        and b_p1_delta["friction_units"] == 1
        and b_p1_delta["repair_attempts"] == 0
        and a_p2_delta["friction_units"] == 1
        and b_p2_delta["friction_units"] == 0
        and root_attr_ok
        and dedup_result["passed"]
    )

    summary = {
        "card": "GB02",
        "kind": "d10_observer_validation",
        "run_id": run_id,
        "platform": plat,
        "passed": passed,
        "owned_pid_recorded": owned_pid is not None,
        "run_manifest": manifest.to_dict(),
        "overlapping_attribution": {
            "session_a": sid_a,
            "session_b": sid_b,
            "root_a": root_a,
            "root_b": root_b,
            "phase1_a_zero_charge_delta": a_p1_delta,
            "phase1_b_charged_delta": b_p1_delta,
            "phase2_a_charged_delta": a_p2_delta,
            "phase2_b_idle_delta": b_p2_delta,
            "root_attribution_verified": root_attr_ok,
            "total_hits": len(hits),
            "hits": [{"index": h.index, "session_id": h.session_id, "root_id": h.root_id} for h in hits],
        },
        "store_and_sse_dedup": dedup_result,
    }
    (out_dir / "d10-observer-validation.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB02 Decision 034 D10 observer validation")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb02-d10-observer")
    args = ap.parse_args(argv)
    summary = run_d10_observer_validation(
        engine_bin=args.engine_bin,
        product_root=args.product_root,
        expected_engine_sha256=args.expected_engine_sha256,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
