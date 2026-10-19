"""The release consumer refuses missing, stale or damaged required evidence."""

import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from linux_release.release_admission import main


class LinuxReleaseAdmissionTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory)
        evidence = root / "evidence"
        evidence.mkdir()
        probe = evidence / "probe.json"
        probe.write_text('{"fixture":true}')
        source = "a" * 40
        catalog = {"format_version": 1, "recorded_environment_facts": ["environment"],
                   "architectures": [{"id": "amd64", "nix_system": "x86_64-linux"}],
                   "installations": [{"id": "deb", "package_owner": "dpkg/apt", "vault_mode": "app password", "issues": [457]}],
                   "capabilities": [{"id": "workflow", "outcome": "fixture enforcement", "tier": "native-security", "issues": [457], "cases": ["execution"]}],
                   "environments": [{"id": "fixture", "distribution": "fixture", "release": "1", "architectures": ["amd64"], "installations": ["deb"],
                                     "desktop": "fixture", "display": "Wayland", "session": "fixture", "portal": "fixture", "security": "fixture",
                                     "scaling": "100%", "coverage": "native fixture", "issues": [457]}]}
        files = root / "packages"
        files.mkdir()
        (files / "app").write_bytes(b"final package")
        artifact = {"id": "app", "installation": "deb", "architecture": "amd64", "source_commit": source,
                    "identity": {"kind": "file", "sha256": hashlib.sha256(b"final package").hexdigest()}}
        artifacts = {"format_version": 1, "source_commit": source, "artifacts": [artifact]}
        campaign = {**artifacts, "results": [{"row_id": "fixture/deb/amd64/workflow", "source_commit": source, "artifact_id": "app",
                    "tier": "native-security", "execution": "native", "status": "passed", "environment_facts": {"environment": "fixture only"},
                    "cases": [{"id": "execution", "status": "passed", "evidence": [{"path": probe.name, "sha256": hashlib.sha256(probe.read_bytes()).hexdigest()}]}]}]}
        for name, value in [("catalog", catalog), ("artifacts", artifacts), ("campaign", campaign)]:
            (root / (name + ".json")).write_text(json.dumps(value))
        arguments = ["--catalog", str(root / "catalog.json"), "--source-commit", source,
                     "--final-artifacts", str(root / "artifacts.json"), "--campaign", str(root / "campaign.json"),
                     "--evidence-root", str(evidence), "--file-root", str(files), "--receipt", str(root / "admission.json")]
        return root, campaign, arguments

    def test_complete_fixture_binds_the_separately_selected_input_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, arguments = self.fixture(directory)
            self.assertEqual(main(arguments), 0)
            receipt = json.loads((root / "admission.json").read_text())
            self.assertTrue(receipt["accepted"])
            self.assertEqual(receipt["inputs"]["campaign_sha256"], hashlib.sha256((root / "campaign.json").read_bytes()).hexdigest())

    def test_skipped_missing_or_emulated_required_result_refuses_release(self):
        for mutation in ["skipped", "missing", "emulated"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, campaign, arguments = self.fixture(directory)
                if mutation == "missing":
                    campaign["results"] = []
                elif mutation == "skipped":
                    campaign["results"][0]["cases"][0]["status"] = "skipped"
                else:
                    campaign["results"][0]["execution"] = "emulated"
                (root / "campaign.json").write_text(json.dumps(campaign))
                self.assertEqual(main(arguments), 1)
                self.assertFalse(json.loads((root / "admission.json").read_text())["accepted"])

    def test_changed_retained_evidence_and_stale_artifact_source_refuse_release(self):
        for mutation in ["evidence", "source"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, campaign, arguments = self.fixture(directory)
                if mutation == "evidence":
                    (root / "evidence/probe.json").write_text("tampered")
                else:
                    campaign["artifacts"][0]["source_commit"] = "c" * 40
                    (root / "campaign.json").write_text(json.dumps(campaign))
                self.assertEqual(main(arguments), 1)
                self.assertFalse(json.loads((root / "admission.json").read_text())["accepted"])

    def test_receipt_cannot_overwrite_the_evidence_it_just_admitted(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, arguments = self.fixture(directory)
            arguments[-1] = str(root / "evidence/probe.json")
            with self.assertRaises(SystemExit):
                main(arguments)
            self.assertEqual((root / "evidence/probe.json").read_text(), '{"fixture":true}')

    def test_different_missing_or_symlinked_publication_file_refuses_admission(self):
        for mutation in ("changed", "missing", "symlink"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, _, arguments = self.fixture(directory)
                package = root / "packages/app"
                if mutation == "changed":
                    package.write_bytes(b"different package")
                else:
                    package.unlink()
                    if mutation == "symlink":
                        (root / "outside").write_bytes(b"final package")
                        package.symlink_to(root / "outside")
                self.assertEqual(main(arguments), 1)
                self.assertFalse(json.loads((root / "admission.json").read_text())["accepted"])

    def test_receipt_binds_actual_final_file_bytes_and_cannot_replace_package(self):
        with tempfile.TemporaryDirectory() as directory:
            root, _, arguments = self.fixture(directory)
            self.assertEqual(main(arguments), 0)
            receipt = json.loads((root / "admission.json").read_text())
            self.assertEqual(receipt["verified_files"], [{"id": "app", "sha256": hashlib.sha256(b"final package").hexdigest(), "bytes": 13}])
            arguments[-1] = str(root / "packages/app")
            with self.assertRaises(SystemExit):
                main(arguments)
            self.assertEqual((root / "packages/app").read_bytes(), b"final package")
