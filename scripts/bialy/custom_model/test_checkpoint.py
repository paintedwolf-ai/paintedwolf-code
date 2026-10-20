"""Crash recovery contracts for checkpoint publication and cache corruption."""
from pathlib import Path
import tempfile
import unittest

from checkpoint import Checkpoints, ReferenceCache, exclusive_run


class Recovery(unittest.TestCase):
    def test_partial_and_corrupt_latest_fall_back(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            store = Checkpoints(root / "checkpoints", "identity")
            first = store.save({"cursor":{"step":1},"value":1})
            latest = store.save({"cursor":{"step":2},"value":2})
            (root / "checkpoints/.pending-crashed").mkdir()
            (latest / "state.pt").write_bytes(b"interrupted")
            restored, rejected = store.restore()
            self.assertEqual(restored["value"],1)
            self.assertEqual(len(rejected),1)
            self.assertTrue(first.exists())
            with self.assertRaisesRegex(ValueError,"no intact"):
                Checkpoints(root / "checkpoints", "other").restore()

    def test_lock_and_retention_preserve_intact_generations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "run"
            with exclusive_run(root, False):
                with self.assertRaisesRegex(ValueError, "another process"):
                    with exclusive_run(root, True):
                        self.fail("second writer admitted")
            with exclusive_run(root, True):
                store = Checkpoints(root / "checkpoints", "identity", keep=2)
                intact = store.save({"cursor":{"step":1}})
                for step in (2, 3):
                    broken = store.save({"cursor":{"step":step}})
                    (broken / "state.pt").write_bytes(b"corrupt")
                store.save({"cursor":{"step":4}})
                self.assertTrue(intact.exists())

    def test_reference_scores_survive_reopen_and_detect_corruption(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "cache.sqlite"
            cache = ReferenceCache(path,"identity")
            cache.put("sample",-4.)
            cache.close()
            cache = ReferenceCache(path,"identity")
            self.assertEqual(cache.get("sample"),-4.)
            with cache.db:
                cache.db.execute("UPDATE scores SET value=-5")
            self.assertIsNone(cache.get("sample"))
            cache.put("sample",-4.)
            cache.close()
            other = ReferenceCache(path,"other")
            self.assertIsNone(other.get("sample"))
            other.close()


if __name__ == "__main__":
    unittest.main()
