"""A killed test can leave a mount in its run directory; collection must not walk into it."""

import pathlib
import tempfile
import unittest
from unittest import mock

from verification_lifecycle import run_lease


class LeaseCollectionWithMounts(unittest.TestCase):
    def setUp(self):
        self.lease = run_lease
        holder = tempfile.TemporaryDirectory()
        self.addCleanup(holder.cleanup)
        self.root = pathlib.Path(holder.name).resolve()
        self.run_dir = self.root / "go-test.lease.abc"
        (self.run_dir / "tmp" / "mnt").mkdir(parents=True)
        (self.run_dir / ".lease").touch()
        self.mount = str(self.run_dir / "tmp" / "mnt")

    def test_mount_is_detached_before_the_directory_is_removed(self):
        table = [["/", self.mount, "/elsewhere"], ["/", "/elsewhere"]]
        with mock.patch.object(self.lease, "mount_points", side_effect=table), mock.patch.object(
            self.lease, "force_unmount"
        ) as unmount:
            self.lease.prune(self.root)
        unmount.assert_called_once_with(self.mount)
        self.assertFalse(self.run_dir.exists())

    def test_directory_stays_when_the_mount_cannot_be_removed(self):
        with mock.patch.object(self.lease, "mount_points", return_value=[self.mount]), mock.patch.object(
            self.lease, "force_unmount"
        ):
            self.lease.prune(self.root)
        self.assertTrue(self.run_dir.exists(), "must not walk into a mount it could not detach")

    def test_mount_outside_the_run_is_never_touched(self):
        with mock.patch.object(self.lease, "mount_points", return_value=["/", str(self.root) + "/other/mnt"]), mock.patch.object(
            self.lease, "force_unmount"
        ) as unmount:
            self.lease.prune(self.root)
        unmount.assert_not_called()
        self.assertFalse(self.run_dir.exists())

    def test_unmount_failure_to_start_is_reported_not_raised(self):
        with mock.patch.object(self.lease.subprocess, "run", side_effect=OSError("missing helper")):
            self.lease.force_unmount(self.mount)

    def test_mount_table_parses_paths_with_spaces(self):
        listing = mock.Mock(stdout="/dev/disk3 on / (apfs)\n127.0.0.1:/views/a on /tmp/a b/mnt (nfs, nodev)\nnot a mount\n")
        with mock.patch.object(self.lease.sys, "platform", "darwin"), mock.patch.object(
            self.lease.subprocess, "run", return_value=listing
        ):
            self.assertEqual(self.lease.mount_points(), ["/", "/tmp/a b/mnt"])


if __name__ == "__main__":
    unittest.main()
