"""A real process killed while holding a real view mount inside its run: teardown settles it alone."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

from verification_health import owner_identity
from verification_lifecycle import custody, run_lease
from verification_lifecycle import terminal as lifetime

SCRIPTS = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("killed_owner_execution", SCRIPTS / "test-execution.py")
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)
HOLDER = "./internal/layeredfs/adapter/platform/testdata/mountholder"
WAIT_SECONDS = 120


def mount_facility_absent():
    if sys.platform == "linux":
        return None if os.path.exists("/dev/fuse") and shutil.which("fusermount3") else "no FUSE device or helper"
    if sys.platform == "darwin":
        return None
    return "no mounted adapter on this platform"


def wait_until(what, done):
    deadline = time.monotonic() + WAIT_SECONDS
    while not done():
        if time.monotonic() >= deadline:
            raise AssertionError(f"timed out waiting for {what}")
        time.sleep(0.02)


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


@unittest.skipIf(mount_facility_absent(), mount_facility_absent())
class KilledMountOwnerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tools = tempfile.TemporaryDirectory(prefix="pw-holder-", dir="/tmp")
        cls.addClassCleanup(cls.tools.cleanup)
        cls.binary = Path(cls.tools.name) / "mountholder"
        subprocess.run(["go", "build", "-o", str(cls.binary), HOLDER], cwd=SCRIPTS.parent / "lycaon", check=True)

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="pw-rl-", dir="/tmp")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(os.path.realpath(self.temporary.name))
        patcher = mock.patch.object(run_lease, "RUN_ROOT", self.root)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.queue = execution.Queue(self.root / "queue-state")
        self.sequence = 0

    def run_directory(self, name):
        run = self.root / f"go-test.lease.{name}"
        (run / "tmp").mkdir(parents=True)
        lease = (run / ".lease").open("w")
        execution.lock_file(lease)
        return run, lease

    def holder(self, run, inherited, wedge):
        """Start a holder through a producer that exits, as a digest's descendants outlive it."""
        self.sequence += 1
        ready, stop = run / f"ready-{self.sequence}", run / f"stop-{self.sequence}"
        environment = {**os.environ, **({"MOUNTHOLDER_WEDGE": "1"} if wedge else {})}
        command = [str(self.binary), str(run / "tmp" / "v"), str(ready), str(stop)]
        producer = ("import subprocess,sys,time\nfrom pathlib import Path\n"
                    f"subprocess.Popen({command!r},pass_fds={inherited!r},start_new_session=True,"
                    "stdout=subprocess.DEVNULL)\n"
                    f"while not Path({str(ready)!r}).exists(): time.sleep(.01)\n")
        child = subprocess.Popen([sys.executable, "-c", producer], pass_fds=inherited, env=environment,
                                 stdout=subprocess.DEVNULL, start_new_session=True)
        child.wait(timeout=WAIT_SECONDS)
        self.assertEqual(child.returncode, 0)
        report = json.loads(ready.read_text())
        self.addCleanup(self.settle_fixture, report, stop)
        return child, report, stop

    def settle_fixture(self, report, stop):
        """A failed test still ends what it started, by its exact identity."""
        stop.parent.mkdir(parents=True, exist_ok=True)
        stop.touch()
        if report["mount"] in run_lease.mount_points() and sys.platform == "linux":
            for connection in run_lease.fuse_connections(report["mount"]):
                if not alive(report["pid"]) or run_lease.thread_group_dying(report["pid"]):
                    run_lease.abort_connection(connection)
        deadline = time.monotonic() + 20
        while report["mount"] in run_lease.mount_points() and time.monotonic() < deadline:
            if not alive(report["pid"]) or sys.platform == "darwin":
                run_lease.force_unmount(report["mount"])
            time.sleep(0.05)

    def capability(self, run):
        path = self.queue.root / "fixture.resource"
        file = path.open("x+")
        execution.lock_file(file)
        entry = {"ticket": path.stem, "kind": "invocation", "state": "running", "name": "fixture",
                 "pid": os.getpid(), "owner_identity": owner_identity(os.getpid()), "batch": "shared",
                 "locks": ["fixture"], "shared_locks": [], "workers": 1}
        capability = lifetime.Capability(path, file, entry)
        capability.write()
        self.addCleanup(lambda: capability.file.close())
        return capability

    def wait_wedged(self, report):
        if sys.platform != "linux":
            return
        connection = run_lease.fuse_connections(report["mount"])[0]
        waiting = run_lease.FUSE_CONNECTIONS / connection.device / "waiting"
        wait_until("two parked requests", lambda: int(waiting.read_text()) >= 2)

    def outside_mount(self):
        """A live owner's mount in another leased run, whose connection must keep answering."""
        run, lease = self.run_directory("outside")
        self.addCleanup(lease.close)
        _, report, _ = self.holder(run, (lease.fileno(),), wedge=False)
        return run, report

    def assert_outside_untouched(self, run, report):
        self.assertTrue(run.is_dir())
        self.assertIn(report["mount"], run_lease.mount_points())
        self.assertEqual((Path(report["mount"]) / "f").read_text(), "x")

    def assert_settled(self, run, report):
        wait_until("the killed owner to exit", lambda: not alive(report["pid"]))
        self.assertNotIn(report["mount"], run_lease.mount_points())
        self.assertFalse(run.exists())

    def test_terminal_settlement_frees_a_killed_owner_with_no_blocked_batch(self):
        outside_run, outside = self.outside_mount()
        run, lease = self.run_directory("killed")
        capability = self.capability(run)
        child, report, _ = self.holder(run, (lease.fileno(), capability.file.fileno()), wedge=True)
        lease.close()
        self.wait_wedged(report)
        os.kill(report["pid"], signal.SIGKILL)
        if sys.platform == "linux":
            wait_until("the kill to arrive", lambda: run_lease.thread_group_dying(report["pid"]))
            self.assertTrue(alive(report["pid"]), "the wedge this test exists for did not form")
            self.assertEqual(custody.lease_holders(capability.path) - {os.getpid()}, {report["pid"]})

        tainted = lifetime.TerminalSettlement(self.queue, capability, child).finish()

        self.assertFalse(tainted)
        self.assertEqual(capability.entry["terminal_settlement"]["state"], "settled")
        self.assert_settled(run, report)
        self.assert_outside_untouched(outside_run, outside)

    def test_the_next_collection_frees_a_killed_owner_with_no_supervisor(self):
        outside_run, outside = self.outside_mount()
        run, lease = self.run_directory("orphan")
        _, report, _ = self.holder(run, (lease.fileno(),), wedge=True)
        lease.close()
        self.wait_wedged(report)
        os.kill(report["pid"], signal.SIGKILL)
        if sys.platform == "linux":
            wait_until("the kill to arrive", lambda: run_lease.thread_group_dying(report["pid"]))
            self.assertTrue(alive(report["pid"]), "the wedge this test exists for did not form")
        else:
            # Nothing proves another process's death here until it has exited.
            wait_until("the killed owner to exit", lambda: not alive(report["pid"]))

        subprocess.run([sys.executable, str(SCRIPTS / "test-run-lease.py"), "prune", str(self.root)], check=True)

        self.assert_settled(run, report)
        self.assert_outside_untouched(outside_run, outside)

    def test_a_held_run_with_a_live_owner_is_not_settled(self):
        run, lease = self.run_directory("live")
        self.addCleanup(lease.close)
        _, report, stop = self.holder(run, (lease.fileno(),), wedge=False)
        run_lease.collect_runs(frozenset())
        self.assert_outside_untouched(run, report)
        self.assertFalse(run_lease.release_stranded_connections(run))
        stop.touch()
        wait_until("the live owner to release its mount", lambda: report["mount"] not in run_lease.mount_points())


class StrandedConnectionFactTests(unittest.TestCase):
    @unittest.skipUnless(sys.platform == "linux", "only Linux exposes another process's pending signals")
    def test_a_thread_without_a_pending_kill_is_not_dying(self):
        self.assertFalse(run_lease.thread_group_dying(os.getpid()))

    @unittest.skipUnless(sys.platform == "linux", "only Linux exposes another process's pending signals")
    def test_a_process_with_no_record_has_no_claim(self):
        self.assertTrue(run_lease.thread_group_dying(2 ** 22 + 1))

    @unittest.skipIf(sys.platform == "linux", "Linux proves death from the process itself")
    def test_a_holder_this_process_did_not_kill_is_not_proven_dead(self):
        with tempfile.TemporaryDirectory() as run:
            with mock.patch.object(run_lease, "mount_points") as table:
                self.assertFalse(run_lease.release_stranded_connections(Path(run)))
            table.assert_not_called()

    def test_collection_failure_is_reported_not_raised(self):
        with mock.patch.object(run_lease, "prune", side_effect=OSError("unreadable")):
            run_lease.collect_runs()


if __name__ == "__main__":
    unittest.main()
