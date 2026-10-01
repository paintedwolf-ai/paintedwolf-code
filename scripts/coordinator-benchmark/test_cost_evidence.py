import pathlib
import tempfile
import unittest
from unittest.mock import patch
import cost_evidence as cost


def bucket(**values):
    return {**{k: 0 for k in cost.COUNTS}, 'provider': 'cloud', 'model': 'candidate', 'caller': 'coordinator',
            'status': 'reported', 'no_charge': False, 'pricing_source': 'catalog', 'priced_as_of': '2026-09-12T00:00:00Z',
            'usage_source': 'provider', 'rate': {'input_per_1k': .001, 'currency': 'USD'},
            'calls': 1, 'known_nano_usd': 1_000_000, **values}


class CostEvidenceTests(unittest.TestCase):
    def test_estimates_include_support_and_preserve_distinct_price_snapshots(self):
        result = cost.summarize([bucket(), bucket(caller='worker'), bucket(model='utility'),
                                 bucket(priced_as_of='2026-09-13T00:00:00Z')])
        self.assertEqual(result['known_nano_usd'], 4_000_000)
        self.assertTrue(result['complete'])
        self.assertEqual(len(result['buckets']), 4)

    def test_partial_usage_unknown_price_and_missing_captures_never_become_free(self):
        for uncertain in [bucket(status='started', known_nano_usd=None),
                          bucket(known_nano_usd=None), bucket(usage_source='provider_partial'), bucket(unpriced_calls=1)]:
            result = cost.summarize([bucket(), uncertain])
            self.assertFalse(result['complete'])
            self.assertEqual(result['unknown_calls'], 1)
        self.assertIsNone(cost.summarize([], missing_captures=1)['known_nano_usd'])
        self.assertFalse(cost.summarize([bucket()], missing_captures=1)['complete'])

    def test_explicit_no_charge_and_host_counted_usage_are_disclosed(self):
        result = cost.summarize([bucket(no_charge=True, known_nano_usd=None, status='unknown')])
        self.assertEqual(result['known_nano_usd'], 0)
        self.assertTrue(result['complete'])
        self.assertEqual(cost.summarize([bucket(usage_source='host')])['host_counted_calls'], 1)

    def test_all_attempts_count_even_if_only_the_last_is_measured(self):
        with tempfile.TemporaryDirectory() as temporary:
            run = pathlib.Path(temporary)
            for n in range(3):
                (run / 'captures' / 'episode-000' / f'attempt-{n:04}').mkdir(parents=True)
            with patch.object(cost, 'export', side_effect=[[bucket()], OSError('missing'), [bucket(caller='worker')]]):
                result = cost.case_cost(run, [{'index': 0}])
            self.assertEqual(result['capture_attempts'], 3)
            self.assertEqual(result['known_nano_usd'], 2_000_000)
            self.assertEqual(result['missing_captures'], 1)
            self.assertFalse(result['complete'])

    def test_unadmitted_episode_has_no_estimate(self):
        with tempfile.TemporaryDirectory() as temporary:
            result = cost.case_cost(pathlib.Path(temporary), [{'index': 0}])
            self.assertIsNone(result['known_nano_usd'])
            self.assertEqual(result['capture_attempts'], 0)

    def test_preflight_is_separate_and_keeps_cache_and_price_provenance(self):
        import json
        with tempfile.TemporaryDirectory() as temporary:
            run = pathlib.Path(temporary)
            path = run / 'provider-preflight/model/attempt-0000/probe/result.json'
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps({'provider': 'cloud', 'model': 'candidate',
                'stages': [{'stage': 'continuation', 'usage': {'present': True, 'prompt_tokens': 100,
                           'cache_read_input_tokens': 60, 'cache_creation_1h_input_tokens': 10}}],
                'costs': {'continuation': {'estimated_usd': .000003, 'complete': True, 'source': 'catalog',
                          'as_of': '2026-09-12T00:00:00Z', 'rate': {'currency': 'USD', 'cache_read_per_1k': .001}}}}))
            result = cost.preflight_cost(run)
            self.assertEqual(result['known_nano_usd'], 3000)
            self.assertTrue(result['complete'])
            self.assertEqual(result['buckets'][0]['cache_read_tokens'], 60)
            self.assertEqual(result['buckets'][0]['cache_write_1h_tokens'], 10)
            self.assertEqual(result['buckets'][0]['pricing_source'], 'catalog')
            missing = run / 'provider-preflight/model/attempt-0001'
            missing.mkdir()
            result = cost.preflight_cost(run)
            self.assertFalse(result['complete'])
            self.assertEqual(result['known_nano_usd'], 3000)

    def test_malformed_preflight_receipt_leaves_cost_unavailable(self):
        with tempfile.TemporaryDirectory() as temporary:
            run = pathlib.Path(temporary)
            path = run / 'provider-preflight/model/attempt-0000/probe/result.json'
            path.parent.mkdir(parents=True)
            path.write_text('{broken')
            result = cost.preflight_cost(run)
            self.assertFalse(result['complete'])
            self.assertIsNone(result['known_nano_usd'])
            self.assertEqual(result['missing_captures'], 1)
