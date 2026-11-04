import importlib.util
from pathlib import Path
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
        self.root = Path(temporary.name)

    def run_dir(self, name):
        run = self.root / name
        (run / "tmp" / "mounts" / "primary").mkdir(parents=True)
        (run / ".lease").write_text("")
        return run

    def test_prune_removes_unleased_runs(self):
        run = self.run_dir("go-test.lease.a")
        self.lease.prune(self.root)
        self.assertFalse(run.exists())

    def test_a_hung_mount_keeps_only_its_own_run(self):
        hung = self.run_dir("go-test.lease.hung")
        other = self.run_dir("go-test.lease.other")
        real_lstat = Path.lstat

        def lstat(path):
            if path.name == "primary" and hung in path.parents:
                raise TimeoutError(60, "Operation timed out")
            return real_lstat(path)

        with mock.patch.object(Path, "lstat", lstat):
            self.lease.prune(self.root)
        self.assertTrue(hung.exists())
        self.assertFalse(other.exists())

    def test_a_filesystem_boundary_keeps_its_run(self):
        run = self.run_dir("go-test.lease.mounted")
        real_lstat = Path.lstat

        class OtherDevice:
            def __init__(self, info):
                self.st_dev = info.st_dev + 1
                self.st_mode = info.st_mode

        def lstat(path):
            info = real_lstat(path)
            return OtherDevice(info) if path.name == "primary" else info

        with mock.patch.object(Path, "lstat", lstat):
            self.lease.prune(self.root)
        self.assertTrue(run.exists())


if __name__ == "__main__":
    unittest.main()
