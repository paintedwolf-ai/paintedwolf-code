"""Go stage events: early outcomes from the exec wrapper and the durations they record."""

import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import resources
from verification_execute import StageEvents, read_events
from verification_history import package_durations, record_package_durations

WRAPPER = Path(__file__).resolve().parents[2] / "go-test-exec.py"


class EventTests(unittest.TestCase):
    def test_stage_events_report_failures_once_and_ignore_interruptions(self):
        with tempfile.TemporaryDirectory() as directory:
            events, raw = Path(directory) / "events.jsonl", Path(directory) / "raw.json"
            watcher = StageEvents(events, raw)
            self.assertEqual(watcher.failures(), {})
            events.write_text(json.dumps({"package": "a", "exit_code": 1, "tests": ["TestA"]}) + "\n"
                              + json.dumps({"package": "b", "exit_code": -9}) + "\n"
                              + json.dumps({"package": "c", "exit_code": 0}) + "\n" + '{"package": "d", "exit')
            raw.write_text(json.dumps({"Package": "e", "Action": "fail"}) + "\n"
                           + json.dumps({"Package": "f", "Test": "TestF", "Action": "fail"}) + "\n"
                           + json.dumps({"ImportPath": "g [g.test]", "Action": "build-fail"}) + "\n")
            self.assertEqual(watcher.failures(), {"a": {"tests": ["TestA"]}, "e": {}, "g": {"build_failed": True}})
            with events.open("a") as stream:
                stream.write('_code": 2}\n')
            self.assertEqual(watcher.failures(), {"d": {}})
            self.assertEqual(watcher.failures(), {})

    def test_terminal_event_in_a_later_read_keeps_the_failure_details(self):
        with tempfile.TemporaryDirectory() as directory:
            events, raw = Path(directory) / "events.jsonl", Path(directory) / "raw.json"
            watcher = StageEvents(events, raw)
            events.write_text(json.dumps({"package": "a", "exit_code": 1, "tests": ["TestA"]}) + "\n")
            self.assertEqual(watcher.failures(), {"a": {"tests": ["TestA"]}})
            raw.write_text(json.dumps({"Package": "a", "Action": "fail"}) + "\n")
            self.assertEqual(watcher.failures(), {})

    def test_package_durations_keep_recent_measurements_per_stage(self):
        with tempfile.TemporaryDirectory() as directory:
            queue = type("Queue", (), {"history": Path(directory), "locked": lambda self: open(os.devnull)})()
            for seconds in (10, 30, 20):
                record_package_durations(queue, "stage", {
                    "a": {"exit_code": 0, "elapsed": seconds}, "b": {"exit_code": 0, "elapsed": 1},
                    # A failure or a backstop kill stopped part-way, so neither measured the package.
                    "d": {"exit_code": 1, "elapsed": 5}, "e": {"exit_code": -3, "elapsed": 1800},
                    "f": {"elapsed": 7}})
            self.assertEqual(package_durations(queue, "stage"), {"a": 20, "b": 1})
            self.assertEqual(package_durations(queue, "other"), {})


@unittest.skipIf(os.name == "nt", "the wrapper runs POSIX test binaries")
class WrapperTests(unittest.TestCase):
    """A shell script stands in for a Go test binary."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.module = self.root / "source" / "lycaon"
        self.package = self.module / "pkg"
        self.package.mkdir(parents=True)
        self.scratch = self.root / "scratch"
        self.scratch.mkdir()
        (self.module / "go.mod").write_text("module fixture\n")
        (self.package / "input.txt").write_text("one\n")
        self.events = self.root / "stage-001-events.jsonl"
        self.binary = self.root / "pkg.test"
        self.binary.write_bytes(b'''#!/bin/sh
read -r value < input.txt
printf '=== RUN   TestFixture\\n'
# close_fds=False keeps inherited descriptors, as Go's os/exec does.
if [ "$FIXTURE_MODE" = orphan ]; then
  python3 -c 'import subprocess; subprocess.Popen(["/bin/sleep", "300"], close_fds=False, start_new_session=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)'
fi
if [ "$FIXTURE_MODE" = holder ]; then /bin/sleep 300 & fi
if [ "$FIXTURE_MODE" = daemon ]; then
  python3 -c 'import os; os.fork() or (os.setsid(), os.closerange(0, 256), os.execv("/bin/sleep", ["sleep", "300"]))'
fi
if [ "$FIXTURE_MODE" = fail ]; then printf -- '--- FAIL: TestFixture (0.00s)\\nFAIL\\n'; exit 1; fi
printf -- '--- PASS: TestFixture (0.00s)\\nPASS %s\\n' "$value"
printf 'GOMEMLIMIT %s RACE %s\\n' "$GOMEMLIMIT" "${PW_TEST_RACE:-unset}"
''')
        self.binary.chmod(0o755)

    def run_wrapper(self, mode="pass", **extra):
        env = {"PATH": os.environ["PATH"], "FIXTURE_MODE": mode, "PW_TEST_STAGE_EVENTS": str(self.events), **extra,
               "PW_TEST_SCRATCH_ROOT": str(self.scratch), "PW_TEST_MODULE_ROOT": str(self.module),
               "PW_TEST_BATCH_DIRECTORY": str(self.root / f"batch-{mode}"), "PW_TEST_STAGE_ID": "stage-001"}
        result = subprocess.run([sys.executable, str(WRAPPER), str(self.binary), "-test.paniconexit0",
                                 "-test.timeout=10m0s", "-test.v=test2json"], cwd=self.package, env=env,
                                capture_output=True, timeout=30)
        return result, read_events(self.events)["fixture/pkg"]

    def test_outside_a_stage_the_binary_runs_unchanged(self):
        result = subprocess.run([sys.executable, str(WRAPPER), str(self.binary)], cwd=self.package,
                                env={"PATH": os.environ["PATH"]}, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b"PASS one", result.stdout)
        self.assertFalse(self.events.exists())

    def test_a_race_build_gets_a_scaled_memory_ceiling_the_binary_never_sees(self):
        ceiling = resources.budget("fixture/pkg")
        result, _ = self.run_wrapper()
        self.assertIn(f"GOMEMLIMIT {ceiling * 80 // 100} RACE unset".encode(), result.stdout)
        result, _ = self.run_wrapper(PW_TEST_RACE="1")
        scaled = int(ceiling * json.loads(resources.POLICY.read_text())["race_scale"])
        self.assertGreater(scaled, ceiling)
        self.assertEqual(resources.budget("fixture/pkg", race=True), scaled)
        self.assertIn(f"GOMEMLIMIT {scaled * 80 // 100} RACE unset".encode(), result.stdout)

    def test_failures_are_reported_as_they_happen_with_their_output(self):
        result, event = self.run_wrapper("fail")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(event["exit_code"], 1)
        self.assertEqual(event["tests"], ["TestFixture"])
        self.assertIn("--- FAIL: TestFixture", Path(event["output"]).read_text())

    @unittest.skipUnless(sys.platform in {"darwin", "linux"}, "process custody requires a supported POSIX host")
    def test_a_detached_child_fails_the_package(self):
        result, event = self.run_wrapper("orphan")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(b"package left child processes: fixture/pkg: sleep (pid ", result.stdout)
        self.assertEqual(event["exit_code"], 1)
        self.assertGreaterEqual(event["child_scan_seconds"], 0)

    @unittest.skipUnless(sys.platform == "linux", "macOS hides the environment of Apple platform binaries")
    def test_a_daemon_that_closed_every_descriptor_fails_the_package(self):
        result, event = self.run_wrapper("daemon")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(b"package left child processes: fixture/pkg: sleep (pid ", result.stdout)
        self.assertEqual(event["exit_code"], 1)

    @unittest.skipUnless(sys.platform in {"darwin", "linux"}, "process custody requires a supported POSIX host")
    def test_a_child_holding_the_output_open_fails_the_package_without_delaying_it(self):
        result, event = self.run_wrapper("holder")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(b"PASS one", result.stdout)
        self.assertIn(b"package left child processes: fixture/pkg: sleep (pid ", result.stdout)
        self.assertEqual(event["exit_code"], 1)

    def test_a_process_gone_before_it_is_named_is_not_a_leak(self):
        spec = importlib.util.spec_from_file_location("go_test_exec", WRAPPER)
        wrapper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wrapper)
        births = {11: 100, 12: 200}
        with tempfile.NamedTemporaryFile() as owner, \
                patch.object(wrapper.custody, "lease_holders", return_value={11}), \
                patch.object(wrapper.custody, "carriers", return_value={12}), \
                patch.object(wrapper.custody, "birth", side_effect=births.get), \
                patch.object(wrapper.custody, "commands", return_value={12: "sleep"}), \
                patch.object(wrapper.custody, "carries", return_value=True), \
                patch.object(wrapper.os, "kill") as kill:
            leaked = wrapper.settle_package_children(owner, "PW_TEST_PACKAGE_OWNER=1-x")
        self.assertEqual(leaked, {12: "sleep"})
        kill.assert_called_once_with(12, signal.SIGKILL)

    def test_every_run_executes_the_binary(self):
        (self.package / "input.txt").write_text("two\n")
        for _ in range(2):
            result, event = self.run_wrapper()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(b"PASS two", result.stdout)
            self.assertEqual(event["exit_code"], 0)


if __name__ == "__main__":
    unittest.main()
