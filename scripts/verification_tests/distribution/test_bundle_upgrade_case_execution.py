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
    def exercise(self, wrong_channel=False):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        work, config, evidence = root / "work", root / "config", root / "evidence"
        work.mkdir()
        app = work / "Product.app"
        executable = app / "Contents/MacOS/Product"
        executable.parent.mkdir(parents=True)
        executable.write_bytes(b"prior executable")
        prepared = work / ".paintedwolf-update-candidate.app"
        new_executable = prepared / "Contents/MacOS/Product"
        new_executable.parent.mkdir(parents=True)
        new_executable.write_bytes(b"candidate executable")
        step = manifest("1.1.0")
        candidate = {"version": "1.1.0", "channel": "stable" if wrong_channel else "preview", "platform": "darwin-aarch64",
                     "signing_generation": 1, "artifact_url": step["platforms"]["darwin-aarch64"]["url"],
                     "artifact_signature": step["platforms"]["darwin-aarch64"]["signature"], "release_id": "release"}
        case = {"id": "1.0.2-preview", "source_version": "1.0.2", "channel": "preview", "protocol": "native-v2", "steps": [step]}
        plan = {"candidate": step, "archives": {"1.0.2": manifest("1.0.2")}, "schema_version": 5}
        records = config / "updates/installations" / hashlib.sha256(str(app).encode()).hexdigest()
        installation = Mock(executable=executable)
        launches = []
        def launch():
            launches.append(True)
            if len(launches) == 1:
                records.mkdir(parents=True)
                (records / "ready.json").write_text(json.dumps({"candidate": candidate, "prepared_bundle": str(prepared)}))
            if len(launches) == 2:
                saved = work / "saved.app"
                app.rename(saved)
                prepared.rename(app)
                saved.rename(prepared)
                shutil.rmtree(prepared)
                transaction = {"id": "transaction", "phase": "startup_confirmed", "candidate": candidate,
                               "target": str(app), "prepared_bundle": str(prepared),
                               "next_hash": hashlib.sha256(b"candidate executable").hexdigest()}
                (records / "transaction.json").write_text(json.dumps(transaction))
        installation.launch.side_effect = launch
        installation.healthy.side_effect = lambda version: {"health": {"version": version, "status": "ok", "schema_version": 5}, "port": 1234}
        with patch.object(driver, "materialize", side_effect=lambda *_: config.mkdir()), \
             patch.object(driver.subprocess, "run"), \
             patch.object(driver, "Installation", return_value=installation), \
             patch.object(driver, "verify_semantics") as semantics:
            if wrong_channel:
                with self.assertRaisesRegex(ValueError, "unexpected release"):
                    driver.run_case(case, plan, root, work, config, evidence)
                self.assertEqual(len(launches), 1)
            else:
                result = driver.run_case(case, plan, root, work, config, evidence)
                self.assertEqual(result["result"], "passed")
                self.assertEqual(len(launches), 3)
                self.assertEqual(semantics.call_count, 3)
                self.assertTrue((evidence / "1.1.0-transaction.json").exists())
            self.assertGreaterEqual(installation.stop.call_count, 1)

    def test_activation_requires_a_complete_native_transition_and_another_restart(self):
        self.exercise()

    def test_a_different_staged_channel_cannot_reach_activation(self):
        self.exercise(wrong_channel=True)
