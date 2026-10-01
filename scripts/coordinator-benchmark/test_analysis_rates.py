import json
import pathlib
import tempfile
import unittest
import analysis_rates


class OperatorRateTests(unittest.TestCase):
    def test_cache_buckets_are_disjoint_and_missing_prices_stay_unknown(self):
        usage={'prompt_tokens':1000,'completion_tokens':100,'cache_read_tokens':500,
               'cache_write_tokens':200,'cache_write_1h_tokens':50}
        rate={'usd_per_million':{'input':1,'output':2,'cache_read':.1,'cache_write':1.25,'cache_write_1h':2}}
        self.assertAlmostEqual(analysis_rates.estimate(usage,rate),.0008375)
        del rate['usd_per_million']['cache_write_1h']
        self.assertIsNone(analysis_rates.estimate(usage,rate))
        usage['cache_write_1h_tokens']=0
        self.assertAlmostEqual(analysis_rates.estimate(usage,rate),.0008)
        usage['cache_read_tokens']=1001
        with self.assertRaises(ValueError): analysis_rates.estimate(usage,rate)

    def test_rate_identity_provenance_and_numbers_are_required(self):
        entry={'provider':'p','model':'m','source':'https://example.com/prices','observed_at':'2026-09-07',
               'basis':'Published rates','usd_per_million':{'input':1,'output':2}}
        with tempfile.TemporaryDirectory() as temporary:
            path=pathlib.Path(temporary)/'rates.json'
            def write(entries): path.write_text(json.dumps({'version':1,'rates':entries}))
            write([entry]);self.assertEqual(analysis_rates.load(path)[('p','m')],entry)
            for bad in [True,-1,float('inf'),'1']:
                write([{**entry,'usd_per_million':{'input':bad}}])
                with self.assertRaises(ValueError): analysis_rates.load(path)
            write([entry,entry])
            with self.assertRaises(ValueError): analysis_rates.load(path)
            write([{**entry,'basis':''}])
            with self.assertRaises(ValueError): analysis_rates.load(path)
