"""A scoped Linux document fixture cannot be reused as production authority."""
import copy
import json
from pathlib import Path
import unittest
from unittest.mock import patch

from scripts.linux_release.publication.distribution_rehearsal import RehearsalReleaseAuthority


class RehearsalDistributionTests(unittest.TestCase):
    def setUp(self):
        self.policy = {"format_version": 1, "mode": "rehearsal", "catalog_sha256": "a" * 64,
                       "updater_manifest_sha256": "b" * 64, "controller_commit": "c" * 40,
                       "origin": "https://fixture.example.invalid", "version": "1.2.3",
                       "notes": "Explicit disposable fixture", "pub_date": "2026-10-20T00:00:00Z"}
        self.document = {key: self.policy[key] for key in
                         ("mode", "controller_commit", "origin", "version", "notes", "pub_date")}
        self.document.update(source={"catalog_sha256": "a" * 64}, updater_manifest_sha256="b" * 64,
                             platforms={"linux-x86_64": {}, "linux-aarch64": {}}, repositories=[],
                             aur={"enabled": False}, nix={"cache": {"enabled": False}})
        self.binding = {"document": self.document, "catalog": Path("catalog"), "updater_manifest": Path("updater")}
        self.updater = {key: self.policy[key] for key in ("version", "notes", "pub_date")}
        self.updater["platforms"] = {}
        self.catalog = {key: {"publication": "public"} for key in self.document["platforms"]}

    def invoke(self):
        def snapshot(path, **_):
            if path == Path("catalog"):
                return {"sha256": "a" * 64}, b"catalog"
            return {"sha256": "b" * 64}, json.dumps(self.updater).encode()
        with patch("scripts.release_distribution_contract.document.snapshot", side_effect=snapshot), \
                patch("scripts.release_distribution_contract.platforms.parse_platforms", return_value=self.catalog):
            return RehearsalReleaseAuthority(self.policy)(self.binding, {}, {"accepted": True})

    def test_explicit_linux_only_source_fixture_is_accepted(self):
        self.assertTrue(self.invoke())

    def test_policy_and_document_cannot_be_promoted_to_production(self):
        self.policy["mode"] = "production"
        with self.assertRaisesRegex(ValueError, "explicit fixture policy"):
            self.invoke()
        self.policy["mode"] = "rehearsal"
        self.document["mode"] = "production"
        with self.assertRaisesRegex(ValueError, "rehearsal selection"):
            self.invoke()

    def test_other_platform_updater_and_delivery_claims_refuse(self):
        baseline = copy.deepcopy(self.document)
        for name, value in (("repositories", [{}]), ("aur", {"enabled": True}),
                            ("nix", {"cache": {"enabled": True}})):
            with self.subTest(name=name):
                self.document[name] = value
                with self.assertRaisesRegex(ValueError, "cannot claim"):
                    self.invoke()
                self.document.clear()
                self.document.update(copy.deepcopy(baseline))
        self.updater["platforms"] = {"darwin-aarch64": {}}
        with self.assertRaisesRegex(ValueError, "only Linux"):
            self.invoke()

    def test_changed_pinned_catalog_is_not_authorized(self):
        self.policy["catalog_sha256"] = "d" * 64
        with self.assertRaisesRegex(ValueError, "metadata changed"):
            self.invoke()
