"""Unit controls for masked-upstream-failure pipeline publication fence grading."""
import json
import pathlib
import sqlite3
import tempfile
import unittest

from portable_command.contracts import load_suite
from portable_command.grading import grade_case
from portable_command.observe import build_observation
from portable_command.test_portable_command_harness import create_audit_db


def _write_db(path, *, published):
    create_audit_db(path)
    db = sqlite3.connect(path)
    db.executescript("""
        ALTER TABLE invocation_program_executions ADD COLUMN status_code INTEGER;
        ALTER TABLE invocation_program_executions ADD COLUMN publication_eligible INTEGER;
        ALTER TABLE invocation_program_executions ADD COLUMN complete INTEGER;
    """)
    db.execute("INSERT INTO invocation_receipts VALUES ('r1','s1','tc1','command','command','completed',1)")
    db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',1,'')")
    db.execute("INSERT INTO invocation_program_executions VALUES ('ex-ext','p1',1,'external','external','processes','completed','completed','','','',1,0,1)")
    db.execute("INSERT INTO invocation_program_executions VALUES ('ex-cat','p1',2,'cat','cat','files','completed','none','','','',0,1,1)")
    w_state, w_cert, acc, pub, w_status, w_elig = (
        ('completed', 'completed', 'rev1', 'rev2', 0, 1) if published else ('failed', 'none', '', '', 1, 0)
    )
    db.execute(
        "INSERT INTO invocation_program_executions VALUES ('ex-wr','p1',3,'write','write','files',?,?,?,?,?, ?,?,1)",
        (w_state, w_cert, acc, pub, '', w_status, w_elig),
    )
    db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','ex-wr',1,'destination','replace','result.txt')")
    res_json = json.dumps({'outcome': 'completed', 'content': json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 1, 'stdout': '', 'stderr': ''})})
    db.execute("INSERT INTO messages VALUES ('m1','s1',1,'tool','tc1',?)", (res_json,))
    db.commit()
    db.close()


class MaskedUpstreamFailureControlTests(unittest.TestCase):
    def _run_control(self, *, published, final_bytes):
        repo = pathlib.Path(__file__).resolve().parents[3]
        suite = {c['id']: c for c in load_suite(repo / 'lycaon/test/fixtures/eval/portable-command/suite.json')['cases']}
        case = suite['masked-upstream-failure']
        with tempfile.TemporaryDirectory() as td:
            root = pathlib.Path(td)
            proj = root / 'proj'
            proj.mkdir()
            (proj / 'result.txt').write_bytes(final_bytes)
            db_path = root / 'store.db'
            _write_db(db_path, published=published)
            sidecar = root / 'sidecar.log'
            sidecar.write_text('')
            obs = build_observation(
                case,
                session_id='s1',
                identity={'provenance_verified': True, 'engine_provenance': {'provenance_verified': True}},
                project_dir=proj,
                db_path=db_path,
                sidecar_log=sidecar,
                turn_records=[{'program': case['turns'][0]['program'], 'call_ids': ['tc1'], 'await_call_ids': [], 'command_call_id': 'tc1', 'batch_outcomes': [{'call_id': 'tc1', 'tool': 'command', 'outcome': 'completed'}]}],
                prelude_records=[],
                baseline_hashes={'result.txt': '2c0e3f276f2a2d677615a5656b9282b90c66509427029608d3790c178b56684f'},
            )
            return obs, grade_case(case, obs)

    def test_declined_redirect_preserves_bytes_and_passes(self):
        obs, grade = self._run_control(published=False, final_bytes=b'old\n')
        self.assertTrue(obs['facts']['every_producer_eligible'])
        self.assertTrue(obs['facts']['destination_unchanged'])
        self.assertTrue(grade['passed'], grade)

    def test_published_despite_ineligible_producer_fails(self):
        obs, grade = self._run_control(published=True, final_bytes=b'alpha')
        self.assertFalse(obs['facts']['every_producer_eligible'])
        self.assertFalse(obs['facts']['destination_unchanged'])
        self.assertFalse(grade['passed'])
        self.assertFalse(grade['dimensions']['artifacts'])
        self.assertFalse(grade['dimensions']['evidence'])


if __name__ == '__main__':
    unittest.main()
