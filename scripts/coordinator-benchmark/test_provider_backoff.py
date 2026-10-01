import json
import pathlib
import tempfile
import unittest

from provider_backoff import observations


class ProviderObservations(unittest.TestCase):
    def test_all_provider_modes_are_observed_without_altering_application_facts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            self.assertEqual(observations(root), [])
            state_dir = root / 'provider-rate-state'
            state_dir.mkdir()
            states = [{'version': 1, 'provider_id': name, 'policy': {'adaptive': adaptive},
                       'rate_limits': 3, 'admitted': 19, 'accepted': 14}
                      for name, adaptive in [('first', False), ('second', True)]]
            for i, state in enumerate(states):
                (state_dir / f'{i}.json').write_text(json.dumps(state))
            (state_dir / 'held.lock').write_text('')
            self.assertEqual(observations(root), [{'bucket': str(i), **state} for i, state in enumerate(states)])

    def test_corrupt_or_unknown_state_is_not_silently_discarded(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            state_dir = root / 'provider-rate-state'
            state_dir.mkdir()
            path = state_dir / 'bucket.json'
            for content in ['{', '{"version": 99}', 'null', '[]']:
                path.write_text(content)
                with self.assertRaises(ValueError):
                    observations(root)
