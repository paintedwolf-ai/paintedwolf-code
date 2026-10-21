"""Protected AUR availability policy cannot turn fixture metadata into delivery."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.publication.delivery import aur


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
        # The committed protected policy keeps AUR disabled.
        self.assertIsNone(aur.load())
        self.assertEqual({'enabled': False}, aur.facts(Untouched()))


class AurConsumerTests(unittest.TestCase):
    def test_enabled_adapter_requires_real_availability_and_fresh_inventory(self):
        from contextlib import ExitStack
        from unittest.mock import Mock
        from scripts.linux_release import aur_delivery, aur_recipe, aur_release_authority, aur_observation, aur_origin
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            raw = b'{"format_version":1}\n'
            (root / 'final-artifacts.json').write_bytes(raw)
            inputs = {'channel': 'stable', 'artifacts': json.loads(raw),
                      'repositories': {'generations': []}, 'catalog': 'catalog',
                      'campaign': {}, 'evidence_root': root, 'file_root': root,
                      'source_commit': 'a' * 40, 'version': '1.0.0',
                      'signing_generation': 'key', 'key_fingerprint': 'fingerprint', 'release_build': 1}
            selected = Mock(root=root)
            selected.authorize.side_effect = lambda: (dict(inputs), {'accepted': True})
            policy = {'channel': 'stable', 'package': 'painted-wolf-code-bin', 'commit': 'b' * 40}
            operation = {'package': policy['package']}
            offered = {'enabled': True, 'commit': policy['commit']}
            backend = Mock()
            backend.__enter__ = Mock(return_value=backend)
            backend.__exit__ = Mock(return_value=False)

            def observe(plan, recipe, commit, *, backend, authorize):
                self.assertEqual({'accepted': True}, authorize(plan, recipe))
                return {'production_available': True, 'facts': offered}

            with ExitStack() as stack:
                stack.enter_context(patch.object(aur, 'load', return_value=policy))
                stack.enter_context(patch.object(aur_recipe, 'generate', return_value={}))
                stack.enter_context(patch.object(aur_delivery, 'plan', return_value=operation))
                admission = stack.enter_context(patch.object(aur_release_authority, 'authorize', return_value={'accepted': True}))
                stack.enter_context(patch.object(aur_origin, 'ReadOnlyOrigin', return_value=backend))
                observer = stack.enter_context(patch.object(aur_observation, 'observe', side_effect=observe))
                self.assertEqual(offered, aur.facts(selected))
                self.assertEqual(raw, admission.call_args.kwargs['artifacts_bytes'])
                observer.side_effect = None
                observer.return_value = {'production_available': False, 'facts': offered}
                with self.assertRaisesRegex(ValueError, 'fixture observation'):
                    aur.facts(selected)
                observer.side_effect = observe
                (root / 'final-artifacts.json').write_bytes(b'{}')
                with self.assertRaisesRegex(ValueError, 'inventory changed'):
                    aur.facts(selected)
