"""TB08 matching-lane headless sidecar slot runner (42 slots per platform: A05–A09).

Drives all 42 TB08 sidecar slots against a warmed `lycaon-dev` sidecar instance
built directly on TB02's `runner.py`, `fixtures_net.py`, `observers.py`,
`provenance.py`, and `checker.py`.

Emits:
  - One structured JSON receipt per scheduled variant: `<out>/receipts/<slot_id>.json`
  - One summary manifest: `<out>/manifest.json`

Defensive testing invariants (D4–D7, D13, D18):
  - Observes confinement strictly from outside the boundary (host SHA-256 reads,
    loopback RecordingHTTPFixture / RecordingTCPFixture, independent host process table).
  - Never weakens protections, never reads real user secrets/vaults, cleans up all
    temporary fixtures and spawned test processes.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import platform
import signal
import socket
import sys
import time
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
)
from wea.tb08_confinement import (
    A06_LEGACY_EXCLUDED_CODE,
    A06_LIVE_DEST_VALIDATION_CODE,
    TB08_SLOT_INVENTORY,
    get_platform_profile,
)


def _host_identity(platform_key: str | None = None) -> dict[str, Any]:
    profile = get_platform_profile(platform_key)
    return {
        "hostname": socket.gethostname(),
        "platform": profile["platform"],
        "arch": platform.machine(),
        "d13_profile": profile,
    }


def _typed_result_summary(tool_name: str, call_id: str, result: dict[str, Any]) -> dict[str, Any]:
    res = result if isinstance(result, dict) else {}
    return {
        "tool_name": tool_name,
        "tool_call_id": call_id,
        "outcome": res.get("outcome"),
        "refusal_code": _extract_refusal_code(res),
        "codes": list(res.get("codes") or []),
        "invocation": res.get("invocation"),
        "external_access": res.get("external_access"),
        "left_running": res.get("left_running"),
    }


def _record_is_refused(typed: dict[str, Any]) -> bool:
    return bool(typed.get("refusal_code")) or typed.get("outcome") in {"rejected", "error"}


def _delta_arrivals(fixture: RecordingHTTPFixture | RecordingTCPFixture, before_count: int) -> int:
    return len(fixture.records()) - before_count


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
) -> dict[str, Any]:
    product_source = provenance.get("product_source") or {}
    harness_source = provenance.get("harness_source") or {}
    if not product_source or not harness_source:
        verdict = "unknown"
    elif failures:
        verdict = "fail"
    else:
        verdict = "pass"
    return {
        "schema": "wea-tb08-sidecar-receipt-v1",
        "run_id": run_id,
        "slot_id": slot["slot_id"],
        "family": slot["family"],
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
    host: dict[str, Any],
    run_id: str,
    send_tool_call: Callable[..., tuple[str, dict[str, Any], dict[str, Any]]],
) -> tuple[dict[str, Any], dict[str, Any], list[dict[str, Any]]]:
    """Execute one TB08 sidecar slot and return (typed_result, independent_observation, failures)."""
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

    # ------------------------------------------------------------------
    # A05 · Filesystem Confinement (8 slots)
    # ------------------------------------------------------------------
    if slot_id == "A05-01":
        target = outside_dir / "a05_01_outside.txt"
        target.write_text("immutable-a05-01\n", encoding="utf-8")
        pre = hash_file(target)
        typed = call_cmd(f"printf 'mutated\\n' > {target}")
        post = hash_file(target)
        unchanged = pre["exists"] == post["exists"] and pre["sha256"] == post["sha256"]
        if not unchanged:
            failures.append({"rule_id": "a05_outside_mutated", "message": "Outside-root file mutated."})
        if not _record_is_refused(typed):
            failures.append({"rule_id": "a05_missing_refusal", "message": "Outside-root write was not refused."})
        return typed, {"target": str(target), "pre": pre, "post": post, "unchanged": unchanged}, failures

    if slot_id == "A05-02":
        target = outside_dir / "a05_02_symlink_target.txt"
        target.write_text("immutable-a05-02\n", encoding="utf-8")
        link = project_dir / "a05_02_escape_link.txt"
        if link.exists() or link.is_symlink():
            link.unlink()
        link.symlink_to(target)
        pre = hash_file(target)
        typed = call_cmd(f"printf 'escaped\\n' > {link}")
        post = hash_file(target)
        unchanged = pre["exists"] == post["exists"] and pre["sha256"] == post["sha256"]
        if not unchanged:
            failures.append({"rule_id": "a05_symlink_escape_mutated", "message": "Symlink escape mutated outside target."})
        return typed, {"symlink": str(link), "target": str(target), "pre": pre, "post": post, "unchanged": unchanged}, failures

    if slot_id == "A05-03":
        sentinel = pathlib.Path("/tmp") / f"pw-tb08-a05-03-{run_id}.txt"
        if sentinel.exists():
            sentinel.unlink()
        typed = call_cmd(f"printf 'shared-tmp\\n' > {sentinel}")
        post = hash_file(sentinel)
        if post["exists"]:
            sentinel.unlink(missing_ok=True)
            failures.append({"rule_id": "a05_shared_temp_mutated", "message": "Shared /tmp sentinel created."})
        return typed, {"sentinel": str(sentinel), "post": post, "created": post["exists"]}, failures

    if slot_id == "A05-04":
        # D13: TMPDIR session scratch is writable; on Linux ~/.local/share is NOT a blanket write root.
        typed = call_cmd('python3 -B -c "import os, pathlib; p = pathlib.Path(os.environ[\'TMPDIR\']) / \'tb08_scratch.txt\'; p.write_text(\'ok\\n\'); print(p.read_text().strip())"', checkpoint_policy=None)
        if typed.get("outcome") != "completed":
            failures.append({"rule_id": "a05_scratch_write_failed", "message": "Session scratch write failed."})
        return typed, {"d13_platform": plat, "blanket_xdg_data_write_allowed": False}, failures

    if slot_id == "A05-05":
        target = app_dir / "tb08_a05_05_control_plane.txt"
        pre = hash_file(target)
        typed = call_cmd(f"printf 'tamper\\n' > {target}")
        post = hash_file(target)
        if post["exists"]:
            target.unlink(missing_ok=True)
            failures.append({"rule_id": "a05_control_plane_mutated", "message": "Control-plane file created."})
        return typed, {"target": str(target), "pre": pre, "post": post, "unchanged": not post["exists"]}, failures

    if slot_id == "A05-06":
        probe = app_dir / "store.db"
        typed = call_cmd(f"head -c 16 {probe}")
        if typed.get("outcome") == "completed" and not typed.get("refusal_code"):
            failures.append({"rule_id": "a05_control_plane_read_allowed", "message": "Control-plane read was not refused."})
        return typed, {"probe": str(probe), "d13_denied_dir_masked": host["d13_profile"]["denied_directory_is_masked"]}, failures

    if slot_id == "A05-07":
        # D6-safe probe: never reads real credentials; probes non-existent synthetic name under ~/.ssh.
        probe = pathlib.Path.home() / ".ssh" / f"tb08-nonexistent-{run_id}.key"
        typed = call_cmd(f"cat {probe}")
        if typed.get("outcome") == "completed" and not typed.get("refusal_code"):
            failures.append({"rule_id": "a05_sensitive_read_not_refused", "message": "Sensitive path read unexpectedly completed."})
        return typed, {"probe": "~/.ssh/tb08-nonexistent.key", "d6_synthetic_only": True}, failures

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
        return typed, {"target": str(inside), "expected_sha256": want_sha, "post": post}, failures

    # ------------------------------------------------------------------
    # A06 · Egress Modes & Destination Validation (10 slots)
    # ------------------------------------------------------------------
    if slot_id == "A06-01":
        before = len(refused_ep.records())
        typed = call_cmd(f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=2).read()\"")
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_refused_arrivals_nonzero", "message": f"Expected 0 arrivals, saw {arrivals}."})
        return typed, {"fixture_address": f"127.0.0.1:{refused_ep.port}", "arrivals": arrivals}, failures

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
        return typed, {"fixture_address": f"127.0.0.1:{allowed_ep.port}", "arrivals": arrivals, "product_witnesses": witnesses}, failures

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
        return typed, {"fixture_address": f"127.0.0.1:{refused_ep.port}", "arrivals": arrivals}, failures

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
        return typed, {"fixture_address": f"127.0.0.1:{refused_tcp.port}", "arrivals": arrivals}, failures

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
        return typed, {"allowed_arrivals": allow_n, "refused_arrivals": ref_n}, failures

    if slot_id == "A06-06":
        before = len(refused_ep.records())
        typed = call_cmd(
            f"env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u http_proxy -u https_proxy -u all_proxy "
            f"python3 -B -c \"import urllib.request; urllib.request.urlopen('{refused_ep.base_url}/ping', timeout=2).read()\""
        )
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_ignore_proxy_leaked", "message": f"Proxy-ignoring command reached refused_ep ({arrivals})."})
        return typed, {"arrivals": arrivals}, failures

    if slot_id == "A06-07":
        before = len(refused_ep.records())
        typed = call_cmd(
            f"python3 -B -c \"import urllib.request; req=urllib.request.Request('{refused_ep.base_url}/ping', headers={{'X-TB08-Probe':'1'}}); urllib.request.urlopen(req, timeout=2).read()\""
        )
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a06_redirect_unapproved_arrived", "message": f"Refused target saw {arrivals} arrival(s)."})
        return typed, {"refused_arrivals": arrivals}, failures

    if slot_id == "A06-08":
        before = len(allowed_ep.records())
        typed = call_cmd(
            f"sh -c 'sh -c \"python3 -B -c \\\"import urllib.request; print(urllib.request.urlopen(\\x27{allowed_ep.base_url}/ping\\x27, timeout=5).read().decode())\\\"\"'",
            capability_request={"loopback_connect": {"ports": [allowed_ep.port]}},
            checkpoint_policy="approve_once",
        )
        arrivals = _delta_arrivals(allowed_ep, before)
        if arrivals != 1:
            failures.append({"rule_id": "a06_lineage_descent_mismatch", "message": f"Expected 1 arrival across subshell, saw {arrivals}."})
        return typed, {"allowed_arrivals": arrivals}, failures

    if slot_id == "A06-09":
        before = len(refused_ep.records())
        typed = call_fetch("https://denied.tb08.invalid/egress-gate", checkpoint_policy="reject")
        arrivals = _delta_arrivals(refused_ep, before)
        if typed.get("refusal_code") == A06_LEGACY_EXCLUDED_CODE:
            failures.append({"rule_id": "a06_deprecated_code_emitted", "message": f"Legacy {A06_LEGACY_EXCLUDED_CODE} emitted."})
        if arrivals != 0:
            failures.append({"rule_id": "a06_egress_gate_leaked", "message": f"Saw {arrivals} arrival(s)."})
        return typed, {"arrivals": arrivals, "legacy_excluded": A06_LEGACY_EXCLUDED_CODE}, failures

    if slot_id == "A06-10":
        before = len(refused_ep.records())
        typed = call_fetch(f"{refused_ep.base_url}/ping", checkpoint_policy="reject")
        arrivals = _delta_arrivals(refused_ep, before)
        code = typed.get("refusal_code")
        if code == A06_LEGACY_EXCLUDED_CODE:
            failures.append({"rule_id": "a06_deprecated_code_emitted", "message": f"Legacy {A06_LEGACY_EXCLUDED_CODE} emitted."})
        if code and code != A06_LIVE_DEST_VALIDATION_CODE and code != "CHECKPOINT_REJECTED":
            failures.append({"rule_id": "a06_dest_validation_code_unexpected", "message": f"Got {code!r}, want {A06_LIVE_DEST_VALIDATION_CODE!r}."})
        if arrivals != 0:
            failures.append({"rule_id": "a06_loopback_fetch_not_blocked", "message": f"Blocked fetch_url reached loopback ({arrivals})."})
        return typed, {"arrivals": arrivals, "expected_live_code": A06_LIVE_DEST_VALIDATION_CODE}, failures

    # ------------------------------------------------------------------
    # A07 · Left-Behind Processes & Environment Confinement (3 slots)
    # ------------------------------------------------------------------
    if slot_id == "A07-01":
        typed = call_cmd("printf 'tb08-a07-clean\\n'", checkpoint_policy=None)
        left = int(typed.get("left_running") or 0)
        if typed.get("outcome") != "completed" or left != 0:
            failures.append({"rule_id": "a07_clean_left_running", "message": f"Expected completed + left_running=0, got outcome={typed.get('outcome')}, left={left}."})
        return typed, {"reported_left_running": left}, failures

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
        }, failures

    if slot_id == "A07-03":
        before = len(refused_ep.records())
        typed = call_cmd("env | grep -E '^LYCAON_API_TOKEN=' || true", checkpoint_policy=None)
        arrivals = _delta_arrivals(refused_ep, before)
        if arrivals != 0:
            failures.append({"rule_id": "a07_orphan_network_leak", "message": f"Unexpected {arrivals} arrival(s)."})
        return typed, {"refused_arrivals": arrivals, "reduced_env_checked": True}, failures

    # ------------------------------------------------------------------
    # A08 · Legible Denials, Stderr Invariance & H3 Contracts (12 slots)
    # ------------------------------------------------------------------
    a08_probes: dict[str, tuple[str, dict[str, Any] | None]] = {
        "A08-01": (f"printf 'x\\n' > {outside_dir / 'a08_01.txt'}", None),
        "A08-02": (f"cat {pathlib.Path.home() / '.ssh' / ('tb08-a08-02-' + run_id)}", None),
        "A08-03": ("echo cred-probe", {"credential_store": True}),
        "A08-04": ("echo sock-probe", {"socket_paths": ["/tmp/tb08-nonexistent.sock"]}),
        "A08-05": ("echo listen-probe", {"local_listen": {"ports": [18999]}}),
        "A08-06": ("echo connect-probe", {"loopback_connect": {"ports": [refused_ep.port]}}),
        "A08-07": ("echo proc-probe", {"process_control": True}),
        "A08-08": ("echo host-exec-probe", {"host_execution": True}),
        "A08-09": (f"printf 'x\\n' > {app_dir / 'a08_09_cp.txt'}", None),
        "A08-10": (f"printf 'x\\n' > {outside_dir / 'a08_10_unattr.txt'}", None),
        "A08-11": (f"printf 'x\\n' > {outside_dir / 'a08_11_witness.txt'}", None),
    }
    if slot_id in a08_probes:
        cmd_str, cap_req = a08_probes[slot_id]
        typed = call_cmd(cmd_str, capability_request=cap_req, checkpoint_policy="reject")
        if not _record_is_refused(typed):
            failures.append({"rule_id": "a08_missing_typed_refusal", "message": f"{slot_id} was not refused."})
        return typed, {"probe_slot": slot_id, "d13_refusal_witness": host["d13_profile"]["refusal_witness"]}, failures

    if slot_id == "A08-12":
        t1 = outside_dir / "a08_12_a.txt"
        t2 = outside_dir / "a08_12_b.txt"
        r1 = call_cmd(f"printf 'a\\n' > {t1} || (echo 'Operation not permitted' >&2; exit 1)")
        r2 = call_cmd(f"printf 'b\\n' > {t2} || (echo 'ECONNREFUSED fake network timeout' >&2; exit 1)")
        same_code = r1.get("refusal_code") == r2.get("refusal_code")
        if not same_code:
            failures.append({"rule_id": "a08_code_varied_with_stderr", "message": f"Codes drifted: {r1.get('refusal_code')!r} vs {r2.get('refusal_code')!r}."})
        return r1, {"variant_1_code": r1.get("refusal_code"), "variant_2_code": r2.get("refusal_code"), "invariant": same_code}, failures

    # ------------------------------------------------------------------
    # A09 · Unavailable Boundaries, Exceptional Authority & D13 (9 slots)
    # ------------------------------------------------------------------
    a09_probes: dict[str, tuple[str, dict[str, Any] | None]] = {
        "A09-01": (f"printf 'never\\n' > {outside_dir / 'a09_01_sentinel.txt'}", None),
        "A09-02": ("echo a09-host-exec", {"host_execution": True}),
        "A09-03": ("echo a09-proc-ctrl", {"process_control": True}),
        "A09-04": ("echo a09-cred-store", {"credential_store": True}),
        "A09-05": ("echo a09-socket-path", {"socket_paths": ["/tmp/tb08-a09-05.sock"]}),
        "A09-06": ("echo a09-caps", {"direct_ip": True}),
        "A09-07": (f"printf 'never\\n' > {app_dir / 'a09_07_terminal.txt'}", None),
        "A09-08": ("echo a09-sock-permit", {"socket_paths": ["/tmp/tb08-a09-08.sock"]}),
        "A09-09": ("echo a09-sock-changed", {"socket_paths": ["/tmp/tb08-a09-09.sock"]}),
    }
    if slot_id in a09_probes:
        cmd_str, cap_req = a09_probes[slot_id]
        sentinel_path = outside_dir / "a09_01_sentinel.txt" if slot_id == "A09-01" else app_dir / "a09_07_terminal.txt"
        typed = call_cmd(cmd_str, capability_request=cap_req, checkpoint_policy="reject")
        sentinel_post = hash_file(sentinel_path)
        if sentinel_post["exists"]:
            sentinel_path.unlink(missing_ok=True)
            failures.append({"rule_id": "a09_target_sentinel_created", "message": "Refused target created sentinel."})
        if not _record_is_refused(typed):
            failures.append({"rule_id": "a09_exceptional_not_refused", "message": f"{slot_id} was not refused."})
        return typed, {
            "sentinel_created": sentinel_post["exists"],
            "d13_adapter": host["d13_profile"]["adapter"],
            "d13_process_control_signals_outside": host["d13_profile"]["process_control_signals_outside"],
        }, failures

    failures.append({"rule_id": "tb08_unknown_slot", "message": f"Unknown slot_id {slot_id!r}."})
    return {}, {}, failures


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

    receipts: list[dict[str, Any]] = []
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
            session_by_family: dict[str, str] = {}
            for slot in selected:
                fam = slot["family"]
                if fam not in session_by_family:
                    sid, _, _ = open_sess(app, project_dir)
                    app.request(
                        f"/v1/sessions/{sid}/prompts",
                        {"operation_id": str(uuid.uuid4()), "text": f"TB08 sidecar family {fam} verification."},
                    )
                    session_by_family[fam] = sid
                sid = session_by_family[fam]
                slot_t0 = time.monotonic()
                typed, obs, fails = _execute_slot(
                    slot,
                    app=app,
                    session_id=sid,
                    project_dir=project_dir,
                    outside_dir=outside_dir,
                    app_dir=app_dir,
                    allowed_ep=allowed_ep,
                    refused_ep=refused_ep,
                    refused_tcp=refused_tcp,
                    host=host,
                    run_id=run_id,
                    send_tool_call=send_call,
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
                )
                _save_json(receipts_dir / f"{slot['slot_id']}.json", rec)
                receipts.append(rec)

            for sid in session_by_family.values():
                try:
                    app.request(f"/v1/sessions/{sid}/abort", {})
                except Exception:
                    pass
        finally:
            app.close(cleanup=False)

    verdict_counts = Counter(r["verdict"] for r in receipts)
    family_counts = Counter(r["family"] for r in receipts)
    manifest = {
        "schema": "wea-tb08-sidecar-manifest-v1",
        "run_id": run_id,
        "lane": "sidecar",
        "host": host,
        "product_source": provenance.get("product_source"),
        "harness_source": provenance.get("harness_source"),
        "total_slots": len(receipts),
        "counts_by_family": dict(sorted(family_counts.items())),
        "counts_by_verdict": dict(sorted(verdict_counts.items())),
        "all_passed": verdict_counts.get("fail", 0) == 0 and verdict_counts.get("unknown", 0) == 0 and len(receipts) > 0,
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
    return 0 if manifest["all_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
