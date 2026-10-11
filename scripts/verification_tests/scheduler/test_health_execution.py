"""Process activity and ownership diagnostics."""

import copy
import contextlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, patch

import verification_health as health


def process(pid, parent=1, cpu="0:00.00", identity="Sun Sep 13 10:00:00 2026", state="S"):
    return {"pid": pid, "parent": parent, "identity": identity, "state": state, "cpu": cpu}


class HealthTests(unittest.TestCase):
    def setUp(self):
        self.processes = {10: process(10), 20: process(20, 10), 30: process(30, 20)}
        self.entry = {"ticket": "fixture", "pid": 10, "owner_identity": self.processes[10]["identity"],
                      "child_pid": 20, "child_identity": self.processes[20]["identity"],
                      "started_at": -1000000, "state": "running", "name": "fixture"}

    def sample(self, previous=None, now=0):
        return health.assess(self.entry, self.processes, previous or {}, now)

    def quiet_window(self):
        previous = {}
        for now in range(0, health.QUIET_SECONDS + 1, health.SAMPLE_SECONDS):
            result, previous = self.sample(previous, now)
        return result, previous

    def test_long_runtime_and_first_quiet_sample_do_not_mean_stalled(self):
        result, _ = self.sample(now=1000000)
        self.assertEqual(result["state"], "observing")
        self.assertEqual(result["quiet_seconds"], 0)

    def test_sustained_quiet_is_advisory_and_parent_housekeeping_is_not_progress(self):
        previous = {}
        for now in range(0, health.QUIET_SECONDS + 1, health.SAMPLE_SECONDS):
            self.processes[10]["cpu"] = str(now)
            result, previous = self.sample(previous, now)
        self.assertEqual(result["state"], "suspected_stall")
        self.assertEqual(result["action"], "none")
        self.assertIn("legitimate wait", result["reason"])

    def test_cpu_busy_descendant_can_run_indefinitely_without_output(self):
        previous = {}
        for now in range(0, health.QUIET_SECONDS * 4, health.SAMPLE_SECONDS):
            self.processes[30]["cpu"] = str(now)
            result, previous = self.sample(previous, now)
            self.assertNotEqual(result["state"], "suspected_stall")
        self.assertEqual(result["state"], "active")

    def test_resumed_descendant_activity_clears_suspicion(self):
        _, previous = self.quiet_window()
        self.processes[30]["cpu"] = "0:01.00"
        result, _ = self.sample(previous, health.QUIET_SECONDS + health.SAMPLE_SECONDS)
        self.assertEqual(result["state"], "active")
        self.assertEqual(result["quiet_seconds"], 0)

    def test_process_turnover_counts_even_when_cpu_totals_are_unchanged(self):
        _, previous = self.sample()
        self.processes[30]["identity"] = "new child creation"
        result, _ = self.sample(previous, health.SAMPLE_SECONDS)
        self.assertEqual(result["state"], "active")

    def test_raw_events_count_without_digest_console_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            raw = Path(temporary) / "events.json"
            log = Path(temporary) / "stage.log"
            log.touch()
            self.entry["activity_paths"] = [str(log), str(raw)]
            _, previous = self.quiet_window()
            raw.write_text('{"Action":"pass","Test":"TestSlow"}\n')
            result, _ = self.sample(previous, health.QUIET_SECONDS + health.SAMPLE_SECONDS)
            self.assertEqual(result["state"], "active")

    def test_missing_samples_and_clock_reset_break_continuity(self):
        _, previous = self.quiet_window()
        for now in (health.QUIET_SECONDS + health.MAX_SAMPLE_GAP + 1, -1):
            with self.subTest(now=now):
                result, _ = self.sample(previous, now)
                self.assertEqual(result["state"], "observing")
                self.assertEqual(result["quiet_seconds"], 0)

    def test_missing_zombie_and_reused_owner_are_lost_supervision(self):
        for replacement in (None, process(10, state="Z"), process(10, identity="another process")):
            with self.subTest(replacement=replacement):
                processes = copy.deepcopy(self.processes)
                processes.pop(10)
                if replacement:
                    processes[10] = replacement
                result, _ = health.assess(self.entry, processes, {}, 0)
                self.assertEqual(result["state"], "supervision_lost")
                self.assertEqual(result["action"], "none")

    def test_missing_identity_and_replaced_command_do_not_claim_activity(self):
        self.entry["owner_identity"] = None
        result, _ = self.sample()
        self.assertEqual(result["state"], "unknown")
        self.entry["owner_identity"] = self.processes[10]["identity"]
        self.processes[20]["identity"] = "reused child pid"
        result, _ = self.sample()
        self.assertEqual(result["state"], "unknown")

    def test_stopped_supervisor_and_waiting_batch_do_not_claim_a_stall(self):
        self.processes[10]["state"] = "T"
        result, _ = self.sample()
        self.assertEqual(result["state"], "supervisor_stopped")
        self.processes[10]["state"] = "S"
        self.entry.update(kind="batch", phase="execution")
        result, _ = self.quiet_window()
        self.assertEqual(result["state"], "supervised")

    @unittest.skipIf(os.name == "nt", "POSIX process sampling")
    def test_status_persists_observations_but_never_changes_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            queue = Mock(root=Path(temporary))
            queue.locked.side_effect = contextlib.nullcontext
            waiting = {**self.entry, "ticket": "waiting", "state": "queued"}
            status = {"runs": [], "operations": [self.entry, waiting]}
            queue.status.return_value = status
            with patch.object(health, "process_snapshot", return_value=self.processes), \
                    patch.object(health.time, "clock_gettime") as clock:
                for now in range(0, health.QUIET_SECONDS + 1, health.SAMPLE_SECONDS):
                    clock.return_value = now
                    health.annotate(queue, status)
                self.assertEqual(self.entry["health"]["state"], "suspected_stall")
                self.assertNotIn("health", waiting)
                self.assertEqual(self.entry["state"], "running")
                with patch("builtins.print") as output:
                    health.report(queue)
                self.assertIn("suspected_stall", output.call_args.args[0])
                self.assertIn("fixture", output.call_args.args[0])

    @unittest.skipIf(os.name == "nt", "POSIX process sampling")
    def test_separate_observers_share_the_activity_clock(self):
        with tempfile.TemporaryDirectory() as temporary:
            queue = Mock(root=Path(temporary))
            queue.locked.side_effect = contextlib.nullcontext
            status = {"runs": [self.entry], "operations": []}
            before = time.clock_gettime(time.CLOCK_MONOTONIC)
            with patch.object(health, "process_snapshot", return_value=self.processes):
                health.annotate(queue, status)
            path = queue.root / "health.json"
            first = json.loads(path.read_text())[self.entry["ticket"]]
            command = f'''import contextlib, json
from pathlib import Path
from types import SimpleNamespace
import verification_health as health
queue = SimpleNamespace(root=Path({temporary!r}), locked=contextlib.nullcontext)
health.process_snapshot = lambda: {self.processes!r}
health.annotate(queue, {{"runs": [{self.entry!r}], "operations": []}})
'''
            subprocess.run([sys.executable, "-c", command], cwd=Path(health.__file__).parent,
                           check=True, capture_output=True, timeout=5)
            second = json.loads(path.read_text())[self.entry["ticket"]]
            after = time.clock_gettime(time.CLOCK_MONOTONIC)
            self.assertLessEqual(before, first["sampled_at"])
            self.assertLessEqual(first["sampled_at"], second["sampled_at"])
            self.assertLessEqual(second["sampled_at"], after)
            self.assertEqual(first["activity_at"], second["activity_at"])

    def test_unavailable_telemetry_reports_unknown(self):
        status = {"runs": [self.entry], "operations": []}
        with patch.object(health, "process_snapshot", side_effect=OSError("unavailable")):
            health.annotate(Mock(), status)
        self.assertEqual(self.entry["health"]["state"], "unknown")
        self.assertEqual(self.entry["state"], "running")

    @unittest.skipIf(os.name == "nt", "POSIX process sampling")
    def test_process_sampler_handles_macos_and_linux_cpu_formats(self):
        output = " 10 1 Sun Sep 13 10:00:00 2026 Ss 391:01.96\n20 10 Sun Sep 13 10:00:01 2026 S 1-02:03:04\n"
        with patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, output)):
            records = health.process_snapshot()
        self.assertEqual(records[10]["cpu"], "391:01.96")
        self.assertEqual(records[20]["parent"], 10)
        self.assertEqual(records[20]["identity"], "Sun Sep 13 10:00:01 2026")

    @unittest.skipIf(os.name == "nt", "POSIX process sampling")
    def test_live_process_creation_identity_matches_its_snapshot(self):
        identity = health.identity(os.getpid())
        self.assertIsNotNone(identity)
        self.assertEqual(health.ownership(os.getpid(), identity, health.process_snapshot()), "present")


if __name__ == "__main__":
    unittest.main()
