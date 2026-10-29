"""Terminal producer ownership, distinct from custody after a supervisor crash."""

import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

import verification_lifecycle.custody as custody
import verification_lifecycle.terminal as lifetime
from verification_health import owner_identity
from verification_resources import Reservation, entries as reservations
from verification_execute import Executor

SCRIPT = Path(__file__).resolve().parents[2] / "test-execution.py"
spec = importlib.util.spec_from_file_location("terminal_execution", SCRIPT)
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)


@unittest.skipIf(os.name == "nt", "requires inherited POSIX capability locks")
class TerminalFixture(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.queue = execution.Queue(self.root / "queue")
        self.sequence = 0

    def capability(self, suffix="resource", batch="shared"):
        self.sequence += 1
        path = self.queue.root / f"fixture-{self.sequence}.{suffix}"
        file = path.open("x+")
        execution.lock_file(file)
        entry = {"ticket": path.stem, "kind": "invocation", "state": "running", "name": "fixture",
                 "pid": os.getpid(), "owner_identity": owner_identity(os.getpid()), "batch": batch,
                 "locks": ["fixture"], "shared_locks": [], "workers": 1}
        capability = lifetime.Capability(path, file, entry)
        capability.write()
        self.addCleanup(lambda: capability.file.close())
        return capability

    def producer(self, capability, code=0, ignore_term=False, extra=()):
        self.sequence += 1
        ready, stop = self.root / f"ready-{self.sequence}", self.root / f"stop-{self.sequence}"
        descendant = ("import os,signal,time\nfrom pathlib import Path\n"
                      + ("signal.signal(signal.SIGTERM,signal.SIG_IGN)\n" if ignore_term else "")
                      + f"Path({str(ready)!r}).write_text(str(os.getpid()))\n"
                      + f"while not Path({str(stop)!r}).exists(): time.sleep(.01)\n")
        inherited = tuple([capability.file.fileno(), *extra])
        parent = ("import subprocess,sys,time\nfrom pathlib import Path\n"
                  f"subprocess.Popen([sys.executable,'-c',{descendant!r}],pass_fds={inherited!r},"
                  "start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n"
                  f"while not Path({str(ready)!r}).exists(): time.sleep(.01)\n"
                  f"sys.exit({code})\n")
        child = subprocess.Popen([sys.executable, "-c", parent], pass_fds=inherited,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        self.addCleanup(stop.touch)
        child.wait(timeout=10)
        self.assertEqual(child.returncode, code)
        pid = int(ready.read_text())
        created = custody.birth(pid)
        def stop_fixture():
            stop.touch()
            if custody.birth(pid) == created:
                custody.signal_holder(pid, created, capability.path,
                                      (capability.path.stat().st_dev, capability.path.stat().st_ino), signal.SIGKILL)
        self.addCleanup(stop_fixture)
        return child, pid, stop


class TerminalProcessTests(TerminalFixture):
    def test_success_failure_and_term_ignoring_escapees_settle_before_release(self):
        for code, ignore in [(0, False), (1, False), (0, True)]:
            with self.subTest(code=code, ignore=ignore):
                cap = self.capability()
                child, pid, _ = self.producer(cap, code, ignore)
                with patch.object(lifetime, "DRAIN_SECONDS", .01), patch.object(lifetime, "SIGNAL_SECONDS", .1):
                    self.assertFalse(lifetime.TerminalSettlement(self.queue, cap, child).finish())
                self.assertEqual(child.returncode, code, "cleanup replaced the actual producer outcome")
                self.assertFalse(custody.holds(pid, cap.path, (cap.path.stat().st_dev, cap.path.stat().st_ino)))
                self.assertEqual(cap.entry["terminal_settlement"]["state"], "settled")
                self.assertIn(pid, [r["pid"] for r in cap.entry["terminal_settlement"]["reclaimed"]])

    def test_clean_completion_uses_kernel_probe_without_global_holder_scan(self):
        cap = self.capability()
        child = subprocess.Popen([sys.executable, "-c", "pass"], pass_fds=(cap.file.fileno(),))
        child.wait(timeout=10)
        with patch.object(custody, "lease_holders", side_effect=AssertionError("unnecessary global FD scan")):
            self.assertFalse(lifetime.TerminalSettlement(self.queue, cap, child).finish())

    def test_neighbor_in_the_same_batch_and_source_custody_are_untouched(self):
        first, neighbor, source = self.capability(), self.capability(), self.capability("source")
        child, _, _ = self.producer(first)
        neighbor_child, neighbor_pid, neighbor_stop = self.producer(neighbor, extra=(source.file.fileno(),))
        self.assertFalse(lifetime.TerminalSettlement(self.queue, first, child).finish())
        expected = neighbor.path.stat()
        self.assertTrue(custody.holds(neighbor_pid, neighbor.path, (expected.st_dev, expected.st_ino)))
        source_info = source.path.stat()
        self.assertTrue(custody.holds(neighbor_pid, source.path, (source_info.st_dev, source_info.st_ino)))
        probe = source.path.open("r+")
        with probe, self.assertRaises(BlockingIOError):
            execution.lock_file(probe, blocking=False)
        self.assertEqual(neighbor.entry.get("terminal_settlement"), None)
        neighbor_stop.touch()
        self.assertFalse(lifetime.TerminalSettlement(self.queue, neighbor, neighbor_child).finish())

    def test_live_producer_is_not_terminal_authority(self):
        cap = self.capability()
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"],
                                 pass_fds=(cap.file.fileno(),))
        try:
            with self.assertRaisesRegex(ValueError, "reaped"):
                lifetime.TerminalSettlement(self.queue, cap, child)
            self.assertIsNone(child.poll())
        finally:
            child.terminate()
            child.wait(timeout=10)

    def test_uninspectable_cleanup_keeps_original_owner_and_both_capabilities(self):
        run = self.capability("lease")
        cap = self.capability()
        child, _, stop = self.producer(cap, extra=(run.file.fileno(),))
        blocked = threading.Event()
        result = []
        def settle():
            result.append(lifetime.TerminalSettlement(self.queue, cap, child).finish(blocked.set))
        with patch.object(custody, "lease_holders", side_effect=PermissionError("uninspectable")):
            thread = threading.Thread(target=settle)
            thread.start()
            try:
                self.assertTrue(blocked.wait(10))
                self.assertTrue(thread.is_alive(), "cleanup abandoned the original supervisor")
                self.assertTrue(lifetime.retained(cap.entry))
                for capability in (run, cap):
                    probe = capability.path.open("r+")
                    with probe, self.assertRaises(BlockingIOError):
                        execution.lock_file(probe, blocking=False)
                with self.queue.locked():
                    self.assertIn(cap.entry["ticket"], [r["ticket"] for r in reservations(self.queue, execution.lock_file)])
            finally:
                stop.touch()
                thread.join(timeout=10)
        self.assertFalse(thread.is_alive())
        self.assertEqual(result, [True], "an unverified cleanup later recovered into a pass")
        self.assertTrue(cap.entry["terminal_settlement"]["unverified"])

    def test_exception_cleanup_cannot_manufacture_stage_terminal_authority(self):
        batch = self.capability("lease")
        directory = self.root / "batch"
        directory.mkdir()
        request = {"ticket": "request", "order": 1, "environment_identity": "fixture"}
        plan = {"batch": {"ticket": batch.entry["ticket"], "workers": 1, "members": ["request"]},
                "requests": [request]}
        (directory / "plan.json").write_text(json.dumps(plan))
        ready, stop = self.root / "stage-holder", self.root / "stage-stop"
        self.addCleanup(stop.touch)
        descendant = ("import os,time\nfrom pathlib import Path\n"
                      f"Path({str(ready)!r}).write_text(str(os.getpid()))\n"
                      "deadline=time.monotonic()+20\n"
                      f"while not Path({str(stop)!r}).exists() and time.monotonic()<deadline: time.sleep(.01)\n")
        producer = ("import os,subprocess,sys,time\n"
                    "fds=tuple(int(os.environ[key]) for key in ('PW_TEST_EXECUTION_FD','PW_TEST_RESOURCE_FD'))\n"
                    f"subprocess.Popen([sys.executable,'-c',{descendant!r}],pass_fds=fds,start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n"
                    "while True: time.sleep(.01)\n")
        with patch.dict(os.environ, PW_SOURCE_SNAPSHOT_COMMIT="c" * 40,
                        PW_TEST_EXECUTION_FD=str(batch.file.fileno())):
            executor = Executor(self.queue, directory)
            calls = 0
            def interrupted_interest(_consumers):
                nonlocal calls
                calls += 1
                if calls <= 2:
                    return True
                for _ in range(1000):
                    if ready.exists():
                        raise OSError("owned subscriber inspection interrupted")
                    threading.Event().wait(.01)
                self.fail("actual stage producer never created its escaped holder")
            spec = {"locks": ["fixture"], "workers": 1}
            with Reservation(self.queue, execution.lock_file, batch.entry["ticket"], "fixture", spec, 1) as cap, \
                    patch.object(executor, "interested", side_effect=interrupted_interest), \
                    patch("verification_execute.TerminalSettlement", side_effect=AssertionError("false terminal authority")):
                with self.assertRaisesRegex(OSError, "subscriber inspection interrupted"):
                    executor.run_command([sys.executable, "-c", producer], directory / "stage.log", ["request"],
                                         reservation=cap)
                pid = int(ready.read_text())
                info = cap.path.stat()
                self.assertTrue(custody.holds(pid, cap.path, (info.st_dev, info.st_ino)))
                with self.queue.locked():
                    self.assertTrue(reservations(self.queue, execution.lock_file), "exception released admission")
                stop.touch()
                for _ in range(1000):
                    if not custody.holds(pid, cap.path, (info.st_dev, info.st_ino)):
                        break
                    threading.Event().wait(.01)
                self.assertFalse(custody.holds(pid, cap.path, (info.st_dev, info.st_ino)))

    def test_unlocked_probe_still_has_live_owner_admission_guard(self):
        cap = self.capability()
        cap.entry["terminal_settlement"] = {"state": "blocked"}
        cap.write()
        cap.file.close()
        cap.file = cap.path.open("r+")
        with self.queue.locked():
            self.assertIn(cap.entry["ticket"], [r["ticket"] for r in reservations(self.queue, execution.lock_file)])
        self.assertTrue(cap.path.exists(), "a probe interval was mistaken for an abandoned reservation")
        cap.entry["owner_identity"] = "a different process incarnation"
        cap.write()
        with self.queue.locked():
            self.assertNotIn(cap.entry["ticket"], [r["ticket"] for r in reservations(self.queue, execution.lock_file)])


class HolderIdentityTests(unittest.TestCase):
    def test_recycled_pid_never_receives_a_signal(self):
        with patch.object(custody.os, "pidfd_open", return_value=123, create=True), \
                patch.object(custody.os, "close"), patch.object(custody, "birth", return_value="replacement"), \
                patch.object(custody, "holds") as holds, patch.object(custody.os, "kill") as kill, \
                patch.object(custody.signal, "pidfd_send_signal", create=True) as send:
            self.assertFalse(custody.signal_holder(12345, "original", Path("unused"), (1, 2), signal.SIGTERM))
            holds.assert_not_called()
            kill.assert_not_called()
            send.assert_not_called()

    def test_same_pid_without_the_capability_never_receives_a_signal(self):
        with patch.object(custody.os, "pidfd_open", return_value=123, create=True), \
                patch.object(custody.os, "close"), patch.object(custody, "birth", return_value="original"), \
                patch.object(custody, "holds", return_value=False), patch.object(custody.os, "kill") as kill, \
                patch.object(custody.signal, "pidfd_send_signal", create=True) as send:
            self.assertFalse(custody.signal_holder(12345, "original", Path("unused"), (1, 2), signal.SIGTERM))
            kill.assert_not_called()
            send.assert_not_called()
