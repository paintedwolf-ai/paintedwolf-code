"""The run's one time limit reports where a hung test waits before it ends the run."""

import importlib.util
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[2]
TESTUTIL = SCRIPTS.parent / "lycaon" / "internal" / "testutil"

HUNG = '''package hung

import (
	"os"
	"testing"

	"fixture/testutil"
)

func TestFinishesFirst(t *testing.T) {}

func TestHung(t *testing.T) {
	go func() {
		// Listed once the wait below is registered, so the watchdog starts against a real hang.
		testutil.WaitFor(t, "the hang is registered", func() bool { return testutil.Pending("the fixture channel closes") })
		if err := os.WriteFile(os.Getenv("FIXTURE_HUNG"), nil, 0o600); err != nil {
			panic(err)
		}
	}()
	testutil.AwaitClosed(t, "the fixture channel closes", make(chan struct{}))
}

func TestNeverStarts(t *testing.T) {}
'''

PENDING = '''package testutil

import "strings"

// Pending reports whether a wait on step is registered.
func Pending(step string) bool {
	var report strings.Builder
	writePendingWaits(&report)
	return strings.Contains(report.String(), ": "+step+"\\n")
}
'''

GOOD = '''package good

import "testing"

func TestGood(t *testing.T) {}
'''


@unittest.skipIf(os.name == "nt", "the wait report is a POSIX signal")
class HungTestBackstopTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="backstop-fixture-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        version = next(line for line in (SCRIPTS.parent / "lycaon" / "go.mod").read_text().splitlines()
                       if line.startswith("go "))
        (self.root / "go.mod").write_text("module fixture\n\n" + version + "\n")
        helpers = self.root / "testutil"
        helpers.mkdir()
        # The product's own wait helpers, which depend on nothing outside the standard library.
        for name in ("wait.go", "wait_report_unix.go"):
            shutil.copy2(TESTUTIL / name, helpers / name)
        (helpers / "pending.go").write_text(PENDING)
        for name, body in (("hung", HUNG), ("good", GOOD)):
            (self.root / name).mkdir()
            (self.root / name / "fixture_test.go").write_text(body)

    def test_hung_test_is_named_with_its_step_and_stacks_and_fails_nothing_else(self):
        hung = self.root / "hung-marker"
        raw = self.root / "go-test.json"
        with raw.open("wb") as output:
            run = subprocess.Popen(["go", "test", "-json", "-timeout", "0", "-count=1", "-p", "2", "./..."],
                                   cwd=self.root, stdout=output, stderr=subprocess.STDOUT, start_new_session=True,
                                   env={**os.environ, "FIXTURE_HUNG": str(hung)})
        self.addCleanup(self.reap, run)
        flag = self.root / "timed-out"
        # The limit is counted from the hang, so no host speed decides the outcome.
        while not hung.exists():
            self.assertIsNone(run.poll(), raw.read_text())
            time.sleep(.02)
        watchdog = subprocess.run([sys.executable, str(SCRIPTS / "process-group-watchdog.py"),
                                   "0.1", str(run.pid), str(flag), "fixture"], capture_output=True, text=True)
        self.assertEqual(watchdog.returncode, 0, watchdog.stderr)
        self.assertTrue(flag.exists())
        self.assertNotEqual(run.wait(), 0)
        log = raw.read_text()
        self.assertIn("pending wait: TestHung: the fixture channel closes", log)
        self.assertIn("SIGQUIT: quit", log)
        self.assertRegex(log, r"goroutine \d+ [^\n]*\[chan receive|goroutine \d+ [^\n]*\[select")
        self.assertIn("testutil.AwaitClosed", log)
        digest = subprocess.run([sys.executable, str(SCRIPTS / "test-digest.py"), "--name", "fixture",
                                 "--process-rc", "124"], input=log, capture_output=True, text=True)
        self.assertEqual(digest.returncode, 1, digest.stdout + digest.stderr)
        self.assertIn("pending wait: TestHung: the fixture channel closes", digest.stdout)
        # The hang fails its package; no test that ran or never started is reported failed.
        self.assertIn("2 passed, 0 failed", digest.stdout)
        self.assertIn("0 failing tests across 1 package", digest.stdout)

    def reap(self, run):
        try:
            os.killpg(run.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        run.wait()


class FailureReportTests(unittest.TestCase):
    def test_preparation_does_not_restart_the_hosted_backstop(self):
        spec = importlib.util.spec_from_file_location("watchdog", SCRIPTS / "process-group-watchdog.py")
        watchdog = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(watchdog)
        with tempfile.TemporaryDirectory() as directory:
            flag = Path(directory) / "timeout"
            # Admission finished at 110 after the lane's deadline of 100.
            with patch.object(sys, "argv", ["watchdog", "60", "123", str(flag), "fixture"]), \
                    patch.dict(os.environ, {"PW_TEST_WATCHDOG_DEADLINE": "100"}), \
                    patch.object(watchdog.time, "monotonic", side_effect=[110, 110, 171]), \
                    patch.object(watchdog.time, "sleep") as sleep, \
                    patch.object(watchdog.os, "killpg"), \
                    patch.object(watchdog, "report_waits") as report:
                self.assertEqual(watchdog.main(), 0)
                report.assert_called_once_with(123)
                # Only teardown sleeps; there is no new 60-second execution window.
                sleep.assert_called_once_with(5)
                self.assertTrue(flag.exists())

    def test_a_test_found_waiting_is_named_with_the_tests_that_failed(self):
        spec = importlib.util.spec_from_file_location("go_test_exec", SCRIPTS / "go-test-exec.py")
        wrapper = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wrapper)
        output = (b"--- FAIL: TestFailed (0.00s)\n"
                  b"pending wait: TestHung/sub: the job ends\n"
                  b"pending wait: TestHung/sub: the job ends\n"
                  b"pending waits: none\n")
        with tempfile.TemporaryDirectory() as directory:
            report = wrapper.failure_report(str(Path(directory) / "stage-events.jsonl"), "fixture/p", output)
        self.assertEqual(report["tests"], ["TestFailed", "TestHung/sub"])


if __name__ == "__main__":
    unittest.main()
