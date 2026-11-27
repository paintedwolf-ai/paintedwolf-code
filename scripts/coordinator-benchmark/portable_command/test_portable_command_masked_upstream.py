"""Unit controls for masked-upstream-failure pipeline publication fence grading."""
import pathlib
import sqlite3
import tempfile
import unittest

from portable_command.contracts import load_suite
from portable_command.grading import grade_case
from portable_command.observe import build_observation


def _write_db(path, *, published):
    db = sqlite3.connect(path)
    db.executescript("""
        CREATE TABLE invocation_receipts (id TEXT, session_id TEXT, tool_name TEXT, outcome TEXT, error_code TEXT, started_at TEXT, ended_at TEXT);
        CREATE TABLE invocation_programs (id TEXT, parent_receipt_id TEXT, state TEXT, exit_code INTEGER, stop_code TEXT, complete INTEGER);
        CREATE TABLE invocation_program_executions (id TEXT, program_id TEXT, parent_execution_id TEXT, execution_id TEXT, operation_id TEXT, state TEXT, exit_code INTEGER, publication_eligible INTEGER, effect_certainty TEXT);
        CREATE TABLE invocation_program_revision_pins (id TEXT, program_id TEXT, execution_id TEXT, path TEXT, basis TEXT, accepted_revision TEXT, published_revision TEXT, disk_sha256 TEXT);
        CREATE TABLE invocation_program_outputs (id TEXT, program_id TEXT, execution_id TEXT, stream TEXT, reference TEXT, sha256 TEXT, bytes INTEGER, preview_truncated INTEGER, blob_id TEXT, redacted INTEGER);
        CREATE TABLE invocation_program_sources (id TEXT, program_id TEXT, source_unit_id TEXT, sha256 TEXT);
        CREATE TABLE source_command_windows (id TEXT, session_id TEXT, invocation_id TEXT, state TEXT);
    """)
    db.execute("INSERT INTO invocation_receipts VALUES ('r1','s1','command','completed','', 't0','t1')")
    db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',1,'',1)")
    db.execute("INSERT INTO invocation_program_executions VALUES ('e1','p1','', 'ex-ext','external','completed',1,0,'completed')")
    db.execute("INSERT INTO invocation_program_executions VALUES ('e2','p1','', 'ex-cat','read','completed',0,1,'none')")
    w_state, w_exit, w_elig, w_cert = ('completed', 0, 1, 'completed') if published else ('failed', 1, 0, 'none')
    db.execute("INSERT INTO invocation_program_executions VALUES ('e3','p1','', 'ex-wr','write',?,?,?,?)", (w_state, w_exit, w_elig, w_cert))
    acc, pub = ('rev1', 'rev2') if published else ('', '')
    db.execute("INSERT INTO invocation_program_revision_pins VALUES ('pin1','p1','ex-wr','result.txt','redirect',?,?, '')", (acc, pub))
    db.commit()
    db.close()


class MaskedUpstreamFailureControlTests(unittest.TestCase):
    def _run_control(self, *, published, final_bytes):
        repo = pathlib.Path(__file__).resolve().parents[3]
        suite = {c['id']: c for c in load_suite(repo / 'lycaon/test/fixtures/eval/portable-command/suite.json')}
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
                turn_records=[{'call_ids': ['r1'], 'await_call_ids': [], 'command_call_id': 'r1', 'batch_outcomes': [{'call_id': 'r1', 'tool': 'command', 'outcome': 'completed'}]}],
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
