"""Control setup stays separate and uses independent controller trust and selection."""

import json
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

from linux_release.publication.repositories import digest
from linux_release.publication.setups import public_key, verify


class PublicationSetupsTests(unittest.TestCase):
    def fixture(self, root):
        manifest = {"family": "deb", "inventory_sha256": "d" * 64}
        generation = digest(manifest)
        selection = {"repositories": {"generations": [{"manifest": manifest}]},
            "source_commit": "a" * 40, "version": "1.0.0", "channel": "stable",
            "signing_generation": 2, "key_fingerprint": "A" * 40}
        directory = root / "repository-setup" / generation
        directory.mkdir(parents=True)
        filename = "painted-wolf-code-repo_1.0.0+repo.2.stable_all.deb"
        for name in [filename, "SHA256SUMS", "SHA256SUMS.asc"]:
            (directory / name).write_bytes(b"signed fixture handled by native admission")
        trusted = root / "protected-key.asc"
        trusted.write_bytes(b"protected controller key")
        (directory / "package-key.asc").write_bytes(trusted.read_bytes())
        report = {"format_version": 1, "family": "deb", "source_commit": "a" * 40,
                  "repository_generation": generation, "package": {"file": filename}}
        (directory / "setup.json").write_text(json.dumps(report))
        native = types.ModuleType("native")
        native.admit_setup = Mock(return_value=report)
        trust = types.ModuleType("trust")
        trust.selection = Mock(return_value=({}, trusted))
        identity = types.ModuleType("identity")
        identity.package_identity = Mock(return_value={"file": filename})
        modules = {"linux_release.repository_setup_admission": native, "linux_release.openpgp": trust,
                   "linux_release.repository_setup_identity": identity}
        return selection, directory, native, identity, modules

    def test_separate_control_admission_receives_independent_generation_origin_and_signer(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selection, directory, native, identity, modules = self.fixture(root)
            original = json.dumps(selection, sort_keys=True)
            origin = "https://downloads.example.test/linux/repositories"
            with patch.dict(sys.modules, modules):
                reports = verify(root, selection, base_url=origin)
            self.assertEqual(json.dumps(selection, sort_keys=True), original)
            self.assertEqual(reports[0]["package"]["file"], native.admit_setup.return_value["package"]["file"])
            identity.package_identity.assert_called_once_with("1.0.0", "deb", "stable", 2)
            arguments = native.admit_setup.call_args.kwargs
            self.assertEqual(arguments, {"family": "deb", "source_commit": "a" * 40, "version": "1.0.0",
                "channel": "stable", "repository_generation": directory.name, "inventory_sha256": "d" * 64,
                "base_url": origin, "signing_generation": 2, "key_fingerprint": "A" * 40, "rehearsal": False})

    def test_missing_extra_swapped_or_changed_key_bundle_never_reaches_native_admission(self):
        for mutation in ["missing", "extra", "generation", "key", "key-link"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                selection, directory, native, _, modules = self.fixture(root)
                if mutation == "missing":
                    (directory / "SHA256SUMS").unlink()
                elif mutation == "extra":
                    (directory / "accepted.json").write_text('{"accepted":true}')
                elif mutation == "generation":
                    directory.rename(directory.with_name("f" * 64))
                elif mutation == "key":
                    (directory / "package-key.asc").write_bytes(b"downloaded alternate key")
                else:
                    (directory / "package-key.asc").unlink()
                    (directory / "package-key.asc").symlink_to(root / "protected-key.asc")
                with patch.dict(sys.modules, modules), self.assertRaises((ValueError, OSError)):
                    verify(root, selection, base_url="https://downloads.example.test/linux/repositories")
                native.admit_setup.assert_not_called()

    def test_producer_receipt_does_not_replace_native_payload_admission(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selection, _, native, _, modules = self.fixture(root)
            native.admit_setup.side_effect = ValueError("native payload refused")
            with patch.dict(sys.modules, modules), self.assertRaisesRegex(ValueError, "native payload"):
                verify(root, selection, base_url="https://downloads.example.test/linux/repositories")

    def test_schema_boolean_and_changed_producer_record_cannot_equal_fresh_native_report(self):
        for changed in [True, 2]:
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                selection, directory, _, _, modules = self.fixture(root)
                record = json.loads((directory / "setup.json").read_text())
                record["format_version"] = changed
                (directory / "setup.json").write_text(json.dumps(record))
                with patch.dict(sys.modules, modules), self.assertRaises(ValueError):
                    verify(root, selection, base_url="https://downloads.example.test/linux/repositories")

    def test_production_origin_is_required_and_key_special_file_cannot_block(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selection, directory, *_ = self.fixture(root)
            with self.assertRaises(ValueError):
                verify(root, selection, base_url=None)
            path = directory / "fifo"
            os.mkfifo(path)
            with self.assertRaises(ValueError):
                public_key(path)
