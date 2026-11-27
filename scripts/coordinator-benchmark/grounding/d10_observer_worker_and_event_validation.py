"""Decision 037 D10 observer supplement: worker root attribution + real product-emitted event dedup.

Proves the two remaining Decision 037 items on exact D1 `lycaon-dev`:
1. Root attribution (`root_id != session_id`): a worker child session incurs a grounding
   reject while a sibling top-level session runs a zero-charge turn. Asserts
   `h.session_id == child_sid`, `h.root_id == root_sid`, `h.root_id != h.session_id`,
   root phase delta >= 1, and sibling delta == 0.
2. Real product-emitted event deduplication: captures a real product-emitted typed event
   (e.g., `MID_TURN_NOTE_HANDLE_UNGROUND`) observed in BOTH `store.db` and `/v1/events` SSE
   stream keyed by its real event/message ID, and verifies `probe_store_db` counts it
   exactly once (not twice).
"""
from __future__ import annotations

import argparse
import concurrent.futures
import json
import pathlib
import subprocess
import sys
import threading
import time
import urllib.request
import uuid
from typing import Any

from grounding.friction_probe import (
    BREAKER_ADVISORY_CODES,
    VALID_MID_TURN_NOTE_CODES,
    count_attributed_friction_hits,
    parse_friction_trace_log,
    probe_store_db,
)
from grounding.sidecar_driver import (
    IsolatedSidecarProcess,
    ScriptedTurn,
    run_scripted_session,
)


class _SSECollector:
    """Background SSE reader for `/v1/events` capturing real product-emitted events."""

    def __init__(self, url: str, headers: dict[str, str] | None = None) -> None:
        self.url = url
        self.headers = dict(headers or {})
        self.headers["Accept"] = "text/event-stream"
        self.events: list[dict[str, Any]] = []
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> None:
        self._thread.start()
        time.sleep(0.3)

    def stop(self) -> None:
        self._stop.set()
        self._thread.join(timeout=2.0)

    def _run(self) -> None:
        req = urllib.request.Request(self.url, headers=self.headers)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                cur_id = ""
                cur_event = ""
                data_lines: list[str] = []
                while not self._stop.is_set():
                    raw = resp.readline()
                    if not raw:
                        break
                    line = raw.decode("utf-8", errors="replace").rstrip("\r\n")
                    if not line:
                        if data_lines:
                            blob = "\n".join(data_lines)
                            try:
                                parsed = json.loads(blob)
                            except ValueError:
                                parsed = {"raw": blob}
                            if isinstance(parsed, dict):
                                if cur_id and "event_id" not in parsed and "id" not in parsed:
                                    parsed["event_id"] = cur_id
                                if cur_event and "event" not in parsed:
                                    parsed["event"] = cur_event
                                self.events.append(parsed)
                        cur_id = ""
                        cur_event = ""
                        data_lines = []
                    elif line.startswith("id:"):
                        cur_id = line[3:].strip()
                    elif line.startswith("event:"):
                        cur_event = line[6:].strip()
                    elif line.startswith("data:"):
                        data_lines.append(line[5:].strip())
        except Exception:
            pass


def _init_git_workspace(workspace: pathlib.Path) -> None:
    workspace.mkdir(parents=True, exist_ok=True)
    (workspace / "app.go").write_text(
        'package app\n\nfunc Main() string { return "ready" }\n',
        encoding="utf-8",
    )
    for cmd in (
        ["git", "init", "-q"],
        ["git", "add", "."],
        ["git", "-c", "user.name=Grounding Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"],
    ):
        subprocess.run(cmd, cwd=workspace, check=True)


def run_d10_worker_and_event_validation(
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
    _init_git_workspace(workspace)

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

        # Start real SSE collector on /v1/events (with sidecar auth headers)
        sse = _SSECollector(f"{sidecar.base_url}/v1/events", headers=client._headers())
        sse.start()

        # --- Part 1: Real product-emitted event deduplication (MID_TURN_NOTE_HANDLE_UNGROUND) ---
        ev_sid = client.create_session(pid)["id"]
        client.wait_session_ready(ev_sid)
        p_ev = client.send_prompt(ev_sid, "Read app.go and report line 3.")
        art_ev = run_scripted_session(
            client,
            session_id=ev_sid,
            turns=(
                ScriptedTurn(
                    content="Inspecting bogus handle [read#99:99] before running.",
                    tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},),
                ),
                ScriptedTurn(content="Verified Main() at [read#1:3]."),
            ),
            db_path=sidecar.store_db_path,
            workspace_dir=workspace,
            submission_id=str(p_ev.get("submission_id") or p_ev.get("id") or ""),
            wait_for_settlement=True,
            max_repair_replies=0,
            engine_provenance=sidecar.provenance,
            run_manifest=manifest,
            poll_timeout=180.0,
        )
        time.sleep(0.5)
        sse_snapshot = list(sse.events)
        (out_dir / "real-event-session.json").write_text(
            json.dumps(art_ev.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
        )
        (out_dir / "captured-sse-events.json").write_text(
            json.dumps(sse_snapshot, indent=2, sort_keys=True), encoding="utf-8"
        )

        # Find real product-emitted grounding/note events in SSE for ev_sid
        target_codes = VALID_MID_TURN_NOTE_CODES | BREAKER_ADVISORY_CODES
        matching_sse = [
            ev for ev in sse_snapshot
            if ev_sid in json.dumps(ev) and any(c in json.dumps(ev) for c in target_codes)
        ]
        snap_db_only = probe_store_db(sidecar.store_db_path, ev_sid, sse_events=(), trace_log_path=trace_log)
        snap_db_plus_sse = probe_store_db(sidecar.store_db_path, ev_sid, sse_events=sse_snapshot, trace_log_path=trace_log)
        real_event_ids = sorted(
            {
                str(ev.get("event_id") or ev.get("id") or (ev.get("data") or {}).get("message_id") or "")
                for ev in matching_sse
                if isinstance(ev, dict)
            }
            - {""}
        )
        real_dedup_ok = (
            len(matching_sse) >= 1
            and snap_db_only.withheld_notes.value is not None
            and snap_db_only.withheld_notes.value >= 1
            and snap_db_plus_sse.withheld_notes.value == snap_db_only.withheld_notes.value
        )

        # --- Part 2: Worker child session root attribution (root_id != session_id) + sibling zero ---
        root_sid = client.create_session(pid, posture="build")["id"]
        sib_sid = client.create_session(pid, posture="build")["id"]
        client.wait_session_ready(root_sid)
        client.wait_session_ready(sib_sid)

        root_before = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
        sib_before = probe_store_db(sidecar.store_db_path, sib_sid, trace_log_path=trace_log)

        def _run_sibling_zero() -> Any:
            p = client.send_prompt(sib_sid, "Read app.go and report line 3.")
            return run_scripted_session(
                client,
                session_id=sib_sid,
                turns=(
                    ScriptedTurn(tool_calls=({"name": "command", "args": {"command": "cat -n app.go"}},)),
                    ScriptedTurn(content="Sibling verified Main() at [read#1:3]."),
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

        child_sid = ""
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            sib_fut = pool.submit(_run_sibling_zero)

            # Dispatch a repo-researcher worker from root_sid
            client.send_prompt(root_sid, "Dispatch a repo researcher to survey app.go.")
            # Step 1: load update_progress and task if needed, then invoke them
            def _answer_tool(sess: str, tool_name: str, tool_args: dict[str, Any]) -> None:
                req = client.wait_pending(sess, timeout=180.0)
                if tool_name not in (req.get("tools") or []):
                    client.respond(
                        req["id"],
                        tool_calls=[{"id": str(uuid.uuid4()), "name": "request_tools", "args": {"need": tool_name}}],
                    )
                    req = client.wait_pending(sess, timeout=180.0)
                client.respond(
                    req["id"],
                    tool_calls=[{"id": str(uuid.uuid4()), "name": tool_name, "args": tool_args}],
                )

            _answer_tool(root_sid, "update_progress", {"content": "## Progress\n- [ ] Survey app.go"})
            _answer_tool(
                root_sid,
                "task",
                {
                    "agent_type": "repo-researcher",
                    "brief": {"goal": "Survey app.go", "done_when": ["The declared scope is delivered."]},
                    "scope": {"mode": "read", "paths": ["app.go"]},
                },
            )

            # Wait for child worker pending request (if root wakes first, tell root to wait)
            deadline = time.monotonic() + 180.0
            child_req = None
            while time.monotonic() < deadline:
                pend = client.request("GET", "/harness/llm/pending")
                if isinstance(pend, dict) and pend.get("pending"):
                    psid = str(pend.get("session_id") or "")
                    if psid == root_sid:
                        client.respond(
                            pend["id"],
                            tool_calls=[{"id": str(uuid.uuid4()), "name": "wait", "args": {"timeout_ms": 15000, "conditions": [{"kind": "next_worker_done"}]}}],
                        )
                    elif psid and psid != sib_sid:
                        child_sid = psid
                        child_req = pend
                        break
                time.sleep(0.15)
            if not child_sid or child_req is None:
                raise RuntimeError("Worker child session never became pending")

            # Child reads app.go, then incurs a grounding reject on [read#99:99] and repairs to [read#1:3]
            client.respond(
                child_req["id"],
                tool_calls=[{"id": str(uuid.uuid4()), "name": "command", "args": {"command": "cat -n app.go"}}],
            )
            child_req2 = client.wait_pending(child_sid, timeout=180.0)
            client.respond(child_req2["id"], content="Worker claimed [read#99:99].")
            # Wait for repair prompt on child_sid and repair
            child_rep = client.wait_pending(child_sid, timeout=180.0)
            client.respond(child_rep["id"], content="[cite: c1 => read#1:3]")

            art_sib = sib_fut.result()
            (out_dir / "sibling-session.json").write_text(
                json.dumps(art_sib.to_dict(), indent=2, sort_keys=True), encoding="utf-8"
            )
            time.sleep(0.5)

        sse.stop()
        root_after = probe_store_db(sidecar.store_db_path, "", root_id=root_sid, trace_log_path=trace_log)
        sib_after = probe_store_db(sidecar.store_db_path, sib_sid, trace_log_path=trace_log)
        owned_pid, hits = parse_friction_trace_log(trace_log.read_text(encoding="utf-8", errors="replace"))

    root_delta = root_after.delta_observed(root_before)
    sib_delta = sib_after.delta_observed(sib_before)
    child_hits = [
        h for h in hits
        if h.session_id == child_sid and h.root_id == root_sid and h.root_id != h.session_id
    ]
    worker_root_ok = (
        bool(child_sid)
        and child_sid != root_sid
        and len(child_hits) >= 1
        and (root_delta.get("friction_units") or 0) >= 1
        and sib_delta.get("friction_units") == 0
        and count_attributed_friction_hits(hits, root_id=root_sid) >= 1
        and count_attributed_friction_hits(hits, session_id=sib_sid) == 0
    )

    passed = owned_pid is not None and worker_root_ok and real_dedup_ok
    summary = {
        "card": "GB02",
        "kind": "d10_observer_decision_037_supplement",
        "run_id": run_id,
        "platform": plat,
        "passed": passed,
        "owned_pid_recorded": owned_pid is not None,
        "run_manifest": manifest.to_dict(),
        "worker_root_attribution": {
            "passed": worker_root_ok,
            "root_session_id": root_sid,
            "child_session_id": child_sid,
            "sibling_session_id": sib_sid,
            "root_delta": root_delta,
            "sibling_delta": sib_delta,
            "child_distinct_root_hits": [
                {"index": h.index, "session_id": h.session_id, "root_id": h.root_id}
                for h in child_hits
            ],
            "all_hits": [
                {"index": h.index, "session_id": h.session_id, "root_id": h.root_id}
                for h in hits
            ],
        },
        "real_product_event_dedup": {
            "passed": real_dedup_ok,
            "session_id": ev_sid,
            "matching_sse_count": len(matching_sse),
            "real_event_ids": real_event_ids,
            "db_only_withheld": snap_db_only.withheld_notes.value,
            "db_plus_sse_withheld": snap_db_plus_sse.withheld_notes.value,
            "db_only_breaker": snap_db_only.breaker_advisories.value,
            "db_plus_sse_breaker": snap_db_plus_sse.breaker_advisories.value,
        },
    }
    (out_dir / "d10-observer-decision-037.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB02 Decision 037 D10 observer supplement")
    ap.add_argument("--engine-bin", required=True)
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--expected-engine-sha256", default="")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb02-d10-observer-r2")
    args = ap.parse_args(argv)
    summary = run_d10_worker_and_event_validation(
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
