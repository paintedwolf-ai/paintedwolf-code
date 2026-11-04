"""Pruning test-run directories: stale entries that resist removal and live leases."""

import contextlib
import errno
import importlib.util
import io
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[2] / "test-run-lease.py"


def load_helper():
    spec = importlib.util.spec_from_file_location("test_run_lease", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


@unittest.skipIf(os.name == "nt", "test-run leases require POSIX file locks")
class PruneExecution(unittest.TestCase):
    def setUp(self):
        self.helper = load_helper()
        temp = tempfile.TemporaryDirectory(prefix="test-run-lease-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)

    def run_dir(self, name: str) -> Path:
        path = self.root / name
        (path / "tmp" / "runtime" / "dead").mkdir(parents=True)
        (path / "tmp" / "runtime" / "dead" / "served").write_text("exported")
        (path / ".lease").touch()
        return path

    def held_run_dir(self, name: str) -> Path:
        path = self.run_dir(name)
        lease = (path / ".lease").open("r+")
        self.addCleanup(lease.close)
        self.helper.fcntl.flock(lease, self.helper.fcntl.LOCK_EX)
        return path

    def prune(self, *candidate: Path) -> str:
        errors = io.StringIO()
        with contextlib.redirect_stderr(errors):
            self.helper.prune(self.root, *candidate)
        return errors.getvalue()

    def chmod_times_out(self, failing: Path):
        chmod = os.chmod

        def fake(path, mode):
            if Path(path) == failing:
                raise TimeoutError(errno.ETIMEDOUT, "Operation timed out", str(path))
            chmod(path, mode)

        return mock.patch.object(self.helper.os, "chmod", fake)

    def test_unremovable_stale_entry_spares_the_run_and_stays_retryable(self):
        stuck = self.run_dir("go-test.lease.stuck")
        stale = self.run_dir("go-test.lease.stale")
        live = self.held_run_dir("go-test.lease.live")

        with self.chmod_times_out(stuck / "tmp" / "runtime" / "dead"):
            report = self.prune()

        self.assertFalse(stale.exists())
        self.assertTrue((live / ".lease").exists())
        self.assertTrue((stuck / ".lease").exists())
        self.assertIn(str(stuck), report)
        self.assertIn("Operation timed out", report)

        self.assertEqual(self.prune(), "")
        self.assertFalse(stuck.exists())
        self.assertTrue((live / "tmp" / "runtime" / "dead" / "served").exists())

    def test_removing_the_own_run_does_not_raise_when_removal_fails(self):
        stuck = self.run_dir("go-test.lease.own")

        with self.chmod_times_out(stuck / "tmp" / "runtime" / "dead"):
            report = self.prune(stuck)

        self.assertTrue((stuck / ".lease").exists())
        self.assertIn(str(stuck), report)

    def test_failed_removal_that_lost_its_lease_is_reclaimed_later(self):
        stuck = self.run_dir("go-test.lease.partial")

        def lose_lease_then_fail(path):
            (Path(path) / ".lease").unlink()
            raise OSError(errno.ENOTEMPTY, "Directory not empty", str(path))

        with mock.patch.object(self.helper.shutil, "rmtree", lose_lease_then_fail):
            self.assertIn(str(stuck), self.prune())
        self.assertFalse((stuck / ".lease").exists())
        self.assertTrue(stuck.exists())

        self.assertEqual(self.prune(), "")
        self.assertFalse(stuck.exists())

    def test_mount_inside_a_stale_run_is_never_changed_or_crossed(self):
        mounted_run = self.run_dir("go-test.lease.mounted")
        stale = self.run_dir("go-test.lease.stale")
        mount = mounted_run / "tmp" / "runtime" / "dead"
        mount.chmod(0o500)
        self.addCleanup(mount.chmod, 0o700)
        lstat = os.lstat

        def fake(path, **arguments):
            status = lstat(path, **arguments)
            if Path(path) != mount:
                return status
            fields = list(status)
            fields[stat.ST_DEV] = status.st_dev + 1
            return os.stat_result(fields)

        with mock.patch.object(self.helper.os, "lstat", fake):
            report = self.prune()

        self.assertIn(str(mount), report)
        self.assertEqual(stat.S_IMODE(lstat(mount).st_mode), 0o500)
        self.assertTrue((mount / "served").exists())
        self.assertTrue((mounted_run / ".lease").exists())
        self.assertFalse(stale.exists())


if __name__ == "__main__":
    unittest.main()
