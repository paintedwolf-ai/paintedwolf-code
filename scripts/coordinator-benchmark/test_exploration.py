import argparse
import json
import pathlib
import sqlite3
import tempfile
import unittest
import analyze
import preparation


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
