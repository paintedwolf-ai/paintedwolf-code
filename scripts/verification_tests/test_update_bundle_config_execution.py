"""Artifact-signing configuration must follow the release trust generation."""
import unittest

from update_keys import bundler_config


class BundleConfigTests(unittest.TestCase):
    def test_ordinary_and_bridge_releases_select_the_artifact_signing_key(self):
        for embedded in (1, 2):
            registry = {"signing_generation": 1, "embedded_generation": embedded,
                        "generations": [{"public_key": "artifact-one", "feed_public_key": "feed-one"},
                                        {"public_key": "artifact-two", "feed_public_key": "feed-two"}]}
            self.assertEqual(bundler_config(registry),
                             {"plugins": {"updater": {"pubkey": "artifact-one"}}})

    def test_rotated_release_selects_its_new_artifact_key(self):
        registry = {"signing_generation": 2, "generations": [{"public_key": "old"}, {"public_key": "new"}]}
        self.assertEqual(bundler_config(registry)["plugins"]["updater"]["pubkey"], "new")
