import json
import math
import pathlib
import unittest
from sampling import weights, estimate


class SamplingTests(unittest.TestCase):
    def test_bank_endpoints_are_exact_in_every_tier(self):
        operations=json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())['operations']
        for tier in {op.get('tier','gate') for op in operations if op['role']=='scored'}:
            for passed in [0,3]:
                cases=[{'id':o['id'],'passed':passed,'measured':3} for o in operations if o['role']=='scored']
                self.assertEqual(estimate(cases,operations,tier)['score'],100 if passed else 0)

    def test_equal_weights_survive_unequal_scenario_and_fixture_counts(self):
        operations=[{'id':i,'family':f,'scenario':s,'role':'scored'} for i,f,s in
                    [('a','one','x'),('b','one','x'),('c','one','y'),('d','two','z')]]
        self.assertEqual(weights(operations),{'a':.125,'b':.125,'c':.25,'d':.5})
        cases=[{'id':i,'passed':p,'measured':5} for i,p in [('a',0),('b',0),('c',0),('d',5)]]
        self.assertEqual(estimate(cases,operations)['score'],50)

    def test_fixed_bank_bound_stays_nonzero_at_perfect_success(self):
        operations=[{'id':i,'family':i,'scenario':i,'role':'scored'} for i in ['one','two']]
        cases=[{'id':o['id'],'passed':5,'measured':5} for o in operations]
        result=estimate(cases,operations)
        radius=100*math.sqrt(math.log(40)/20)
        self.assertAlmostEqual(result['score'],100)
        self.assertAlmostEqual(result['interval_95'][0],100-radius)
        self.assertEqual(result['interval_95'][1],100)
        with self.assertRaises(ValueError): estimate(cases[1:],operations)

    def test_pass_k_is_the_unbiased_all_pass_probability(self):
        from sampling import pass_k, reliability
        self.assertEqual(pass_k(5,5,5),1.0)
        self.assertEqual(pass_k(0,5,5),0.0)
        self.assertAlmostEqual(pass_k(4,5,5),0.0)
        self.assertAlmostEqual(pass_k(4,5,2),0.6)
        self.assertIsNone(pass_k(3,4,5))
        operations=[{'id':i,'family':f,'scenario':s,'role':'scored','tier':'orchestration'} for i,f,s in
                    [('a','one','one'),('b','two','two')]]
        cases=[{'id':'a','passed':5,'measured':5},{'id':'b','passed':4,'measured':5}]
        result=reliability(cases,operations,'orchestration',5)
        self.assertAlmostEqual(result['pass_k'],50)
        self.assertAlmostEqual(result['consistency'],100*(1+0.36)/2)
        self.assertIsNone(reliability([{'id':'a','passed':5,'measured':5},{'id':'b','passed':2,'measured':3}],operations,'orchestration',5))
