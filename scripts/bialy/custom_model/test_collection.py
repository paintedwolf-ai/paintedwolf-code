"""Collection reads verified bundles and writes one contract-checked candidate per capture."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from bundle import Bundle
from collect import collect
from common import digest, file_digest, read_rows
from test_bundle import write_bundle


def capture(**fields):
    return {"schema": "pw-training-turn/1", "epoch": "fixture", "session_id": "session", "message_id": "message",
            "output_id": "output", "call_id": "call", "dispatches": 1, "settled": True, "student_provider": "student-e4b",
            "student_kind": "openai-compatible", "student_model": "e4b", **fields}


class CandidateExport(unittest.TestCase):
    identity = {"epoch": "fixture", "student_provider": "student-e4b", "student_kind": "openai-compatible", "student_model": "e4b"}

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def test_every_capture_becomes_one_bound_candidate_or_a_counted_reason(self):
        bundle = write_bundle(self.root / "one", captures=[
            capture(call_id="a", exclusion_reason="host_modified_response"), capture(call_id="b", exclusion_reason="ambiguous_dispatch"),
            capture(call_id="b", exclusion_reason="ambiguous_dispatch"), capture(call_id="c", epoch="other")])
        counts = collect([bundle], self.identity, "catalog", self.root / "out")
        self.assertEqual(counts, {"bundles": 1, "host_excluded": 2, "unbuildable:duplicate captured call": 1,
                                  "unbuildable:foreign collection epoch": 1})
        rows = read_rows(self.root / "out/candidates.jsonl")
        opened = Bundle(bundle)
        self.assertEqual([row["host_exclusion"] for row in rows], ["host_modified_response", "ambiguous_dispatch"])
        self.assertEqual(rows[0]["source"], {"bundle_sha256": opened.manifest_sha256, "capture_sha256": digest(opened.captures[0])})
        manifest = json.loads((self.root / "out/manifest.json").read_text())
        self.assertEqual((manifest["schema"], manifest["counts"]), ("pw-candidate-export/1", counts))
        self.assertEqual(manifest["bundles"], {str(bundle): opened.manifest_sha256})
        self.assertEqual(manifest["candidates_sha256"], file_digest(self.root / "out/candidates.jsonl"))

    def test_a_candidate_that_breaks_its_contract_is_not_written(self):
        bundle = write_bundle(self.root / "one", captures=[capture()])
        with patch("collect.candidate", return_value={"schema": "pw-training-candidate/1", "session": "s", "message_id": "m", "call_id": "c"}):
            counts = collect([bundle], self.identity, "catalog", self.root / "out")
        self.assertEqual(counts, {"bundles": 1, "unbuildable:training-candidate contract violated": 1})

    def test_declared_peer_capture_is_not_a_failed_candidate(self):
        peer = {"provider": "student-12b", "kind": "openai-compatible", "model": "12b"}
        bundle = write_bundle(self.root / "one", captures=[capture(student_provider="student-12b", student_model="12b")])
        with patch("collect.candidate") as built:
            counts = collect([bundle], dict(self.identity, students={"12b": peer}), "catalog", self.root / "out")
        self.assertEqual(counts["other_student"], 1)
        built.assert_not_called()

    def test_a_bundle_changed_during_collection_invalidates_it(self):
        bundle = write_bundle(self.root / "one", captures=[capture()])

        def change(*_arguments):
            manifest = json.loads((bundle / "manifest.json").read_text())
            (bundle / "manifest.json").write_text(json.dumps(dict(manifest, store_sha256="9" * 64)))
            raise ValueError("fixture exclusion")

        with patch("collect.candidate", side_effect=change):
            with self.assertRaisesRegex(ValueError, "a bundle changed"):
                collect([bundle], self.identity, "catalog", self.root / "out")

    def test_command_line_prints_the_collection_counts(self):
        bundle = write_bundle(self.root / "one")
        binding = self.root / "binding.json"
        binding.write_text(json.dumps(self.identity))
        script = Path(__file__).with_name("collect.py")
        completed = subprocess.run([sys.executable, str(script), "--bundle", str(bundle), "--binding", str(binding),
                                    "--catalog-sha256", "catalog", "--out", str(self.root / "out")],
                                   check=True, capture_output=True, text=True, cwd=script.parent)
        self.assertEqual(json.loads(completed.stdout), {"bundles": 1})
        self.assertEqual(json.loads((self.root / "out/manifest.json").read_text())["identity"], self.identity)


if __name__ == "__main__":
    unittest.main()
