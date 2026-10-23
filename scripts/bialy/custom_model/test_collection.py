"""Collection snapshots distinguish SQLite bookkeeping from source changes."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from collect import collect


class CollectionSnapshots(unittest.TestCase):
    def collect_with_reader_wal(self, wal_bytes):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shard = root / "fleet/shards/one"
            captures = shard / "training-captures"
            captures.mkdir(parents=True)
            (shard / "store.db").write_bytes(b"store")
            (captures / "capture.json").write_text(json.dumps({"schema": "pw-training-turn/1"}))

            def open_reader(path):
                path.with_name(path.name + "-wal").write_bytes(wal_bytes)
                raise ValueError("fixture exclusion")

            with patch("collect.open_store", side_effect=open_reader):
                return collect([root / "fleet"], {"epoch": "fixture"}, "catalog", root / "out")

    def test_empty_reader_wal_does_not_change_collection(self):
        counts = self.collect_with_reader_wal(b"")
        self.assertEqual(counts["excluded:fixture exclusion"], 1)

    def test_nonempty_reader_wal_invalidates_collection(self):
        with self.assertRaisesRegex(ValueError, "collection changed"):
            self.collect_with_reader_wal(b"new frames")
