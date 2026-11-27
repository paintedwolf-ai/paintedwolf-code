"""TB08 · Confinement, egress and legible denials (Case families A05–A09).

Defensive verification module (D4–D7, D13, D18):
  - Observes confinement strictly from outside the boundary (host SHA-256 reads,
    loopback RecordingHTTPFixture / RecordingTCPFixture, independent host process table).
  - Never weakens protections, never probes exploit chains, uses isolated temp fixtures.
  - Enforces TB01 catalog decisions:
      * WEB_SEARCH_HOST_DENIED -> egress gate
      * FETCH_URL_BLOCKED      -> destination validation
      * FETCH_URL_DEST_DENIED  -> legacy exclusion (deprecated, 0 Go emitters)
  - Names D13 documented platform differences between macOS (Seatbelt) and
    Linux (namespaces/Landlock/seccomp).
  - Includes H3 triage verification (`component=tool_owner` unstructured failures
    against the typed refusal contract).
"""

from __future__ import annotations

import pathlib
import platform
from collections import Counter
from typing import Any, Iterable

from wea.observers import hash_file, read_host_process_table

# ---------------------------------------------------------------------------
# TB01 Catalog Decisions & Isolation Code Constants
# ---------------------------------------------------------------------------

A06_LIVE_EGRESS_GATE_CODE = "WEB_SEARCH_HOST_DENIED"
A06_LIVE_DEST_VALIDATION_CODE = "FETCH_URL_BLOCKED"
A06_LEGACY_EXCLUDED_CODE = "FETCH_URL_DEST_DENIED"

A05_RECOVERY_CODES = frozenset({
    "SANDBOX_TRY_WRITE_ROOT",
    "SANDBOX_TRY_READ_PATH",
})

A09_UNAVAILABLE_CODE = "SANDBOX_EXECUTION_BOUNDARY_UNAVAILABLE"
A09_SOCKET_PERMIT_REQUIRED_CODE = "SANDBOX_SOCKET_PERMIT_REQUIRED"
A09_SOCKET_PATH_CHANGED_CODE = "SANDBOX_SOCKET_PATH_CHANGED"
A09_EXCEPTIONAL_CODES = frozenset({
    "SANDBOX_TRY_HOST_EXECUTION",
    "SANDBOX_TRY_PROCESS_CONTROL",
    "SANDBOX_TRY_CREDENTIAL_STORE",
    "SANDBOX_TRY_SOCKET_PATH",
    "SANDBOX_TRY_CAPABILITIES",
    "SANDBOX_TERMINAL_PATH_REFUSED",
})

OPERATION_RECOVERY_TO_CODE: dict[str, str] = {
    "write_root": "SANDBOX_TRY_WRITE_ROOT",
    "read_path": "SANDBOX_TRY_READ_PATH",
    "credential_store": "SANDBOX_TRY_CREDENTIAL_STORE",
    "socket_paths": "SANDBOX_TRY_SOCKET_PATH",
    "local_listen": "SANDBOX_TRY_LOCAL_NETWORK",
    "loopback_connect_or_direct_ip": "SANDBOX_TRY_LOCAL_NETWORK",
    "process_control": "SANDBOX_TRY_PROCESS_CONTROL",
    "host_execution": "SANDBOX_TRY_HOST_EXECUTION",
    "control_plane": "SANDBOX_TERMINAL_PATH_REFUSED",
    "unattributed": "SANDBOX_BOUNDARY_REFUSED",
}

# ---------------------------------------------------------------------------
# D13 Documented Platform Differences (macOS Seatbelt vs Linux namespaces)
# ---------------------------------------------------------------------------

D13_PLATFORM_PROFILES: dict[str, dict[str, Any]] = {
    "darwin": {
        "platform": "darwin",
        "adapter": "seatbelt",
        "refusal_witness": "kernel",
        "left_behind_outlives_command": True,
        "denied_directory_is_masked": False,
        "blanket_xdg_data_write_allowed": False,
        "process_control_signals_outside": True,
        "notes": (
            "macOS Seatbelt reads tagged kernel refusals from unified log; "
            "left-behind daemons keep their Seatbelt boundary and reach the broker "
            "only under a running action of the same project."
        ),
    },
    "linux": {
        "platform": "linux",
        "adapter": "namespaces+landlock+seccomp",
        "refusal_witness": "incomplete",
        "left_behind_outlives_command": False,
        "denied_directory_is_masked": True,
        "blanket_xdg_data_write_allowed": False,
        "process_control_signals_outside": False,
        "notes": (
            "Linux helper runs as PID 1 in fresh user/mount/PID/net/IPC namespaces "
            "with Landlock + seccomp; command descendants end on helper exit; "
            "Landlock has no unprivileged refusal stream so every action reports "
            "sandbox_refusal_witness='incomplete'; no blanket XDG_DATA_HOME/XDG_STATE_HOME write root."
        ),
    },
}


def current_platform_key() -> str:
    sys_name = platform.system().lower()
    if sys_name == "darwin":
        return "darwin"
    if sys_name == "linux":
        return "linux"
    return sys_name


def get_platform_profile(platform_key: str | None = None) -> dict[str, Any]:
    key = (platform_key or current_platform_key()).lower()
    if key not in D13_PLATFORM_PROFILES:
        raise ValueError(f"Unsupported TB08 platform {key!r} (expected 'darwin' or 'linux')")
    return dict(D13_PLATFORM_PROFILES[key])


# ---------------------------------------------------------------------------
# D18 Fixed Slot Inventory for TB08 (A05=8, A06=10, A07=3, A08=12, A09=9 -> 42/platform)
# ---------------------------------------------------------------------------

TB08_SLOT_INVENTORY: list[dict[str, Any]] = [
    # A05 · Filesystem boundary (8 slots)
    {"slot_id": "A05-01", "family": "A05", "name": "write_outside_admitted_roots", "expected_code": "SANDBOX_TRY_WRITE_ROOT"},
    {"slot_id": "A05-02", "family": "A05", "name": "write_through_escaping_symlink", "expected_code": "SANDBOX_TRY_WRITE_ROOT"},
    {"slot_id": "A05-03", "family": "A05", "name": "write_into_shared_temp", "expected_code": "SANDBOX_SHARED_TEMP_REFUSED"},
    {"slot_id": "A05-04", "family": "A05", "name": "write_into_cache_root_policy", "expected_code": None, "d13_note": "macOS cache conventions vs Linux no blanket XDG_DATA_HOME/XDG_STATE_HOME"},
    {"slot_id": "A05-05", "family": "A05", "name": "write_into_protected_location", "expected_code": "SANDBOX_TERMINAL_PATH_REFUSED"},
    {"slot_id": "A05-06", "family": "A05", "name": "read_protected_control_plane", "expected_code": "SANDBOX_TERMINAL_PATH_REFUSED"},
    {"slot_id": "A05-07", "family": "A05", "name": "read_sensitive_key_material", "expected_code": "SANDBOX_TRY_READ_PATH"},
    {"slot_id": "A05-08", "family": "A05", "name": "frozen_write_roots_positive_control", "expected_code": None},
    # A06 · Egress modes & destination validation (10 slots)
    {"slot_id": "A06-01", "family": "A06", "name": "network_deny_zero_arrivals", "expected_code": "SANDBOX_TRY_LOCAL_NETWORK"},
    {"slot_id": "A06-02", "family": "A06", "name": "mediated_allowed_exact_1to1", "expected_code": None},
    {"slot_id": "A06-03", "family": "A06", "name": "mediated_refused_zero_arrivals", "expected_code": "WEB_SEARCH_HOST_DENIED"},
    {"slot_id": "A06-04", "family": "A06", "name": "direct_ip_authority_mode", "expected_code": None},
    {"slot_id": "A06-05", "family": "A06", "name": "local_capability_loopback_port", "expected_code": "SANDBOX_TRY_LOCAL_NETWORK"},
    {"slot_id": "A06-06", "family": "A06", "name": "ignore_proxy_gains_no_direct_path", "expected_code": "SANDBOX_TRY_LOCAL_NETWORK"},
    {"slot_id": "A06-07", "family": "A06", "name": "redirect_crosses_gate_and_drops_headers", "expected_code": None},
    {"slot_id": "A06-08", "family": "A06", "name": "lineage_follows_fork_exec_setsid_reparent", "expected_code": None},
    {"slot_id": "A06-09", "family": "A06", "name": "fetch_url_egress_gate_denied", "expected_code": A06_LIVE_EGRESS_GATE_CODE, "legacy_excluded": A06_LEGACY_EXCLUDED_CODE},
    {"slot_id": "A06-10", "family": "A06", "name": "fetch_url_destination_validation_blocked", "expected_code": A06_LIVE_DEST_VALIDATION_CODE, "legacy_excluded": A06_LEGACY_EXCLUDED_CODE},
    # A07 · Processes a command leaves behind (3 slots)
    {"slot_id": "A07-01", "family": "A07", "name": "clean_command_zero_left_behind", "expected_code": None},
    {"slot_id": "A07-02", "family": "A07", "name": "daemon_left_behind_host_table_parity", "expected_code": None, "d13_note": "macOS outlives with Seatbelt; Linux PID namespace ends on exit"},
    {"slot_id": "A07-03", "family": "A07", "name": "orphan_broker_refusal_without_running_action", "expected_code": None, "d13_note": "macOS refuses orphan broker dial naming leaving command; Linux already reaped"},
    # A08 · Legible denials (12 slots)
    {"slot_id": "A08-01", "family": "A08", "name": "refusal_write_root_code", "expected_code": "SANDBOX_TRY_WRITE_ROOT"},
    {"slot_id": "A08-02", "family": "A08", "name": "refusal_read_path_code", "expected_code": "SANDBOX_TRY_READ_PATH"},
    {"slot_id": "A08-03", "family": "A08", "name": "refusal_credential_store_code", "expected_code": "SANDBOX_TRY_CREDENTIAL_STORE"},
    {"slot_id": "A08-04", "family": "A08", "name": "refusal_socket_path_code", "expected_code": "SANDBOX_TRY_SOCKET_PATH"},
    {"slot_id": "A08-05", "family": "A08", "name": "refusal_local_listen_code", "expected_code": "SANDBOX_TRY_LOCAL_NETWORK"},
    {"slot_id": "A08-06", "family": "A08", "name": "refusal_outbound_port_code", "expected_code": "SANDBOX_TRY_LOCAL_NETWORK"},
    {"slot_id": "A08-07", "family": "A08", "name": "refusal_process_control_code", "expected_code": "SANDBOX_TRY_PROCESS_CONTROL"},
    {"slot_id": "A08-08", "family": "A08", "name": "refusal_host_execution_code", "expected_code": "SANDBOX_TRY_HOST_EXECUTION"},
    {"slot_id": "A08-09", "family": "A08", "name": "refusal_terminal_control_plane_code", "expected_code": "SANDBOX_TERMINAL_PATH_REFUSED"},
    {"slot_id": "A08-10", "family": "A08", "name": "unattributed_refusal_stays_visible", "expected_code": "SANDBOX_BOUNDARY_REFUSED"},
    {"slot_id": "A08-11", "family": "A08", "name": "linux_incomplete_witness_present", "expected_code": None, "d13_note": "Linux always reports sandbox_refusal_witness='incomplete'"},
    {"slot_id": "A08-12", "family": "A08", "name": "stderr_text_invariance_and_h3_triage", "expected_code": "SANDBOX_TRY_WRITE_ROOT"},
    # A09 · Unavailable boundaries & exceptional authority (9 slots)
    {"slot_id": "A09-01", "family": "A09", "name": "required_boundary_unavailable_fail_closed", "expected_code": A09_UNAVAILABLE_CODE},
    {"slot_id": "A09-02", "family": "A09", "name": "exceptional_host_execution", "expected_code": "SANDBOX_TRY_HOST_EXECUTION"},
    {"slot_id": "A09-03", "family": "A09", "name": "exceptional_process_control", "expected_code": "SANDBOX_TRY_PROCESS_CONTROL"},
    {"slot_id": "A09-04", "family": "A09", "name": "exceptional_credential_store", "expected_code": "SANDBOX_TRY_CREDENTIAL_STORE"},
    {"slot_id": "A09-05", "family": "A09", "name": "exceptional_socket_path", "expected_code": "SANDBOX_TRY_SOCKET_PATH"},
    {"slot_id": "A09-06", "family": "A09", "name": "exceptional_capabilities", "expected_code": "SANDBOX_TRY_CAPABILITIES"},
    {"slot_id": "A09-07", "family": "A09", "name": "exceptional_terminal_path_refused", "expected_code": "SANDBOX_TERMINAL_PATH_REFUSED"},
    {"slot_id": "A09-08", "family": "A09", "name": "socket_missing_permit_refused", "expected_code": A09_SOCKET_PERMIT_REQUIRED_CODE},
    {"slot_id": "A09-09", "family": "A09", "name": "socket_changed_recipient_binding_refused", "expected_code": A09_SOCKET_PATH_CHANGED_CODE},
]


# ---------------------------------------------------------------------------
# A05 Verifier · Filesystem Confinement
# ---------------------------------------------------------------------------

def verify_a05_filesystem_boundary(
    *,
    outside_targets: dict[str, dict[str, Any]],
    permitted_writes: list[dict[str, Any]],
    product_write_records: Iterable[str],
    refusal_code: str,
    platform_key: str | None = None,
) -> dict[str, Any]:
    """Verify A05 invariants from D4 host-side SHA-256 observations.

    Args:
        outside_targets: mapping label -> {"pre": hash_file(...), "post": hash_file(...)}.
        permitted_writes: list of {"path": str, "expected_sha256": str, "actual": hash_file(...)}.
        product_write_records: paths/roots recorded by the product boundary or file-edit records.
        refusal_code: typed refusal code emitted for the refused outside-root attempt.
    """
    profile = get_platform_profile(platform_key)
    failures: list[dict[str, Any]] = []

    for label, entry in outside_targets.items():
        pre = entry.get("pre") or {}
        post = entry.get("post") or {}
        if pre.get("exists") != post.get("exists") or pre.get("sha256") != post.get("sha256"):
            failures.append({
                "rule_id": "a05_outside_target_mutated",
                "message": f"Outside-root target '{label}' changed across confined execution.",
                "details": {"label": label, "pre": pre, "post": post},
            })

    recorded_norm = {str(pathlib.Path(p).resolve()) for p in product_write_records if p}
    for pw in permitted_writes:
        p = str(pathlib.Path(pw["path"]).resolve())
        actual = pw.get("actual") or hash_file(p)
        expected_sha = pw.get("expected_sha256")
        if not actual.get("exists") or (expected_sha and actual.get("sha256") != expected_sha):
            failures.append({
                "rule_id": "a05_permitted_write_mismatch",
                "message": f"Permitted write '{p}' missing or SHA-256 mismatched.",
                "details": {"path": p, "expected_sha256": expected_sha, "actual": actual},
            })
        matched_record = any(
            p == rec or p.startswith(rec.rstrip("/") + "/")
            for rec in recorded_norm
        )
        if not matched_record:
            failures.append({
                "rule_id": "a05_permitted_write_unrecorded",
                "message": f"Permitted write '{p}' has no matching product boundary/write record.",
                "details": {"path": p, "recorded": sorted(recorded_norm)},
            })

    if refusal_code not in A05_RECOVERY_CODES and refusal_code != "SANDBOX_TERMINAL_PATH_REFUSED":
        failures.append({
            "rule_id": "a05_unexpected_refusal_code",
            "message": f"Expected typed A05 refusal code, got {refusal_code!r}.",
            "details": {"refusal_code": refusal_code},
        })

    return {
        "family": "A05",
        "platform": profile,
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
    }


# ---------------------------------------------------------------------------
# A06 Verifier · Egress Modes & Destination Validation (TB01 Catalog Decisions)
# ---------------------------------------------------------------------------

def extract_observed_external_endpoints(tool_result: dict[str, Any]) -> list[str]:
    """Extract only `visibility == 'observed'` endpoints from `tool_result.external_access.endpoints`."""
    ext = tool_result.get("external_access")
    if not isinstance(ext, dict):
        return []
    endpoints = ext.get("endpoints")
    if not isinstance(endpoints, list):
        return []
    observed: list[str] = []
    for ep in endpoints:
        if not isinstance(ep, dict):
            continue
        if ep.get("visibility") != "observed":
            continue
        host = ep.get("host") or ep.get("address") or ""
        port = ep.get("port")
        if host and port and ":" not in str(host):
            addr = f"{host}:{port}"
        else:
            addr = str(host)
        if addr:
            count = int(ep.get("count", 1) or 1)
            observed.extend([addr] * max(count, 1))
    return observed


def verify_a06_egress_boundary(
    *,
    allowed_fixture_snapshots: list[dict[str, Any]],
    refused_fixture_snapshots: list[dict[str, Any]],
    allowed_tool_results: list[dict[str, Any]],
    egress_gate_refusal_code: str | None = None,
    dest_validation_refusal_code: str | None = None,
    platform_key: str | None = None,
) -> dict[str, Any]:
    """Verify A06 invariants:
      - 0 refused arrivals at recording fixtures
      - Exact 1:1 allowed arrival match with `tool_result.external_access.endpoints` (`visibility: "observed"`)
      - TB01 catalog live codes (`WEB_SEARCH_HOST_DENIED` vs `FETCH_URL_BLOCKED`) and legacy exclusion
        (`FETCH_URL_DEST_DENIED` rejected if emitted on live paths).
    """
    profile = get_platform_profile(platform_key)
    failures: list[dict[str, Any]] = []

    # 1. Refused fixtures MUST have 0 arrivals.
    for snap in refused_fixture_snapshots:
        count = int(snap.get("connection_count", 0))
        if count != 0:
            failures.append({
                "rule_id": "a06_refused_fixture_received_connection",
                "message": f"Refused fixture '{snap.get('fixture_id')}' received {count} connection(s); expected 0.",
                "details": {"fixture_id": snap.get("fixture_id"), "address": snap.get("address"), "connection_count": count},
            })

    # 2. Allowed fixture arrivals MUST match observed product endpoints 1:1.
    fixture_counts: Counter[str] = Counter()
    for snap in allowed_fixture_snapshots:
        addr = str(snap.get("address") or "")
        count = int(snap.get("connection_count", 0))
        if addr and count > 0:
            fixture_counts[addr] += count

    product_counts: Counter[str] = Counter()
    for tr in allowed_tool_results:
        for addr in extract_observed_external_endpoints(tr):
            product_counts[addr] += 1

    for addr in sorted(set(fixture_counts) | set(product_counts)):
        f_n = fixture_counts.get(addr, 0)
        p_n = product_counts.get(addr, 0)
        if f_n > p_n:
            failures.append({
                "rule_id": "a06_unrecorded_allowed_arrival",
                "message": f"Fixture observed {f_n} arrival(s) at {addr}, but product recorded {p_n} observed endpoint(s).",
                "details": {"address": addr, "fixture_count": f_n, "product_observed_count": p_n},
            })
        elif p_n > f_n:
            failures.append({
                "rule_id": "a06_unobserved_product_endpoint",
                "message": f"Product recorded {p_n} observed endpoint(s) at {addr}, but fixture observed {f_n}.",
                "details": {"address": addr, "fixture_count": f_n, "product_observed_count": p_n},
            })

    # 3. TB01 catalog live refusal codes (and explicit legacy exclusion).
    if egress_gate_refusal_code is not None:
        if egress_gate_refusal_code == A06_LEGACY_EXCLUDED_CODE:
            failures.append({
                "rule_id": "a06_deprecated_legacy_code_used",
                "message": f"Deprecated {A06_LEGACY_EXCLUDED_CODE} cannot be substituted for {A06_LIVE_EGRESS_GATE_CODE}.",
                "details": {"seam": "egress_gate", "actual_code": egress_gate_refusal_code},
            })
        elif egress_gate_refusal_code != A06_LIVE_EGRESS_GATE_CODE:
            failures.append({
                "rule_id": "a06_egress_gate_code_mismatch",
                "message": f"Egress gate emitted {egress_gate_refusal_code!r}, want {A06_LIVE_EGRESS_GATE_CODE!r}.",
                "details": {"actual_code": egress_gate_refusal_code, "expected_code": A06_LIVE_EGRESS_GATE_CODE},
            })

    if dest_validation_refusal_code is not None:
        if dest_validation_refusal_code == A06_LEGACY_EXCLUDED_CODE:
            failures.append({
                "rule_id": "a06_deprecated_legacy_code_used",
                "message": f"Deprecated {A06_LEGACY_EXCLUDED_CODE} cannot be substituted for {A06_LIVE_DEST_VALIDATION_CODE}.",
                "details": {"seam": "destination_validation", "actual_code": dest_validation_refusal_code},
            })
        elif dest_validation_refusal_code != A06_LIVE_DEST_VALIDATION_CODE:
            failures.append({
                "rule_id": "a06_dest_validation_code_mismatch",
                "message": f"Destination validation emitted {dest_validation_refusal_code!r}, want {A06_LIVE_DEST_VALIDATION_CODE!r}.",
                "details": {"actual_code": dest_validation_refusal_code, "expected_code": A06_LIVE_DEST_VALIDATION_CODE},
            })

    return {
        "family": "A06",
        "platform": profile,
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
        "fixture_counts": dict(fixture_counts),
        "product_observed_counts": dict(product_counts),
    }


# ---------------------------------------------------------------------------
# A07 Verifier · Left-Behind Processes (D4 Host Process Table + D13 Platform)
# ---------------------------------------------------------------------------

def verify_a07_left_behind_processes(
    *,
    reported_left_running: int,
    launched_pids: Iterable[int],
    host_process_table: dict[str, Any] | None = None,
    platform_key: str | None = None,
) -> dict[str, Any]:
    """Compare command result's `left_running` against independent host process table."""
    profile = get_platform_profile(platform_key)
    table = host_process_table if host_process_table is not None else read_host_process_table(launched_pids)
    alive_map = table.get("alive") or {}
    host_alive_count = len(alive_map)
    failures: list[dict[str, Any]] = []

    if int(reported_left_running) != host_alive_count:
        failures.append({
            "rule_id": "a07_left_behind_count_mismatch",
            "message": (
                f"Command result reported left_running={reported_left_running}, "
                f"but independent host process table shows {host_alive_count} alive PID(s)."
            ),
            "details": {
                "reported_left_running": int(reported_left_running),
                "host_alive_count": host_alive_count,
                "alive_pids": sorted(int(k) for k in alive_map),
            },
        })

    if not profile["left_behind_outlives_command"] and host_alive_count > 0:
        failures.append({
            "rule_id": "a07_linux_pid_namespace_leak",
            "message": f"Linux PID namespace left {host_alive_count} surviving process(es) after command exit.",
            "details": {"alive_pids": sorted(int(k) for k in alive_map)},
        })

    return {
        "family": "A07",
        "platform": profile,
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
        "reported_left_running": int(reported_left_running),
        "host_alive_count": host_alive_count,
    }


# ---------------------------------------------------------------------------
# A08 Verifier · Legible Boundary Refusals + H3 Triage
# ---------------------------------------------------------------------------

def verify_a08_legible_denials(
    *,
    observations: list[dict[str, Any]],
    stderr_variants: list[dict[str, Any]],
    sandbox_refusal_witness: str | None = None,
    platform_key: str | None = None,
) -> dict[str, Any]:
    """Verify A08 refusal code accuracy, stderr invariance, and D13 Linux witness."""
    profile = get_platform_profile(platform_key)
    failures: list[dict[str, Any]] = []

    for obs in observations:
        recovery = obs.get("recovery", "")
        actual_code = obs.get("code", "")
        expected_code = OPERATION_RECOVERY_TO_CODE.get(recovery)
        if not expected_code:
            failures.append({
                "rule_id": "a08_unknown_recovery_axis",
                "message": f"Unrecognized recovery axis {recovery!r}.",
                "details": obs,
            })
        elif actual_code != expected_code:
            failures.append({
                "rule_id": "a08_refusal_code_mismatch",
                "message": f"Recovery '{recovery}' emitted {actual_code!r}, want {expected_code!r}.",
                "details": {"recovery": recovery, "actual_code": actual_code, "expected_code": expected_code},
            })

    if len(stderr_variants) >= 2:
        codes = {v.get("code") for v in stderr_variants}
        if len(codes) != 1 or "" in codes or None in codes:
            failures.append({
                "rule_id": "a08_refusal_code_varied_with_stderr",
                "message": "Refusal code changed when command stderr/error prose varied.",
                "details": {"variants": stderr_variants},
            })

    if profile["platform"] == "linux" and sandbox_refusal_witness is not None:
        if sandbox_refusal_witness != "incomplete":
            failures.append({
                "rule_id": "a08_linux_missing_incomplete_witness",
                "message": f"Linux action reported sandbox_refusal_witness={sandbox_refusal_witness!r}, want 'incomplete'.",
                "details": {"sandbox_refusal_witness": sandbox_refusal_witness},
            })

    return {
        "family": "A08",
        "platform": profile,
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
    }


def triage_h3_unstructured_owner_failure(failure_record: dict[str, Any]) -> dict[str, Any]:
    """Triage H3 (`component=tool_owner` unstructured failures) against the typed refusal contract.

    A compliant typed refusal carries a specific typed code (not TOOL_OWNER_FAILED) and
    `failure_class != 'owner_error'`. An unstructured owner error falls back to
    `TOOL_OWNER_FAILED` with `failure_class == 'owner_error'`.
    """
    code = str(failure_record.get("code") or "")
    failure_class = str(failure_record.get("class") or "")
    is_unstructured_fallback = (code == "TOOL_OWNER_FAILED" or failure_class == "owner_error" or not code)
    return {
        "incident_id": "H3",
        "code": code,
        "class": failure_class,
        "structured_typed_refusal": not is_unstructured_fallback,
        "verdict": "fail" if is_unstructured_fallback else "pass",
        "failed_rule_ids": ["a08_h3_unstructured_owner_failure"] if is_unstructured_fallback else [],
    }


# ---------------------------------------------------------------------------
# A09 Verifier · Required Boundary Unavailable & Exceptional Authority
# ---------------------------------------------------------------------------

def verify_a09_unavailable_boundary(
    *,
    launch_failed_closed: bool,
    refusal_code: str,
    target_sentinel_path: str | pathlib.Path,
    target_pid: int | None = None,
    socket_missing_permit_code: str | None = None,
    socket_changed_binding_code: str | None = None,
    platform_key: str | None = None,
) -> dict[str, Any]:
    """Verify A09 fail-closed launch (target sentinel/process never created) and socket refusal distinction."""
    profile = get_platform_profile(platform_key)
    failures: list[dict[str, Any]] = []

    if not launch_failed_closed:
        failures.append({
            "rule_id": "a09_boundary_unavailable_failed_open",
            "message": "Launch did not fail closed when required confinement boundary was unavailable.",
            "details": {"refusal_code": refusal_code},
        })

    if refusal_code != A09_UNAVAILABLE_CODE:
        failures.append({
            "rule_id": "a09_unexpected_unavailable_code",
            "message": f"Expected {A09_UNAVAILABLE_CODE!r}, got {refusal_code!r}.",
            "details": {"actual_code": refusal_code, "expected_code": A09_UNAVAILABLE_CODE},
        })

    sentinel_info = hash_file(target_sentinel_path)
    if sentinel_info["exists"]:
        failures.append({
            "rule_id": "a09_target_executed_sentinel_created",
            "message": f"Target sentinel '{target_sentinel_path}' was created despite unavailable boundary.",
            "details": sentinel_info,
        })

    if target_pid is not None and target_pid > 0:
        table = read_host_process_table([target_pid])
        if table.get("alive"):
            failures.append({
                "rule_id": "a09_target_process_started",
                "message": f"Target process PID {target_pid} is alive despite unavailable boundary.",
                "details": table,
            })

    if socket_missing_permit_code is not None and socket_missing_permit_code != A09_SOCKET_PERMIT_REQUIRED_CODE:
        failures.append({
            "rule_id": "a09_socket_permit_code_mismatch",
            "message": f"Missing socket permit emitted {socket_missing_permit_code!r}, want {A09_SOCKET_PERMIT_REQUIRED_CODE!r}.",
            "details": {"actual": socket_missing_permit_code},
        })

    if socket_changed_binding_code is not None and socket_changed_binding_code != A09_SOCKET_PATH_CHANGED_CODE:
        failures.append({
            "rule_id": "a09_socket_changed_code_mismatch",
            "message": f"Changed socket binding emitted {socket_changed_binding_code!r}, want {A09_SOCKET_PATH_CHANGED_CODE!r}.",
            "details": {"actual": socket_changed_binding_code},
        })

    return {
        "family": "A09",
        "platform": profile,
        "verdict": "pass" if not failures else "fail",
        "passed": not failures,
        "failures": failures,
        "failed_rule_ids": sorted({f["rule_id"] for f in failures}),
    }
