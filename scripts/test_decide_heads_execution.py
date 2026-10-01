"""Release staging must never silently substitute missing or different heads."""
import hashlib
import importlib.util
import json
from pathlib import Path
import struct
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("stage_heads", Path(__file__).with_name("stage-decide-heads.py"))
staging = importlib.util.module_from_spec(spec)
spec.loader.exec_module(staging)


class DecisionReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.destination = self.root / "engine" / "heads"
        self.destination.mkdir(parents=True)
        (self.destination / "prior-release").write_text("preserve on failure")
        self.manifest = self.root / "release.json"
        self.release = {"version": 1, "release": "test", "backbone": {"model": "test-model"}, "heads": {}, "preload": {"catalog_revision": "test-corpus", "encoding": "joint", "max_len": 1024, "head_tokens": 512, "options": {"command": "Run a command"}}}
        for name in sorted(staging.REQUIRED):
            header = json.dumps({"__metadata__": {"format": "pw-decide-head/1", "model": "test-model", "label": name, "corpus": "test-corpus", "max_len": "1024", "head_max_len": "512"}}).encode()
            data = struct.pack("<Q", len(header)) + header
            (self.source / f"{name}.safetensors").write_bytes(data)
            self.release["heads"][name] = {"sha256": hashlib.sha256(data).hexdigest(), "label": name}
        self.save()

    def save(self):
        self.manifest.write_text(json.dumps(self.release))

    def test_complete_release_replaces_old_stage_and_records_manifest(self):
        staging.stage(self.manifest, self.source, self.destination)
        self.assertFalse((self.destination / "prior-release").exists())
        self.assertEqual(json.loads((self.destination / "release.json").read_text()), self.release)
        self.assertEqual(len(list(self.destination.glob("*.safetensors"))), 3)

    def test_partial_cache_is_rejected_without_replacing_stage(self):
        (self.source / "unit-rank.safetensors").unlink()
        with self.assertRaisesRegex(ValueError, "required head missing"):
            staging.stage(self.manifest, self.source, self.destination)
        self.assertTrue((self.destination / "prior-release").exists())

    def test_modified_bytes_are_rejected_without_replacing_stage(self):
        with (self.source / "code-rank.safetensors").open("ab") as output:
            output.write(b"different weights")
        with self.assertRaisesRegex(ValueError, "does not match release"):
            staging.stage(self.manifest, self.source, self.destination)
        self.assertTrue((self.destination / "prior-release").exists())

    def test_empty_manifest_cannot_fall_back_to_the_backbone_head(self):
        self.release["heads"] = {}
        self.save()
        with self.assertRaisesRegex(ValueError, "must pin"):
            staging.stage(self.manifest, self.source, self.destination)

    def test_matching_hash_does_not_override_backbone_or_label_contract(self):
        for key, value in (("model", "different-model"), ("label", "different-label")):
            with self.subTest(key=key):
                if key == "model":
                    self.release["backbone"][key] = value
                else:
                    self.release["backbone"]["model"] = "test-model"
                    self.release["heads"]["turn-load"][key] = value
                self.save()
                with self.assertRaises(ValueError):
                    staging.stage(self.manifest, self.source, self.destination)


if __name__ == "__main__":
    unittest.main()
