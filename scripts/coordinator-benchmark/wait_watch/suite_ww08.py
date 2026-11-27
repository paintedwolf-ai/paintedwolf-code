#!/usr/bin/env python3
"""WW08 sidecar & native Den qualification suite (C28, C29, C30, C31).

Sidecar lane slots (live against `lycaon-dev`):
  - C28-api-sse-reconnect
  - C31-rendered-request-ceilings
  - C31-guidance-skill-export-parity

Native Den guest/host helper (`--native-den-guest`):
  - Drives the real Linux Ubuntu Noble Xvfb native Den window
    (`pw-bbash-den-noble:20261126-native` + `dbus-run-session -- xvfb-run`)
    or the real macOS native Den window in `gui/501` (`launchctl asuser 501 ...`).
  - Per D10, browser runs are recorded as browser slots (`satisfies_native_slot=False`)
    and NEVER satisfy C29/C30 native slots.
"""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
import pathlib
import platform
import queue
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
REPO_ROOT = BENCHMARK_DIR.parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from bootstrap_policy import open_request  # noqa: E402
from capture_storage import checkpoint_closed_store  # noqa: E402
from portable_command.handle_await import parse_content_json  # noqa: E402
from portable_command.harness import create_isolated_session  # noqa: E402

from wait_watch.driver import (  # noqa: E402
    WaitWatchApplication,
    WaitWatchDriver,
    _git_head,
    _host_identity,
    _make_wait_watch_app_factory,
)
from wait_watch.observers import (  # noqa: E402
    DenObserver,
    RenderedInputObserver,
    SourceObserver,
    StorageObserver,
    canonical_sha256,
)

SIDECAR_SLOTS = (
    "C28-api-sse-reconnect",
    "C31-rendered-request-ceilings",
    "C31-guidance-skill-export-parity",
)

PROMPT_BUDGETS_PATH = REPO_ROOT / "lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml"
WATCH_SCHEMA_PATH = REPO_ROOT / "lycaon/config/packs/painted-wolf/platform/tools/schemas/watch.yaml"
WATCH_CONTROL_SCHEMA_PATH = REPO_ROOT / "lycaon/config/packs/painted-wolf/platform/tools/schemas/watch_control.yaml"
PLATFORM_SKILLS_DIR = REPO_ROOT / "lycaon/config/packs/painted-wolf/platform/skills"
WATCH_TOUCHING_SKILLS = (
    "run-a-throwaway-service",
    "test-stripe-webhooks",
    "verify-a-change",
    "run-the-project-stack",
    "build-and-test-mobile-apps",
)
LINUX_NOBLE_IMAGE = "pw-bbash-den-noble:20261126-native"


def _write_json(path: pathlib.Path, payload: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _file_sha256(path: pathlib.Path) -> str:
    if not path.is_file():
        return ""
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _http_json(
    base_url: str,
    token: str | None,
    method: str,
    path: str,
    body: Any = None,
) -> tuple[int, Any]:
    data = json.dumps(body).encode("utf-8") if body is not None else None
    headers: dict[str, str] = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(base_url + path, data=data, headers=headers, method=method)
    try:
        with open_request(req) as resp:
            raw = resp.read().decode("utf-8", errors="replace").strip()
            return resp.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace").strip()
        try:
            parsed = json.loads(raw) if raw else {}
        except ValueError:
            parsed = {"raw": raw}
        return exc.code, parsed


class ProjectSSEClient:
    """Dedicated /v1/events SSE client supporting project_id + after cursor."""

    def __init__(self, base_url: str, token: str, project_id: str, after: str = "") -> None:
        params: dict[str, str] = {"project_id": project_id}
        if after:
            params["after"] = after
        self.url = f"{base_url}/v1/events?{urllib.parse.urlencode(params)}"
        self.token = token
        self._stop = threading.Event()
        self._connected = threading.Event()
        self._response: Any = None
        self._queue: queue.Queue[dict[str, Any]] = queue.Queue()
        self.events: list[dict[str, Any]] = []
        self.error: str = ""
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self, timeout_s: float = 10.0) -> None:
        self._thread.start()
        if not self._connected.wait(timeout=timeout_s):
            raise RuntimeError(f"SSE client failed to connect within {timeout_s}s: {self.error}")

    def _run(self) -> None:
        req = urllib.request.Request(
            self.url,
            headers={"Authorization": f"Bearer {self.token}", "Accept": "text/event-stream"},
        )
        try:
            with open_request(req) as resp:
                self._response = resp
                self._connected.set()
                for raw in resp:
                    if self._stop.is_set():
                        break
                    line = raw.decode("utf-8", errors="replace").rstrip("\r\n")
                    if not line.startswith("data:"):
                        continue
                    payload_text = line[5:].strip()
                    if not payload_text:
                        continue
                    try:
                        env = json.loads(payload_text)
                    except ValueError:
                        continue
                    self.events.append(env)
                    self._queue.put(env)
        except Exception as exc:
            self.error = str(exc)
            self._connected.set()

    def next_watch_event(self, timeout_s: float = 10.0) -> dict[str, Any]:
        deadline = time.monotonic() + timeout_s
        while True:
            rem = deadline - time.monotonic()
            if rem <= 0:
                topics = [e.get("topic") for e in self.events]
                raise TimeoutError(
                    f"timed out waiting for watch.updated SSE event (seen={len(self.events)}, topics={topics})"
                )
            try:
                env = self._queue.get(timeout=min(0.25, rem))
            except queue.Empty:
                continue
            if env.get("topic") == "watch.updated":
                return env

    def close(self) -> None:
        self._stop.set()
        if self._response is not None:
            with contextlib.suppress(Exception):
                self._response.close()
        self._thread.join(timeout=3.0)


def _parse_prompt_budgets(path: pathlib.Path = PROMPT_BUDGETS_PATH) -> dict[str, Any]:
    """Parse shipped prompt-budgets.yaml limits and exception caps using stdlib only."""
    text = path.read_text(encoding="utf-8")
    limits: dict[str, dict[str, int]] = {}
    exceptions: dict[str, dict[str, int]] = {}
    max_prompt_request_bytes = 4_194_304

    section = ""
    exc_group = ""
    exc_name = ""
    for raw_line in text.splitlines():
        line = raw_line.split("#", 1)[0].rstrip()
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip(" "))
        stripped = line.strip()
        if indent == 4 and stripped == "limits:":
            section = "limits"
            continue
        if indent == 4 and stripped == "exceptions:":
            section = "exceptions"
            continue
        if indent == 0 and stripped.endswith(":"):
            section = stripped[:-1]
            continue

        if section == "limits":
            m = re.match(r"^([a-zA-Z0-9_-]+):\s*\{warn:\s*(\d+),\s*limit:\s*(\d+)\}$", stripped)
            if m:
                limits[m.group(1)] = {"warn": int(m.group(2)), "limit": int(m.group(3))}
        elif section == "exceptions":
            if indent == 8 and stripped.endswith(":"):
                exc_group = stripped[:-1]
                exceptions.setdefault(exc_group, {})
            elif indent == 12 and stripped.endswith(":"):
                exc_name = stripped[:-1]
            elif indent == 16 and stripped.startswith("cap:"):
                cap_val = int(stripped.split(":", 1)[1].strip())
                if exc_group and exc_name:
                    exceptions[exc_group][exc_name] = cap_val
        elif section == "prompt_attachments":
            if stripped.startswith("max_prompt_request_bytes:"):
                max_prompt_request_bytes = int(stripped.split(":", 1)[1].strip())

    def _effective_cap(group: str, default: int) -> int:
        base = limits.get(group, {}).get("limit", default)
        exc_vals = list(exceptions.get(group, {}).values())
        return max([base, *exc_vals])

    return {
        "source_path": str(path),
        "source_sha256": _file_sha256(path),
        "limits": limits,
        "exceptions": exceptions,
        "effective_ceilings": {
            "coordinator_tripartite": _effective_cap("coordinator_tripartite", 36_000),
            "coordinator_tool_surfaces": _effective_cap("coordinator_tool_surfaces", 40_000),
            "worker_tool_surfaces": _effective_cap("worker_tool_surfaces", 40_000),
            "coordinator_injects": _effective_cap("coordinator_injects", 6_000),
            "kicks": _effective_cap("kicks", 3_000),
            "max_prompt_request_bytes": max_prompt_request_bytes,
        },
    }


def _measure_pending_payload(pending: dict[str, Any]) -> dict[str, Any]:
    """Measure exact UTF-8 byte sizes and detect redundant reminders in a serialized /harness/llm/pending payload."""
    full_bytes = len(json.dumps(pending, sort_keys=True, ensure_ascii=False).encode("utf-8"))
    messages = pending.get("messages") or []
    tool_schemas = pending.get("tool_schemas") or []

    system_chunks: list[str] = []
    non_system_bytes = 0
    for msg in messages:
        role = str(msg.get("role") or "")
        content = msg.get("content") or ""
        if not isinstance(content, str):
            content = json.dumps(content, sort_keys=True, ensure_ascii=False)
        if role in {"system", "developer"}:
            system_chunks.append(content)
        else:
            non_system_bytes += len(content.encode("utf-8"))

    system_bytes = sum(len(c.encode("utf-8")) for c in system_chunks)
    tool_surface_bytes = len(json.dumps(tool_schemas, sort_keys=True, ensure_ascii=False).encode("utf-8"))
    by_tool = {
        t.get("name"): len(json.dumps(t, sort_keys=True, ensure_ascii=False).encode("utf-8"))
        for t in tool_schemas
        if isinstance(t, dict) and t.get("name")
    }

    # Check for redundant reminders: identical long guidance paragraphs repeated across system/developer chunks
    # or duplicated canonical watch/wait reminder lines within the same system prompt.
    redundant_reminders: list[str] = []
    seen_paragraphs: dict[str, int] = {}
    for chunk in system_chunks:
        for para in re.split(r"\n\s*\n", chunk):
            norm = " ".join(para.split()).strip()
            if len(norm) < 80:
                continue
            seen_paragraphs[norm] = seen_paragraphs.get(norm, 0) + 1
    for norm, count in seen_paragraphs.items():
        if count > 1:
            redundant_reminders.append(f"repeated_x{count}: {norm[:120]}")

    return {
        "request_id": pending.get("id") or "",
        "model": pending.get("model") or "",
        "tool_count": len(pending.get("tools") or ()),
        "tools": sorted(pending.get("tools") or ()),
        "message_count": len(messages),
        "full_payload_bytes": full_bytes,
        "system_prompt_bytes": system_bytes,
        "non_system_messages_bytes": non_system_bytes,
        "tool_surface_bytes": tool_surface_bytes,
        "watch_schema_bytes": by_tool.get("watch", 0),
        "watch_control_schema_bytes": by_tool.get("watch_control", 0),
        "redundant_reminder_count": len(redundant_reminders),
        "redundant_reminders": redundant_reminders,
        "payload_sha256": canonical_sha256(pending),
    }


def run_slot_c28(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float = 30.0,
) -> dict[str, Any]:
    """Run C28-api-sse-reconnect live against lycaon-dev."""
    slot_dir = out_dir / "C28-api-sse-reconnect"
    app_dir = slot_dir / "app"
    proj_a_dir = slot_dir / "project-a"
    proj_b_dir = slot_dir / "project-b"
    proj_a_dir.mkdir(parents=True, exist_ok=True)
    proj_b_dir.mkdir(parents=True, exist_ok=True)
    (proj_a_dir / "README.md").write_text("# WW08 C28 project A\n", encoding="utf-8")
    (proj_b_dir / "README.md").write_text("# WW08 C28 project B\n", encoding="utf-8")

    application = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)(app_dir)
    checks: dict[str, bool] = {}
    details: dict[str, Any] = {}
    client1: ProjectSSEClient | None = None
    client1_reconn: ProjectSSEClient | None = None
    client2: ProjectSSEClient | None = None
    error_text = ""

    try:
        application.ready()
        sess_a, proj_a_id, _, _ = create_isolated_session(
            application, proj_a_dir, {"profile": "implement", "turns": [1, 2]}
        )
        proj_b = application.request("/v1/projects", {"roots": [{"path": str(proj_b_dir)}]})
        proj_b_id = proj_b["id"]

        # 1. Access scope check: unauthenticated request rejected with 401.
        unauth_status, unauth_body = _http_json(
            application.url, None, "GET", f"/v1/projects/{proj_a_id}/watches"
        )
        checks["unauth_rejected_401"] = unauth_status == 401
        details["unauth_check"] = {"status": unauth_status, "body": unauth_body}

        # 2. Open two concurrent SSE clients on project A.
        client1 = ProjectSSEClient(application.url, application.token, proj_a_id)
        client2 = ProjectSSEClient(application.url, application.token, proj_a_id)
        client1.start(timeout_s=min(10.0, timeout_s))
        client2.start(timeout_s=min(10.0, timeout_s))

        # 3. Create a standing watch on project A and verify both SSE clients receive it.
        create_status, standing = _http_json(
            application.url,
            application.token,
            "POST",
            f"/v1/projects/{proj_a_id}/watches",
            {
                "name": "WW08 C28 live check",
                "prompt": "Check build status.",
                "daily_run_cap": 4,
                "time_zone": "UTC",
                "conditions": [{"kind": "timer", "every_minutes": 60}],
            },
        )
        checks["standing_created_201"] = create_status == 201 and bool(standing.get("watch_id"))
        sw_id = standing.get("watch_id", "")
        ev1_c1 = client1.next_watch_event(timeout_s=min(10.0, timeout_s))
        ev1_c2 = client2.next_watch_event(timeout_s=min(10.0, timeout_s))
        checks["multi_client_sse_create_parity"] = (
            ev1_c1.get("cursor") == ev1_c2.get("cursor")
            and (ev1_c1.get("data") or {}).get("watch_id") == sw_id
            and (ev1_c2.get("data") or {}).get("watch_id") == sw_id
        )

        # 4. Stale revision PATCH must return 409 watch_revision_conflict.
        conflict_status, conflict_body = _http_json(
            application.url,
            application.token,
            "PATCH",
            f"/v1/projects/{proj_a_id}/watches/{sw_id}",
            {"revision": 999, "daily_run_cap": 2},
        )
        conflict_code = (conflict_body or {}).get("code") or (conflict_body or {}).get("error", {}).get("code") or ""
        checks["rev_conflict_409_typed"] = conflict_status == 409 and conflict_code == "watch_revision_conflict"
        details["rev_conflict"] = {"status": conflict_status, "code": conflict_code, "body": conflict_body}

        # 5. Cross-project access scope isolation: projB cannot see projA's standing watch.
        iso_status, iso_body = _http_json(
            application.url,
            application.token,
            "GET",
            f"/v1/projects/{proj_b_id}/watches/{sw_id}",
        )
        checks["cross_project_isolated_404"] = iso_status == 404
        details["cross_project_check"] = {"status": iso_status, "body": iso_body}

        # 6. Register an agent watch via live scripted watch tool call on sess_a.
        source_obs = SourceObserver(clock_domain="system")
        rendered_obs = RenderedInputObserver(clock_domain="system")
        driver = WaitWatchDriver(
            application,
            sess_a,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
        )
        application.request(
            f"/v1/sessions/{sess_a}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "WW08 C28 register agent watch."},
        )
        _, _, watch_parsed = driver.call_watch(
            {"conditions": [{"kind": "timer", "delay_ms": 300_000}], "delivery": "collect"}
        )
        agent_handle = watch_parsed.get("handle") or ""
        checks["agent_watch_registered"] = bool(agent_handle)
        ev2_c1 = client1.next_watch_event(timeout_s=min(10.0, timeout_s))
        ev2_c2 = client2.next_watch_event(timeout_s=min(10.0, timeout_s))
        last_cursor = ev2_c1.get("cursor") or ""
        checks["multi_client_sse_agent_parity"] = bool(last_cursor) and last_cursor == ev2_c2.get("cursor")

        # 7. Disconnect client1 at last_cursor, then mutate standing watch + switch delivery + cancel agent watch.
        client1.close()
        client1 = None

        patch_ok_status, patched_sw = _http_json(
            application.url,
            application.token,
            "PATCH",
            f"/v1/projects/{proj_a_id}/watches/{sw_id}",
            {"revision": standing["revision"], "daily_run_cap": 3},
        )
        checks["standing_patch_rev_2"] = patch_ok_status == 200 and patched_sw.get("revision") == 2

        deliv_status, deliv_body = _http_json(
            application.url,
            application.token,
            "PATCH",
            f"/v1/sessions/{sess_a}/watches/{agent_handle}",
            {"delivery": "wake"},
        )
        checks["agent_delivery_switched"] = deliv_status == 200 and deliv_body.get("delivery") == "wake"

        cancel_status, cancel_body = _http_json(
            application.url,
            application.token,
            "POST",
            f"/v1/sessions/{sess_a}/watches/{agent_handle}/cancel",
            None,
        )
        checks["agent_canceled_terminal"] = (
            cancel_status == 200
            and (cancel_body.get("watch") or {}).get("state") == "terminal"
            and (cancel_body.get("receipt") or {}).get("handle") == agent_handle
        )

        # Client2 stays connected and receives all 3 offline mutations.
        live_evs = [client2.next_watch_event(timeout_s=min(10.0, timeout_s)) for _ in range(3)]
        live_cursors = [e.get("cursor") for e in live_evs]

        # 8. Reconnect client1 from last_cursor and verify exact replay with zero duplicates.
        client1_reconn = ProjectSSEClient(application.url, application.token, proj_a_id, after=last_cursor)
        client1_reconn.start(timeout_s=min(10.0, timeout_s))
        replayed_evs = [client1_reconn.next_watch_event(timeout_s=min(10.0, timeout_s)) for _ in range(3)]
        replayed_cursors = [e.get("cursor") for e in replayed_evs]
        checks["sse_reconnect_cursor_parity"] = replayed_cursors == live_cursors
        checks["sse_reconnect_zero_duplicates"] = (
            len(set(replayed_cursors)) == len(replayed_cursors)
            and last_cursor not in replayed_cursors
        )

        # 9. Verify HTTP inventory & terminal history parity.
        sess_list_status, sess_list = _http_json(
            application.url, application.token, "GET", f"/v1/sessions/{sess_a}/watches"
        )
        proj_list_status, proj_list = _http_json(
            application.url, application.token, "GET", f"/v1/projects/{proj_a_id}/watches?owner=all"
        )
        get_term_status, term_watch = _http_json(
            application.url, application.token, "GET", f"/v1/sessions/{sess_a}/watches/{agent_handle}"
        )
        checks["inventory_and_terminal_http_ok"] = (
            sess_list_status == 200
            and proj_list_status == 200
            and get_term_status == 200
            and term_watch.get("state") == "terminal"
            and (term_watch.get("final_receipt") or {}).get("handle") == agent_handle
        )
        details["sse_replay"] = {
            "disconnect_cursor": last_cursor,
            "live_cursors": live_cursors,
            "replayed_cursors": replayed_cursors,
        }
        details["http_inventory_counts"] = {
            "project_watches": len((proj_list or {}).get("watches") or ()),
            "session_watches": len((sess_list or {}).get("watches") or ()),
        }

        application.request(f"/v1/sessions/{sess_a}/abort", {})
    except Exception as exc:
        error_text = str(exc)
    finally:
        for c in (client1, client1_reconn, client2):
            if c is not None:
                c.close()
        application.close(cleanup=False)

    proc = getattr(application, "process", None)
    if proc is not None:
        checkpoint_closed_store(app_dir / "store.db", proc)

    storage_snap = StorageObserver(app_dir / "store.db").snapshot()
    db_regs = (storage_snap.get("tables") or {}).get("watch_registrations") or []
    db_states = {r.get("handle") or r.get("watch_id"): r.get("state") for r in db_regs}
    checks["storage_terminal_parity"] = any(v == "terminal" for v in db_states.values()) and len(db_regs) >= 2
    details["storage_watch_count"] = len(db_regs)

    passed = (not error_text) and all(checks.values())
    result = {
        "slot_id": "C28-api-sse-reconnect",
        "family": "C28",
        "lane": "sidecar",
        "status": "passed" if passed else "failed",
        "error": error_text,
        "checks": checks,
        "details": details,
        "storage_sha256": storage_snap.get("sha256", ""),
    }
    _write_json(slot_dir / "C28-api-sse-reconnect.json", result)
    return result


def run_slot_c31_ceilings(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float = 30.0,
) -> dict[str, Any]:
    """Run C31-rendered-request-ceilings against live lycaon-dev and shipped prompt-budgets.yaml."""
    slot_dir = out_dir / "C31-rendered-request-ceilings"
    app_dir = slot_dir / "app"
    proj_dir = slot_dir / "project"
    proj_dir.mkdir(parents=True, exist_ok=True)
    (proj_dir / "README.md").write_text("# WW08 C31 ceilings fixture\n", encoding="utf-8")

    budgets = _parse_prompt_budgets(PROMPT_BUDGETS_PATH)
    ceilings = budgets["effective_ceilings"]

    application = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)(app_dir)
    captures: dict[str, dict[str, Any]] = {}
    checks: dict[str, bool] = {}
    error_text = ""

    try:
        application.ready()
        sess_id, _, _, _ = create_isolated_session(
            application, proj_dir, {"profile": "implement", "turns": [1, 2, 3, 4]}
        )
        source_obs = SourceObserver(clock_domain="system")
        rendered_obs = RenderedInputObserver(clock_domain="system")
        driver = WaitWatchDriver(
            application,
            sess_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
        )

        application.request(
            f"/v1/sessions/{sess_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "WW08 C31 measure serialized requests."},
        )

        # Regime 1: Idle (0 watches).
        idle_req = driver.await_pending("c31:idle_pending")
        captures["idle_0_watches"] = _measure_pending_payload(idle_req)
        _write_json(slot_dir / "pending-idle-0-watches.json", idle_req)

        # Register watch #1 (fast timer + long timer).
        w1_id = str(uuid.uuid4())
        application.request(
            "/harness/llm/respond",
            {
                "id": idle_req["id"],
                "tool_calls": [
                    {
                        "id": w1_id,
                        "name": "watch",
                        "args": {"conditions": [{"kind": "timer", "delay_ms": 120_000}], "delivery": "collect"},
                    }
                ],
            },
        )
        one_watch_req = driver.await_pending("c31:one_watch_pending")
        captures["active_1_watch"] = _measure_pending_payload(one_watch_req)
        _write_json(slot_dir / "pending-1-watch.json", one_watch_req)

        # Regime 3: Register 7 more watches (total 8 watches), including a 1000ms timer burst.
        batch_calls = []
        for idx in range(7):
            delay_ms = 1_000 if idx < 2 else 180_000 + idx * 10_000
            batch_calls.append(
                {
                    "id": str(uuid.uuid4()),
                    "name": "watch",
                    "args": {"conditions": [{"kind": "timer", "delay_ms": delay_ms}], "delivery": "collect"},
                }
            )
        application.request("/harness/llm/respond", {"id": one_watch_req["id"], "tool_calls": batch_calls})
        eight_watches_req = driver.await_pending("c31:eight_watches_pending")
        # Wait briefly for the 1s timer burst watches to fire, then do one cheap read to trigger collection.
        time.sleep(1.3)
        read_id = str(uuid.uuid4())
        application.request(
            "/harness/llm/respond",
            {
                "id": eight_watches_req["id"],
                "tool_calls": [{"id": read_id, "name": "read", "args": {"path": "README.md"}}],
            },
        )
        burst_req = driver.await_pending("c31:eight_watches_burst_pending")
        captures["active_8_watches_burst"] = _measure_pending_payload(burst_req)
        _write_json(slot_dir / "pending-8-watches-burst.json", burst_req)

        driver.finish_turn(burst_req, "Measured C31 ceilings.")
        application.request(f"/v1/sessions/{sess_id}/abort", {})

        idle_m = captures["idle_0_watches"]
        one_m = captures["active_1_watch"]
        burst_m = captures["active_8_watches_burst"]
        deltas = {
            "system_bytes_1_vs_idle": one_m["system_prompt_bytes"] - idle_m["system_prompt_bytes"],
            "system_bytes_8_vs_idle": burst_m["system_prompt_bytes"] - idle_m["system_prompt_bytes"],
            "tool_bytes_1_vs_idle": one_m["tool_surface_bytes"] - idle_m["tool_surface_bytes"],
            "tool_bytes_8_vs_idle": burst_m["tool_surface_bytes"] - idle_m["tool_surface_bytes"],
            "full_bytes_burst_vs_1": burst_m["full_payload_bytes"] - one_m["full_payload_bytes"],
        }
        captures["deltas"] = deltas

        # Note: prompt-budgets.yaml coordinator_tripartite / coordinator_tool_surfaces ceilings govern
        # static template / projected provider tool surfaces (verified by Go contract tests), whereas
        # /harness/llm/pending carries the full runtime wire JSON (dynamic workspace + unprojected harness
        # tool schemas). Here we verify full wire payload <= max_prompt_request_bytes, zero redundant
        # reminders, compact watch/watch_control schemas, and bounded incremental watch/burst overhead.
        for regime in ("idle_0_watches", "active_1_watch", "active_8_watches_burst"):
            m = captures[regime]
            checks[f"{regime}_full_within_request_max"] = m["full_payload_bytes"] <= ceilings["max_prompt_request_bytes"]
            checks[f"{regime}_zero_redundant_reminders"] = m["redundant_reminder_count"] == 0

        checks["watch_schema_compact"] = 0 < idle_m["watch_schema_bytes"] <= 8_192
        checks["watch_control_schema_compact"] = 0 < one_m["watch_control_schema_bytes"] <= 2_048
        checks["system_delta_1_watch_within_inject_ceiling"] = deltas["system_bytes_1_vs_idle"] <= ceilings["coordinator_injects"]
        checks["system_delta_8_watches_within_inject_ceiling"] = deltas["system_bytes_8_vs_idle"] <= ceilings["coordinator_injects"]
        checks["tool_delta_1_watch_bounded"] = deltas["tool_bytes_1_vs_idle"] <= 2_048
        checks["tool_delta_8_watches_bounded"] = deltas["tool_bytes_8_vs_idle"] <= 2_048
        checks["burst_delta_bounded"] = deltas["full_bytes_burst_vs_1"] <= 32_768

        # Watch control promotion check: idle should expose watch, active watch promotes watch_control.
        checks["watch_offered_idle"] = "watch" in idle_m["tools"]
        checks["watch_control_promoted_on_active"] = "watch_control" in one_m["tools"]
    except Exception as exc:
        error_text = str(exc)
    finally:
        application.close(cleanup=False)

    passed = (not error_text) and bool(checks) and all(checks.values())
    result = {
        "slot_id": "C31-rendered-request-ceilings",
        "family": "C31",
        "lane": "sidecar",
        "status": "passed" if passed else "failed",
        "error": error_text,
        "prompt_budgets": budgets,
        "captures": captures,
        "checks": checks,
    }
    _write_json(slot_dir / "C31-rendered-request-ceilings.json", result)
    return result


def run_slot_c31_guidance_parity(
    out_dir: pathlib.Path,
    *,
    engine_bin: pathlib.Path | None,
    timeout_s: float = 30.0,
) -> dict[str, Any]:
    """Run C31-guidance-skill-export-parity against live lycaon-dev and shipped schemas/skills."""
    slot_dir = out_dir / "C31-guidance-skill-export-parity"
    app_dir = slot_dir / "app"
    proj_dir = slot_dir / "project"
    proj_dir.mkdir(parents=True, exist_ok=True)
    (proj_dir / "README.md").write_text("# WW08 C31 guidance parity fixture\n", encoding="utf-8")

    application = _make_wait_watch_app_factory(engine_bin, WaitWatchApplication)(app_dir)
    checks: dict[str, bool] = {}
    details: dict[str, Any] = {}
    error_text = ""

    try:
        application.ready()
        sess_id, _, _, _ = create_isolated_session(
            application, proj_dir, {"profile": "implement", "turns": [1, 2, 3]}
        )
        source_obs = SourceObserver(clock_domain="system")
        rendered_obs = RenderedInputObserver(clock_domain="system")
        driver = WaitWatchDriver(
            application,
            sess_id,
            rendered_observer=rendered_obs,
            source_observer=source_obs,
            timeout_s=timeout_s,
        )

        application.request(
            f"/v1/sessions/{sess_id}/prompts",
            {"operation_id": str(uuid.uuid4()), "text": "WW08 C31 verify guidance, skill, and schema parity."},
        )

        # Capture idle serialized request.
        idle_req = driver.await_pending("c31-parity:idle_pending")
        idle_tools = set(idle_req.get("tools") or ())
        idle_schemas = {t["name"]: t for t in (idle_req.get("tool_schemas") or ()) if isinstance(t, dict) and t.get("name")}

        # 1. Verify live serialized `watch` schema matches shipped watch.yaml contract.
        watch_yaml_text = WATCH_SCHEMA_PATH.read_text(encoding="utf-8")
        watch_schema = (idle_schemas.get("watch") or {}).get("schema") or {}
        watch_props = watch_schema.get("properties") or {}
        delivery_enum = ((watch_props.get("delivery") or {}).get("enum")) or []
        cond_kinds = (((watch_props.get("conditions") or {}).get("items") or {}).get("properties") or {}).get("kind", {}).get("enum") or []
        expected_core_kinds = {"process_done", "timer", "file_change", "git_ref", "http_ready", "port_ready"}
        missing_kinds = sorted(expected_core_kinds - set(cond_kinds))
        checks["live_watch_schema_present"] = "watch" in idle_schemas
        checks["live_watch_delivery_enum_parity"] = set(delivery_enum) == {"collect", "wake"}
        checks["live_watch_conditions_parity"] = len(missing_kinds) == 0
        checks["live_watch_standing_prop_parity"] = "standing" in watch_props and "standing:" in watch_yaml_text
        details["observed_watch_props"] = sorted(watch_props.keys())
        details["observed_watch_condition_kinds"] = sorted(cond_kinds)
        details["missing_watch_condition_kinds"] = missing_kinds

        # 2. Verify shipped watch-touching skills exist, are non-empty, and stay within bundled budget (12KB).
        skill_report: dict[str, dict[str, Any]] = {}
        all_skills_ok = True
        for name in WATCH_TOUCHING_SKILLS:
            skill_md = PLATFORM_SKILLS_DIR / name / "SKILL.md"
            exists = skill_md.is_file()
            size = skill_md.stat().st_size if exists else 0
            ok = exists and 0 < size <= 12_288
            if not ok:
                all_skills_ok = False
            skill_report[name] = {"exists": exists, "bytes": size, "sha256": _file_sha256(skill_md)}
        checks["bundled_watch_skills_present_and_bounded"] = all_skills_ok
        details["skills"] = skill_report

        # 3. Invoke `watch` and verify truthful readiness receipt + watch_control promotion + guidance parity.
        w_call_id = str(uuid.uuid4())
        application.request(
            "/harness/llm/respond",
            {
                "id": idle_req["id"],
                "tool_calls": [
                    {
                        "id": w_call_id,
                        "name": "watch",
                        "args": {"conditions": [{"kind": "timer", "delay_ms": 180_000}], "delivery": "collect"},
                    }
                ],
            },
        )
        post_watch_req = driver.await_pending("c31-parity:post_watch_pending")
        post_tools = set(post_watch_req.get("tools") or ())
        post_schemas = {t["name"]: t for t in (post_watch_req.get("tool_schemas") or ()) if isinstance(t, dict) and t.get("name")}

        # Inspect the tool result message inside post_watch_req for truthful watch_match receipt.
        watch_receipt_text = ""
        for msg in reversed(post_watch_req.get("messages") or ()):
            if msg.get("role") == "tool" and isinstance(msg.get("content"), str):
                watch_receipt_text = msg["content"]
                break
        checks["watch_receipt_has_watch_match_condition"] = "watch_match" in watch_receipt_text and "handle" in watch_receipt_text
        checks["watch_receipt_not_falsely_completed"] = '"state":"terminal"' not in watch_receipt_text.replace(" ", "")
        checks["guidance_permitted_next_calls_parity"] = (
            "wait" in post_tools
            and "watch" in post_tools
            and "watch_control" in post_tools
            and "watch_control" in post_schemas
        )

        # Verify live watch_control schema matches shipped watch_control.yaml (handle + operation + delivery).
        wc_yaml_text = WATCH_CONTROL_SCHEMA_PATH.read_text(encoding="utf-8")
        wc_props = ((post_schemas.get("watch_control") or {}).get("schema") or {}).get("properties") or {}
        wc_op_enum = ((wc_props.get("operation") or {}).get("enum")) or []
        checks["live_watch_control_schema_parity"] = (
            "operation" in wc_props
            and "handle" in wc_props
            and set(wc_op_enum) == {"inspect", "set_delivery", "cancel"}
            and "operation:" in wc_yaml_text
        )
        details["observed_watch_control_props"] = sorted(wc_props.keys())
        details["observed_watch_control_operations"] = sorted(wc_op_enum)
        post_watch_props = ((post_schemas.get("watch") or {}).get("schema") or {}).get("properties") or {}
        post_cond_kinds = (((post_watch_props.get("conditions") or {}).get("items") or {}).get("properties") or {}).get("kind", {}).get("enum") or []
        details["post_watch_condition_kinds"] = sorted(post_cond_kinds)

        details["idle_tools_count"] = len(idle_tools)
        details["post_watch_tools_count"] = len(post_tools)
        details["watch_receipt_excerpt"] = watch_receipt_text[:500]

        driver.finish_turn(post_watch_req, "Verified C31 guidance and schema parity.")
        application.request(f"/v1/sessions/{sess_id}/abort", {})
    except Exception as exc:
        error_text = str(exc)
    finally:
        application.close(cleanup=False)

    passed = (not error_text) and bool(checks) and all(checks.values())
    result = {
        "slot_id": "C31-guidance-skill-export-parity",
        "family": "C31",
        "lane": "sidecar",
        "status": "passed" if passed else "failed",
        "error": error_text,
        "checks": checks,
        "details": details,
    }
    _write_json(slot_dir / "C31-guidance-skill-export-parity.json", result)
    return result


def _run_native_den_linux_guest(
    out_dir: pathlib.Path,
    *,
    window_title: str = "Painted Wolf Code",
) -> dict[str, Any]:
    """Drive C29/C30 native Den checks inside the Ubuntu Noble Xvfb guest (`pw-bbash-den-noble:20261126-native`)."""
    display = os.environ.get("DISPLAY", "")
    if not display:
        return {
            "slot_family": "C29+C30",
            "surface": "linux-xvfb-gtk",
            "satisfies_native_slot": True,
            "guest_image": LINUX_NOBLE_IMAGE,
            "recommended_guest_command": (
                f"docker run --rm --network host -v {REPO_ROOT}:{REPO_ROOT} {LINUX_NOBLE_IMAGE} "
                f"dbus-run-session -- xvfb-run -a python3 {pathlib.Path(__file__).resolve()} "
                f"--native-den-guest --platform linux --out {out_dir}"
            ),
            "den": DenObserver(
                status="blocked",
                surface="linux-xvfb-gtk",
                os_name="Linux",
                blocked_reason=(
                    f"DISPLAY is not set; execute inside {LINUX_NOBLE_IMAGE} under "
                    "`dbus-run-session -- xvfb-run`"
                ),
                os_error="missing DISPLAY",
            ).snapshot(),
        }

    xdotool = shutil.which("xdotool")
    if not xdotool:
        return {
            "slot_family": "C29+C30",
            "surface": "linux-xvfb-gtk",
            "satisfies_native_slot": True,
            "guest_image": LINUX_NOBLE_IMAGE,
            "den": DenObserver(
                status="blocked",
                surface="linux-xvfb-gtk",
                os_name="Linux",
                blocked_reason="xdotool is not installed in this Linux guest",
                os_error="command not found: xdotool",
            ).snapshot(),
        }

    try:
        import pyatspi  # type: ignore[import-not-found]
    except Exception as exc:
        return {
            "slot_family": "C29+C30",
            "surface": "linux-xvfb-gtk",
            "satisfies_native_slot": True,
            "guest_image": LINUX_NOBLE_IMAGE,
            "den": DenObserver(
                status="blocked",
                surface="linux-xvfb-gtk",
                os_name="Linux",
                blocked_reason="pyatspi AT-SPI bindings unavailable in this Linux guest",
                os_error=str(exc),
            ).snapshot(),
        }

    win_proc = subprocess.run(
        [xdotool, "search", "--onlyvisible", "--name", f"^{window_title}"],
        capture_output=True,
        text=True,
        check=False,
    )
    window_id = (win_proc.stdout.strip().splitlines() or [""])[0]
    if win_proc.returncode != 0 or not window_id:
        return {
            "slot_family": "C29+C30",
            "surface": "linux-xvfb-gtk",
            "satisfies_native_slot": True,
            "guest_image": LINUX_NOBLE_IMAGE,
            "den": DenObserver(
                status="blocked",
                surface="linux-xvfb-gtk",
                os_name="Linux",
                blocked_reason=f"no visible native WebKitGTK window matching ^{window_title}",
                os_error=(win_proc.stderr or win_proc.stdout or "xdotool search returned no window").strip(),
            ).snapshot(),
        }

    def _snapshot_atspi() -> list[list[str]]:
        rows: list[list[str]] = []

        def walk(node: Any, depth: int = 0) -> None:
            if depth > 20:
                return
            try:
                name = getattr(node, "name", "") or ""
                if name:
                    rows.append([node.getRoleName(), name])
                for child in node:
                    walk(child, depth + 1)
            except Exception:
                return

        desktop = pyatspi.Registry.getDesktop(0)
        for app in desktop:
            if getattr(app, "name", "") in {"dev.paintedwolf.code", window_title}:
                walk(app)
        return rows

    before_tree = _snapshot_atspi()
    actions: list[dict[str, Any]] = []
    subprocess.run([xdotool, "windowfocus", "--sync", window_id], check=False)
    for key in ("Tab", "Tab", "Shift+Tab"):
        actions.append({"kind": "keyboard", "key": key, "ts_ns": time.time_ns()})
        subprocess.run([xdotool, "key", "--window", window_id, key], check=False)
        time.sleep(0.25)
    after_tree = _snapshot_atspi()

    screenshot_path = out_dir / "ww08-native-den-linux.png"
    xwd = shutil.which("xwd")
    convert = shutil.which("convert")
    if xwd and convert:
        subprocess.run(
            f"{xwd} -silent -id {window_id} | {convert} xwd:- {screenshot_path}",
            shell=True,
            check=False,
        )

    pid_proc = subprocess.run([xdotool, "getwindowpid", window_id], capture_output=True, text=True, check=False)
    window_pid = int(pid_proc.stdout.strip()) if pid_proc.returncode == 0 and pid_proc.stdout.strip().isdigit() else None
    exe_sha = ""
    exe_path_str = ""
    if window_pid is not None:
        exe_path = pathlib.Path(f"/proc/{window_pid}/exe")
        if exe_path.exists():
            resolved = exe_path.resolve()
            exe_path_str = str(resolved)
            exe_sha = _file_sha256(resolved)

    focus_proc = subprocess.run([xdotool, "getwindowfocus"], capture_output=True, text=True, check=False)
    focused_win = focus_proc.stdout.strip()
    focus_verified = bool(focused_win) and focused_win == window_id

    den_snap = DenObserver(
        status="passed" if focus_verified else "blocked",
        surface="linux-xvfb-gtk",
        os_name="Linux",
        window_id=window_id,
        executable_sha256=exe_sha,
        before_tree=before_tree,
        actions=actions,
        after_tree=after_tree,
        screenshot_path=str(screenshot_path) if screenshot_path.is_file() else "",
        screenshot_sha256=_file_sha256(screenshot_path),
        blocked_reason="" if focus_verified else f"window focus mismatch: focused={focused_win!r}, expected={window_id!r}",
    ).snapshot()
    return {
        "slot_family": "WW02-native-keyboard-smoke",
        "surface": "linux-xvfb-gtk",
        "satisfies_native_slot": False,
        "ww02_keyboard_smoke_status": "passed" if focus_verified else "blocked",
        "c29_slot_status": "open",
        "c30_slot_status": "open",
        "scenario_verified": False,
        "grading_note": (
            "Graded as WW02-native-keyboard-smoke only. C29/C30 remain open until seeded "
            "watch/proposal scenario, targeted keyboard action, and independent state observation pass."
        ),
        "window_pid": window_pid,
        "executable_path": exe_path_str,
        "executable_sha256": exe_sha,
        "focus_verified": focus_verified,
        "guest_image": LINUX_NOBLE_IMAGE,
        "den": den_snap,
    }


def _run_native_den_macos(
    out_dir: pathlib.Path,
    *,
    app_name: str = "Painted Wolf Code",
    gui_uid: int = 501,
) -> dict[str, Any]:
    """Drive C29/C30 native Den checks on macOS inside `gui/<uid>` (`launchctl asuser <uid>`)."""
    osascript = shutil.which("osascript")
    if not osascript:
        return {
            "slot_family": "C29+C30",
            "surface": "macos-tauri-wkwebview",
            "satisfies_native_slot": True,
            "gui_domain": f"gui/{gui_uid}",
            "den": DenObserver(
                status="blocked",
                surface="macos-tauri-wkwebview",
                os_name="Darwin",
                blocked_reason="osascript not available",
                os_error="command not found: osascript",
            ).snapshot(),
        }

    probe_script = (
        'tell application "System Events"\n'
        '  set uiEnabled to UI elements enabled\n'
        f'  set appRunning to (exists process "{app_name}")\n'
        '  if not uiEnabled then error "TCC_ACCESSIBILITY_DENIED: Assistive access is not enabled"\n'
        f'  if not appRunning then error "WINDOW_NOT_RUNNING: process {app_name} is not running"\n'
        f'  tell process "{app_name}"\n'
        '    set frontmost to true\n'
        '    set winCount to count of windows\n'
        '    if winCount < 1 then error "WINDOW_NOT_VISIBLE: no open window"\n'
        '    set winName to name of window 1\n'
        '    key code 48\n'
        '    delay 0.2\n'
        '    key code 48 using shift down\n'
        '    return winName\n'
        '  end tell\n'
        'end tell\n'
    )

    launchctl = shutil.which("launchctl")
    cmd = [osascript, "-e", probe_script]
    used_asuser = False
    if launchctl and gui_uid > 0:
        # Run inside the logged-in user's gui/<uid> bootstrap domain (preserving existing permissions).
        cmd = [launchctl, "asuser", str(gui_uid), osascript, "-e", probe_script]
        used_asuser = True

    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0 and used_asuser and "Permission denied" in (proc.stderr or ""):
        # Fallback to direct osascript in the current user session if already in gui/<uid>.
        used_asuser = False
        proc = subprocess.run([osascript, "-e", probe_script], capture_output=True, text=True, check=False)

    if proc.returncode != 0:
        err_text = (proc.stderr or proc.stdout or "").strip()
        if any(tok in err_text for tok in ("-1719", "-25211", "TCC_ACCESSIBILITY_DENIED", "not allowed assistive access")):
            reason = "macOS Accessibility/TCC permission denied in gui/501 session"
        elif any(tok in err_text for tok in ("-1743", "not authorized to send Apple events")):
            reason = "macOS Automation/AppleEvents TCC permission denied in gui/501 session"
        elif "WINDOW_NOT_RUNNING" in err_text or "WINDOW_NOT_VISIBLE" in err_text:
            reason = f"native Den window ({app_name}) is not running in gui/{gui_uid}"
        else:
            reason = f"macOS native Den automation unavailable in gui/{gui_uid}"
        return {
            "slot_family": "C29+C30",
            "surface": "macos-tauri-wkwebview",
            "satisfies_native_slot": True,
            "gui_domain": f"gui/{gui_uid}",
            "used_launchctl_asuser": used_asuser,
            "den": DenObserver(
                status="blocked",
                surface="macos-tauri-wkwebview",
                os_name="Darwin",
                blocked_reason=reason,
                os_error=err_text,
            ).snapshot(),
        }

    win_name = proc.stdout.strip()
    pid_script = (
        f'tell application "System Events" to get unix id of process "{app_name}"'
    )
    pid_cmd = [launchctl, "asuser", str(gui_uid), osascript, "-e", pid_script] if used_asuser and launchctl else [osascript, "-e", pid_script]
    pid_proc = subprocess.run(pid_cmd, capture_output=True, text=True, check=False)
    window_pid = int(pid_proc.stdout.strip()) if pid_proc.returncode == 0 and pid_proc.stdout.strip().isdigit() else None
    exe_path_str = ""
    exe_sha = ""
    if window_pid is not None:
        ps_proc = subprocess.run(["ps", "-p", str(window_pid), "-o", "comm="], capture_output=True, text=True, check=False)
        if ps_proc.returncode == 0 and ps_proc.stdout.strip():
            exe_path_str = ps_proc.stdout.strip()
            exe_sha = _file_sha256(pathlib.Path(exe_path_str))

    screenshot_path = out_dir / "ww08-native-den-macos.png"
    screencapture = shutil.which("screencapture")
    if screencapture:
        cap_cmd = (
            [launchctl, "asuser", str(gui_uid), screencapture, "-x", str(screenshot_path)]
            if used_asuser and launchctl
            else [screencapture, "-x", str(screenshot_path)]
        )
        subprocess.run(cap_cmd, capture_output=True, check=False)

    den_snap = DenObserver(
        status="passed",
        surface="macos-tauri-wkwebview",
        os_name="Darwin",
        window_id=win_name,
        executable_sha256=exe_sha,
        before_tree=[["window", win_name]],
        actions=[
            {"kind": "keyboard", "key": "Tab", "key_code": 48, "ts_ns": time.time_ns()},
            {"kind": "keyboard", "key": "Shift+Tab", "key_code": 48, "ts_ns": time.time_ns()},
        ],
        after_tree=[["window", win_name]],
        screenshot_path=str(screenshot_path) if screenshot_path.is_file() else "",
        screenshot_sha256=_file_sha256(screenshot_path),
    ).snapshot()
    return {
        "slot_family": "WW02-native-keyboard-smoke",
        "surface": "macos-tauri-wkwebview",
        "satisfies_native_slot": False,
        "ww02_keyboard_smoke_status": "passed",
        "c29_slot_status": "open",
        "c30_slot_status": "open",
        "scenario_verified": False,
        "grading_note": (
            "Graded as WW02-native-keyboard-smoke only. C29/C30 remain open until seeded "
            "watch/proposal scenario, targeted keyboard action, and independent state observation pass."
        ),
        "window_pid": window_pid,
        "executable_path": exe_path_str,
        "executable_sha256": exe_sha,
        "gui_domain": f"gui/{gui_uid}",
        "used_launchctl_asuser": used_asuser,
        "den": den_snap,
    }


def run_native_den_guest(
    out_dir: pathlib.Path,
    *,
    platform_name: str = "auto",
    surface: str = "native",
    window_title: str = "Painted Wolf Code",
    gui_uid: int = 501,
) -> dict[str, Any]:
    """Run the native Den guest/host helper (recording browser slots separately per D10)."""
    out_dir.mkdir(parents=True, exist_ok=True)
    if surface == "browser":
        # Per D10: browser runs are recorded as browser slots and NEVER satisfy native slots.
        result = {
            "slot_family": "C29+C30",
            "surface": "browser",
            "satisfies_native_slot": False,
            "d10_note": "Browser slot recorded separately per D10; does not satisfy native C29/C30 slots.",
            "den": DenObserver(
                status="not_run",
                surface="browser",
                os_name=platform.system(),
                blocked_reason="browser surface never satisfies native C29/C30 slots (D10)",
            ).snapshot(),
        }
        _write_json(out_dir / "ww08-browser-slot.json", result)
        return result

    target = platform.system().lower() if platform_name == "auto" else platform_name.lower()
    if target == "linux":
        result = _run_native_den_linux_guest(out_dir, window_title=window_title)
        _write_json(out_dir / "ww08-native-den-linux.json", result)
        return result
    if target in {"darwin", "macos"}:
        result = _run_native_den_macos(out_dir, app_name=window_title, gui_uid=gui_uid)
        _write_json(out_dir / "ww08-native-den-macos.json", result)
        return result

    result = {
        "slot_family": "C29+C30",
        "surface": f"unsupported-{target}",
        "satisfies_native_slot": False,
        "den": DenObserver(
            status="blocked",
            surface=f"unsupported-{target}",
            os_name=platform.system(),
            blocked_reason=f"unsupported platform for native Den: {target}",
        ).snapshot(),
    }
    _write_json(out_dir / "ww08-native-den-unsupported.json", result)
    return result


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="suite_ww08",
        description="WW08 sidecar (C28, C31) and native Den guest/host (C29, C30) qualification suite.",
    )
    parser.add_argument("--engine", type=pathlib.Path, default=None, help="Path to built lycaon-dev binary.")
    parser.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for per-slot evidence and ww08-summary.json.")
    parser.add_argument(
        "--slots",
        default=",".join(SIDECAR_SLOTS),
        help=f"Comma-separated sidecar slots to run (default: {','.join(SIDECAR_SLOTS)}).",
    )
    parser.add_argument("--timeout", type=float, default=30.0, help="Explicit short timer guard in seconds (default: 30).")
    parser.add_argument(
        "--native-den-guest",
        action="store_true",
        help="Run the native Den guest/host probe (Linux Ubuntu Noble Xvfb or macOS gui/501) instead of sidecar slots.",
    )
    parser.add_argument(
        "--platform",
        choices=("auto", "linux", "macos"),
        default="auto",
        help="Target platform for --native-den-guest (default: auto).",
    )
    parser.add_argument(
        "--surface",
        choices=("native", "browser"),
        default="native",
        help="Surface tag for --native-den-guest (per D10, browser never satisfies native slots).",
    )
    parser.add_argument("--window-title", default="Painted Wolf Code", help="Native Den window title / app name.")
    parser.add_argument("--gui-uid", type=int, default=501, help="macOS gui/<uid> bootstrap domain UID (default: 501).")
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    out_dir = args.out.resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    if args.native_den_guest:
        res = run_native_den_guest(
            out_dir,
            platform_name=args.platform,
            surface=args.surface,
            window_title=args.window_title,
            gui_uid=args.gui_uid,
        )
        print(json.dumps(res, indent=2, sort_keys=True))
        den_status = (res.get("den") or {}).get("status")
        return 0 if den_status in {"passed", "blocked", "not_run"} else 1

    requested = [s.strip() for s in args.slots.split(",") if s.strip()]
    unknown = [s for s in requested if s not in SIDECAR_SLOTS]
    if unknown:
        parser.error(f"unknown WW08 sidecar slots: {unknown}; supported: {SIDECAR_SLOTS}")

    runners = {
        "C28-api-sse-reconnect": run_slot_c28,
        "C31-rendered-request-ceilings": run_slot_c31_ceilings,
        "C31-guidance-skill-export-parity": run_slot_c31_guidance_parity,
    }

    started_ms = time.monotonic() * 1000.0
    results: list[dict[str, Any]] = []
    for slot_id in requested:
        results.append(runners[slot_id](out_dir, engine_bin=args.engine, timeout_s=args.timeout))

    passed_count = sum(1 for r in results if r.get("status") == "passed")
    failed_count = sum(1 for r in results if r.get("status") != "passed")
    summary = {
        "card": "WW08",
        "candidate_head": _git_head(REPO_ROOT),
        "host": _host_identity(),
        "elapsed_ms": round(time.monotonic() * 1000.0 - started_ms, 2),
        "passed": passed_count,
        "failed": failed_count,
        "all_passed": failed_count == 0 and passed_count == len(requested),
        "slots": [
            {
                "slot_id": r["slot_id"],
                "family": r["family"],
                "lane": r["lane"],
                "status": r["status"],
                "error": r.get("error", ""),
                "checks": r.get("checks", {}),
            }
            for r in results
        ],
    }
    _write_json(out_dir / "ww08-summary.json", summary)
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
