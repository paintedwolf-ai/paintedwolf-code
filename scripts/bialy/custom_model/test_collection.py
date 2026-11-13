"""Collection reads verified bundles and writes contract-checked rows once per call."""
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

ROW = {"schema": "pw-custom-sft/1", "session": "s1", "message_id": "m1",
       "prompt": [{"role": "system", "content": "Be precise"}],
       "target": {"role": "assistant", "text": "Done.", "tool_calls": []},
       "request_capture": {"schema": "pw-model-request-capture/3", "request_id": "call-1", "session_id": "s1",
                           "message_id": "m1", "host_revision": "a" * 40, "epoch": "fixture", "contract_sha256": "d" * 64,
                           "catalog_sha256": "c" * 64, "student_provider": "student-e4b", "student_kind": "openai-compatible",
                           "student_model": "e4b", "dispatch_sha256": "e" * 64, "state_sha256": "f" * 64,
                           "messages": [{"role": "system", "content": "Be precise"}], "tools": [], "controls": {},
                           "sha256": "1" * 64},
       "validation": {"schema": "pw-training-validation/1", "validator": "paintedwolf-host-training-validation/1",
                      "receipt_id": "o1", "operation_ids": [], "request_sha256": "req", "response_sha256": "res",
                      "kind": "accepted_response", "outcome": "accepted", "acceptance": "turn_committed",
                      "contract_sha256": "d" * 64}}


def capture(**fields):
    return {"schema": "pw-training-turn/1", "epoch": "fixture", "session_id": "session", "message_id": "message",
            "output_id": "output", "call_id": "call", "dispatches": 1, "settled": True, "student_provider": "student-e4b",
            "student_kind": "openai-compatible", "student_model": "e4b", **fields}


class AcceptedCandidateExport(unittest.TestCase):
    identity = {"epoch": "fixture", "student_provider": "student-e4b",
                "student_kind": "openai-compatible", "student_model": "e4b"}

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def test_accepted_rows_bind_their_bundle_quality_and_capture_once_per_call(self):
        bundle = write_bundle(self.root / "one", captures=[capture(call_id="a"), capture(call_id="b")])
        quality = [{"request_sha256": "req", "response_sha256": "res", "score": 1}]
        seen = []

        def accepted(record, item, identity, catalog):
            seen.append((record["call_id"], item.manifest_sha256, catalog))
            return json.loads(json.dumps(ROW))

        with patch("collect.accepted_row", side_effect=accepted):
            counts = collect([bundle], self.identity, "catalog", self.root / "out", quality)
        self.assertEqual(counts, {"bundles": 1, "accepted_candidates": 1, "accepted_by:turn_committed": 1,
                                  "excluded:duplicate captured call": 1})
        manifest_sha256 = Bundle(bundle).manifest_sha256
        self.assertEqual(seen, [("a", manifest_sha256, "catalog"), ("b", manifest_sha256, "catalog")])
        rows = read_rows(self.root / "out/sft.receipts.jsonl")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["validation"]["quality"], quality[0])
        self.assertEqual(rows[0]["source"], {"bundle_sha256": manifest_sha256,
                                             "capture_sha256": digest(Bundle(bundle).captures[0])})
        manifest = json.loads((self.root / "out/manifest.json").read_text())
        self.assertEqual(manifest["counts"], counts)
        self.assertEqual(manifest["bundles"], {str(bundle): manifest_sha256})
        self.assertEqual(manifest["rows_sha256"], file_digest(self.root / "out/sft.receipts.jsonl"))

    def test_a_row_that_breaks_the_sft_contract_is_excluded(self):
        bundle = write_bundle(self.root / "one", captures=[capture()])
        broken = json.loads(json.dumps(ROW))
        del broken["validation"]["contract_sha256"]
        with patch("collect.accepted_row", return_value=broken):
            counts = collect([bundle], self.identity, "catalog", self.root / "out")
        self.assertEqual(counts, {"bundles": 1, "excluded:sft-row contract violated": 1})

    def test_declared_peer_capture_is_not_a_failed_candidate(self):
        peer = {"provider": "student-12b", "kind": "openai-compatible", "model": "12b"}
        bundle = write_bundle(self.root / "one", captures=[capture(student_provider="student-12b", student_model="12b")])
        identity = dict(self.identity, students={"12b": peer})
        with patch("collect.accepted_row") as accepted:
            counts = collect([bundle], identity, "catalog", self.root / "out")
        self.assertEqual(counts["other_student"], 1)
        accepted.assert_not_called()

    def test_a_bundle_changed_during_collection_invalidates_it(self):
        bundle = write_bundle(self.root / "one", captures=[capture()])

        def change(*_arguments):
            manifest = json.loads((bundle / "manifest.json").read_text())
            manifest["store_sha256"] = "9" * 64
            (bundle / "manifest.json").write_text(json.dumps(manifest))
            raise ValueError("fixture exclusion")

        with patch("collect.accepted_row", side_effect=change):
            with self.assertRaisesRegex(ValueError, "a bundle changed"):
                collect([bundle], self.identity, "catalog", self.root / "out")

    def test_duplicate_quality_binding_is_refused_before_any_bundle_is_read(self):
        binding = {"request_sha256": "req", "response_sha256": "res"}
        with patch("collect.Bundle") as opened:
            with self.assertRaisesRegex(ValueError, "duplicate quality binding"):
                collect([self.root / "missing"], self.identity, "catalog", self.root / "out", [binding, dict(binding)])
        opened.assert_not_called()

    def test_command_line_prints_the_collection_counts(self):
        bundle = write_bundle(self.root / "one")
        binding = self.root / "binding.json"
        binding.write_text(json.dumps(self.identity))
        quality = self.root / "quality.jsonl"
        quality.write_text(json.dumps({"request_sha256": "req", "response_sha256": "res"}) + "\n")
        script = Path(__file__).with_name("collect.py")
        completed = subprocess.run(
            [sys.executable, str(script), "--bundle", str(bundle), "--binding", str(binding),
             "--catalog-sha256", "catalog", "--out", str(self.root / "out"), "--quality", str(quality)],
            check=True, capture_output=True, text=True, cwd=script.parent)
        self.assertEqual(json.loads(completed.stdout), {"bundles": 1})
        manifest = json.loads((self.root / "out/manifest.json").read_text())
        self.assertEqual(manifest["identity"], self.identity)
        self.assertEqual(manifest["quality_sha256"], digest([{"request_sha256": "req", "response_sha256": "res"}]))


if __name__ == "__main__":
    unittest.main()
