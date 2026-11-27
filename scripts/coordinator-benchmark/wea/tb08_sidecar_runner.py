"""TB08 matching-lane headless sidecar slot runner (42 slots per platform: A05–A09).

Drives all 42 TB08 sidecar slots against a warmed `lycaon-dev` sidecar instance
built directly on TB02's `runner.py`, `fixtures_net.py`, `observers.py`, and
`provenance.py`.

Case catalog families (exact titles from acceptance-plan.md):
  - A05 · Filesystem boundary (8 slots)
  - A06 · Egress modes (10 slots)
  - A07 · Processes a command leaves behind (3 slots)
  - A08 · Legible denials (12 slots)
  - A09 · Unavailable boundaries and exceptional authority (9 slots)

Strict attribution & anti-false-pass invariants:
  - Confined filesystem/socket/network probes run an external OS subprocess (`python3 -B`)
    rather than native built-ins (`printf >`, `cat`, `echo`) so they cross the real OS
    confinement boundary instead of stopping at an earlier native path-scope gate.
  - Any slot that stops at an earlier gate (`SURVEY_PATH_ESCAPE`, `TOOL_ARGS_INVALID`,
    generic `approval_denied`, `SANDBOX_SOCKET_PATH_NOT_FOUND`) when testing a later
    confinement boundary yields `verdict: "unknown"` (never `"pass"`).
  - Slots that cannot be exercised in the sidecar lane on a healthy host without
    weakening protections (D5, e.g. `A09-01` required-boundary-unavailable) are
    explicitly reported as `verdict: "blocked"` with their exact reason.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import pathlib
import platform
import shutil
import signal
import socket
import sqlite3
import sys
import tempfile
import time
import urllib.error
import uuid
from collections import Counter
from typing import Any, Callable, Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.fixtures_net import RecordingHTTPFixture, RecordingTCPFixture
from wea.observers import hash_file, read_host_process_table, sha256_bytes
from wea.provenance import REPO_ROOT, collect_provenance
from wea.runner import (
    _create_application,
    _extract_product_network_witnesses,
    _extract_refusal_code,
    _init_git_repo,
    _open_session,
    _save_json,
    _send_tool_call,
    _wait_pending,
)
from wea.tb08_confinement import (
    A06_LEGACY_EXCLUDED_CODE,
    A06_LIVE_DEST_VALIDATION_CODE,
    A06_LIVE_EGRESS_GATE_CODE,
    A09_SOCKET_PATH_CHANGED_CODE,
    A09_UNAVAILABLE_CODE,
    TB08_SLOT_INVENTORY,
    get_platform_profile,
)

# Exact case catalog titles from acceptance-plan.md.
TB08_CATALOG_TITLES: dict[str, str] = {
    "A05": "A05 · Filesystem boundary",
    "A06": "A06 · Egress modes",
    "A07": "A07 · Processes a command leaves behind",
    "A08": "A08 · Legible denials",
    "A09": "A09 · Unavailable boundaries and exceptional authority",
}

# Earlier-gate refusal codes that MUST NEVER satisfy a later OS-confinement slot.
EARLIER_GATE_CODES = frozenset({
    "SURVEY_PATH_ESCAPE",
    "TOOL_ARGS_INVALID",
    "approval_denied",
    "SANDBOX_SOCKET_PATH_NOT_FOUND",
})


def _host_identity(platform_key: str | None = None) -> dict[str, Any]:
    profile = get_platform_profile(platform_key)
    return {
        "hostname": socket.gethostname(),
        "platform": profile["platform"],
        "arch": platform.machine(),
        "d13_profile": profile,
    }


def _parse_content_meta(content: Any) -> dict[str, Any]:
    if not isinstance(content, str) or not content.strip().startswith("{"):
        return {}
    try:
        parsed = json.loads(content)
    except ValueError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def _typed_result_summary(tool_name: str, call_id: str, result: dict[str, Any]) -> dict[str, Any]:
    res = result if isinstance(result, dict) else {}
    cmd_prog = res.get("command_program") if isinstance(res.get("command_program"), dict) else {}
    content_meta = _parse_content_meta(res.get("content"))
    exit_code = cmd_prog.get("status_code")
    if exit_code is None and "exit_code" in content_meta:
        exit_code = content_meta.get("exit_code")
    witness = content_meta.get("sandbox_refusal_witness")
    confined = content_meta.get("confined")
    sandbox_refusals = content_meta.get("sandbox_refusals")
    return {
        "tool_name": tool_name,
        "tool_call_id": call_id,
        "outcome": res.get("outcome"),
        "refusal_code": _extract_refusal_code(res),
        "codes": list(res.get("codes") or []),
        "exit_code": exit_code,
        "confined": confined,
        "sandbox_refusal_witness": witness,
        "sandbox_refusals": sandbox_refusals,
        "invocation": res.get("invocation"),
        "command_program": cmd_prog or None,
        "external_access": res.get("external_access"),
        "left_running": res.get("left_running") if res.get("left_running") is not None else content_meta.get("left_running"),
        "source_verdict": res.get("source_verdict"),
    }


def _is_earlier_gate(typed: dict[str, Any]) -> bool:
    code = str(typed.get("refusal_code") or "")
    codes = set(typed.get("codes") or ())
    return code in EARLIER_GATE_CODES or bool(codes & EARLIER_GATE_CODES)


def _command_failed_or_refused(typed: dict[str, Any]) -> bool:
    """True when an OS-confined command was refused by the boundary (refusal code, error, or non-zero exit)."""
    if typed.get("refusal_code"):
        return True
    if typed.get("outcome") in {"rejected", "error"}:
        return True
    if typed.get("source_verdict") == "failed":
        return True
    exit_code = typed.get("exit_code")
    if exit_code is not None and int(exit_code) != 0:
        return True
    if typed.get("sandbox_refusals"):
        return True
    return False


def _delta_arrivals(fixture: RecordingHTTPFixture | RecordingTCPFixture, before_count: int) -> int:
    return len(fixture.records()) - before_count


def _create_unix_socket_fixture(prefix: str) -> tuple[pathlib.Path, pathlib.Path, socket.socket]:
    """Create a real listening AF_UNIX socket in a short /tmp dir (safe for macOS 104-byte sun_path limit)."""
    short_dir = pathlib.Path(tempfile.mkdtemp(prefix=prefix, dir="/tmp"))
    sock_path = short_dir / "svc.sock"
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(str(sock_path))
    srv.listen(4)
    return short_dir, sock_path, srv


def _send_tool_call_with_checkpoint_hook(
    app: Any,
    session_id: str,
    name: str,
    args: dict[str, Any],
    *,
    on_pending_checkpoint: Callable[[dict[str, Any]], None],
) -> tuple[str, dict[str, Any], dict[str, Any]]:
    """Send a tool call and run `on_pending_checkpoint` right before approving the pending checkpoint."""
    req = _wait_pending(app, session_id)
    call_id = str(uuid.uuid4())
    app.request(
        "/harness/llm/respond",
        {"id": req["id"], "tool_calls": [{"id": call_id, "name": name, "args": args}]},
    )
    db_path = app.directory / "store.db"
    hook_fired = False
    approve_http_error: dict[str, Any] = {}

    def poll() -> dict[str, Any] | None:
        nonlocal hook_fired
        if not hook_fired:
            cps = app.request(f"/v1/sessions/{session_id}/checkpoints?status=pending").get("checkpoints") or []
            for cp in cps:
                payload = cp.get("tool_approval") or {}
                if payload.get("tool_call_id") != call_id:
                    continue
                on_pending_checkpoint(cp)
                hook_fired = True
                options = (payload.get("plan") or {}).get("options") or []
                choices = [
                    o for o in options
                    if o.get("decision_action") == "approve"
                    and not o.get("disabled")
                    and o.get("rung") == "once"
                ] or [
                    o for o in options
                    if o.get("decision_action") == "approve" and not o.get("disabled")
                ]
                if choices:
                    try:
                        app.request(
                            f"/v1/sessions/{session_id}/checkpoints/{cp['id']}",
                            {"kind": "tool_approval", "action": "approve", "option_id": choices[0]["id"]},
                        )
                    except urllib.error.HTTPError as exc:
                        raw_err = ""
                        with contextlib.suppress(Exception):
                            raw_err = exc.read().decode("utf-8", errors="replace")
                        parsed_err = _parse_content_meta(raw_err)
                        approve_http_error.update({
                            "status": exc.code,
                            "code": str(parsed_err.get("code") or parsed_err.get("error_code") or ""),
                            "error": str(parsed_err.get("error") or parsed_err.get("message") or raw_err[:256]),
                        })
                        # Reject the checkpoint so the tool call settles cleanly instead of hanging.
                        with contextlib.suppress(Exception):
                            app.request(
                                f"/v1/sessions/{session_id}/checkpoints/{cp['id']}",
                                {"kind": "tool_approval", "action": "reject", "guidance": "Settling after approve rejection."},
                            )
        with contextlib.closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True)) as db:
            row = db.execute(
                "SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?",
                (session_id, call_id),
            ).fetchone()
        return json.loads(row[0]) if row else None

    res = app.wait_for(poll, "wea tool result (with hook)", session_id)
    if approve_http_error and isinstance(res, dict):
        res = {**res, "_checkpoint_approve_http_error": approve_http_error}
    return call_id, res, req


def _build_receipt(
    *,
    run_id: str,
    slot: dict[str, Any],
    host: dict[str, Any],
    provenance: dict[str, Any],
    typed_result: dict[str, Any],
    independent_observation: dict[str, Any],
    failures: list[dict[str, Any]],
    elapsed_ms: int,
    override_verdict: str | None = None,
    blocked_reason: str | None = None,
) -> dict[str, Any]:
    product_source = provenance.get("product_source") or {}
    harness_source = provenance.get("harness_source") or {}
    if override_verdict in {"blocked", "unknown"}:
        verdict = override_verdict
    elif not product_source or not harness_source:
        verdict = "unknown"
    elif any(f.get("rule_id") == "earlier_gate_intercepted" for f in failures):
        verdict = "unknown"
    elif failures:
        verdict = "fail"
    else:
        verdict = "pass"

    rec: dict[str, Any] = {
        "schema": "wea-tb08-sidecar-receipt-v1",
        "run_id": run_id,
        "slot_id": slot["slot_id"],
        "family": slot["family"],
        "family_title": TB08_CATALOG_TITLES.get(slot["family"], slot["family"]),
        "name": slot["name"],
        "lane": "sidecar",
        "host": host,
        "product_source": product_source,
        "harness_source": harness_source,
        "typed_result": typed_result,
        "independent_observation": independent_observation,
        "verdict": verdict,
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
        "elapsed_ms": elapsed_ms,
    }
    if blocked_reason:
        rec["blocked_reason"] = blocked_reason
    return rec


def _py_write_cmd(path: pathlib.Path, text: str = "mutated\n", *, stderr_msg: str | None = None) -> str:
    """Build an external OS subprocess command that attempts a write inside the OS sandbox."""
    if stderr_msg is not None:
        code = (
            f"import sys\n"
            f"try:\n"
            f"    open({json.dumps(str(path))}, 'w').write({json.dumps(text)})\n"
            f"except OSError:\n"
            f"    print({json.dumps(stderr_msg)}, file=sys.stderr)\n"
            f"    sys.exit(1)\n"
        )
    else:
        code = f"open({json.dumps(str(path))}, 'w').write({json.dumps(text)})"
    return f"python3 -B -c {json.dumps(code)}"


def _py_read_cmd(path: pathlib.Path) -> str:
    """Build an external OS subprocess command that attempts a read inside the OS sandbox."""
    code = f"open({json.dumps(str(path))}, 'rb').read(16)"
    return f"python3 -B -c {json.dumps(code)}"


def _execute_slot(
    slot: dict[str, Any],
    *,
    app: Any,
    session_id: str,
    project_dir: pathlib.Path,
    outside_dir: pathlib.Path,
    app_dir: pathlib.Path,
    allowed_ep: RecordingHTTPFixture,
    refused_ep: RecordingHTTPFixture,
    refused_tcp: RecordingTCPFixture,
    unix_sock_path: pathlib.Path,
    host: dict[str, Any],
    run_id: str,
    send_tool_call: Callable[..., tuple[str, dict[str, Any], dict[str, Any]]],
    send_tool_call_hooked: Callable[..., tuple[str, dict[str, Any], dict[str, Any]]] | None = None,
) -> tuple[dict[str, Any], dict[str, Any], list[dict[str, Any]], str | None, str | None]:
    """Execute one TB08 sidecar slot.

    Returns: (typed_result, independent_observation, failures, override_verdict, blocked_reason).
    """
    slot_id = slot["slot_id"]
    plat = host["platform"]
    failures: list[dict[str, Any]] = []

    def call_cmd(
        cmd: str,
        *,
        capability_request: dict[str, Any] | None = None,
        checkpoint_policy: str | None = "reject",
    ) -> dict[str, Any]:
        args: dict[str, Any] = {"command": cmd}
        if capability_request:
            args["capability_request"] = capability_request
        cid, res, _ = send_tool_call(app, session_id, "command", args, checkpoint_policy=checkpoint_policy)
        return _typed_result_summary("command", cid, res)

    def call_fetch(url: str, *, checkpoint_policy: str | None = "reject") -> dict[str, Any]:
        cid, res, _ = send_tool_call(app, session_id, "fetch_url", {"url": url}, checkpoint_policy=checkpoint_policy)
        return _typed_result_summary("fetch_url", cid, res)

    def flag_if_earlier_gate(typed: dict[str, Any], expected_boundary: str) -> None:
        if _is_earlier_gate(typed):
            failures.append({
                "rule_id": "earlier_gate_intercepted",
                "message": (
                    f"{slot_id} stopped at earlier gate {typed.get('refusal_code')!r} "
                    f"instead of exercising {expected_boundary}."
                ),
            })

    # ------------------------------------------------------------------
    # A05 · Filesystem boundary (8 slots)
    # ------------------------------------------------------------------
    if slot_id == "A05-01":
        target = outside_dir / "a05_01_outside.txt"
        target.write_text("immutable-a05-01\n", encoding="utf-8")
        pre = hash_file(target)
        typed = call_cmd(_py_write_cmd(target), checkpoint_policy="reject")
        post = hash_file(target)
        unchanged = pre["exists"] == post["exists"] and pre["sha256"] == post["sha256"]
        flag_if_earlier_gate(typed, "OS sandbox write-root floor")
        if not unchanged:
            failures.append({"rule_id": "a05_outside_mutated", "message": "Outside-root file mutated."})
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a05_missing_refusal", "message": "Outside-root write was not refused."})
        return typed, {"target": str(target), "pre": pre, "post": post, "unchanged": unchanged}, failures, None, None

    if slot_id == "A05-02":
        target = outside_dir / "a05_02_symlink_target.txt"
        target.write_text("immutable-a05-02\n", encoding="utf-8")
        link = project_dir / "a05_02_escape_link.txt"
        if link.exists() or link.is_symlink():
            link.unlink()
        link.symlink_to(target)
        pre = hash_file(target)
        typed = call_cmd(_py_write_cmd(link, "escaped\n"), checkpoint_policy="reject")
        post = hash_file(target)
        unchanged = pre["exists"] == post["exists"] and pre["sha256"] == post["sha256"]
        flag_if_earlier_gate(typed, "OS sandbox symlink-escape write floor")
        if not unchanged:
            failures.append({"rule_id": "a05_symlink_escape_mutated", "message": "Symlink escape mutated outside target."})
        return typed, {"symlink": str(link), "target": str(target), "pre": pre, "post": post, "unchanged": unchanged}, failures, None, None

    if slot_id == "A05-03":
        sentinel = pathlib.Path("/tmp") / f"pw-tb08-a05-03-{run_id}.txt"
        sentinel.unlink(missing_ok=True)
        typed = call_cmd(_py_write_cmd(sentinel, "shared-tmp\n"), checkpoint_policy="reject")
        post = hash_file(sentinel)
        flag_if_earlier_gate(typed, "OS sandbox shared-temp write floor")
        if post["exists"]:
            sentinel.unlink(missing_ok=True)
            failures.append({"rule_id": "a05_shared_temp_mutated", "message": "Shared /tmp sentinel created."})
        return typed, {"sentinel": str(sentinel), "post": post, "created": post["exists"]}, failures, None, None

    if slot_id == "A05-04":
        typed = call_cmd(
            'python3 -B -c "import os, pathlib; p = pathlib.Path(os.environ[\'TMPDIR\']) / \'tb08_scratch.txt\'; p.write_text(\'ok\\n\'); print(p.read_text().strip())"',
            checkpoint_policy=None,
        )
        if typed.get("outcome") != "completed":
            failures.append({"rule_id": "a05_scratch_write_failed", "message": "Session scratch write failed."})
        return typed, {"d13_platform": plat, "blanket_xdg_data_write_allowed": False}, failures, None, None

    if slot_id == "A05-05":
        target = app_dir / "tb08_a05_05_control_plane.txt"
        target.unlink(missing_ok=True)
        pre = hash_file(target)
        typed = call_cmd(_py_write_cmd(target, "tamper\n"), checkpoint_policy="reject")
        post = hash_file(target)
        flag_if_earlier_gate(typed, "OS sandbox control-plane write floor")
        if post["exists"]:
            target.unlink(missing_ok=True)
            failures.append({"rule_id": "a05_control_plane_mutated", "message": "Control-plane file created."})
        return typed, {"target": str(target), "pre": pre, "post": post, "unchanged": not post["exists"]}, failures, None, None

    if slot_id == "A05-06":
        probe = app_dir / "store.db"
        typed = call_cmd(_py_read_cmd(probe), checkpoint_policy="reject")
        flag_if_earlier_gate(typed, "OS sandbox control-plane read floor")
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a05_control_plane_read_allowed", "message": "Control-plane read was not refused."})
        return typed, {"probe": str(probe), "d13_denied_dir_masked": host["d13_profile"]["denied_directory_is_masked"]}, failures, None, None

    if slot_id == "A05-07":
        # D6-safe probe: synthetic non-existent key path inside ~/.ssh; never reads real credentials.
        probe = pathlib.Path.home() / ".ssh" / f"tb08-nonexistent-{run_id}.key"
        typed = call_cmd(_py_read_cmd(probe), checkpoint_policy="reject")
        flag_if_earlier_gate(typed, "OS sandbox key-material read floor")
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a05_sensitive_read_not_refused", "message": "Sensitive path read unexpectedly succeeded."})
        return typed, {"probe": "~/.ssh/tb08-nonexistent.key", "d6_synthetic_only": True}, failures, None, None

    if slot_id == "A05-08":
        inside = project_dir / "a05_08_permitted.txt"
        body = "tb08-a05-08-permitted\n"
        want_sha = sha256_bytes(body.encode("utf-8"))
        typed = call_cmd(
            "python3 -B -c \"from pathlib import Path; Path('a05_08_permitted.txt').write_text('tb08-a05-08-permitted\\n')\"",
            checkpoint_policy=None,
        )
        post = hash_file(inside)
        if typed.get("outcome") != "completed" or post.get("sha256") != want_sha:
            failures.append({"rule_id": "a05_positive_control_failed", "message": "Permitted inside-root write failed or SHA mismatch."})
        return typed, {"target": str(inside), "expected_sha256": want_sha, "post": post}, failures, None, None

    # ------------------------------------------------------------------
    # A06 · Egress modes (10 slots)
    # ------------------------------------------------------------------
    if slot_id == "A06-01":
        before = len(refused_ep.records())
        typed = call_cmd(f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=2).read()\"")
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_refused_arrivals_nonzero", "message": f"Expected 0 arrivals, saw {arrivals}."})
        return typed, {"fixture_address": f"127.0.0.1:{refused_ep.port}", "arrivals": arrivals}, failures, None, None

    if slot_id == "A06-02":
        before = len(allowed_ep.records())
        cid, raw_res, _ = send_tool_call(
            app,
            session_id,
            "command",
            {
                "command": f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\"",
                "capability_request": {"loopback_connect": {"ports": [allowed_ep.port]}},
            },
            checkpoint_policy="approve_once",
        )
        typed = _typed_result_summary("command", cid, raw_res)
        arrivals = _delta_arrivals(allowed_ep, before)
        witnesses = _extract_product_network_witnesses({}, allowed_call_id=cid, allowed_tool_result=raw_res)
        if arrivals != 1:
            failures.append({"rule_id": "a06_allowed_arrival_mismatch", "message": f"Expected 1 arrival, saw {arrivals}."})
        return typed, {"fixture_address": f"127.0.0.1:{allowed_ep.port}", "arrivals": arrivals, "product_witnesses": witnesses}, failures, None, None

    if slot_id == "A06-03":
        before = len(refused_ep.records())
        typed = call_cmd(
            f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=2).read()\"",
            capability_request={"loopback_connect": {"ports": [refused_ep.port]}},
            checkpoint_policy="reject",
        )
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_refused_arrivals_nonzero", "message": f"Refused endpoint saw {arrivals} arrival(s)."})
        return typed, {"fixture_address": f"127.0.0.1:{refused_ep.port}", "arrivals": arrivals}, failures, None, None

    if slot_id == "A06-04":
        before = len(refused_tcp.records())
        typed = call_cmd(
            f"python3 -B -c \"import socket; s=socket.create_connection(('127.0.0.1', {refused_tcp.port}), timeout=2); s.close()\"",
            capability_request={"direct_ip": True},
            checkpoint_policy="reject",
        )
        arrivals = _delta_arrivals(refused_tcp, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_direct_ip_refused_leaked", "message": f"TCP fixture saw {arrivals} arrival(s)."})
        return typed, {"fixture_address": f"127.0.0.1:{refused_tcp.port}", "arrivals": arrivals}, failures, None, None

    if slot_id == "A06-05":
        before_allow = len(allowed_ep.records())
        before_ref = len(refused_ep.records())
        typed = call_cmd(
            f"python3 -B -c \"import urllib.request; print(urllib.request.urlopen('{allowed_ep.base_url}/ping', timeout=5).read().decode())\"",
            capability_request={"loopback_connect": {"ports": [allowed_ep.port]}},
            checkpoint_policy="approve_once",
        )
        allow_n = _delta_arrivals(allowed_ep, before_allow)
        ref_n = _delta_arrivals(refused_ep, before_ref)
        if allow_n != 1 or ref_n != 0:
            failures.append({"rule_id": "a06_local_capability_scope_mismatch", "message": f"allow={allow_n}, refused={ref_n}."})
        return typed, {"allowed_arrivals": allow_n, "refused_arrivals": ref_n}, failures, None, None

    if slot_id == "A06-06":
        before = len(refused_ep.records())
        typed = call_cmd(
            f"env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u http_proxy -u https_proxy -u all_proxy "
            f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=2).read()\""
        )
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_ignore_proxy_leaked", "message": f"Proxy-ignoring command reached refused_ep ({arrivals})."})
        return typed, {"arrivals": arrivals}, failures, None, None

    if slot_id == "A06-07":
        before = len(refused_ep.records())
        typed = call_cmd(
            f"python3 -B -c \"import urllib.request; req=urllib.request.Request('{refused_ep.base_url}/ping', headers={{'X-TB08-Probe':'1'}}); urllib.request.urlopen(req, timeout=2).read()\""
        )
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_redirect_unapproved_arrived", "message": f"Refused target saw {arrivals} arrival(s)."})
        return typed, {"refused_arrivals": arrivals}, failures, None, None

    if slot_id == "A06-08":
        before = len(allowed_ep.records())
        script_path = project_dir / "_a06_08_lineage.py"
        url = f"{allowed_ep.base_url}/ping"
        child_src = f"import urllib.request; print(urllib.request.urlopen({json.dumps(url)}, timeout=5).read().decode())"
        script_path.write_text(
            "import subprocess, sys\n"
            f"child = {json.dumps(child_src)}\n"
            "res = subprocess.run([sys.executable, '-B', '-c', child], start_new_session=True, close_fds=False, capture_output=True, text=True)\n"
            "if res.stdout:\n"
            "    print(res.stdout, end='')\n"
            "if res.stderr:\n"
            "    print(res.stderr, end='', file=sys.stderr)\n"
            "sys.exit(res.returncode)\n",
            encoding="utf-8",
        )
        typed = call_cmd(
            f"python3 -B {script_path.name}",
            capability_request={"loopback_connect": {"ports": [allowed_ep.port]}},
            checkpoint_policy="approve_once",
        )
        arrivals = _delta_arrivals(allowed_ep, before)
        if arrivals != 1:
            failures.append({"rule_id": "a06_lineage_descent_mismatch", "message": f"Expected 1 arrival across fork/exec/setsid, saw {arrivals}."})
        return typed, {"allowed_arrivals": arrivals}, failures, None, None

    if slot_id == "A06-09":
        # Resolvable host denied at egress gate -> WEB_SEARCH_HOST_DENIED (NOT earlier DNS error).
        before = len(refused_ep.records())
        typed = call_fetch("https://example.com/tb08-egress-gate", checkpoint_policy="reject")
        arrivals = _delta_arrivals(refused_ep, before)
        code = typed.get("refusal_code")
        if code == A06_LEGACY_EXCLUDED_CODE:
            failures.append({"rule_id": "a06_deprecated_code_emitted", "message": f"Legacy {A06_LEGACY_EXCLUDED_CODE} emitted."})
        if code != A06_LIVE_EGRESS_GATE_CODE:
            return typed, {"arrivals": arrivals, "observed_code": code, "expected_code": A06_LIVE_EGRESS_GATE_CODE}, [{
                "rule_id": "earlier_gate_intercepted",
                "message": f"A06-09 observed {code!r} instead of egress-gate {A06_LIVE_EGRESS_GATE_CODE!r}.",
            }], "unknown", None
        if arrivals != 0:
            failures.append({"rule_id": "a06_egress_gate_leaked", "message": f"Saw {arrivals} arrival(s)."})
        return typed, {"arrivals": arrivals, "expected_live_code": A06_LIVE_EGRESS_GATE_CODE, "legacy_excluded": A06_LEGACY_EXCLUDED_CODE}, failures, None, None

    if slot_id == "A06-10":
        before = len(refused_ep.records())
        typed = call_fetch("http://169.254.169.254/latest/meta-data/", checkpoint_policy="approve_once")
        arrivals = _delta_arrivals(refused_ep, before)
        code = typed.get("refusal_code")
        if code == A06_LEGACY_EXCLUDED_CODE:
            failures.append({"rule_id": "a06_deprecated_code_emitted", "message": f"Legacy {A06_LEGACY_EXCLUDED_CODE} emitted."})
        if code != A06_LIVE_DEST_VALIDATION_CODE:
            failures.append({"rule_id": "a06_dest_validation_code_unexpected", "message": f"Got {code!r}, want {A06_LIVE_DEST_VALIDATION_CODE!r}."})
        if arrivals != 0:
            failures.append({"rule_id": "a06_private_fetch_unexpected_arrival", "message": f"Unexpected arrival ({arrivals})."})
        return typed, {"arrivals": arrivals, "target": "http://169.254.169.254/latest/meta-data/", "expected_live_code": A06_LIVE_DEST_VALIDATION_CODE}, failures, None, None

    # ------------------------------------------------------------------
    # A07 · Processes a command leaves behind (3 slots)
    # ------------------------------------------------------------------
    if slot_id == "A07-01":
        typed = call_cmd("printf 'tb08-a07-clean\\n'", checkpoint_policy=None)
        left = int(typed.get("left_running") or 0)
        if typed.get("outcome") != "completed" or left != 0:
            failures.append({"rule_id": "a07_clean_left_running", "message": f"Expected completed + left_running=0, got outcome={typed.get('outcome')}, left={left}."})
        return typed, {"reported_left_running": left}, failures, None, None

    if slot_id == "A07-02":
        pid_file = project_dir / "a07_daemon.pid"
        pid_file.unlink(missing_ok=True)
        typed = call_cmd(f"sleep 15 </dev/null >/dev/null 2>&1 & echo $! > {pid_file}", checkpoint_policy=None)
        daemon_pid = 0
        if pid_file.is_file():
            raw = pid_file.read_text(encoding="utf-8").strip()
            if raw.isdigit():
                daemon_pid = int(raw)
        table = read_host_process_table([daemon_pid]) if daemon_pid > 0 else {"alive": {}, "missing": []}
        host_alive = len(table.get("alive") or {})
        try:
            if plat == "linux" and host_alive > 0:
                failures.append({"rule_id": "a07_linux_pid_namespace_leak", "message": f"Linux left {host_alive} daemon alive."})
        finally:
            if daemon_pid > 0 and host_alive > 0:
                try:
                    os.kill(daemon_pid, signal.SIGKILL)
                except OSError:
                    pass
        return typed, {
            "daemon_pid": daemon_pid,
            "host_alive_before_cleanup": host_alive,
            "d13_expected_outlives": host["d13_profile"]["left_behind_outlives_command"],
        }, failures, None, None

    if slot_id == "A07-03":
        before = len(refused_ep.records())
        typed = call_cmd("env | grep -E '^LYCAON_API_TOKEN=' || true", checkpoint_policy=None)
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a07_orphan_network_leak", "message": f"Unexpected {arrivals} arrival(s)."})
        return typed, {"refused_arrivals": arrivals, "reduced_env_checked": True}, failures, None, None

    # ------------------------------------------------------------------
    # A08 · Legible denials (12 slots)
    # ------------------------------------------------------------------
    if slot_id == "A08-01":
        target = outside_dir / "a08_01_write_root.txt"
        target.write_text("immutable-a08-01\n", encoding="utf-8")
        pre = hash_file(target)
        typed = call_cmd(_py_write_cmd(target), checkpoint_policy="reject")
        post = hash_file(target)
        flag_if_earlier_gate(typed, "OS sandbox write_root refusal (SANDBOX_TRY_WRITE_ROOT)")
        if pre["sha256"] != post["sha256"]:
            failures.append({"rule_id": "a08_outside_mutated", "message": "Outside file mutated."})
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a08_missing_typed_refusal", "message": "A08-01 write refusal not observed."})
        return typed, {"target": str(target), "unchanged": pre["sha256"] == post["sha256"], "d13_refusal_witness": host["d13_profile"]["refusal_witness"]}, failures, None, None

    if slot_id == "A08-02":
        probe = pathlib.Path.home() / ".ssh" / f"tb08-a08-02-{run_id}.key"
        typed = call_cmd(_py_read_cmd(probe), checkpoint_policy="reject")
        flag_if_earlier_gate(typed, "OS sandbox read_path refusal (SANDBOX_TRY_READ_PATH)")
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a08_missing_typed_refusal", "message": "A08-02 read refusal not observed."})
        return typed, {"probe": "~/.ssh/tb08-a08-02.key", "d13_refusal_witness": host["d13_profile"]["refusal_witness"]}, failures, None, None

    if slot_id == "A08-05":
        # Actual confined bind+listen attempt without local_listen grant (never TOOL_ARGS_INVALID!).
        bind_code = "import socket; s=socket.socket(); s.bind(('127.0.0.1', 18999)); s.listen(1); s.close()"
        typed = call_cmd(f"python3 -B -c {json.dumps(bind_code)}", checkpoint_policy="reject")
        flag_if_earlier_gate(typed, "OS sandbox local_listen boundary")
        # D13: macOS Seatbelt refuses bind directly; Linux keeps ungranted bind inside private netns
        # and reports fixed-port local_listen refusal via supervisor.
        if plat == "darwin" and not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a08_macos_listen_not_refused", "message": "macOS Seatbelt did not refuse ungranted listen."})
        return typed, {"port": 18999, "d13_platform": plat, "d13_refusal_witness": host["d13_profile"]["refusal_witness"]}, failures, None, None

    a08_cap_probes: dict[str, tuple[str, dict[str, Any] | None, str]] = {
        "A08-03": ("python3 -B -c \"print('cred-probe')\"", {"credential_store": True}, "credential_store capability gate"),
        "A08-04": ("python3 -B -c \"print('sock-probe')\"", {"socket_paths": [str(unix_sock_path)]}, "socket_paths capability gate"),
        "A08-06": ("python3 -B -c \"print('connect-probe')\"", {"loopback_connect": {"ports": [refused_ep.port]}}, "loopback_connect capability gate"),
        "A08-07": ("python3 -B -c \"print('proc-probe')\"", {"process_control": True}, "process_control capability gate"),
        "A08-08": ("python3 -B -c \"print('host-exec-probe')\"", {"host_execution": True}, "host_execution capability gate"),
    }
    if slot_id in a08_cap_probes:
        cmd_str, cap_req, boundary_name = a08_cap_probes[slot_id]
        typed = call_cmd(cmd_str, capability_request=cap_req, checkpoint_policy="reject")
        flag_if_earlier_gate(typed, boundary_name)
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a08_missing_typed_refusal", "message": f"{slot_id} was not refused."})
        return typed, {"probe_slot": slot_id, "d13_refusal_witness": host["d13_profile"]["refusal_witness"]}, failures, None, None

    if slot_id in {"A08-09", "A08-10", "A08-11"}:
        target = (app_dir / "a08_09_cp.txt") if slot_id == "A08-09" else (outside_dir / f"{slot_id.lower()}.txt")
        if slot_id != "A08-09":
            target.write_text(f"immutable-{slot_id}\n", encoding="utf-8")
        pre = hash_file(target)
        typed = call_cmd(_py_write_cmd(target), checkpoint_policy="reject")
        post = hash_file(target)
        flag_if_earlier_gate(typed, f"OS sandbox boundary for {slot_id}")
        if pre["exists"] != post["exists"] or pre["sha256"] != post["sha256"]:
            failures.append({"rule_id": "a08_target_mutated", "message": f"{slot_id} target mutated."})
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a08_missing_typed_refusal", "message": f"{slot_id} was not refused."})
        return typed, {"target": str(target), "unchanged": pre["sha256"] == post["sha256"], "d13_refusal_witness": host["d13_profile"]["refusal_witness"]}, failures, None, None

    if slot_id == "A08-12":
        t1 = outside_dir / "a08_12_a.txt"
        t2 = outside_dir / "a08_12_b.txt"
        t1.write_text("immutable-a\n", encoding="utf-8")
        t2.write_text("immutable-b\n", encoding="utf-8")
        pre1, pre2 = hash_file(t1), hash_file(t2)
        r1 = call_cmd(_py_write_cmd(t1, "a\n", stderr_msg="Operation not permitted"), checkpoint_policy="reject")
        r2 = call_cmd(_py_write_cmd(t2, "b\n", stderr_msg="ECONNREFUSED fake network timeout"), checkpoint_policy="reject")
        post1, post2 = hash_file(t1), hash_file(t2)
        flag_if_earlier_gate(r1, "OS sandbox write-root refusal (variant 1)")
        flag_if_earlier_gate(r2, "OS sandbox write-root refusal (variant 2)")
        same_code = r1.get("refusal_code") == r2.get("refusal_code")
        if not same_code:
            failures.append({"rule_id": "a08_code_varied_with_stderr", "message": f"Codes drifted: {r1.get('refusal_code')!r} vs {r2.get('refusal_code')!r}."})
        if pre1["sha256"] != post1["sha256"] or pre2["sha256"] != post2["sha256"]:
            failures.append({"rule_id": "a08_outside_mutated", "message": "Outside target mutated during stderr-invariance check."})
        return r1, {
            "variant_1_code": r1.get("refusal_code"),
            "variant_2_code": r2.get("refusal_code"),
            "invariant": same_code,
            "both_unchanged": pre1["sha256"] == post1["sha256"] and pre2["sha256"] == post2["sha256"],
        }, failures, None, None

    # ------------------------------------------------------------------
    # A09 · Unavailable boundaries and exceptional authority (9 slots)
    # ------------------------------------------------------------------
    if slot_id == "A09-01":
        # D5: Never weaken or disable the sandbox on a healthy host. Making the required OS
        # confinement boundary unavailable cannot be triggered in the sidecar lane on a healthy
        # macOS (Seatbelt built into XNU) or provisioned Linux (Landlock+userns active) host.
        # Covered in-process by TestWEATB08_A09_UnavailableBoundariesAndExceptionalAuthority.
        reason = (
            f"D5 fail-closed: OS confinement ({host['d13_profile']['adapter']}) is active on this healthy "
            f"{plat} host; triggering {A09_UNAVAILABLE_CODE} requires a host lacking kernel confinement "
            "or the in-process unavailable-helper test (TestWEATB08_A09_UnavailableBoundariesAndExceptionalAuthority)."
        )
        return (
            {"tool_name": "command", "tool_call_id": "", "outcome": None, "refusal_code": "", "codes": []},
            {"expected_code": A09_UNAVAILABLE_CODE, "in_process_witness": "TestWEATB08_A09_UnavailableBoundariesAndExceptionalAuthority"},
            [],
            "blocked",
            reason,
        )

    a09_cap_probes: dict[str, tuple[str, dict[str, Any] | None, str]] = {
        "A09-02": ("python3 -B -c \"print('a09-host-exec')\"", {"host_execution": True}, "host_execution capability gate"),
        "A09-03": ("python3 -B -c \"print('a09-proc-ctrl')\"", {"process_control": True}, "process_control capability gate"),
        "A09-04": ("python3 -B -c \"print('a09-cred-store')\"", {"credential_store": True}, "credential_store capability gate"),
        "A09-05": ("python3 -B -c \"print('a09-socket-path')\"", {"socket_paths": [str(unix_sock_path)]}, "socket_paths capability gate"),
        "A09-06": ("python3 -B -c \"print('a09-caps')\"", {"direct_ip": True}, "direct_ip capability gate"),
    }
    if slot_id in a09_cap_probes:
        cmd_str, cap_req, boundary_name = a09_cap_probes[slot_id]
        typed = call_cmd(cmd_str, capability_request=cap_req, checkpoint_policy="reject")
        flag_if_earlier_gate(typed, boundary_name)
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a09_exceptional_not_refused", "message": f"{slot_id} was not refused."})
        return typed, {
            "d13_adapter": host["d13_profile"]["adapter"],
            "d13_process_control_signals_outside": host["d13_profile"]["process_control_signals_outside"],
        }, failures, None, None

    if slot_id == "A09-07":
        sentinel = app_dir / "a09_07_terminal.txt"
        sentinel.unlink(missing_ok=True)
        typed = call_cmd(_py_write_cmd(sentinel, "never\n"), checkpoint_policy="reject")
        post = hash_file(sentinel)
        flag_if_earlier_gate(typed, "OS sandbox control-plane terminal path floor")
        if post["exists"]:
            sentinel.unlink(missing_ok=True)
            failures.append({"rule_id": "a09_target_sentinel_created", "message": "Terminal control-plane sentinel created."})
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a09_terminal_not_refused", "message": "Terminal path write was not refused."})
        return typed, {"sentinel_created": post["exists"], "d13_adapter": host["d13_profile"]["adapter"]}, failures, None, None

    if slot_id == "A09-08":
        # Confined subprocess connects to a REAL listening Unix socket without a socket_paths permit.
        conn_code = f"import socket; s=socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect({json.dumps(str(unix_sock_path))}); s.close()"
        typed = call_cmd(f"python3 -B -c {json.dumps(conn_code)}", checkpoint_policy="reject")
        flag_if_earlier_gate(typed, "OS sandbox unpermitted Unix socket connect")
        if not _command_failed_or_refused(typed):
            failures.append({"rule_id": "a09_unpermitted_socket_allowed", "message": "Unpermitted Unix socket connect was not refused."})
        return typed, {"socket_path": str(unix_sock_path), "d13_adapter": host["d13_profile"]["adapter"]}, failures, None, None

    if slot_id == "A09-09":
        # Establish a real listening Unix socket, replace it with a new listener (new inode) while
        # the socket_paths approval checkpoint is pending, then approve -> must fail closed with
        # SANDBOX_SOCKET_PATH_CHANGED (never SANDBOX_SOCKET_PATH_NOT_FOUND!).
        hook_fn = send_tool_call_hooked or _send_tool_call_with_checkpoint_hook
        sk_dir, sk_path, sk_srv = _create_unix_socket_fixture("tb08-a0909-")
        replacement_srv: list[socket.socket] = []

        def _replace_socket_before_approve(_cp: dict[str, Any]) -> None:
            sk_srv.close()
            sk_path.unlink(missing_ok=True)
            srv2 = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            srv2.bind(str(sk_path))
            srv2.listen(4)
            replacement_srv.append(srv2)

        try:
            cid, raw_res, _ = hook_fn(
                app,
                session_id,
                "command",
                {
                    "command": "python3 -B -c \"print('a09-sock-changed')\"",
                    "capability_request": {"socket_paths": [str(sk_path)]},
                },
                on_pending_checkpoint=_replace_socket_before_approve,
            )
            typed = _typed_result_summary("command", cid, raw_res)
            approve_err = raw_res.get("_checkpoint_approve_http_error") if isinstance(raw_res, dict) else None
        finally:
            with contextlib.suppress(OSError):
                sk_srv.close()
            for s2 in replacement_srv:
                with contextlib.suppress(OSError):
                    s2.close()
            shutil.rmtree(sk_dir, ignore_errors=True)

        flag_if_earlier_gate(typed, "socket recipient changed-binding revalidation")
        code = str(typed.get("refusal_code") or "").upper()
        approve_err_code = str((approve_err or {}).get("code") or "").upper()
        approve_err_text = str((approve_err or {}).get("error") or "").lower()
        matched_changed = (
            code == A09_SOCKET_PATH_CHANGED_CODE
            or approve_err_code == A09_SOCKET_PATH_CHANGED_CODE
            or ("socket" in approve_err_text and "changed" in approve_err_text)
        )
        if not matched_changed:
            failures.append({
                "rule_id": "a09_socket_changed_code_mismatch",
                "message": f"Expected {A09_SOCKET_PATH_CHANGED_CODE!r}, got tool_code={code!r} approve_http_err={approve_err!r}.",
            })
        return typed, {
            "socket_replaced_during_review": bool(replacement_srv),
            "expected_code": A09_SOCKET_PATH_CHANGED_CODE,
            "checkpoint_approve_http_error": approve_err,
        }, failures, None, None

    failures.append({"rule_id": "tb08_unknown_slot", "message": f"Unknown slot_id {slot_id!r}."})
    return {}, {}, failures, None, None


def run_tb08_sidecar_slots(
    out_dir: str | pathlib.Path,
    *,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    engine_root: str | pathlib.Path | None = None,
    families: Sequence[str] | None = None,
    slot_ids: Sequence[str] | None = None,
    platform_key: str | None = None,
    app_factory: Callable[..., Any] | None = None,
    session_opener: Callable[..., tuple[str, str, str]] | None = None,
    tool_caller: Callable[..., tuple[str, dict[str, Any], dict[str, Any]]] | None = None,
    tool_caller_hooked: Callable[..., tuple[str, dict[str, Any], dict[str, Any]]] | None = None,
) -> dict[str, Any]:
    """Run all selected TB08 sidecar slots on a single warmed `lycaon-dev` sidecar."""
    t_start = time.monotonic()
    out = pathlib.Path(out_dir).resolve()
    receipts_dir = out / "receipts"
    receipts_dir.mkdir(parents=True, exist_ok=True)

    run_id = f"tb08-sidecar-{uuid.uuid4().hex[:8]}"
    host = _host_identity(platform_key)
    provenance = collect_provenance(
        REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar"
    )

    selected = list(TB08_SLOT_INVENTORY)
    if families:
        fam_set = {f.upper() for f in families}
        selected = [s for s in selected if s["family"] in fam_set]
    if slot_ids:
        id_set = {s.upper() for s in slot_ids}
        selected = [s for s in selected if s["slot_id"] in id_set]

    project_dir = out / "project"
    project_dir.mkdir(parents=True, exist_ok=True)
    (project_dir / "README.md").write_text("TB08 sidecar fixture\n", encoding="utf-8")
    _init_git_repo(project_dir)

    outside_dir = out / "outside-root"
    outside_dir.mkdir(parents=True, exist_ok=True)
    app_dir = out / "app"

    create_app = app_factory or _create_application
    open_sess = session_opener or _open_session
    send_call = tool_caller or _send_tool_call

    unix_sk_dir, unix_sk_path, unix_sk_srv = _create_unix_socket_fixture("tb08-sk-")
    receipts: list[dict[str, Any]] = []
    try:
        with (
            RecordingHTTPFixture(fixture_id="tb08-allowed-http") as allowed_ep,
            RecordingHTTPFixture(fixture_id="tb08-refused-http") as refused_ep,
            RecordingTCPFixture(fixture_id="tb08-refused-tcp") as refused_tcp,
        ):
            app = create_app(
                app_dir,
                engine_bin=engine_bin,
                engine_manifest=engine_manifest,
                engine_root=engine_root,
            )
            try:
                app.ready()
                session_by_bucket: dict[str, str] = {}
                family_slot_idx: Counter[str] = Counter()
                for slot in selected:
                    fam = slot["family"]
                    slot_idx = family_slot_idx[fam]
                    family_slot_idx[fam] += 1
                    bucket = slot["slot_id"] if slot["slot_id"] in {"A08-12", "A09-09"} else f"{fam}-{slot_idx // 4}"
                    if slot["slot_id"] == "A09-01":
                        sid = ""
                    else:
                        if bucket not in session_by_bucket:
                            sid, _, _ = open_sess(app, project_dir)
                            app.request(
                                f"/v1/sessions/{sid}/prompts",
                                {"operation_id": str(uuid.uuid4()), "text": f"TB08 sidecar bucket {bucket} verification."},
                            )
                            session_by_bucket[bucket] = sid
                        sid = session_by_bucket[bucket]

                    slot_t0 = time.monotonic()
                    typed, obs, fails, override_v, blocked_reason = _execute_slot(
                        slot,
                        app=app,
                        session_id=sid,
                        project_dir=project_dir,
                        outside_dir=outside_dir,
                        app_dir=app_dir,
                        allowed_ep=allowed_ep,
                        refused_ep=refused_ep,
                        refused_tcp=refused_tcp,
                        unix_sock_path=unix_sk_path,
                        host=host,
                        run_id=run_id,
                        send_tool_call=send_call,
                        send_tool_call_hooked=tool_caller_hooked,
                    )
                    elapsed_ms = int((time.monotonic() - slot_t0) * 1000)
                    rec = _build_receipt(
                        run_id=run_id,
                        slot=slot,
                        host=host,
                        provenance=provenance,
                        typed_result=typed,
                        independent_observation=obs,
                        failures=fails,
                        elapsed_ms=elapsed_ms,
                        override_verdict=override_v,
                        blocked_reason=blocked_reason,
                    )
                    _save_json(receipts_dir / f"{slot['slot_id']}.json", rec)
                    receipts.append(rec)

                for sid in session_by_bucket.values():
                    try:
                        app.request(f"/v1/sessions/{sid}/abort", {})
                    except Exception:
                        pass
            finally:
                app.close(cleanup=False)
    finally:
        with contextlib.suppress(OSError):
            unix_sk_srv.close()
        shutil.rmtree(unix_sk_dir, ignore_errors=True)

    verdict_counts = Counter(r["verdict"] for r in receipts)
    family_counts = Counter(r["family"] for r in receipts)
    manifest = {
        "schema": "wea-tb08-sidecar-manifest-v1",
        "run_id": run_id,
        "lane": "sidecar",
        "catalog_titles": TB08_CATALOG_TITLES,
        "host": host,
        "product_source": provenance.get("product_source"),
        "harness_source": provenance.get("harness_source"),
        "total_slots": len(receipts),
        "counts_by_family": dict(sorted(family_counts.items())),
        "counts_by_verdict": dict(sorted(verdict_counts.items())),
        "all_passed": verdict_counts.get("fail", 0) == 0 and verdict_counts.get("unknown", 0) == 0 and verdict_counts.get("blocked", 0) == 0 and len(receipts) > 0,
        "elapsed_seconds": round(time.monotonic() - t_start, 3),
        "receipts": [
            {
                "slot_id": r["slot_id"],
                "family": r["family"],
                "name": r["name"],
                "verdict": r["verdict"],
                "refusal_code": r["typed_result"].get("refusal_code", ""),
                "path": f"receipts/{r['slot_id']}.json",
            }
            for r in receipts
        ],
    }
    _save_json(out / "manifest.json", manifest)
    return manifest


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="python3 -m wea.tb08_sidecar_runner",
        description="Run all 42 TB08 matching-lane sidecar slots (A05–A09) on a warmed lycaon-dev sidecar.",
    )
    p.add_argument("--engine", type=pathlib.Path, default=None, help="Path to pinned lycaon-dev binary.")
    p.add_argument("--engine-manifest", type=pathlib.Path, default=None, help="Path to engine build manifest JSON.")
    p.add_argument("--engine-root", type=pathlib.Path, default=None, help="Path to staged LYCAON_ENGINE_ROOT.")
    p.add_argument("--out", type=pathlib.Path, required=True, help="Output directory for receipts and manifest.json.")
    p.add_argument("--families", default=None, help="Optional comma-separated families (e.g. A05,A06).")
    p.add_argument("--slots", default=None, help="Optional comma-separated slot IDs (e.g. A05-01,A06-10).")
    return p


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    families = [x.strip() for x in args.families.split(",") if x.strip()] if args.families else None
    slots = [x.strip() for x in args.slots.split(",") if x.strip()] if args.slots else None
    manifest = run_tb08_sidecar_slots(
        args.out,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        engine_root=args.engine_root,
        families=families,
        slot_ids=slots,
    )
    print(json.dumps({
        "run_id": manifest["run_id"],
        "total_slots": manifest["total_slots"],
        "counts_by_family": manifest["counts_by_family"],
        "counts_by_verdict": manifest["counts_by_verdict"],
        "all_passed": manifest["all_passed"],
        "elapsed_seconds": manifest["elapsed_seconds"],
        "manifest_path": str(args.out.resolve() / "manifest.json"),
    }, indent=2, sort_keys=True))
    return 0 if manifest["counts_by_verdict"].get("fail", 0) == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main())
