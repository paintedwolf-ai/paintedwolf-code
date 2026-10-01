import json
import pathlib
import tempfile
import unittest
from preflight_cost import spending


class PreflightCostTests(unittest.TestCase):
    def test_application_quotes_support_reporting_and_preserve_partial_unknowns(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            attempt = root / 'provider-preflight/model/attempt-0000/probe'
            attempt.mkdir(parents=True)
            receipt = {'provider': 'p', 'model': 'm', 'stages': [{'stage': 'host_continuation', 'usage': {
                'present': True, 'prompt_tokens': 100, 'completion_tokens': 10}}]}
            for amount, complete, unpriced in [(0.25, True, 0), (0.0, True, 0), (0.1, False, 1), (None, False, 1)]:
                with self.subTest(amount=amount, complete=complete):
                    receipt['costs'] = {'host_continuation': {'estimated_usd': amount, 'complete': complete}}
                    (attempt / 'result.json').write_text(json.dumps(receipt))
                    total = spending(root)
                    self.assertEqual(total['unpriced_calls'], unpriced)
                    self.assertEqual(total['priced_usd'], amount or 0)
                    self.assertEqual(total['repriced_calls'], 0)
            receipt['costs']['host_continuation'] = {'estimated_usd': 0.25, 'complete': True}
            receipt['stages'][0]['usage']['incomplete'] = True
            (attempt / 'result.json').write_text(json.dumps(receipt))
            self.assertEqual(spending(root)['unpriced_calls'], 1)

    def test_counts_retained_attempts_once_and_requires_actual_usage(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp)
            model=root/'provider-preflight/small'
            attempt=model/'attempt-0000/probe'
            attempt.mkdir(parents=True)
            receipt={'provider':'p','model':'m','stages':[{'accepted':True,'usage':{
                'present':True,'prompt_tokens':100,'completion_tokens':10,'cache_read_input_tokens':20}}]}
            (attempt/'result.json').write_text(json.dumps(receipt))
            (model/'result.json').write_text(json.dumps(receipt))
            unknown=spending(root)
            self.assertEqual(unknown['calls'],1)
            self.assertEqual(unknown['unpriced_calls'],1)
            rates={('p','m'):{'usd_per_million':{'input':1,'output':2,'cache_read':0.5}}}
            total=spending(root,rates)
            self.assertAlmostEqual(total['priced_usd'],0.00011)
            self.assertEqual(total['unpriced_calls'],0)
            interrupted=model/'attempt-0001'
            interrupted.mkdir()
            (interrupted/'launched.json').write_text('{}')
            self.assertEqual(spending(root,rates)['unpriced_calls'],1)
