"""Website acknowledgement binds signed distribution metadata and every package link."""
import hashlib
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch
from scripts.linux_release.publication import website
from scripts.release_distribution_contract.document_schema import canonical
ROOT = Path(__file__).resolve().parents[4]
SPEC = importlib.util.spec_from_file_location("publication_website_distribution", ROOT / "scripts/release_distribution.py")
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)

class WebsiteDistributionTests(unittest.TestCase):
    def fixture(self):
        document = {"mode": "production", "version": "1.2.3", "updater_manifest_sha256": "a" * 64,
                    "signing_generation": 1, "platforms": {"linux-x86_64": {"packages": {
                        "deb": {"url": "https://downloads.paintedwolf.dev/releases/v1.2.3/app.deb"}}},
                        "darwin-aarch64": {"packages": {"dmg": {"url": "https://downloads.paintedwolf.dev/releases/v1.2.3/app.dmg"}}}}}
        raw = canonical(document)
        receipt = {"channel": "stable", "version": "1.2.3", "manifest_sha256": "a" * 64,
                   "distribution_sha256": hashlib.sha256(raw).hexdigest(), "withdrawn_version": None}
        return document, raw, receipt

    def test_receipt_requires_both_independent_digests_and_schema2(self):
        _, _, expected = self.fixture()
        release.validate_website_receipt({"schema_version": 2, **expected}, expected)
        for changes in ({"schema_version": 1}, {"distribution_sha256": "b" * 64}, {"manifest_sha256": "b" * 64}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                release.validate_website_receipt({"schema_version": 2, **expected, **changes}, expected)

    def test_every_package_link_comes_from_verified_signed_document(self):
        document, raw, receipt = self.fixture()
        with patch.object(website, "read", side_effect=[raw, b"signature"]), patch.object(website, "validate"), patch.object(website, "check"), patch.object(website, "verify") as verify, patch.object(website, "generation", return_value={"feed_public_key": "maintained"}), patch.object(website, "load_registry", return_value={}):
            urls = website.public_downloads(receipt)
        verify.assert_called_once()
        html = "".join('<a data-release-channel="stable" download href="' + url + '"></a>' for url in urls)
        release.validate_website_downloads(html, receipt, urls)
        with self.assertRaisesRegex(ValueError, "download links"):
            release.validate_website_downloads(html + '<a data-release-channel="stable" download href="https://bad.invalid/app"></a>', receipt, urls)
        receipt["distribution_sha256"] = "c" * 64
        with patch.object(website, "read", side_effect=[raw, b"signature"]), self.assertRaisesRegex(ValueError, "independently selected"):
            website.public_downloads(receipt)

    def test_all_withdrawn_state_has_no_artifacts_or_lingering_digest(self):
        receipt = {"version": None, "distribution_sha256": None, "manifest_sha256": None}
        self.assertEqual(website.public_downloads(receipt), [])
        receipt["distribution_sha256"] = "a" * 64
        with self.assertRaisesRegex(ValueError, "withdrawn"):
            website.public_downloads(receipt)
