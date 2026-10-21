"""Website acknowledgement binds signed distribution metadata and every package link."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

from scripts.linux_release.publication.document import website
from verification_tests.distribution.publication import signed_release as fixture

ROOT = Path(__file__).resolve().parents[4]
SPEC = importlib.util.spec_from_file_location("publication_website_distribution", ROOT / "scripts/release_distribution.py")
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)
SITE = "https://website.invalid"


def receipt(channel="stable", **changes):
    return {"channel": channel, "version": "1.2.3", "manifest_sha256": fixture.manifest_sha256(),
            "distribution_sha256": fixture.distribution_sha256(), "withdrawn_version": None, **changes}


def links(channel, urls):
    return "".join(f'<a data-release-channel="{channel}" download href="{url}"></a>' for url in urls).encode()


def package_urls():
    document = json.loads((fixture.FIXTURE / fixture.DOCUMENT).read_text())
    return sorted(package["url"] for row in document["platforms"].values() for package in row["packages"].values())


def acknowledged(*channels):
    pages = fixture.downloads()
    for channel in channels:
        pages[f"{SITE}/.well-known/releases/{channel}.json"] = json.dumps({"schema_version": 2, **receipt(channel)}).encode()
        pages[SITE + ("/download/" if channel == "stable" else "/download/preview/")] = links(channel, package_urls())
    return pages


class WebsiteDistributionTests(unittest.TestCase):
    def setUp(self):
        signer = fixture.minisign()
        signer.__enter__()
        self.addCleanup(signer.__exit__, None, None, None)

    def test_receipt_requires_both_independent_digests_and_schema2(self):
        expected = receipt()
        release.validate_website_receipt({"schema_version": 2, **expected}, expected)
        for changes in ({"schema_version": 1}, {"distribution_sha256": "b" * 64}, {"manifest_sha256": "b" * 64}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                release.validate_website_receipt({"schema_version": 2, **expected, **changes}, expected)

    def test_every_package_link_comes_from_the_committed_signed_document(self):
        with fixture.web(fixture.downloads()):
            urls = website.public_downloads(receipt())
        self.assertEqual(urls, package_urls())
        html = links("stable", urls).decode()
        release.validate_website_downloads(html, receipt(), urls)
        with self.assertRaisesRegex(ValueError, "download links"):
            release.validate_website_downloads(html + links("stable", ["https://bad.invalid/app"]).decode(), receipt(), urls)

    def test_public_document_must_match_the_selected_digests_and_signature(self):
        with fixture.web(fixture.downloads()):
            with self.assertRaisesRegex(ValueError, "independently selected"):
                website.public_downloads(receipt(distribution_sha256="c" * 64))
            with self.assertRaisesRegex(ValueError, "differs from the selected release"):
                website.public_downloads(receipt(manifest_sha256="c" * 64))
        tampered = fixture.MISMATCH / fixture.DOCUMENT
        with fixture.web(fixture.downloads(document=tampered)), \
                self.assertRaisesRegex(ValueError, "^feed signature verification failed$"):
            website.public_downloads(receipt(distribution_sha256=fixture.distribution_sha256(fixture.MISMATCH)))
        for name, message in (("wrong-file.sig", "bound to 'latest.json'"), ("foreign-signer.sig", "registered feed key")):
            with self.subTest(signature=name), fixture.web(fixture.downloads(signature=fixture.MISMATCH / name)), \
                    self.assertRaisesRegex(ValueError, message):
                website.public_downloads(receipt())

    def test_all_withdrawn_state_has_no_artifacts_or_lingering_digest(self):
        withdrawn = {"version": None, "distribution_sha256": None, "manifest_sha256": None}
        self.assertEqual(website.public_downloads(withdrawn), [])
        with self.assertRaisesRegex(ValueError, "withdrawn"):
            website.public_downloads({**withdrawn, "distribution_sha256": "a" * 64})

    def test_incomplete_newer_preview_receipt_refuses_without_an_unhandled_key_error(self):
        newer = {"schema_version": 1, "channel": "preview", "version": "1.2.4", "manifest_sha256": "b" * 64,
                 "withdrawn_version": None}
        with fixture.web({f"{SITE}/.well-known/releases/preview.json": json.dumps(newer).encode()}), \
                self.assertRaisesRegex(ValueError, "publication was not verified"):
            release.wait_for_website(SITE, receipt("preview"), seconds=0, allow_newer=True)

    def test_legacy_release_accepts_explicit_schema2_null_without_claiming_a_document(self):
        expected = receipt()
        expected.pop("distribution_sha256")
        release.validate_website_receipt({"schema_version": 1, **expected}, expected)
        release.validate_website_receipt({"schema_version": 2, **expected, "distribution_sha256": None}, expected)
        with self.assertRaises(ValueError):
            release.validate_website_receipt({"schema_version": 2, **expected, "distribution_sha256": "b" * 64}, expected)

    def test_only_the_exact_verified_notification_becomes_a_dispatch_body(self):
        payload = fixture.notification()
        self.assertEqual(website.dispatch_body(payload), {"event_type": "painted-wolf-release", "client_payload": payload})
        for name, message in (("notification-tag.json", "tag or channel"),
                              ("notification-unbound.json", "independent updater and distribution digests"),
                              ("notification-extra.json", "exactly the verified release identities")):
            with self.subTest(fixture=name), self.assertRaisesRegex(ValueError, message):
                website.dispatch_body(fixture.notification(fixture.MISMATCH / name))

    def test_stable_confirmation_requires_both_promoted_channels_to_serve_the_signed_document(self):
        with fixture.storage({}), fixture.web(acknowledged("stable", "preview")):
            website.confirm(fixture.notification(), 1, SITE, seconds=0)
        with fixture.storage({}), fixture.web(acknowledged("stable")), \
                self.assertRaisesRegex(ValueError, "publication was not verified"):
            website.confirm(fixture.notification(), 1, SITE, seconds=0)
        newer = {"updates/preview/key-1/latest.json": json.dumps({"version": "1.2.4"}).encode()}
        with fixture.storage(newer), fixture.web(acknowledged("stable")):
            website.confirm(fixture.notification(), 1, SITE, seconds=0)

    def test_command_writes_a_fresh_dispatch_body_and_refuses_an_altered_notification(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "dispatch.json"
            arguments = ["dispatch-body", "--notification", str(fixture.FIXTURE / "notification.json"), "--output", str(output)]
            self.assertEqual(website.main(arguments), 0)
            self.assertEqual(json.loads(output.read_text())["client_payload"], fixture.notification())
            with self.assertRaises(SystemExit) as refused:
                website.main(arguments)
            self.assertEqual(refused.exception.code, 1)
            altered = ["dispatch-body", "--notification", str(fixture.MISMATCH / "notification-tag.json"),
                       "--output", str(Path(temporary) / "altered.json")]
            with self.assertRaises(SystemExit) as refused:
                website.main(altered)
            self.assertEqual(refused.exception.code, 1)
            self.assertFalse((Path(temporary) / "altered.json").exists())
