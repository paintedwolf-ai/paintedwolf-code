"""Signed fixture preparation preserves exact archives and refuses inconsistent inputs."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import release_upgrade.prepare as preparation
from updater_signature import rehearsal_signature


def manifest(version):
    relative = f"releases/v{version}/painted-wolf-code_v{version}_darwin-aarch64.app.tar.gz"
    return {"version": version, "update_keys": {"signing_generation": 1},
            "platforms": {"darwin-aarch64": {"url": preparation.ORIGIN + "/" + relative,
                                            "signature": rehearsal_signature(version)}}}


class PreparationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.candidate, self.prior = manifest("1.1.0"), manifest("1.0.2")
        (self.source / "VERSION").write_text("1.1.0")
        lock = self.source / "lycaon/internal/db/schema_version_lock.json"
        lock.parent.mkdir(parents=True)
        lock.write_text('{"schema_version": 5}')
        for version in ("1.0.2", "1.1.0"):
            fixture = self.source / "lycaon/testdata/upgrade-corpus" / version
            fixture.mkdir(parents=True)
            (fixture / "MANIFEST.json").write_text(json.dumps({"app_version": version, "schema_version": 5}))
        self.pointer = self.root / "latest.json"
        self.pointer.write_text(json.dumps(self.candidate))
        self.archives = self.root / "archives"
        self.archives.mkdir()
        (self.archives / Path(preparation.archive_path(self.candidate)).name).write_bytes(b"exact candidate")
        self.output = self.root / "prepared"
        self.plan = {"format_version": 1, "cases": [{"id": "1.0.2-preview", "source_version": "1.0.2", "channel": "preview",
                               "source_manifest": self.prior, "steps": [self.candidate]}],
                     "feeds": [{"path": "prior", "channel": "stable", "generation": 1, "manifest": self.prior}], "candidate": self.candidate,
                     "anchors": [{"manifest": self.prior, "verification": "artifact_signature_required"}]}
        self.patches = [patch.object(preparation, "load_registry", return_value={}),
                        patch.object(preparation, "validate_publication"),
                        patch.object(preparation, "capture_sources", side_effect=lambda *_: copy.deepcopy(self.plan)),
                        patch.object(preparation, "generation", return_value={"public_key": "public"}),
                        patch.object(preparation.subprocess, "run"),
                        patch.object(preparation.subprocess, "check_output", return_value="a" * 40),
                        patch.object(preparation, "fetch_archive", side_effect=lambda _, path: path.write_bytes(b"exact prior"))]
        for item in self.patches:
            item.start()
            self.addCleanup(item.stop)

    def prepare(self):
        return preparation.prepare(self.pointer, self.archives, self.source, self.output)

    def test_exact_archives_are_verified_before_offers_are_signed(self):
        events = []
        def sign(pointer, *_):
            events.append("sign")
            path = Path(str(pointer) + ".sig")
            path.write_text("signed fixture")
            return path
        with patch.object(preparation, "verify", side_effect=lambda *_: events.append("verify")), \
             patch.object(preparation, "sign", side_effect=sign):
            result = self.prepare()
        self.assertEqual(events, ["verify", "verify", "sign"])
        self.assertEqual((self.output / preparation.archive_path(self.candidate)).read_bytes(), b"exact candidate")
        self.assertEqual((self.output / "offers/preview/key-1/latest.json.sig").read_text(), "signed fixture")
        self.assertEqual(result["source_sha"], "a" * 40)
        self.assertEqual(result["anchors"][0]["verification"], "artifact_signature_verified")
        inventory = json.loads((self.output / "inventory.json").read_text())
        self.assertEqual(inventory[preparation.archive_path(self.candidate)], result["archives"]["1.1.0"]["archive_sha256"])

    def test_signature_failure_prevents_any_signed_offer(self):
        with patch.object(preparation, "verify", side_effect=ValueError("invalid signature")), \
             patch.object(preparation, "sign") as signer:
            with self.assertRaisesRegex(ValueError, "invalid signature"):
                self.prepare()
            signer.assert_not_called()

    def test_retained_bridge_pointer_and_signature_are_served_byte_for_byte(self):
        bridge = manifest("1.0.3")
        bridge["update_keys"]["embedded_generation"] = 2
        self.candidate["update_keys"]["signing_generation"] = 2
        self.pointer.write_text(json.dumps(self.candidate))
        self.plan["cases"][0]["steps"] = [bridge, self.candidate]
        relative = "updates/preview/key-1/latest.json"
        self.plan["feeds"].append({"channel": "preview", "generation": 1, "path": relative, "manifest": bridge})
        fixture = self.source / "lycaon/testdata/upgrade-corpus/1.0.3"
        fixture.mkdir()
        (fixture / "MANIFEST.json").write_text('{"app_version":"1.0.3","schema_version":5}')
        original = json.dumps(bridge, separators=(",", ":")).encode() + b"\n"
        def capture(_origin, _candidate, destination):
            pointer = destination / relative
            pointer.parent.mkdir(parents=True)
            pointer.write_bytes(original)
            pointer.with_suffix(".json.sig").write_bytes(b"original authenticated bridge signature")
            return copy.deepcopy(self.plan)
        def sign(pointer, *_):
            signature = Path(str(pointer) + ".sig")
            signature.write_text("signed candidate")
            return signature
        with patch.object(preparation, "capture_sources", side_effect=capture), \
             patch.object(preparation, "verify"), patch.object(preparation, "sign", side_effect=sign) as signer:
            self.prepare()
        signer.assert_called_once()
        self.assertEqual(signer.call_args.args[1], 2)
        offered = self.output / "offers/preview/key-1/latest.json"
        self.assertEqual(offered.read_bytes(), original)
        self.assertEqual(offered.with_suffix(".json.sig").read_bytes(), b"original authenticated bridge signature")

    def test_uncovered_baseline_cannot_be_marked_authenticated(self):
        self.plan["anchors"][0]["manifest"] = manifest("1.0.3")
        with patch.object(preparation, "verify"), patch.object(preparation, "sign") as signer:
            with self.assertRaisesRegex(ValueError, "absent from the authenticated archive"):
                self.prepare()
            signer.assert_not_called()

    def test_candidate_version_and_canonical_origin_are_required(self):
        (self.source / "VERSION").write_text("1.1.1")
        with self.assertRaisesRegex(ValueError, "selected source"):
            self.prepare()
        bad = copy.deepcopy(self.candidate)
        bad["platforms"]["darwin-aarch64"]["url"] = "https://example.test/archive.tar.gz"
        with self.assertRaisesRegex(ValueError, "canonical"):
            preparation.archive_path(bad)

    def test_conflicting_artifacts_for_one_version_are_refused(self):
        self.plan["cases"][0]["steps"][0] = copy.deepcopy(self.candidate)
        self.plan["cases"][0]["steps"][0]["platforms"]["darwin-aarch64"]["signature"] = "different"
        with self.assertRaisesRegex(ValueError, "conflicting"):
            self.prepare()

    def test_case_matrix_covers_every_planned_case_and_records_an_empty_scope(self):
        self.assertEqual(preparation.case_matrix({"cases": [{"id": "1.0.2-stable"}, {"id": "1.0.2-preview"}]}),
                         {"include": [{"case_id": "1.0.2-stable"}, {"case_id": "1.0.2-preview"}]})
        self.assertEqual(preparation.case_matrix({"cases": []}), {"include": [{"case_id": "not_applicable"}]})
