"""The desktop driver requires staging, bundle exchange, confirmation, and restart."""
import hashlib
import json
from pathlib import Path
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

import release_upgrade.case as driver
from verification_tests.distribution.test_bundle_upgrade_prepare_execution import manifest


class CaseTests(unittest.TestCase):
    def exercise(self, fault=None, failing_phase=None, source="1.0.3", helper_status=1):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name).resolve()
        work, config, evidence = root / "work", root / "config", root / "evidence"
        work.mkdir()
        app = work / "Product.app"
        executable = app / "Contents/MacOS/Product"
        executable.parent.mkdir(parents=True)
        executable.write_bytes(b"prior executable")
        prepared = work / ".paintedwolf-update-candidate.app"
        new_executable = prepared / "Contents/MacOS/Product"
        step = manifest("1.1.0")
        candidate = {"version": "1.1.0", "channel": "stable" if fault == "channel" else "preview", "platform": "darwin-aarch64",
                     "signing_generation": 1, "artifact_url": step["platforms"]["darwin-aarch64"]["url"],
                     "artifact_signature": step["platforms"]["darwin-aarch64"]["signature"], "release_id": "release"}
        case = {"id": f"{source}-preview", "source_version": source, "channel": "preview", "protocol": "native-v2", "steps": [step]}
        plan = {"candidate": step, "archives": {source: manifest(source)}, "schema_version": 5, "schemas": {source: 5}}
        # A source through 1.0.2 commits and exits on the launch after staging; the next launch activates.
        activating = 3 if source == "1.0.2" else 2
        records = config / "updates/installations" / hashlib.sha256(str(app).encode()).hexdigest()
        installation = Mock(executable=executable, last_observation={"state": "healthy"})
        installation.app_running.return_value = False
        launches = []
        def launch():
            launches.append(True)
            if len(launches) == 1:
                new_executable.parent.mkdir(parents=True)
                new_executable.write_bytes(b"candidate executable")
                records.mkdir(parents=True)
                (records / "ready.json").write_text(json.dumps({"candidate": candidate, "prepared_bundle": str(prepared)}))
            if len(launches) == 2 and activating == 3:
                (records / "transaction.json").write_text(json.dumps({"id": "transaction", "phase": "committed", "candidate": candidate}))
            if len(launches) == activating:
                if fault == "exchange":
                    executable.write_bytes(b"candidate executable")
                else:
                    saved = work / "saved.app"
                    app.rename(saved)
                    prepared.rename(app)
                    saved.rename(prepared)
                shutil.rmtree(prepared)
                transaction = {"id": "transaction", "phase": "startup_confirmed", "candidate": candidate,
                               "target": str(app), "prepared_bundle": str(prepared),
                               "next_hash": hashlib.sha256(b"candidate executable").hexdigest()}
                if fault == "hash":
                    transaction["next_hash"] = "0" * 64
                (records / "transaction.json").write_text(json.dumps(transaction))
        installation.launch.side_effect = launch
        installation.healthy.side_effect = lambda version: {"health": {"version": version, "status": "ok",
            "schema_version": 6 if fault == "schema" and version == "1.1.0" else 5}, "port": 1234}
        def run(arguments, stdout=None, **_):
            if arguments[1:2] == ["--apply-update"]:
                self.assertEqual(arguments[0], str(executable))
                return Mock(returncode=helper_status)
            if stdout is None:
                return
            # The semantic verifier is the only command given a log.
            phase = Path(stdout.name).stem.removeprefix("semantics-")
            stdout.write(f"{phase} verified against {arguments[arguments.index('--base') + 1]}\n".encode())
            if phase == failing_phase:
                raise subprocess.CalledProcessError(1, arguments)
        with patch.object(driver, "materialize", side_effect=lambda *_: config.mkdir()), \
             patch.object(driver.subprocess, "run", side_effect=run), \
             patch.object(driver, "Installation", return_value=installation):
            if helper_status != 1:
                with self.assertRaisesRegex(ValueError, f"status {helper_status}, not its refusal"):
                    driver.run_case(case, plan, root, work, config, evidence)
            elif failing_phase:
                with self.assertRaises(subprocess.CalledProcessError):
                    driver.run_case(case, plan, root, work, config, evidence)
            elif fault:
                message = {"channel": "unexpected release", "exchange": "did not exchange",
                           "hash": "executable differs", "schema": "unexpected schema"}[fault]
                with self.assertRaisesRegex(ValueError, message):
                    driver.run_case(case, plan, root, work, config, evidence)
                self.assertEqual(len(launches), 1 if fault == "channel" else activating)
            else:
                result = driver.run_case(case, plan, root, work, config, evidence)
                self.assertEqual(result["result"], "passed")
                self.assertEqual(len(launches), activating + 1)
                self.assertEqual([row["activation_trigger"] for row in result["steps"]],
                                 ["next_launch_after_committed_handoff" if source == "1.0.2" else "startup_after_staging"])
                self.assertEqual(result["helper_probe"], {"exit_code": 1})
                installation.stop.assert_any_call(crash_app=True)
                self.assertTrue((evidence / "1.1.0-transaction.json").exists())
                self.assertEqual(json.loads((evidence / "startup-observation.json").read_text()), {"state": "healthy"})
            self.assertGreaterEqual(installation.stop.call_count, 1)
        return {path.name: path.read_text() for path in evidence.glob("semantics-*.log")}

    def test_every_semantic_phase_keeps_its_own_log(self):
        passed = self.exercise()
        self.assertEqual(passed, {f"semantics-{phase}.log": f"{phase} verified against http://127.0.0.1:1234\n"
                                  for phase in ("prior", "activated", "restarted")})
        for phase, retained in (("activated", ("prior", "activated")), ("restarted", ("prior", "activated", "restarted"))):
            failed = self.exercise(failing_phase=phase)
            self.assertEqual(set(failed), {f"semantics-{name}.log" for name in retained})
            for name in retained[:-1]:
                self.assertEqual(failed[f"semantics-{name}.log"], passed[f"semantics-{name}.log"])

    def test_activation_requires_a_complete_native_transition_and_another_restart(self):
        self.exercise()

    def test_a_source_through_1_0_2_activates_on_the_launch_after_its_committed_handoff(self):
        self.exercise(source="1.0.2")
        for fault in ("exchange", "hash", "schema"):
            with self.subTest(fault=fault):
                self.exercise(fault=fault, source="1.0.2")

    def test_the_next_launch_waits_for_the_source_app_to_exit_after_committing(self):
        records = Path(self.enterContext(tempfile.TemporaryDirectory()))
        installation = Mock()
        installation.app_running.return_value = True
        self.assertFalse(driver.committed_handoff(records, "1.1.0", installation))
        (records / "transaction.json").write_text(json.dumps({"phase": "prepared", "candidate": {"version": "1.1.0"}}))
        self.assertFalse(driver.committed_handoff(records, "1.1.0", installation))
        (records / "transaction.json").write_text(json.dumps({"phase": "committed", "candidate": {"version": "1.1.0"}}))
        self.assertFalse(driver.committed_handoff(records, "1.1.0", installation))
        installation.app_running.return_value = False
        self.assertTrue(driver.committed_handoff(records, "1.1.0", installation))
        with self.assertRaisesRegex(ValueError, "another release"):
            driver.committed_handoff(records, "1.2.0", installation)

    def test_the_installed_helper_must_refuse_its_probe_from_inside_the_bundle(self):
        for status in (-9, 0):
            with self.subTest(status=status):
                self.exercise(helper_status=status)

    def test_a_different_staged_channel_cannot_reach_activation(self):
        self.exercise(fault="channel")

    def test_healthy_startup_cannot_substitute_for_the_prepared_bundle_inode(self):
        self.exercise(fault="exchange")

    def test_startup_confirmation_cannot_substitute_for_the_installed_executable_hash(self):
        self.exercise(fault="hash")

    def test_upgraded_health_must_report_the_candidate_schema(self):
        self.exercise(fault="schema")

    def test_diagnostic_write_failure_preserves_the_activation_failure_and_cleanup(self):
        write = Path.write_text
        def fail_observation(path, *args, **kwargs):
            if path.name == "startup-observation.json":
                raise OSError("evidence disk unavailable")
            return write(path, *args, **kwargs)
        with patch.object(Path, "write_text", fail_observation), patch.object(driver, "retain_state") as retain:
            self.exercise(fault="channel")
            retain.assert_called_once()

    def test_cleanup_attempts_every_operation_and_retains_primary_error(self):
        primary = ValueError("activation failed")
        installation = Mock(last_observation={})
        installation.stop.side_effect = OSError("stop failed")
        with patch.object(Path, "write_text", side_effect=OSError("write failed")), \
                patch.object(driver, "retain_state", side_effect=OSError("retain failed")) as retain:
            driver.finish_case(installation, Path("config"), Path("records"), Path("evidence"), primary)
            installation.stop.assert_called_once()
            retain.assert_called_once()
            self.assertEqual(len(primary.__notes__), 3)
            with self.assertRaisesRegex(OSError, "write failed"):
                driver.finish_case(installation, Path("config"), Path("records"), Path("evidence"), None)

    def test_semantics_verification_syncs_the_desktop_engine_token(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config, fixture, evidence = root / "config", root / "fixture", root / "evidence"
            for path in (config, fixture, evidence):
                path.mkdir()
            outputs = [unittest.mock.Mock(stdout="/Applications/Painted Wolf Code.app/Contents/MacOS/pw serve LYCAON_ADDR=127.0.0.1:0 LYCAON_API_TOKEN=desktop-bearer-token\n"),
                       unittest.mock.Mock()]
            with patch.object(driver.subprocess, "run", side_effect=outputs) as calls:
                driver.verify_semantics(config, fixture, {"engine_pid": 42, "port": 1234}, evidence, "prior")
            self.assertEqual((config / "api.token").read_text(), "desktop-bearer-token")
            self.assertEqual((config / "api.token").stat().st_mode & 0o777, 0o600)
            self.assertEqual(calls.call_count, 2)



class PriorAppSemanticsTests(unittest.TestCase):
    """A launched app relays the corpus's pending editor update before the prior-app check reads it."""

    def setUp(self):
        import threading
        from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        corpus = driver.SCRIPTS.parent / "lycaon/testdata/upgrade-corpus/1.0.2"
        self.manifest = corpus / "MANIFEST.json"
        self.config = root / "config"
        subprocess.run([sys.executable, str(driver.SCRIPTS / "upgrade-fixture-files.py"), "materialize", str(corpus), str(self.config)],
                       check=True, timeout=120, env={**os.environ, "PYTHONPATH": str(driver.SCRIPTS)})
        (self.config / "api.token").write_text("fixture-token")
        evidence = json.loads(self.manifest.read_text())["editor_history"]
        self.evidence = evidence
        self.outbox = self.config / Path(evidence["outbox_path"]).parent
        header = json.loads((self.outbox / "header.json").read_text())
        log = (self.outbox / header["log"]).read_bytes()
        self.records = [json.loads(log[frame["offset"]:frame["offset"] + frame["length"]]) for frame in header["frames"]]
        checkpoint = next(record for record in self.records if record["kind"] == "checkpoint")
        # The relayed host document carries the window's local text over the accepted base.
        document = json.dumps({"crdt_update": checkpoint["state"], "base_content": evidence["accepted_text"], "dirty": True}).encode()
        expected = f"/v1/projects/{json.loads(self.manifest.read_text())['project_id']}/editor-documents/{evidence['document_id']}/snapshots"

        class Host(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers["Content-Length"]))
                authorized = self.path == expected and self.headers["Authorization"] == "Bearer fixture-token"
                self.send_response(200 if authorized else 404)
                self.send_header("Content-Length", str(len(document) if authorized else 0))
                self.end_headers()
                if authorized:
                    self.wfile.write(document)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Host)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)
        self.base = f"http://127.0.0.1:{server.server_address[1]}"

    def relay(self, acknowledged=True, compacted=False):
        """Rewrite the outbox as the app leaves it after relaying the pending update."""
        header = json.loads((self.outbox / "header.json").read_text())
        frames, log = [], b""
        for frame, record in zip(header["frames"], self.records):
            if record["kind"] == "update":
                if compacted:
                    continue
                record = {**record, "acknowledged": acknowledged}
            raw = json.dumps(record).encode()
            frame = {**frame, "offset": len(log), "length": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}
            if frame["kind"] == "checkpoint":
                frame.update(synchronized=True, pending=[])
            frames.append(frame)
            log += raw
        (self.outbox / header["log"]).write_bytes(log)
        (self.outbox / "header.json").write_text(json.dumps({**header, "synchronized": True, "frames": frames, "logBytes": len(log)}))

    def verify(self):
        return subprocess.run(["node", str(driver.SCRIPTS / "upgrade-editor-fixture.mjs"), "verify", self.base,
                               str(self.config), str(self.manifest), str(self.config / "project")],
                              capture_output=True, text=True, timeout=120)

    def test_an_acknowledged_relay_passes_the_prior_app_check(self):
        self.relay()
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_a_compacted_relay_passes_the_prior_app_check(self):
        self.relay(compacted=True)
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_a_relay_without_acknowledgement_fails_the_prior_app_check(self):
        self.relay(acknowledged=False)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("acknowledgement missing", result.stderr)
