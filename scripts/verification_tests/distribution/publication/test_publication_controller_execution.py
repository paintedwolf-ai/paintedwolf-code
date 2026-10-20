"""Protected publication reevaluates the retained campaign and actual package bytes."""

import json
import os
import sys
import types
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release.publication.controller import SelectedCampaign, main, retain
from verification_tests.distribution import test_linux_release_admission_execution as fixtures


class PublicationControllerTests(unittest.TestCase):
    def fixture(self, directory):
        crypto = patch("linux_release.publication.controller.verify_generations")
        crypto.start()
        setups = patch("linux_release.publication.controller.verify_setups", return_value=[])
        setups.start()
        self.addCleanup(setups.stop)
        self.addCleanup(crypto.stop)
        root, _, _ = fixtures.LinuxReleaseAdmissionTests().fixture(directory)
        (root / "final-artifacts.json").write_bytes((root / "artifacts.json").read_bytes())
        provenance = {"repository_id": 42, "run_id": 123, "run_attempt": 2,
                      "controller_commit": "b" * 40}
        source = {"source_commit": "a" * 40, "source_tree": "c" * 40, "version": "1.0.0",
                  "channel": "stable", "release_build": 14}
        (root / "producer.json").write_text(json.dumps({"format_version": 1, **provenance,
            "source_commit": source["source_commit"], "source_tree": source["source_tree"]}))
        selected = SelectedCampaign(root, provenance, source, json.loads((root / "catalog.json").read_text()), 1, "A" * 40)
        return root, selected

    def test_accepted_receipt_never_substitutes_for_actual_files_or_campaign(self):
        for mutation in ["package", "campaign"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, selected = self.fixture(directory)
                _, report = selected.authorize()
                self.assertTrue(report["accepted"])
                (root / "accepted.json").write_text(json.dumps(report))
                if mutation == "package":
                    (root / "packages/app").write_bytes(b"changed")
                else:
                    campaign = json.loads((root / "campaign.json").read_text())
                    campaign["results"] = []
                    (root / "campaign.json").write_text(json.dumps(campaign))
                with self.assertRaises(ValueError):
                    selected.authorize()

    def test_producer_and_immutable_source_lock_mismatch_refuse_before_publication(self):
        for mutation in ["attempt", "lock"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root, selected = self.fixture(directory)
                if mutation == "attempt":
                    producer = json.loads((root / "producer.json").read_text())
                    producer["run_attempt"] = 1
                    (root / "producer.json").write_text(json.dumps(producer))
                else:
                    manifest = json.loads((root / "final-artifacts.json").read_text())
                    manifest["artifacts"].append({"id": "nix", "installation": "nix-profile", "architecture": "amd64",
                        "source_commit": "a" * 40, "identity": {"kind": "nix-closure", "lock_sha256": "d" * 64,
                        "store_path": "/nix/store/" + "a" * 32 + "-app", "sha256": "e" * 64}})
                    (root / "final-artifacts.json").write_text(json.dumps(manifest))
                with self.assertRaises(ValueError):
                    selected.authorize()

    def test_refused_server_provenance_never_downloads_campaign_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch("linux_release.publication.controller.facts", return_value={}), \
                 patch("linux_release.publication.controller.select_final", side_effect=ValueError("untrusted workflow")), \
                 patch("linux_release.publication.controller.download") as download:
                with self.assertRaisesRegex(ValueError, "untrusted workflow"):
                    retain(None, "owner/repo", 42, 123, Path(directory), "a" * 40,
                           Path(directory) / "out", catalog={}, signing_generation=1, key_fingerprint="A" * 40)
                download.assert_not_called()

    def test_format_signature_refusal_cannot_be_replaced_with_accepted_campaign(self):
        with tempfile.TemporaryDirectory() as directory:
            root, selected = self.fixture(directory)
            with patch("linux_release.publication.controller.verify_generations",
                       side_effect=ValueError("invalid repository signature")) as crypto:
                with self.assertRaisesRegex(ValueError, "repository signature"):
                    selected.authorize()
                crypto.assert_called_once_with(root, json.loads((root / "repositories.json").read_text()))

    def test_managed_controller_checks_actual_publication_files_and_retains_a_fresh_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            root, selected = self.fixture(directory)
            source = root / "source"
            source.mkdir()
            public = root / "current"
            public.mkdir()
            (public / "app").write_bytes(b"final package")
            key = types.ModuleType("openpgp")
            key.selection = Mock(return_value=({"generation": 1, "fingerprint": "A" * 40}, b"public"))
            arguments = ["--campaign-run-id", "123", "--source-commit", "a" * 40,
                "--source-repository", str(source), "--retained-dir", str(root / "retained"),
                "--publication-files", str(public), "--receipt", str(root / "receipt.json")]
            with patch.dict(sys.modules, {"scripts.linux_release.openpgp": key}), \
                 patch.dict(os.environ, {"GITHUB_REPOSITORY": "owner/repo", "GITHUB_REPOSITORY_ID": "42",
                    "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main",
                    "GITHUB_WORKFLOW_REF": "owner/repo/.github/workflows/release.yml@refs/heads/main",
                    "GITHUB_SHA": "b" * 40, "GITHUB_WORKFLOW_SHA": "b" * 40,
                    "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2",
                    "LINUX_REPOSITORY_ORIGIN": "https://downloads.example.test"}), \
                 patch("linux_release.publication.controller.CATALOG", root / "catalog.json"), \
                 patch("linux_release.publication.controller.retain", return_value=selected):
                self.assertEqual(main(arguments), 0)
                receipt = json.loads((root / "receipt.json").read_text())
                self.assertEqual(receipt["publication_files"][0]["id"], "app")
                with self.assertRaises(SystemExit):
                    main(arguments)
                (public / "app").write_bytes(b"rebuilt different bytes")
                with self.assertRaises(SystemExit):
                    main([*arguments[:-1], str(root / "refused.json")])
                self.assertFalse((root / "refused.json").exists())
