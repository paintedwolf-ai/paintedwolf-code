"""Campaign upload retains verified inputs, with no reusable receipt authority."""

import json
import io
from contextlib import redirect_stderr
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.publication_producer import copy_inputs, main, produce
from verification_tests.distribution import test_linux_release_admission_execution as fixtures


class PublicationProducerTests(unittest.TestCase):
    def fixture(self, directory):
        source = Path(directory) / "inputs"
        source.mkdir()
        root, _, _ = fixtures.LinuxReleaseAdmissionTests().fixture(source)
        catalog = json.loads((root / "catalog.json").read_text())
        (root / "catalog.json").unlink()
        (root / "artifacts.json").rename(root / "final-artifacts.json")
        immutable = {"source_commit": "a" * 40, "source_tree": "c" * 40,
                     "version": "1.0.0", "channel": "stable", "release_build": 14}
        producer = {"repository_id": 42, "run_id": 123, "run_attempt": 2, "controller_commit": "b" * 40}
        return root, Path(directory) / "retained", immutable, producer, catalog

    def run_producer(self, fixture):
        source, output, immutable, producer, catalog = fixture
        with patch("linux_release.publication_controller.verify_generations"), \
             patch("linux_release.publication_controller.verify_setups", return_value=[]):
            return produce(source, output, immutable, producer, catalog=catalog,
                           signing_generation=1, key_fingerprint="A" * 40)

    def test_complete_copied_fixture_is_reverified_and_bound_to_controller_attempt(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            self.assertTrue(self.run_producer(fixture)["accepted"])
            source, output, immutable, producer, _ = fixture
            self.assertEqual(json.loads((output / "producer.json").read_text()),
                             {"format_version": 1, **producer,
                              "source_commit": immutable["source_commit"], "source_tree": immutable["source_tree"]})
            (source / "packages/app").write_bytes(b"later source mutation")
            self.assertEqual((output / "packages/app").read_bytes(), b"final package")
            self.assertEqual((output / "packages/app").stat().st_mode & 0o777, 0o600)

    def test_incomplete_or_tampered_input_never_leaves_an_uploadable_bundle(self):
        for mutation in ["campaign", "package", "crypto"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                fixture = self.fixture(directory)
                source, output, immutable, producer, catalog = fixture
                if mutation == "campaign":
                    campaign = json.loads((source / "campaign.json").read_text())
                    campaign["results"] = []
                    (source / "campaign.json").write_text(json.dumps(campaign))
                elif mutation == "package":
                    (source / "packages/app").write_bytes(b"changed")
                with patch("linux_release.publication_controller.verify_generations",
                           side_effect=ValueError("bad signature") if mutation == "crypto" else None):
                    with self.assertRaises(ValueError):
                        produce(source, output, immutable, producer, catalog=catalog,
                                signing_generation=1, key_fingerprint="A" * 40)
                self.assertFalse(output.exists())

    def test_links_special_files_and_unexpected_receipts_are_rejected(self):
        for mutation in ["file-link", "directory-link", "fifo", "receipt"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                source, output, *_ = self.fixture(directory)
                if mutation == "file-link":
                    (source / "packages/link").symlink_to("app")
                elif mutation == "directory-link":
                    (source / "evidence/link").symlink_to("../packages", target_is_directory=True)
                elif mutation == "fifo":
                    os.mkfifo(source / "packages/fifo")
                else:
                    (source / "accepted.json").write_text('{"accepted":true}')
                with self.assertRaises((ValueError, OSError)):
                    copy_inputs(source, output)
                self.assertFalse(output.exists())

    def test_existing_nested_and_linked_destination_refusal_preserves_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            source, output, *_ = self.fixture(directory)
            for destination in [source / "nested", source]:
                with self.assertRaises(ValueError):
                    copy_inputs(source, destination)
            output.symlink_to(source, target_is_directory=True)
            with self.assertRaises(ValueError):
                copy_inputs(source, output)
            self.assertTrue((source / "packages/app").is_file())

    def test_entry_and_disk_bounds_clean_up_owned_destination(self):
        for constraint in ["count", "disk"]:
            with self.subTest(constraint=constraint), tempfile.TemporaryDirectory() as directory:
                source, output, *_ = self.fixture(directory)
                target = "linux_release.publication_producer."
                manager = patch(target + "MAX_FILES", 1) if constraint == "count" else patch(
                    target + "shutil.disk_usage", return_value=type("Disk", (), {"free": 0})())
                with manager, self.assertRaises(ValueError):
                    copy_inputs(source, output)
                self.assertFalse(output.exists())

    def test_branch_or_unprotected_workflow_cannot_stamp_production_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            source, output, *_ = self.fixture(directory)
            errors = io.StringIO()
            with redirect_stderr(errors), patch.dict(os.environ, {"GITHUB_EVENT_NAME": "pull_request", "GITHUB_REF": "refs/heads/work"}), \
                 self.assertRaises(SystemExit):
                main(["--inputs", str(source), "--output", str(output),
                      "--source-repository", str(source), "--source-commit", "a" * 40])
            self.assertFalse(output.exists())
            self.assertIn("protected main release controller", errors.getvalue())
