"""Collection snapshots distinguish SQLite bookkeeping from source changes."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from collect import FACT_SCHEMA, collect
from common import digest, file_digest, read_rows


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

    def test_declared_peer_capture_is_not_a_failed_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shard = root / 'fleet/shards/one'
            captures = shard / 'training-captures'
            captures.mkdir(parents=True)
            (shard / 'store.db').write_bytes(b'store')
            peer = {'provider': 'student-12b', 'kind': 'openai-compatible', 'model': '12b'}
            record = {'schema': 'pw-training-turn/1', **{'student_' + key: value for key, value in peer.items()}}
            (captures / 'peer.json').write_text(json.dumps(record))
            identity = {'epoch': 'fixture', 'student_provider': 'student-e4b',
                        'student_kind': 'openai-compatible', 'student_model': 'e4b', 'students': {'12b': peer}}
            with patch('collect.open_store') as opened:
                counts = collect([root / 'fleet'], identity, 'catalog', root / 'out')
            self.assertEqual(counts['other_student'], 1)
            opened.assert_not_called()


class StoreStub:
    """Stands in for the read-only SQLite handle that accepted_row consults."""

    def execute(self, *_arguments):
        return None

    def close(self):
        return None


class AcceptedCandidateExport(unittest.TestCase):
    identity = {"epoch": "fixture", "student_provider": "student-e4b",
                "student_kind": "openai-compatible", "student_model": "e4b"}

    def fleet(self, root, files):
        shard = root / "fleet/shards/one"
        captures = shard / "training-captures"
        captures.mkdir(parents=True)
        (shard / "store.db").write_bytes(b"store")
        for name, value in files.items():
            (captures / name).write_text(json.dumps(value))
        return root / "fleet", shard

    def test_accepted_rows_bind_sources_quality_and_commits_once_per_call(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            record = {"schema": "pw-training-turn/1"}
            committed = {"schema": FACT_SCHEMA, "epoch": "fixture", "kind": "turn_committed",
                         "session_id": "s1", "message_id": "m1", "decided_at": "2026-10-28T00:00:00Z"}
            foreign = dict(committed, epoch="other")
            fleet, shard = self.fleet(root, {"a.json": record, "b.json": record,
                                             "committed.json": committed, "foreign.json": foreign})
            quality = [{"request_sha256": "req", "response_sha256": "res", "score": 1}]
            commits_seen = []

            def accepted(_record, _db, _identity, _catalog, commits):
                commits_seen.append(dict(commits))
                return {"session": "s1", "request_capture": {"request_id": "call-1"},
                        "validation": {"request_sha256": "req", "response_sha256": "res",
                                       "acceptance": "turn_committed"}}

            with patch("collect.open_store", return_value=StoreStub()), \
                    patch("collect.accepted_row", side_effect=accepted):
                counts = collect([fleet], self.identity, "catalog", root / "out", quality)
            self.assertEqual(counts, {"turn_commits": 1, "accepted_candidates": 1,
                                      "accepted_by:turn_committed": 1,
                                      "excluded:duplicate captured call": 1})
            self.assertEqual(commits_seen, [{("s1", "m1"): "2026-10-28T00:00:00Z"}] * 2)
            rows = read_rows(root / "out/sft.receipts.jsonl")
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["validation"]["quality"], quality[0])
            self.assertEqual(rows[0]["source"], {
                "capture_sha256": file_digest(shard / "training-captures/a.json"),
                "store_sha256": file_digest(shard / "store.db")})
            manifest = json.loads((root / "out/manifest.json").read_text())
            self.assertEqual(manifest["counts"], counts)
            self.assertEqual(manifest["quality_sha256"], digest(quality))
            self.assertEqual(manifest["rows_sha256"], file_digest(root / "out/sft.receipts.jsonl"))

    def test_duplicate_quality_binding_is_refused_before_any_capture_is_read(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = {"request_sha256": "req", "response_sha256": "res"}
            with patch("collect.open_store") as opened:
                with self.assertRaisesRegex(ValueError, "duplicate quality binding"):
                    collect([], self.identity, "catalog", root / "out", [binding, dict(binding)])
            opened.assert_not_called()

    def test_command_line_prints_the_collection_counts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fleet, _shard = self.fleet(root, {})
            binding = root / "binding.json"
            binding.write_text(json.dumps(self.identity))
            quality = root / "quality.jsonl"
            quality.write_text(json.dumps({"request_sha256": "req", "response_sha256": "res"}) + "\n")
            script = Path(__file__).with_name("collect.py")
            completed = subprocess.run(
                [sys.executable, str(script), "--fleet", str(fleet), "--binding", str(binding),
                 "--catalog-sha256", "catalog", "--out", str(root / "out"), "--quality", str(quality)],
                check=True, capture_output=True, text=True, cwd=script.parent)
            self.assertEqual(json.loads(completed.stdout), {"turn_commits": 0})
            manifest = json.loads((root / "out/manifest.json").read_text())
            self.assertEqual(manifest["identity"], self.identity)
            self.assertEqual(manifest["quality_sha256"], digest([{"request_sha256": "req", "response_sha256": "res"}]))
