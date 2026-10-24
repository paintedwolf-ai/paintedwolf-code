"""Process cleanup preserves foreign PIDs and retires supervision before engines."""
from pathlib import Path
import signal
import unittest
from unittest.mock import patch

import release_upgrade.process as process


class ProcessTests(unittest.TestCase):
    def installation(self):
        installation = process.Installation.__new__(process.Installation)
        installation.app = Path("/test/Product.app")
        installation.config = Path("/test/config")
        installation.executable = installation.app / "Contents/MacOS/Product"
        installation.last_observation = {"state": "not_observed"}
        return installation

    def test_cleanup_orders_owned_processes_and_does_not_signal_a_reused_pid(self):
        installation = self.installation()
        installation.owned = {10: ("app", 1), 11: ("engine", 2), 12: ("helper", 0), 13: ("old identity", 1)}
        identities = {10: "app", 11: "engine", 12: "helper", 13: "foreign process"}
        signals = []
        def kill(pid, sig):
            signals.append((pid, sig))
            identities.pop(pid)
        with patch.object(installation, "capture_owned"), \
             patch.object(process, "process_identity", side_effect=lambda pid: identities.get(pid)), \
             patch.object(process.os, "kill", side_effect=kill):
            installation.stop()
        self.assertEqual(signals, [(12, signal.SIGTERM), (10, signal.SIGTERM), (11, signal.SIGTERM)])
        self.assertEqual(identities, {13: "foreign process"})

    def test_wait_timeout_is_bounded_and_reports_the_missing_transition(self):
        with patch.object(process.time, "monotonic", side_effect=[0, 1]), \
             patch.object(process.time, "sleep") as sleep:
            with self.assertRaisesRegex(TimeoutError, "startup"):
                process.wait_for("startup", lambda: None, seconds=0.5)
            sleep.assert_not_called()

    def test_launch_supplies_the_model_mock_without_replacing_the_account_home(self):
        installation = self.installation()
        with patch.object(process.subprocess, "run") as run:
            installation.launch()
        self.assertEqual(run.call_args.args[0], ["/usr/bin/open", "-n", "-j", "--env", "LYCAON_LLM_MOCK=1",
            "--stdout", str(installation.config / "desktop-launch.log"),
            "--stderr", str(installation.config / "desktop-launch.log"), str(installation.app)])

    def test_process_ownership_uses_exact_executable_paths(self):
        installation = self.installation()
        installation.owned = {}
        table = f"10 1 {installation.executable}\n11 1 /other/Product --argument {installation.executable}\n12 10 {installation.app / 'Contents/MacOS/pw'}\n13 12 /bin/resource\n14 99 /bin/unrelated\nmalformed\n15 0\n"
        with patch.object(process.subprocess, "run", return_value=unittest.mock.Mock(stdout=table)), \
             patch.object(process, "process_identity", side_effect=lambda pid: f"identity:{pid}"):
            installation.capture_owned()
        self.assertEqual(installation.owned, {10: ("identity:10", 1), 12: ("identity:12", 2), 13: ("identity:13", 3)})

    def test_health_requires_owned_app_parentage_and_the_expected_version(self):
        import io
        import json
        installation = self.installation()
        installation.owned = {10: ("engine", 2), 11: ("app", 1)}
        for version, status, accepted in (("1.1.0", "ok", True), ("1.0.2", "ok", False), ("1.1.0", "recovery", False)):
            client = unittest.mock.Mock()
            client.open.return_value = io.BytesIO(json.dumps({"version": version, "status": status}).encode())
            with self.subTest(version=version, status=status), \
                 patch.object(installation, "capture_owned"), \
                 patch.object(process, "read_json", return_value={"pid": 10, "port": 1234}), \
                 patch.object(process, "process_identity", side_effect=lambda pid: {10: "engine", 11: "app"}.get(pid)), \
                 patch.object(process.subprocess, "run", return_value=unittest.mock.Mock(stdout="11", returncode=0)), \
                 patch.object(process.urllib.request, "build_opener", return_value=client):
                observed = installation.observe("1.1.0")
            self.assertEqual(observed is not None, accepted)

    def test_a_retiring_engine_is_retried_when_it_exits_during_parent_inspection(self):
        installation = self.installation()
        installation.owned = {10: ("engine", 2)}
        with patch.object(installation, "capture_owned"), \
             patch.object(process, "read_json", return_value={"pid": 10, "port": 1234}), \
             patch.object(process, "process_identity", return_value="engine"), \
             patch.object(process.subprocess, "run", return_value=unittest.mock.Mock(stdout="", returncode=1)):
            self.assertIsNone(installation.observe("1.1.0"))

    def test_a_process_that_exits_during_escalation_does_not_fail_cleanup(self):
        installation = self.installation()
        installation.owned = {10: ("app", 1)}
        with patch.object(installation, "capture_owned"), \
             patch.object(process, "process_identity", return_value="app"), \
             patch.object(process.time, "monotonic", side_effect=[0, 11]), \
             patch.object(process.os, "kill", side_effect=[None, ProcessLookupError]):
            installation.stop()
        self.assertEqual(installation.owned, {})

    def test_startup_recovery_ends_the_staged_app_without_running_its_quit_handler(self):
        installation = self.installation()
        installation.owned = {10: ("app", 1), 11: ("engine", 2)}
        identities = {10: "app", 11: "engine"}
        signals = []
        def kill(pid, sig):
            signals.append((pid, sig))
            identities.pop(pid)
        with patch.object(installation, "capture_owned"), \
             patch.object(process, "process_identity", side_effect=lambda pid: identities.get(pid)), \
             patch.object(process.os, "kill", side_effect=kill):
            installation.stop(crash_app=True)
        self.assertEqual(signals, [(10, signal.SIGKILL), (11, signal.SIGTERM)])

    def test_health_timeout_explains_the_last_structured_rejection(self):
        installation = self.installation()
        installation.last_observation = {"state": "engine_not_owned", "engine_pid": 10}
        with patch.object(process, "wait_for", side_effect=TimeoutError("startup")):
            with self.assertRaisesRegex(TimeoutError, "engine_not_owned"):
                installation.healthy("1.1.0")

    def test_missing_daemon_retains_process_evidence_without_accepting_health(self):
        installation = self.installation()
        installation.owned = {10: ("app", 1)}
        with patch.object(installation, "capture_owned"), patch.object(process, "read_json", return_value=None):
            self.assertIsNone(installation.observe("1.1.0"))
        self.assertEqual(installation.last_observation["state"], "daemon_missing")
        self.assertEqual(installation.last_observation["owned"][10], {"identity": "app", "kind": 1})

    def test_startup_sampling_does_not_inspect_a_reused_pid_or_an_engine(self):
        import tempfile
        installation = self.installation()
        installation.owned = {10: ("app", 1), 11: ("old app", 1), 12: ("engine", 2)}
        with tempfile.TemporaryDirectory() as directory, \
             patch.object(installation, "capture_owned"), \
             patch.object(process, "process_identity", side_effect=lambda pid: {10: "app", 11: "foreign"}.get(pid)), \
             patch.object(process.subprocess, "run", return_value=unittest.mock.Mock(returncode=0)) as run:
            installation.sample_startup(Path(directory))
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0][:4], ["/usr/bin/sample", "10", "1", "1"])
            self.assertTrue((Path(directory) / "startup-process-10-status.json").exists())


    def test_native_crashes_require_this_installation_and_current_launch(self):
        import tempfile
        import json
        installation = self.installation()
        installation.launched_at = 0
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            reports = root / "Library/Logs/DiagnosticReports"
            reports.mkdir(parents=True)
            evidence = root / "evidence"
            evidence.mkdir()
            (reports / "owned.ips").write_text('{}\n' + json.dumps({"procPath": str(installation.executable), "pid": 10}))
            (reports / "foreign.ips").write_text('{}\n' + json.dumps({"procPath": "/foreign/app", "pid": 11}))
            with patch.object(Path, "home", return_value=root):
                installation.retain_crashes(evidence)
            self.assertEqual(len(list(evidence.glob("native-crash-*.ips"))), 1)
            self.assertEqual(json.loads((evidence / "native-crash-0.ips").read_text().split("\n", 1)[1])["pid"], 10)
