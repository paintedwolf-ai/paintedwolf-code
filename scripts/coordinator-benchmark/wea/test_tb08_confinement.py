"""Unit & defensive fixture tests for TB08 (A05–A09 Confinement, egress, legible denials)."""

from __future__ import annotations

import pathlib
import subprocess
import sys
import tempfile
import unittest
import urllib.request
from collections import Counter

from wea.fixtures_net import RecordingHTTPFixture, RecordingTCPFixture
from wea.observers import hash_file, read_host_process_table, sha256_bytes
from wea.tb08_confinement import (
    A06_LEGACY_EXCLUDED_CODE,
    A06_LIVE_DEST_VALIDATION_CODE,
    A06_LIVE_EGRESS_GATE_CODE,
    A09_SOCKET_PATH_CHANGED_CODE,
    A09_SOCKET_PERMIT_REQUIRED_CODE,
    A09_UNAVAILABLE_CODE,
    D13_PLATFORM_PROFILES,
    TB08_SLOT_INVENTORY,
    extract_observed_external_endpoints,
    get_platform_profile,
    triage_h3_unstructured_owner_failure,
    verify_a05_filesystem_boundary,
    verify_a06_egress_boundary,
    verify_a07_left_behind_processes,
    verify_a08_legible_denials,
    verify_a09_unavailable_boundary,
)


class TestTB08InventoryAndD13Platforms(unittest.TestCase):
    def test_slot_ledger_counts_match_d18(self) -> None:
        counts = Counter(slot["family"] for slot in TB08_SLOT_INVENTORY)
        self.assertEqual(counts, {"A05": 8, "A06": 10, "A07": 3, "A08": 12, "A09": 9})
        self.assertEqual(len(TB08_SLOT_INVENTORY), 42)
        self.assertEqual(len({s["slot_id"] for s in TB08_SLOT_INVENTORY}), 42)

    def test_d13_platform_differences_explicitly_named(self) -> None:
        mac = get_platform_profile("darwin")
        lin = get_platform_profile("linux")
        self.assertEqual(mac["adapter"], "seatbelt")
        self.assertEqual(lin["adapter"], "namespaces+landlock+seccomp")
        self.assertTrue(mac["left_behind_outlives_command"])
        self.assertFalse(lin["left_behind_outlives_command"])
        self.assertEqual(mac["refusal_witness"], "kernel")
        self.assertEqual(lin["refusal_witness"], "incomplete")
        self.assertFalse(lin["blanket_xdg_data_write_allowed"])
        self.assertIn("darwin", D13_PLATFORM_PROFILES)
        self.assertIn("linux", D13_PLATFORM_PROFILES)


class TestTB08_A05_FilesystemBoundary(unittest.TestCase):
    def test_outside_unchanged_and_permitted_write_recorded_passes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "admitted"
            outside = pathlib.Path(tmp) / "outside"
            root.mkdir()
            outside.mkdir()
            outside_file = outside / "secret.txt"
            outside_file.write_text("immutable-outside\n", encoding="utf-8")
            pre_out = hash_file(outside_file)
            post_out = hash_file(outside_file)

            inside_file = root / "ok.txt"
            inside_body = "permitted-inside\n"
            inside_file.write_text(inside_body, encoding="utf-8")

            res = verify_a05_filesystem_boundary(
                outside_targets={"outside": {"pre": pre_out, "post": post_out}},
                permitted_writes=[{
                    "path": str(inside_file),
                    "expected_sha256": sha256_bytes(inside_body.encode("utf-8")),
                    "actual": hash_file(inside_file),
                }],
                product_write_records=[str(root)],
                refusal_code="SANDBOX_TRY_WRITE_ROOT",
                platform_key="darwin",
            )
            self.assertEqual(res["verdict"], "pass")

    def test_outside_mutation_and_unrecorded_write_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "admitted"
            outside = pathlib.Path(tmp) / "outside"
            root.mkdir()
            outside.mkdir()
            outside_file = outside / "secret.txt"
            outside_file.write_text("before\n", encoding="utf-8")
            pre_out = hash_file(outside_file)
            outside_file.write_text("mutated\n", encoding="utf-8")
            post_out = hash_file(outside_file)

            inside_file = root / "unrecorded.txt"
            inside_file.write_text("hello\n", encoding="utf-8")

            res = verify_a05_filesystem_boundary(
                outside_targets={"outside": {"pre": pre_out, "post": post_out}},
                permitted_writes=[{
                    "path": str(inside_file),
                    "expected_sha256": sha256_bytes(b"hello\n"),
                    "actual": hash_file(inside_file),
                }],
                product_write_records=[],
                refusal_code="SANDBOX_TRY_WRITE_ROOT",
                platform_key="linux",
            )
            self.assertEqual(res["verdict"], "fail")
            self.assertIn("a05_outside_target_mutated", res["failed_rule_ids"])
            self.assertIn("a05_permitted_write_unrecorded", res["failed_rule_ids"])


class TestTB08_A06_EgressAndCatalogCodes(unittest.TestCase):
    def test_recording_fixtures_1to1_observed_match_and_live_codes(self) -> None:
        with (
            RecordingHTTPFixture(fixture_id="allowed-ep") as allowed_ep,
            RecordingHTTPFixture(fixture_id="refused-ep") as refused_ep,
            RecordingTCPFixture(fixture_id="refused-tcp") as refused_tcp,
        ):
            with urllib.request.urlopen(f"{allowed_ep.base_url}/ping", timeout=3) as resp:
                self.assertEqual(resp.status, 200)

            tool_result = {
                "outcome": "completed",
                "external_access": {
                    "endpoints": [
                        {"host": "127.0.0.1", "port": allowed_ep.port, "visibility": "observed", "count": 1},
                        # Planned/declared entries must NOT be counted as observed arrivals.
                        {"host": "127.0.0.1", "port": refused_ep.port, "visibility": "declared", "count": 1},
                    ],
                },
            }
            self.assertEqual(
                extract_observed_external_endpoints(tool_result),
                [f"127.0.0.1:{allowed_ep.port}"],
            )
            res = verify_a06_egress_boundary(
                allowed_fixture_snapshots=[allowed_ep.snapshot()],
                refused_fixture_snapshots=[refused_ep.snapshot(), refused_tcp.snapshot()],
                allowed_tool_results=[tool_result],
                egress_gate_refusal_code=A06_LIVE_EGRESS_GATE_CODE,
                dest_validation_refusal_code=A06_LIVE_DEST_VALIDATION_CODE,
            )
            self.assertEqual(res["verdict"], "pass")

    def test_refused_arrival_and_deprecated_fetch_url_dest_denied_fail(self) -> None:
        with (
            RecordingHTTPFixture(fixture_id="allowed-ep") as allowed_ep,
            RecordingHTTPFixture(fixture_id="refused-ep") as refused_ep,
        ):
            # Simulate a leaked refused arrival.
            with urllib.request.urlopen(f"{refused_ep.base_url}/ping", timeout=3) as resp:
                self.assertEqual(resp.status, 200)

            res = verify_a06_egress_boundary(
                allowed_fixture_snapshots=[allowed_ep.snapshot()],
                refused_fixture_snapshots=[refused_ep.snapshot()],
                allowed_tool_results=[],
                egress_gate_refusal_code=A06_LEGACY_EXCLUDED_CODE,
                dest_validation_refusal_code=A06_LEGACY_EXCLUDED_CODE,
            )
            self.assertEqual(res["verdict"], "fail")
            self.assertIn("a06_refused_fixture_received_connection", res["failed_rule_ids"])
            self.assertIn("a06_deprecated_legacy_code_used", res["failed_rule_ids"])


class TestTB08_A07_LeftBehindProcesses(unittest.TestCase):
    def test_host_process_table_comparison_with_cleanup(self) -> None:
        proc = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(15)"])
        try:
            table = read_host_process_table([proc.pid])
            self.assertIn(str(proc.pid), table["alive"])

            # macOS: 1 left_running matches 1 alive in host process table.
            mac_ok = verify_a07_left_behind_processes(
                reported_left_running=1,
                launched_pids=[proc.pid],
                host_process_table=table,
                platform_key="darwin",
            )
            self.assertEqual(mac_ok["verdict"], "pass")

            # Mismatched count fails closed.
            mismatch = verify_a07_left_behind_processes(
                reported_left_running=0,
                launched_pids=[proc.pid],
                host_process_table=table,
                platform_key="darwin",
            )
            self.assertEqual(mismatch["verdict"], "fail")
            self.assertIn("a07_left_behind_count_mismatch", mismatch["failed_rule_ids"])

            # Linux D13: surviving process after PID namespace exit fails.
            linux_leak = verify_a07_left_behind_processes(
                reported_left_running=1,
                launched_pids=[proc.pid],
                host_process_table=table,
                platform_key="linux",
            )
            self.assertEqual(linux_leak["verdict"], "fail")
            self.assertIn("a07_linux_pid_namespace_leak", linux_leak["failed_rule_ids"])
        finally:
            proc.kill()
            proc.wait(timeout=5)

        after_kill = read_host_process_table([proc.pid])
        self.assertEqual(after_kill["alive"], {})
        linux_clean = verify_a07_left_behind_processes(
            reported_left_running=0,
            launched_pids=[proc.pid],
            host_process_table=after_kill,
            platform_key="linux",
        )
        self.assertEqual(linux_clean["verdict"], "pass")


class TestTB08_A08_LegibleDenialsAndH3(unittest.TestCase):
    def test_codes_stderr_invariance_linux_witness_and_h3_triage(self) -> None:
        obs = [
            {"recovery": "write_root", "code": "SANDBOX_TRY_WRITE_ROOT"},
            {"recovery": "read_path", "code": "SANDBOX_TRY_READ_PATH"},
            {"recovery": "credential_store", "code": "SANDBOX_TRY_CREDENTIAL_STORE"},
            {"recovery": "socket_paths", "code": "SANDBOX_TRY_SOCKET_PATH"},
            {"recovery": "local_listen", "code": "SANDBOX_TRY_LOCAL_NETWORK"},
            {"recovery": "process_control", "code": "SANDBOX_TRY_PROCESS_CONTROL"},
            {"recovery": "host_execution", "code": "SANDBOX_TRY_HOST_EXECUTION"},
            {"recovery": "control_plane", "code": "SANDBOX_TERMINAL_PATH_REFUSED"},
            {"recovery": "unattributed", "code": "SANDBOX_BOUNDARY_REFUSED"},
        ]
        stderr_variants = [
            {"stderr": "Operation not permitted", "code": "SANDBOX_TRY_WRITE_ROOT"},
            {"stderr": "misleading network timeout", "code": "SANDBOX_TRY_WRITE_ROOT"},
            {"stderr": "", "code": "SANDBOX_TRY_WRITE_ROOT"},
        ]
        ok = verify_a08_legible_denials(
            observations=obs,
            stderr_variants=stderr_variants,
            sandbox_refusal_witness="incomplete",
            platform_key="linux",
        )
        self.assertEqual(ok["verdict"], "pass")

        # Varying code with stderr must fail.
        bad_stderr = verify_a08_legible_denials(
            observations=obs,
            stderr_variants=[
                {"stderr": "Operation not permitted", "code": "SANDBOX_TRY_WRITE_ROOT"},
                {"stderr": "connection refused", "code": "SANDBOX_TRY_LOCAL_NETWORK"},
            ],
            sandbox_refusal_witness="incomplete",
            platform_key="linux",
        )
        self.assertEqual(bad_stderr["verdict"], "fail")
        self.assertIn("a08_refusal_code_varied_with_stderr", bad_stderr["failed_rule_ids"])

        # H3 triage: missing-identity vs bounded-work kept distinct.
        h3_identity = triage_h3_unstructured_owner_failure({
            "code": "TOOL_OWNER_FAILED",
            "class": "owner_error",
            "reason": "child program identity: sql: no rows in result set",
        })
        self.assertEqual(h3_identity["verdict"], "fail")
        self.assertEqual(h3_identity["symptom"], "missing_retained_identity")
        self.assertFalse(h3_identity["structured_typed_refusal"])

        h3_work_raw = triage_h3_unstructured_owner_failure({
            "code": "TOOL_OWNER_FAILED",
            "class": "owner_error",
            "reason": "work: requested > 1073741824",
        })
        self.assertEqual(h3_work_raw["verdict"], "fail")
        self.assertEqual(h3_work_raw["symptom"], "bounded_work_limit")
        self.assertNotEqual(h3_identity["owner"], h3_work_raw["owner"])

        h3_typed = triage_h3_unstructured_owner_failure({
            "code": "COMMAND_LIMIT_EXCEEDED",
            "class": "host_rejection",
            "reason": "screen_work_units exceeded",
        })
        self.assertEqual(h3_typed["verdict"], "pass")
        self.assertEqual(h3_typed["symptom"], "bounded_work_limit")
        self.assertTrue(h3_typed["structured_typed_refusal"])


class TestTB08_A09_UnavailableBoundary(unittest.TestCase):
    def test_fail_closed_no_sentinel_and_socket_code_distinction(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            sentinel = pathlib.Path(tmp) / "never-created.sentinel"
            res = verify_a09_unavailable_boundary(
                launch_failed_closed=True,
                refusal_code=A09_UNAVAILABLE_CODE,
                target_sentinel_path=sentinel,
                socket_missing_permit_code=A09_SOCKET_PERMIT_REQUIRED_CODE,
                socket_changed_binding_code=A09_SOCKET_PATH_CHANGED_CODE,
            )
            self.assertEqual(res["verdict"], "pass")

            # Injected fail-open defect: sentinel created.
            sentinel.write_text("leaked\n", encoding="utf-8")
            bad = verify_a09_unavailable_boundary(
                launch_failed_closed=False,
                refusal_code="TOOL_OWNER_FAILED",
                target_sentinel_path=sentinel,
                socket_missing_permit_code=A09_SOCKET_PATH_CHANGED_CODE,
                socket_changed_binding_code=A09_SOCKET_PERMIT_REQUIRED_CODE,
            )
            self.assertEqual(bad["verdict"], "fail")
            self.assertIn("a09_boundary_unavailable_failed_open", bad["failed_rule_ids"])
            self.assertIn("a09_target_executed_sentinel_created", bad["failed_rule_ids"])
            self.assertIn("a09_socket_permit_code_mismatch", bad["failed_rule_ids"])
            self.assertIn("a09_socket_changed_code_mismatch", bad["failed_rule_ids"])


class _FakeSidecarApp:
    def __init__(self, directory: pathlib.Path) -> None:
        self.directory = directory
        self.directory.mkdir(parents=True, exist_ok=True)
        self.closed = False
        self.requests: list[tuple[str, dict]] = []

    def ready(self) -> None:
        pass

    def request(self, path: str, body: dict | None = None) -> dict:
        self.requests.append((path, body or {}))
        return {"ok": True}

    def close(self, cleanup: bool = False) -> None:
        self.closed = True


class TestTB08SidecarRunner(unittest.TestCase):
    def test_all_42_slots_emit_receipts_and_manifest_on_warmed_sidecar(self) -> None:
        import json
        import re
        from wea.tb08_sidecar_runner import run_tb08_sidecar_slots

        created_apps: list[_FakeSidecarApp] = []

        def fake_app_factory(app_dir: pathlib.Path, **_kwargs):
            app = _FakeSidecarApp(app_dir)
            created_apps.append(app)
            return app

        def fake_open_session(_app, _project_dir, **_kwargs):
            return (f"sess-{len(_app.requests)}", "proj-1", "root-1")

        def fake_send_tool_call(_app, _sid, name, args, checkpoint_policy=None):
            call_id = f"call-{name}"
            if name == "fetch_url":
                url = str(args.get("url") or "")
                if "169.254.169.254" in url or "127.0.0.1" in url:
                    return call_id, {"outcome": "rejected", "codes": ["FETCH_URL_BLOCKED"]}, {}
                return call_id, {"outcome": "rejected", "codes": ["WEB_SEARCH_HOST_DENIED"]}, {}

            cmd = str(args.get("command") or "")
            if checkpoint_policy is None:
                # Positive controls & A07 clean/daemon/env probes.
                if "a05_08_permitted.txt" in cmd:
                    (created_apps[0].directory.parent / "project" / "a05_08_permitted.txt").write_text(
                        "tb08-a05-08-permitted\n", encoding="utf-8"
                    )
                return call_id, {"outcome": "completed", "left_running": 0}, {}

            if checkpoint_policy == "approve_once":
                port = None
                m = re.search(r"http://127\.0\.0\.1:(\d+)/ping", cmd)
                if m:
                    port = int(m.group(1))
                elif "_a06_08_lineage.py" in cmd:
                    # Actually execute the generated _a06_08_lineage.py script to catch syntax/quoting bugs!
                    script_file = created_apps[0].directory.parent / "project" / "_a06_08_lineage.py"
                    subprocess.run([sys.executable, "-B", str(script_file)], check=True)
                    ports = ((args.get("capability_request") or {}).get("loopback_connect") or {}).get("ports") or []
                    return call_id, {
                        "outcome": "completed",
                        "external_access": {
                            "endpoints": [
                                {
                                    "host": "127.0.0.1",
                                    "port": int(ports[0]) if ports else 0,
                                    "visibility": "observed",
                                    "decision": "allow",
                                    "allowed_count": 1,
                                }
                            ]
                        },
                    }, {}
                if port is not None:
                    with urllib.request.urlopen(f"http://127.0.0.1:{port}/ping", timeout=3) as resp:
                        resp.read()
                    return call_id, {
                        "outcome": "completed",
                        "external_access": {
                            "endpoints": [
                                {
                                    "host": "127.0.0.1",
                                    "port": port,
                                    "visibility": "observed",
                                    "decision": "allow",
                                    "allowed_count": 1,
                                }
                            ]
                        },
                    }, {}
                return call_id, {"outcome": "completed"}, {}

            return call_id, {"outcome": "rejected", "codes": ["SANDBOX_TRY_WRITE_ROOT"]}, {}

        with tempfile.TemporaryDirectory() as tmp:
            out_dir = pathlib.Path(tmp) / "tb08-out"
            manifest = run_tb08_sidecar_slots(
                out_dir,
                platform_key="linux",
                app_factory=fake_app_factory,
                session_opener=fake_open_session,
                tool_caller=fake_send_tool_call,
            )
            # Single warmed sidecar reused across all 42 slots.
            self.assertEqual(len(created_apps), 1)
            self.assertTrue(created_apps[0].closed)
            self.assertEqual(manifest["total_slots"], 42)
            self.assertEqual(
                manifest["counts_by_family"],
                {"A05": 8, "A06": 10, "A07": 3, "A08": 12, "A09": 9},
            )
            self.assertTrue(manifest["all_passed"], f"manifest failures: {manifest}")

            # Verify one structured JSON receipt per scheduled variant.
            receipt_files = sorted((out_dir / "receipts").glob("*.json"))
            self.assertEqual(len(receipt_files), 42)
            sample = json.loads((out_dir / "receipts" / "A06-10.json").read_text(encoding="utf-8"))
            for key in (
                "run_id",
                "slot_id",
                "family",
                "lane",
                "host",
                "product_source",
                "harness_source",
                "typed_result",
                "independent_observation",
                "verdict",
            ):
                self.assertIn(key, sample)
            self.assertEqual(sample["lane"], "sidecar")
            self.assertEqual(sample["typed_result"]["refusal_code"], "FETCH_URL_BLOCKED")


if __name__ == "__main__":
    unittest.main()
