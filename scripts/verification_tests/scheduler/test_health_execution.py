"""Process activity and ownership diagnostics."""

import copy
import contextlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import Mock, patch

SCRIPT = Path(__file__).resolve().parents[2].joinpath("test-execution.py")
spec = importlib.util.spec_from_file_location("test_execution", SCRIPT)
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)
import verification_execute as execute
import verification_plan as planning
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

    @unittest.skipUnless(sys.platform == "darwin", "Darwin sysctl process sampling")
    def test_darwin_sysctl_fallback_matches_ps_identity_when_ps_is_blocked(self):
        pid = os.getpid()
        ps_identity = health.process_snapshot(pid)[pid]["identity"]
        with patch.object(subprocess, "run", side_effect=PermissionError("Operation not permitted")):
            sysctl_single = health.process_snapshot(pid)
            sysctl_all = health.process_snapshot()
        self.assertEqual(sysctl_single[pid]["identity"], ps_identity)
        self.assertEqual(sysctl_all[pid]["identity"], ps_identity)
        self.assertEqual(sysctl_single[pid]["parent"], os.getppid())
        self.assertEqual(health.ownership(pid, ps_identity, sysctl_single), "present")

    @unittest.skipIf(os.name == "nt", "POSIX process sampling")
    def test_declared_progress_signal_keeps_zero_cpu_daemon_job_active_until_signal_stops(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            queue = execution.Queue(root / "queue")
            ready = root / "ready"
            done = root / "done"
            script = (
                "import os, pathlib, time\n"
                f"pathlib.Path({str(ready)!r}).write_text(os.environ.get('PW_TEST_STAGE_PROGRESS', ''))\n"
                f"while not pathlib.Path({str(done)!r}).exists():\n"
                "    time.sleep(0.02)\n"
            )
            outcomes = []
            with patch.dict(os.environ, {"PW_TEST_EXECUTION_TICKET": "", "PW_TEST_STAGE_PROGRESS": ""}):
                worker = threading.Thread(
                    target=lambda: outcomes.append(execution.run(
                        queue, "daemon:build", [sys.executable, "-c", script],
                        {"locks": ["harness"], "workers": 1, "progress": "log"})))
                worker.start()
                try:
                    deadline = time.monotonic() + 5
                    while not ready.exists() and time.monotonic() < deadline:
                        time.sleep(0.02)
                    self.assertTrue(ready.exists())
                    progress = Path(ready.read_text())
                    status = queue.status()
                    run_entry = status["runs"][0]
                    op_entry = status["operations"][0]
                    self.assertEqual(run_entry["activity_paths"], [str(progress)])
                    self.assertEqual(op_entry["activity_paths"], [str(progress)])
                    snapshot = {
                        run_entry["pid"]: process(run_entry["pid"], identity=run_entry["owner_identity"]),
                        run_entry["child_pid"]: process(
                            run_entry["child_pid"], run_entry["pid"], identity=run_entry["child_identity"]),
                    }
                    with patch.object(health, "process_snapshot", return_value=snapshot), \
                            patch.object(health.time, "clock_gettime") as clock:
                        for step, now in enumerate(range(0, health.QUIET_SECONDS * 2 + 1, health.SAMPLE_SECONDS)):
                            progress.write_text(f"step-{step}\n")
                            clock.return_value = now
                            observed = health.annotate(queue, queue.status())["runs"][0]["health"]
                            self.assertNotEqual(observed["state"], "suspected_stall")
                        self.assertEqual(observed["state"], "active")
                        stopped_at = health.QUIET_SECONDS * 2
                        for now in range(stopped_at + health.SAMPLE_SECONDS,
                                         stopped_at + health.QUIET_SECONDS + 1, health.SAMPLE_SECONDS):
                            clock.return_value = now
                            observed = health.annotate(queue, queue.status())["runs"][0]["health"]
                        self.assertEqual(observed["state"], "suspected_stall")
                finally:
                    done.write_text("done")
                    worker.join(timeout=5)
            self.assertEqual(outcomes, [0])
            self.assertFalse(progress.exists())

    @unittest.skipIf(os.name == "nt", "POSIX process sampling")
    def test_ordinary_target_ignores_undeclared_progress_and_stalls_on_schedule(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            queue = execution.Queue(root / "queue")
            ready = root / "ready"
            done = root / "done"
            fake_progress = root / "undeclared.progress"
            script = (
                "import os, pathlib, time\n"
                f"pathlib.Path({str(ready)!r}).write_text(os.environ.get('PW_TEST_STAGE_PROGRESS', ''))\n"
                f"while not pathlib.Path({str(done)!r}).exists():\n"
                "    time.sleep(0.02)\n"
            )
            outcomes = []
            with patch.dict(os.environ, {"PW_TEST_EXECUTION_TICKET": "",
                                         "PW_TEST_STAGE_PROGRESS": str(fake_progress)}):
                worker = threading.Thread(
                    target=lambda: outcomes.append(execution.run(
                        queue, "lint:fast", [sys.executable, "-c", script],
                        {"locks": ["lint"], "workers": 1})))
                worker.start()
                try:
                    deadline = time.monotonic() + 5
                    while not ready.exists() and time.monotonic() < deadline:
                        time.sleep(0.02)
                    self.assertTrue(ready.exists())
                    self.assertEqual(ready.read_text(), "")
                    status = queue.status()
                    run_entry = status["runs"][0]
                    self.assertNotIn("activity_paths", run_entry)
                    snapshot = {
                        run_entry["pid"]: process(run_entry["pid"], identity=run_entry["owner_identity"]),
                        run_entry["child_pid"]: process(
                            run_entry["child_pid"], run_entry["pid"], identity=run_entry["child_identity"]),
                    }
                    with patch.object(health, "process_snapshot", return_value=snapshot), \
                            patch.object(health.time, "clock_gettime") as clock:
                        for step, now in enumerate(range(0, health.QUIET_SECONDS + 1, health.SAMPLE_SECONDS)):
                            fake_progress.write_text(f"ignored-{step}\n")
                            clock.return_value = now
                            observed = health.annotate(queue, queue.status())["runs"][0]["health"]
                        self.assertEqual(observed["state"], "suspected_stall")
                finally:
                    done.write_text("done")
                    worker.join(timeout=5)
            self.assertEqual(outcomes, [0])

    def test_catalog_and_stage_execution_propagate_declared_progress_recipes(self):
        base = json.loads(planning.CATALOG.read_text())
        for valid in ("log", "heartbeat"):
            candidate = copy.deepcopy(base)
            candidate["resources"]["den:webkit:atspi"]["progress"] = valid
            with patch.object(planning.Path, "read_text", return_value=json.dumps(candidate)):
                self.assertEqual(planning.catalog()["resources"]["den:webkit:atspi"]["progress"], valid)
        for invalid in ("prose", "", None, True, 1):
            candidate = copy.deepcopy(base)
            candidate["resources"]["den:webkit:atspi"]["progress"] = invalid
            with patch.object(planning.Path, "read_text", return_value=json.dumps(candidate)), \
                    self.assertRaisesRegex(ValueError, "invalid resource recipe: den:webkit:atspi"):
                planning.catalog()
        self.assertEqual(planning.invocation_profile(["den:webkit:atspi"], ["--prepare"])["progress"], "log")
        self.assertNotIn("progress", planning.invocation_profile(["test:digest"], ["./internal/api"]))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            queue = execution.Queue(root / "queue")
            batch_dir = root / "batch"
            batch_dir.mkdir()
            plan = {"batch": {"ticket": "00000000000000000001-batch", "workers": 1, "source": str(root),
                              "members": ["00000000000000000001"], "state": "running", "kind": "batch"},
                    "requests": [{"ticket": "00000000000000000001", "order": 1, "environment_identity": "env"}]}
            (batch_dir / "plan.json").write_text(json.dumps(plan))
            lease_path = queue.root / "00000000000000000001-batch.lease"
            lease_path.write_text(json.dumps(plan["batch"]))
            captured = []
            pool = Mock()

            def run_inline(fn, *args, **kwargs):
                captured.append((args, kwargs))
                future = Mock()
                future.result.return_value = fn(*args, **kwargs)
                return future

            pool.submit.side_effect = run_inline
            stage = {"kind": "task", "name": "den:webkit:atspi", "arguments": ["den:webkit:atspi"]}
            with lease_path.open("r+") as batch_lease, \
                    patch.dict(os.environ, {"PW_TEST_STAGE_PROGRESS": "/tmp/stale",
                                            "PW_SOURCE_SNAPSHOT_COMMIT": "0" * 40}), \
                    patch.object(execute, "task_command", return_value=[sys.executable, "-c", "pass"]):
                execution.lock_file(batch_lease)
                executor = execute.Executor(queue, batch_dir)
                executor.interested = lambda _: True
                job = executor.start_stage([(plan["requests"][0], stage, [], 0)], "stage-001", pool)
                progress_paths = list(job["reservation"].entry["activity_paths"])
                job["reservation"].__exit__(None, None, None)
                ordinary = {"kind": "task", "name": "lint:fast", "arguments": ["lint:fast"]}
                job2 = executor.start_stage([(plan["requests"][0], ordinary, [], 0)], "stage-002", pool)
                ordinary_paths = list(job2["reservation"].entry["activity_paths"])
                job2["reservation"].__exit__(None, None, None)
            expected_progress = str(batch_dir / "stage-001-progress.log")
            self.assertEqual(captured[0][1]["env"]["PW_TEST_STAGE_PROGRESS"], expected_progress)
            self.assertIn(expected_progress, progress_paths)
            self.assertNotIn("PW_TEST_STAGE_PROGRESS", captured[1][1]["env"])
            self.assertEqual(ordinary_paths, [str(batch_dir / "stage-002.log")])


if __name__ == "__main__":
    unittest.main()
