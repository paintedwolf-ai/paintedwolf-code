"""Protected AUR availability policy cannot turn fixture metadata into delivery."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.publication import aur


class AurPolicyTests(unittest.TestCase):
    def load(self, value):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'policy.json'
            path.write_text(json.dumps(value))
            return aur.load(path)

    def test_disabled_policy_cannot_retain_availability(self):
        self.assertIsNone(self.load({'schema_version': 1, 'enabled': False}))
        with self.assertRaises(ValueError):
            self.load({'schema_version': 1, 'enabled': False, 'commit': 'a' * 40})

    def test_enabled_channel_and_commit_are_explicit(self):
        for channel, package in [('stable', 'painted-wolf-code-bin'),
                                 ('preview', 'painted-wolf-code-preview-bin')]:
            value = {'schema_version': 1, 'enabled': True, 'channel': channel,
                     'package': package, 'commit': 'a' * 40}
            self.assertEqual(value, self.load(value))
            for changes in ({'commit': 'main'}, {'package': 'foreign'}, {'schema_version': True},
                            {'url': 'https://foreign.invalid'}):
                with self.subTest(changes=changes), self.assertRaises(ValueError):
                    self.load({**value, **changes})

    def test_default_off_performs_no_campaign_or_git_work(self):
        class Untouched:
            def authorize(self):
                raise AssertionError('disabled AUR must not request campaign or origin work')
        with patch.object(aur, 'load', return_value=None):
            self.assertEqual({'enabled': False}, aur.facts(Untouched()))
