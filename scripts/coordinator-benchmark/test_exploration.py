import argparse
import json
import pathlib
import sqlite3
import tempfile
import unittest
import analyze
from analyze import preflight_spending as spending
import preparation


class OperatorRateTests(unittest.TestCase):
    def test_cache_buckets_are_disjoint_and_missing_prices_stay_unknown(self):
        usage={'prompt_tokens':1000,'completion_tokens':100,'cache_read_tokens':500,
               'cache_write_tokens':200,'cache_write_1h_tokens':50}
        rate={'usd_per_million':{'input':1,'output':2,'cache_read':.1,'cache_write':1.25,'cache_write_1h':2}}
        self.assertAlmostEqual(analyze.estimate(usage,rate),.0008375)
        del rate['usd_per_million']['cache_write_1h']
        self.assertIsNone(analyze.estimate(usage,rate))
        usage['cache_write_1h_tokens']=0
        self.assertAlmostEqual(analyze.estimate(usage,rate),.0008)
        usage['cache_read_tokens']=1001
        with self.assertRaises(ValueError): analyze.estimate(usage,rate)

    def test_rate_identity_provenance_and_numbers_are_required(self):
        entry={'provider':'p','model':'m','source':'https://example.com/prices','observed_at':'2026-09-07',
               'basis':'Published rates','usd_per_million':{'input':1,'output':2}}
        with tempfile.TemporaryDirectory() as temporary:
            path=pathlib.Path(temporary)/'rates.json'
            def write(entries): path.write_text(json.dumps({'version':1,'rates':entries}))
            write([entry]);self.assertEqual(analyze.load(path)[('p','m')],entry)
            for bad in [True,-1,float('inf'),'1']:
                write([{**entry,'usd_per_million':{'input':bad}}])
                with self.assertRaises(ValueError): analyze.load(path)
            write([entry,entry])
            with self.assertRaises(ValueError): analyze.load(path)
            write([{**entry,'basis':''}])
            with self.assertRaises(ValueError): analyze.load(path)


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


class ExplorationTests(unittest.TestCase):
    def test_application_timestamps_preserve_nanosecond_boundaries(self):
        before=analyze.instant('2026-09-07T12:00:00.123456788Z')
        entry=analyze.instant('2026-09-07T13:00:00.123456789+01:00')
        self.assertLess(before,entry)
        self.assertEqual(entry,analyze.instant('2026-09-07T11:00:00.123456789-01:00'))

    def test_closed_capture_cost_uses_checkpointed_ledger_without_recreating_wal(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary); capture=root/'capture'; capture.mkdir()
            (root/'exit.json').write_text('{"kind":"exited","returncode":0}')
            db=sqlite3.connect(capture/'store.db')
            db.execute('PRAGMA journal_mode=WAL')
            db.execute('CREATE TABLE llm_calls (provider_id TEXT,caller TEXT,started_at TEXT,no_charge INTEGER,estimated_nano_usd INTEGER,prompt_tokens INTEGER,completion_tokens INTEGER,unpriced_tokens INTEGER)')
            db.execute("INSERT INTO llm_calls VALUES ('p','coordinator','2026-09-07T12:00:00Z',0,1000000000,10,5,0)")
            db.commit(); db.close()
            before={p.name:p.read_bytes() for p in capture.iterdir()}
            self.assertEqual(analyze.cost_observation(capture)['priced_usd'],1)
            self.assertEqual({p.name:p.read_bytes() for p in capture.iterdir()},before)

    def test_modes_select_repetitions_before_spend_and_resume_freezes_inputs(self):
        for mode,count in [('exploration',2),('release',5),('calibration',2)]:
            with self.subTest(mode=mode),tempfile.TemporaryDirectory() as temporary:
                root=pathlib.Path(temporary)
                roster=root/'roster.json';roster.write_text('{"models":[{"id":"candidate"}],"repetitions":99}')
                args=argparse.Namespace(out=root,roster=roster,mode=mode,repetitions=None,models=None,cases=None,
                     cadence='selected',concurrency=None,cloud_concurrency=None,provider_limit=[])
                result=preparation.inputs(args,'same')
                self.assertEqual(result['roster']['repetitions'],count)
                args.mode='release'
                self.assertEqual(preparation.inputs(args,'same'),result)

    def test_cost_phase_uses_instants_and_unknown_price_is_not_zero(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary)
            (root/'report.json').write_text(json.dumps({'cases':[{'preparation':{'entry_at':'2026-09-07T12:00:00Z'}}]}))
            db=sqlite3.connect(root/'store.db')
            try:
                db.execute('CREATE TABLE llm_calls (provider_id TEXT,caller TEXT,started_at TEXT,no_charge INTEGER,estimated_nano_usd INTEGER,prompt_tokens INTEGER,completion_tokens INTEGER,unpriced_tokens INTEGER)')
                db.executemany('INSERT INTO llm_calls VALUES (?,?,?,?,?,?,?,?)',[
                    ('fixture','coordinator','2026-09-07T11:59:00Z',0,None,0,0,0),
                    ('p','coordinator','2026-09-07T13:00:01+01:00',0,1000000000,100,5,0),
                    ('p','coordinator','2026-09-07T11:00:01-01:00',0,None,20,3,23)])
                db.commit()
            finally: db.close()
            result=analyze.cost_observation(root)
            self.assertEqual(result['priced_usd'],1)
            self.assertEqual(result['unpriced_calls'],1)
            self.assertEqual(result['phases']['preparation']['calls'],1)
            self.assertEqual(result['phases']['candidate']['calls'],2)
