import contextlib
import errno
import fcntl
import importlib.util
import io
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[2] / "test-run-lease.py"


def load_lease_module():
    spec = importlib.util.spec_from_file_location("test_run_lease", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TestRunPruneExecution(unittest.TestCase):
    def setUp(self):
        self.lease = load_lease_module()
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()

    def run_dir(self, name):
        run = self.root / name
        (run / "tmp" / "mounts" / "primary").mkdir(parents=True)
        (run / ".lease").write_text("")
        return run

    def prune(self, *candidate):
        report = io.StringIO()
        with contextlib.redirect_stderr(report):
            self.lease.prune(self.root, *candidate)
        return report.getvalue()

    def without_mounts(self):
        patcher = mock.patch.object(self.lease, "mount_points", return_value=[])
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_unleased_runs_are_removed_silently(self):
        self.without_mounts()
        run = self.run_dir("go-test.lease.a")
        self.assertEqual(self.prune(), "")
        self.assertFalse(run.exists())

    def test_a_held_lease_keeps_its_run_and_its_mount(self):
        run = self.run_dir("go-test.lease.held")
        with open(run / ".lease") as held:
            fcntl.flock(held, fcntl.LOCK_EX)
            with mock.patch.object(self.lease, "mount_points") as table, mock.patch.object(
                self.lease, "force_unmount"
            ) as unmount:
                self.assertEqual(self.prune(), "")
        table.assert_not_called()
        unmount.assert_not_called()
        self.assertTrue(run.exists())

    def test_a_timed_out_removal_is_reported_and_keeps_only_its_run(self):
        self.without_mounts()
        stuck = self.run_dir("go-test.lease.stuck")
        other = self.run_dir("go-test.lease.other")
        real_chmod = os.chmod

        def chmod(path, mode):
            if Path(path).name == "primary" and stuck in Path(path).parents:
                raise TimeoutError(60, "Operation timed out", str(path))
            real_chmod(path, mode)

        with mock.patch.object(self.lease.os, "chmod", chmod):
            report = self.prune()
        self.assertIn(f"keeping {stuck}", report)
        self.assertIn("Operation timed out", report)
        self.assertTrue(stuck.exists())
        self.assertFalse(other.exists())

    def test_a_partial_removal_is_retried_by_the_next_prune(self):
        self.without_mounts()
        run = self.run_dir("go-test.lease.partial")
        with mock.patch.object(self.lease.shutil, "rmtree", side_effect=OSError("busy")):
            self.assertIn(f"keeping {run}", self.prune())
        self.assertTrue((run / ".lease").exists())
        self.assertEqual(self.prune(), "")
        self.assertFalse(run.exists())

    def test_removing_a_named_run_reports_instead_of_raising(self):
        self.without_mounts()
        run = self.run_dir("go-test.lease.named")
        with mock.patch.object(self.lease.os, "chmod", side_effect=TimeoutError("timed out")):
            self.assertIn(f"keeping {run}", self.prune(run))
        self.assertTrue(run.exists())

    def test_an_unreadable_mount_table_keeps_every_run(self):
        run = self.run_dir("go-test.lease.unknown")
        failure = subprocess.TimeoutExpired("/sbin/mount", 15)
        with mock.patch.object(self.lease, "mount_points", side_effect=failure):
            self.assertIn(f"keeping {run}", self.prune())
        self.assertTrue(run.exists())

    def test_a_left_mount_is_detached_deepest_first_before_removal(self):
        run = self.run_dir("go-test.lease.killed")
        outer = str(run / "tmp" / "mounts")
        inner = str(run / "tmp" / "mounts" / "primary")
        unrelated = "/elsewhere"
        tables = iter([[unrelated, outer, inner], [unrelated]])
        with mock.patch.object(self.lease, "mount_points", lambda: next(tables)), mock.patch.object(
            self.lease, "force_unmount"
        ) as unmount:
            self.assertEqual(self.prune(), "")
        self.assertEqual([call.args[0] for call in unmount.call_args_list], [inner, outer])
        self.assertFalse(run.exists())

    def test_a_mount_that_stays_keeps_its_run_without_touching_it(self):
        run = self.run_dir("go-test.lease.dead")
        other = self.run_dir("go-test.lease.other")
        mount = str(run / "tmp" / "mounts" / "primary")
        real_chmod = os.chmod

        def chmod(path, mode):
            self.assertNotEqual(str(path), mount, "must not touch a mount it could not detach")
            real_chmod(path, mode)

        with mock.patch.object(self.lease, "mount_points", return_value=[mount]), mock.patch.object(
            self.lease, "force_unmount"
        ), mock.patch.object(self.lease.os, "chmod", chmod):
            report = self.prune()
        self.assertIn(f"keeping {run}", report)
        self.assertIn(mount, report)
        self.assertTrue((run / ".lease").exists())
        self.assertFalse(other.exists())

    def test_a_sibling_sharing_the_name_prefix_is_not_detached(self):
        run = self.run_dir("go-test.lease.a")
        sibling_mount = str(self.root / "go-test.lease.ab" / "mount")
        with mock.patch.object(self.lease, "mount_points", return_value=[sibling_mount]), mock.patch.object(
            self.lease, "force_unmount"
        ) as unmount:
            self.assertEqual(self.prune(run), "")
        unmount.assert_not_called()
        self.assertFalse(run.exists())

    def test_a_symlinked_root_still_matches_the_kernels_resolved_path(self):
        run = self.run_dir("go-test.lease.linked")
        link = self.root.parent / f"{self.root.name}-link"
        link.symlink_to(self.root)
        self.addCleanup(link.unlink)
        mount = str(run / "tmp" / "mounts" / "primary")
        tables = iter([[mount], []])
        with mock.patch.object(self.lease, "mount_points", lambda: next(tables)), mock.patch.object(
            self.lease, "force_unmount"
        ) as unmount:
            self.lease.remove_unleased(link / run.name)
        unmount.assert_called_once_with(mount)
        self.assertFalse(run.exists())


class MountHelperExecution(unittest.TestCase):
    MOUNT = "/run root/tmp/mount"

    def setUp(self):
        self.lease = load_lease_module()

    def platform(self, name):
        patcher = mock.patch.object(self.lease.sys, "platform", name)
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_macos_mount_table_keeps_paths_with_spaces(self):
        self.platform("darwin")
        listing = mock.Mock(stdout=b"/dev/disk3 on / (apfs)\n127.0.0.1:/views/a on /tmp/a b/mnt (nfs, nodev)\nnoise\n")
        with mock.patch.object(self.lease.subprocess, "run", return_value=listing):
            self.assertEqual(self.lease.mount_points(), ["/", "/tmp/a b/mnt"])

    def test_linux_mount_table_decodes_escaped_paths(self):
        self.platform("linux")
        table = (
            b"25 1 0:23 / / rw - ext4 /dev/sda rw\n"
            b"36 25 0:30 / /tmp/a\\040b/mnt rw,nosuid - fuse.view view rw\n"
        )
        with mock.patch("builtins.open", mock.mock_open(read_data=table)):
            self.assertEqual(self.lease.mount_points(), ["/", "/tmp/a b/mnt"])

    def helpers(self, *behaviours):
        return mock.patch.object(self.lease.subprocess, "Popen", side_effect=behaviours)

    def test_force_unmount_stops_once_the_mount_is_gone(self):
        self.platform("darwin")
        with self.helpers(mock.Mock()) as popen, mock.patch.object(self.lease, "mount_points", return_value=[]):
            self.lease.force_unmount(self.MOUNT)
        self.assertEqual(popen.call_args.args[0], ["/sbin/umount", "-f", self.MOUNT])

    def test_force_unmount_kills_a_stuck_helper_without_awaiting_it_and_falls_back(self):
        self.platform("darwin")
        stuck = mock.Mock()
        stuck.wait.side_effect = subprocess.TimeoutExpired("umount", 15)
        with self.helpers(stuck, mock.Mock()) as popen, mock.patch.object(
            self.lease, "mount_points", side_effect=[[self.MOUNT], []]
        ):
            self.lease.force_unmount(self.MOUNT)
        stuck.kill.assert_called_once_with()
        self.assertEqual(stuck.wait.call_count, 1)
        self.assertEqual(popen.call_args.args[0], ["/usr/sbin/diskutil", "unmount", "force", self.MOUNT])

    def test_force_unmount_on_linux_tries_the_next_helper_when_one_is_missing(self):
        self.platform("linux")
        with self.helpers(FileNotFoundError("fusermount3"), mock.Mock()) as popen, mock.patch.object(
            self.lease, "mount_points", return_value=[]
        ):
            self.lease.force_unmount(self.MOUNT)
        self.assertEqual(popen.call_args.args[0], ["umount", "-l", self.MOUNT])


@unittest.skipIf(os.name == "nt", "test-run leases require POSIX file locks")
class PruneExecution(unittest.TestCase):
    def setUp(self):
        self.helper = load_lease_module()
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

    def test_directory_without_a_lease_is_reclaimed(self):
        stuck = self.run_dir("go-test.lease.partial")
        (stuck / ".lease").unlink()

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

        with mock.patch.object(self.helper, "mount_points", return_value=[]), mock.patch.object(self.helper.os, "lstat", fake):
            report = self.prune()

        self.assertIn(str(mount), report)
        self.assertEqual(stat.S_IMODE(lstat(mount).st_mode), 0o500)
        self.assertTrue((mount / "served").exists())
        self.assertTrue((mounted_run / ".lease").exists())
        self.assertFalse(stale.exists())


if __name__ == "__main__":
    unittest.main()
