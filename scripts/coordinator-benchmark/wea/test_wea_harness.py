"""Comprehensive unit tests for the TB02 WEA shared driver, observers, canaries, endpoints, checker, and replay."""

from __future__ import annotations

import copy
import json
import os
import pathlib
import socket
import sqlite3
import sys
import tempfile
import unittest
import urllib.request

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea import cli
from wea.canary import CanaryGenerator, scan_for_canaries
from wea.checker import check_evidence
from wea.controls import (
    build_defect_controls,
    build_healthy_controls,
    build_missing_instrumentation_control,
    make_healthy_baseline,
    run_qualification_controls,
)
from wea.fixtures_net import RecordingHTTPFixture, RecordingTCPFixture
from wea.observers import (
    AuthorityObserver,
    EditsObserver,
    WorkersObserver,
    diff_tree_hashes,
    hash_tree,
    outside_diff_dirs,
    outside_unified_diff,
    read_host_process_table,
    read_sqlite_tables_ro,
)
from wea.provenance import collect_provenance, file_sha256
from wea.replay import export_sanitized_bundle, load_and_verify_bundle, replay_on_fresh_profile, sanitize_value


class TestProvenanceSplit(unittest.TestCase):
    def test_product_and_harness_provenance_recorded_separately(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_bin = root / "lycaon-dev"
            fake_bin.write_bytes(b"#!/bin/sh\necho 0.0.1-test\n")
            fake_bin.chmod(0o755)
            bin_sha = file_sha256(fake_bin)

            manifest = root / "engine-manifest.json"
            manifest.write_text(
                json.dumps({
                    "executable_sha256": bin_sha,
                    "git_head": "bbeb46e72ce3d1cfc052259cadcce29185bb40bd",
                    "git_tree": "1830e4d60ed640d4ecf386653377498788fa9320",
                }),
                encoding="utf-8",
            )

            prov = collect_provenance(
                engine_bin=fake_bin,
                engine_manifest=manifest,
                lane="sidecar",
            )
            self.assertIn("product_source", prov)
            self.assertIn("harness_source", prov)
            self.assertEqual(prov["product_source"]["executable_sha256"], bin_sha)
            self.assertTrue(prov["product_source"]["verified"])
            self.assertEqual(prov["product_source"]["effective_git_head"], "bbeb46e72ce3d1cfc052259cadcce29185bb40bd")
            self.assertTrue(prov["harness_source"]["git_head"])
            self.assertTrue(prov["product_harness_distinct"])

    def test_manifest_sha_mismatch_marks_unverified(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_bin = root / "lycaon-dev"
            fake_bin.write_bytes(b"binary-bytes")
            manifest = root / "manifest.json"
            manifest.write_text(
                json.dumps({"executable_sha256": "0" * 64, "git_head": "bbeb46e72ce3d1cfc052259cadcce29185bb40bd"}),
                encoding="utf-8",
            )
            prov = collect_provenance(engine_bin=fake_bin, engine_manifest=manifest)
            self.assertFalse(prov["product_source"]["verified"])
            self.assertFalse(prov["product_source"]["manifest_sha256_match"])


class TestCanaryGeneratorAndScanner(unittest.TestCase):
    def test_deterministic_and_csprng_canaries_and_leak_detection(self) -> None:
        gen_det = CanaryGenerator("run42", seed="fixed-seed")
        t1 = gen_det.mint("api")
        self.assertTrue(t1.startswith("WEA_CANARY_run42_api_"))

        gen_rand = CanaryGenerator("run42")
        t2 = gen_rand.mint("vault")
        self.assertTrue(t2.startswith("WEA_CANARY_run42_vault_"))
        self.assertNotEqual(t1, t2)

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            env_path = gen_rand.materialize_env_file(root / "profile", {"TEST_CANARY": t2})
            self.assertTrue(env_path.is_file())

            # Clean SQLite DB and clean log pass.
            db_path = root / "store.db"
            conn = sqlite3.connect(db_path)
            conn.execute("CREATE TABLE messages (id TEXT, content TEXT)")
            conn.execute("INSERT INTO messages VALUES ('m1', 'clean message')")
            conn.commit()
            conn.close()

            log_path = root / "sidecar.log"
            log_path.write_text("startup ok\nshutdown ok\n", encoding="utf-8")

            clean = scan_for_canaries([t2], sqlite_dbs=[db_path], paths=[root])
            self.assertTrue(clean["clean"])
            self.assertEqual(clean["leak_count"], 0)

            # Inject canary into SQLite and JSON receipt -> scanner flags both without echoing raw canary.
            conn = sqlite3.connect(db_path)
            conn.execute("INSERT INTO messages VALUES ('m2', ?)", (f"leaked: {t2}",))
            conn.commit()
            conn.close()
            (root / "receipt.json").write_text(json.dumps({"nested": {"val": t2}}), encoding="utf-8")

            leaked = scan_for_canaries([t2], sqlite_dbs=[db_path], paths=[root])
            self.assertFalse(leaked["clean"])
            self.assertEqual(leaked["leak_count"], 2)
            self.assertNotIn(t2, json.dumps(leaked))


class TestRecordingNetworkFixtures(unittest.TestCase):
    def test_http_fixture_records_allowed_and_refused_never_arrives(self) -> None:
        secret = "WEA_CANARY_net_secret_123456"
        with (
            RecordingHTTPFixture(fixture_id="allowed", redact_tokens=(secret,)) as allowed,
            RecordingHTTPFixture(fixture_id="refused", redact_tokens=(secret,)) as refused,
        ):
            req = urllib.request.Request(
                f"{allowed.base_url}/ping",
                headers={"Authorization": f"Bearer {secret}", "X-Test": "ok"},
            )
            with urllib.request.urlopen(req, timeout=5) as resp:
                self.assertEqual(resp.status, 200)
                body = json.loads(resp.read().decode("utf-8"))
                self.assertTrue(body["ok"])

            allowed_snap = allowed.snapshot()
            refused_snap = refused.snapshot()
            self.assertEqual(allowed_snap["connection_count"], 1)
            self.assertEqual(refused_snap["connection_count"], 0)
            # Authorization header must be screened so raw secret never appears in evidence.
            self.assertNotIn(secret, json.dumps(allowed_snap))
            self.assertEqual(allowed_snap["requests"][0]["headers"]["X-Test"], "ok")

    def test_tcp_fixture_records_connection(self) -> None:
        with RecordingTCPFixture(fixture_id="tcp-ctrl") as tcp:
            with socket.create_connection(("127.0.0.1", tcp.port), timeout=5) as sock:
                sock.sendall(b"hello-wea")
                reply = sock.recv(64)
            self.assertEqual(reply, b"WEA-TCP-OK\n")
            snap = tcp.snapshot()
            self.assertEqual(snap["connection_count"], 1)
            self.assertEqual(snap["connections"][0]["payload"]["preview"], "hello-wea")


class TestIndependentObservers(unittest.TestCase):
    def test_tree_hash_diff_sqlite_ro_and_process_table(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            dir_a = root / "a"
            dir_b = root / "b"
            dir_a.mkdir()
            dir_b.mkdir()
            (dir_a / "f.txt").write_text("one\n", encoding="utf-8")
            (dir_b / "f.txt").write_text("one\ntwo\n", encoding="utf-8")

            ha = hash_tree(dir_a)
            hb = hash_tree(dir_b)
            self.assertEqual(diff_tree_hashes(ha, hb), {"added": [], "removed": [], "modified": ["f.txt"]})
            d = outside_diff_dirs(dir_a, dir_b)
            self.assertFalse(d["identical"])
            self.assertIn("+two", d["unified_diff"])
            self.assertIn("+two", outside_unified_diff("one\n", "one\ntwo\n", "f.txt"))

            db_path = root / "obs.db"
            conn = sqlite3.connect(db_path)
            conn.execute("CREATE TABLE worker_jobs (id TEXT, session_id TEXT, status TEXT)")
            conn.execute("INSERT INTO worker_jobs VALUES ('j1', 's1', 'complete')")
            conn.execute("CREATE TABLE source_versions (id TEXT, session_id TEXT)")
            conn.execute("INSERT INTO source_versions VALUES ('v1', 's1')")
            conn.execute("CREATE TABLE checkpoints (id TEXT, session_id TEXT)")
            conn.execute("INSERT INTO checkpoints VALUES ('c1', 's1')")
            conn.commit()
            conn.close()

            ro = read_sqlite_tables_ro(db_path, ("worker_jobs", "missing_table"), session_id="s1")
            self.assertTrue(ro["available"])
            self.assertEqual(ro["present_tables"], ["worker_jobs"])
            self.assertEqual(len(ro["rows"]["worker_jobs"]), 1)

            w_obs = WorkersObserver(dir_b, db_path)
            w_obs.capture_pre()
            w_post = w_obs.capture_post(session_id="s1")
            self.assertEqual(w_post["track"], "workers")

            e_obs = EditsObserver(dir_b, db_path)
            e_obs.capture_pre(["f.txt"])
            (dir_b / "f.txt").write_text("one\ntwo\nthree\n", encoding="utf-8")
            e_post = e_obs.capture_post(["f.txt"], session_id="s1")
            self.assertIn("+three", e_post["outside_unified_diffs"]["f.txt"])

            a_obs = AuthorityObserver(db_path)
            outside_f = root / "outside.txt"
            outside_f.write_text("unchanged", encoding="utf-8")
            a_obs.capture_pre_fs({"outside": outside_f})
            a_post = a_obs.capture_post(session_id="s1", fs_paths={"outside": outside_f}, launched_pids=[os.getpid()])
            self.assertTrue(a_post["host_fs"]["outside"]["unchanged"])
            self.assertIn(str(os.getpid()), a_post["host_processes"]["alive"])

            ps = read_host_process_table([os.getpid()])
            self.assertIn(str(os.getpid()), ps["alive"])


class TestCheckerAndAllTwelveControls(unittest.TestCase):
    def test_all_twelve_controls_and_missing_instrumentation(self) -> None:
        report = run_qualification_controls()
        self.assertTrue(report["qualified"], json.dumps(report, indent=2))
        self.assertEqual(len(report["defect_controls"]), 7)
        self.assertEqual(len(report["healthy_controls"]), 5)

        expected_defects = {
            "wrong_author",
            "missing_landed_path",
            "landed_path_absent_from_result",
            "stale_read_basis_accepted",
            "grant_matched_across_chats",
            "unrecorded_connection",
            "canary_in_record",
        }
        self.assertEqual(set(report["defect_controls"].keys()), expected_defects)
        for name, res in report["defect_controls"].items():
            self.assertEqual(res["actual_verdict"], "fail", name)
            self.assertIn(name, res["actual_rule_ids"])

        expected_healthy = {
            "healthy_lag",
            "late_card",
            "expected_refusal",
            "retention_gap",
            "clean_rebase",
        }
        self.assertEqual(set(report["healthy_controls"].keys()), expected_healthy)
        for name, res in report["healthy_controls"].items():
            self.assertEqual(res["actual_verdict"], "pass", name)

        self.assertEqual(report["missing_instrumentation_control"]["actual_verdict"], "unknown")

    def test_retention_gap_distinguished_from_missing_evidence(self) -> None:
        # Unmarked null historical body -> unknown
        ev_missing = copy.deepcopy(make_healthy_baseline())
        ev_missing["historical_bodies"] = {"win-1": {"body": None}}
        res_missing = check_evidence(ev_missing)
        self.assertEqual(res_missing["verdict"], "unknown")

        # Explicitly marked retention_gap -> pass
        ev_gap = copy.deepcopy(make_healthy_baseline())
        ev_gap["historical_bodies"] = {"win-1": {"body": None, "retention_gap": True, "status": "retention_gap"}}
        res_gap = check_evidence(ev_gap)
        self.assertEqual(res_gap["verdict"], "pass")
        self.assertEqual(res_gap["retention_gaps_accepted"], ["win-1"])

    def test_refusal_without_positive_control_fails(self) -> None:
        ev = copy.deepcopy(build_healthy_controls()["expected_refusal"])
        ev["expected_refusal"]["adjacent_positive_control_passed"] = False
        res = check_evidence(ev)
        self.assertEqual(res["verdict"], "fail")
        self.assertIn("refusal_missing_positive_control", res["failed_rule_ids"])

    def test_cli_controls_command_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out_file = pathlib.Path(tmp) / "controls-report.json"
            rc = cli.main(["controls", "--out", str(out_file)])
            self.assertEqual(rc, 0)
            data = json.loads(out_file.read_text(encoding="utf-8"))
            self.assertTrue(data["qualified"])


class TestSanitizedExportAndFreshProfileReplay(unittest.TestCase):
    def test_export_strips_secrets_sessions_paths_and_replays_cleanly(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            proj_path = "/Users/example/private/project"
            prof_path = "/Users/example/private/profile"
            session_id = "sess-secret-12345"
            token = "tok_live_9876543210abcdef"

            fixture_files = {"hello.txt": "hello world\n"}
            baseline = {"hello.txt": hash_tree(root) or ""}
            # Compute exact baseline hash
            proj = root / "src"
            proj.mkdir()
            (proj / "hello.txt").write_text("hello world\n", encoding="utf-8")
            baseline = hash_tree(proj)

            bundle_path = export_sanitized_bundle(
                root / "export",
                track="edits",
                case_id="ctrl_edits",
                fixture_files=fixture_files,
                steps=[{"cmd": f"edit {proj_path}/hello.txt", "auth": f"Bearer {token}", "sid": session_id}],
                expected={"verdict": "pass"},
                baseline_hashes=baseline,
                receipt={"profile": prof_path, "session_id": session_id, "token": token},
                host_paths={proj_path: "${PROJECT_ROOT}", prof_path: "${PROFILE_ROOT}"},
                secrets=[token],
                session_ids=[session_id],
            )
            raw_bundle = bundle_path.read_text(encoding="utf-8")
            self.assertNotIn(proj_path, raw_bundle)
            self.assertNotIn(prof_path, raw_bundle)
            self.assertNotIn(session_id, raw_bundle)
            self.assertNotIn(token, raw_bundle)

            verified = load_and_verify_bundle(bundle_path)
            self.assertEqual(verified["case_id"], "ctrl_edits")

            # Replay on fresh profile using a mock runner function.
            def fake_runner(track, out_dir, **kwargs):
                self.assertEqual(track, "edits")
                self.assertTrue((kwargs["project_dir"] / "hello.txt").is_file())
                return {"checker": {"verdict": "pass", "passed": True}}

            replay_res = replay_on_fresh_profile(bundle_path, root / "replay-out", runner_fn=fake_runner)
            self.assertTrue(replay_res["baseline_hashes_verified"])
            self.assertEqual(replay_res["result"]["checker"]["verdict"], "pass")

            # Tampering with bundle breaks digest verification.
            tampered = json.loads(raw_bundle)
            tampered["expected"]["verdict"] = "fail"
            bad_path = root / "tampered.json"
            bad_path.write_text(json.dumps(tampered), encoding="utf-8")
            with self.assertRaises(ValueError):
                load_and_verify_bundle(bad_path)

    def test_sanitize_value_strips_bearer_headers(self) -> None:
        cleaned = sanitize_value({"Authorization": "Bearer supersecrettoken12345"})
        self.assertEqual(cleaned["Authorization"], "Bearer <redacted-token>")


if __name__ == "__main__":
    unittest.main()
