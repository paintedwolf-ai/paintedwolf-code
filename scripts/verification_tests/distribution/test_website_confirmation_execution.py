"""Actual selected website receipt and page gates require exact public state."""
import io
import json
import unittest
from unittest.mock import patch

from release_distribution_contract import confirmation


class Response(io.BytesIO):
    def __init__(self, body, cache="no-store"):
        super().__init__(body)
        self.headers = {"Cache-Control": cache}


class WebsitePublicationTests(unittest.TestCase):
    def setUp(self):
        self.expected = {"channel": "stable", "version": "1.0.0", "manifest_sha256": "a" * 64, "withdrawn_version": None}
        self.receipt = json.dumps({"schema_version": 1, **self.expected}).encode()
        self.page = b'<a data-release-channel="stable" download href="https://downloads.paintedwolf.dev/releases/v1.0.0/painted-wolf-code_v1.0.0_darwin-aarch64.dmg">Download</a>'

    def test_awaits_actual_download_page_with_matching_confirmation(self):
        with patch.object(confirmation.urllib.request, "urlopen", side_effect=[Response(self.receipt), Response(self.page)]) as get:
            confirmation.wait_for_website("https://paintedwolf.ai", self.expected, seconds=0, legacy_catalog=True)
        self.assertEqual(get.call_count, 2)
        self.assertEqual(get.call_args_list[1].args[0].full_url, "https://paintedwolf.ai/download/")

    def test_preview_waits_on_the_unlisted_preview_page(self):
        expected = {**self.expected, "channel": "preview", "version": "1.0.0-rc.3"}
        receipt = json.dumps({"schema_version": 1, **expected}).encode()
        page = self.page.replace(b"stable", b"preview").replace(b"1.0.0", b"1.0.0-rc.3")
        with patch.object(confirmation.urllib.request, "urlopen", side_effect=[Response(receipt), Response(page)]) as get:
            confirmation.wait_for_website("https://paintedwolf.ai", expected, seconds=0, legacy_catalog=True)
        self.assertEqual(get.call_args_list[1].args[0].full_url, "https://paintedwolf.ai/download/preview/")

    def test_stale_page_or_cache_headers_cannot_confirm_a_release(self):
        for page, headers in [(self.page.replace(b"1.0.0", b"0.9.0"), "no-store"), (self.page, "public, max-age=3600")]:
            with self.subTest(headers=headers), patch.object(confirmation.urllib.request, "urlopen", side_effect=[Response(self.receipt), Response(page, headers)]):
                with self.assertRaisesRegex(ValueError, "not verified"):
                    confirmation.wait_for_website("https://paintedwolf.ai", self.expected, seconds=0, legacy_catalog=True)

    def test_unpublished_channel_has_no_download_link(self):
        expected = {**self.expected, "version": None, "manifest_sha256": None, "withdrawn_version": "1.0.0"}
        confirmation.validate_website_downloads('<p>Downloads unavailable</p>', expected)
        with self.assertRaises(ValueError):
            confirmation.validate_website_downloads(self.page.decode(), expected)

    def test_selected_preview_cannot_confirm_with_a_newer_unrelated_receipt(self):
        expected = {**self.expected, "channel": "preview"}
        newer = {**expected, "version": "1.1.0-rc.1", "manifest_sha256": "b" * 64}
        page = self.page.replace(b"stable", b"preview").replace(b"1.0.0", b"1.1.0-rc.1")
        responses = [Response(json.dumps({"schema_version": 1, **newer}).encode()), Response(page)]
        with patch.object(confirmation.urllib.request, "urlopen", side_effect=responses) as get:
            with self.assertRaisesRegex(ValueError, "not verified"):
                confirmation.wait_for_website("https://paintedwolf.ai", expected, seconds=0, legacy_catalog=True)
        self.assertEqual(get.call_count, 1)

    def test_incorrect_confirmation_never_accepts_a_valid_page(self):
        with patch.object(confirmation.urllib.request, "urlopen", return_value=Response(b'{}')) as get:
            with self.assertRaisesRegex(ValueError, "not verified"):
                confirmation.wait_for_website("https://paintedwolf.ai", self.expected, seconds=0, legacy_catalog=True)
        self.assertEqual(get.call_count, 1)
