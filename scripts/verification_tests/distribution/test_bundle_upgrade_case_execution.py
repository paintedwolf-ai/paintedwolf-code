"""The desktop driver requires staging, bundle exchange, confirmation, and restart."""
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import Mock, patch

import release_upgrade.case as driver
from verification_tests.distribution.test_bundle_upgrade_prepare_execution import manifest


class CaseTests(unittest.TestCase):
    def exercise(self, fault=None):
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
        case = {"id": "1.0.2-preview", "source_version": "1.0.2", "channel": "preview", "protocol": "native-v2", "steps": [step]}
        plan = {"candidate": step, "archives": {"1.0.2": manifest("1.0.2")}, "schema_version": 5, "schemas": {"1.0.2": 5}}
        records = config / "updates/installations" / hashlib.sha256(str(app).encode()).hexdigest()
        installation = Mock(executable=executable)
        launches = []
        def launch():
            launches.append(True)
            if len(launches) == 1:
                new_executable.parent.mkdir(parents=True)
                new_executable.write_bytes(b"candidate executable")
                records.mkdir(parents=True)
                (records / "ready.json").write_text(json.dumps({"candidate": candidate, "prepared_bundle": str(prepared)}))
            if len(launches) == 2:
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
        with patch.object(driver, "materialize", side_effect=lambda *_: config.mkdir()), \
             patch.object(driver.subprocess, "run"), \
             patch.object(driver, "Installation", return_value=installation), \
             patch.object(driver, "verify_semantics") as semantics:
            if fault:
                message = {"channel": "unexpected release", "exchange": "did not exchange",
                           "hash": "executable differs", "schema": "unexpected schema"}[fault]
                with self.assertRaisesRegex(ValueError, message):
                    driver.run_case(case, plan, root, work, config, evidence)
                self.assertEqual(len(launches), 1 if fault == "channel" else 2)
            else:
                result = driver.run_case(case, plan, root, work, config, evidence)
                self.assertEqual(result["result"], "passed")
                self.assertEqual(len(launches), 3)
                self.assertEqual(result["activation_trigger"], "startup_after_staging")
                installation.stop.assert_any_call(crash_app=True)
                self.assertEqual(semantics.call_count, 3)
                self.assertTrue((evidence / "1.1.0-transaction.json").exists())
            self.assertGreaterEqual(installation.stop.call_count, 1)

    def test_activation_requires_a_complete_native_transition_and_another_restart(self):
        self.exercise()

    def test_a_different_staged_channel_cannot_reach_activation(self):
        self.exercise(fault="channel")

    def test_healthy_startup_cannot_substitute_for_the_prepared_bundle_inode(self):
        self.exercise(fault="exchange")

    def test_startup_confirmation_cannot_substitute_for_the_installed_executable_hash(self):
        self.exercise(fault="hash")

    def test_upgraded_health_must_report_the_candidate_schema(self):
        self.exercise(fault="schema")
