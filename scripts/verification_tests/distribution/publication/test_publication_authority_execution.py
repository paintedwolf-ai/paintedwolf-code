"""Transport permission is a fresh complete selection, not an accepted receipt."""

from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock

from linux_release.publication.authority import CATALOG, PREFIX, RepositoryAuthority
from linux_release.publication.repositories import digest


class PublicationAuthorityTests(unittest.TestCase):
    def fixture(self, root):
        manifest = {"family": "deb", "source_commit": "a" * 40}
        identity = digest(manifest)
        entry = {"manifest": manifest, "operation": {"previous": None}}
        selected = Mock(root=Path(root))
        selected.authorize.return_value = ({"repositories": {"generations": [entry]}}, {"accepted": True})
        authority = RepositoryAuthority(selected, base_url="https://downloads.example.test", expected={identity: None})
        request = {"manifest": manifest, "directory": Path(root) / "generations" / identity,
                   "catalog": CATALOG, "storage_prefix": PREFIX,
                   "base_url": "https://downloads.example.test", "expected": None,
                   "operation": "publish", "phase": "stage"}
        return selected, authority, request

    def test_stage_and_activation_each_reevaluate_the_complete_campaign(self):
        with tempfile.TemporaryDirectory() as root:
            selected, authority, request = self.fixture(root)
            self.assertIs(authority(request), True)
            request["phase"] = "activate"
            self.assertIs(authority(request), True)
            self.assertEqual(selected.authorize.call_count, 2)
            selected.authorize.side_effect = ValueError("evidence changed")
            with self.assertRaisesRegex(ValueError, "evidence changed"):
                authority(request)

    def test_origin_namespace_trust_and_operation_are_independently_protected(self):
        for key, value in [("base_url", "https://other.example.test"), ("storage_prefix", "other"),
                           ("catalog", "/tmp/untrusted-keys.json"), ("operation", "recover"),
                           ("phase", "withdraw")]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as root:
                selected, authority, request = self.fixture(root)
                request[key] = value
                with self.assertRaises(ValueError):
                    authority(request)
                selected.authorize.assert_not_called()

    def test_generation_path_bytes_and_predecessor_must_match_fresh_selection(self):
        for key, value in [("manifest", {"family": "rpm", "source_commit": "a" * 40}),
                           ("directory", "/tmp/another-generation"), ("expected", "d" * 64)]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as root:
                selected, authority, request = self.fixture(root)
                request[key] = value
                with self.assertRaises(ValueError):
                    authority(request)
                selected.authorize.assert_called_once()

    def test_incomplete_or_unpinned_generation_set_cannot_publish(self):
        with tempfile.TemporaryDirectory() as root:
            selected, authority, request = self.fixture(root)
            selected.authorize.return_value[0]["repositories"]["generations"].append(
                {"manifest": {"family": "rpm"}, "operation": {"previous": None}})
            with self.assertRaises(ValueError):
                authority(request)
        for expected in [{}, {"invalid": None}, {"a" * 64: True}]:
            with self.subTest(expected=expected), self.assertRaises(ValueError):
                RepositoryAuthority(Mock(), base_url="https://downloads.example.test", expected=expected)

    def test_protected_origin_cannot_supply_credentials_path_query_or_fragment(self):
        for origin in [None, "http://downloads.example.test", "https://user:password@downloads.example.test",
                       "https://downloads.example.test/other", "https://downloads.example.test?key=value",
                       "https://downloads.example.test#fragment"]:
            with self.subTest(origin=origin), self.assertRaises(ValueError):
                RepositoryAuthority(Mock(), base_url=origin, expected={"a" * 64: None})
