"""The live document fixture cannot escape its prefix or omit paginated cleanup."""
import unittest
from unittest.mock import patch
from scripts.linux_release.publication import rehearsal
from scripts.linux_release.publication.immutable import ImmutableOrigin

class DocumentRehearsalTests(unittest.TestCase):
    def test_exact_prefix_listing_consumes_every_page(self):
        prefix = "release-system-tests/document-fixture"
        pages = [{"Contents": [{"Key": prefix + "/one"}], "IsTruncated": True, "NextContinuationToken": "next"},
                 {"Contents": [{"Key": prefix + "/two"}], "IsTruncated": False}]
        with patch.object(rehearsal, "request", side_effect=pages) as request:
            self.assertEqual(rehearsal.objects(prefix), [prefix + "/one", prefix + "/two"])
            self.assertIn("next", request.call_args.args)
        pages[1]["Contents"][0]["Key"] = "foreign/one"
        with patch.object(rehearsal, "request", side_effect=pages), self.assertRaisesRegex(ValueError, "exact disposable"):
            rehearsal.objects(prefix)

    def test_repeated_token_and_duplicate_keys_refuse(self):
        prefix = "release-system-tests/document-fixture"
        page = {"Contents": [], "IsTruncated": True, "NextContinuationToken": "same"}
        with patch.object(rehearsal, "request", side_effect=[page, page]), self.assertRaisesRegex(ValueError, "pagination"):
            rehearsal.objects(prefix)
        page = {"Contents": [{"Key": prefix + "/one"}, {"Key": prefix + "/one"}]}
        with patch.object(rehearsal, "request", return_value=page), self.assertRaisesRegex(ValueError, "exact disposable"):
            rehearsal.objects(prefix)

    def test_transport_rehearsal_requires_isolation_and_production_requires_fixed_root(self):
        for arguments in ({"rehearsal": True, "storage_prefix": "releases"},
                          {"rehearsal": False, "origin": "https://foreign.invalid"},
                          {"rehearsal": False, "storage_prefix": "release-system-tests/fixture"},
                          {"rehearsal": 1, "storage_prefix": "release-system-tests/fixture"}):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                ImmutableOrigin(**arguments)
