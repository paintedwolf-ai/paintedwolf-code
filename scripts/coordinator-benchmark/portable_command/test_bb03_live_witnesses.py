"""Unit tests and negative controls for integrated Q13, Q14, Q15, and Q17 live witness oracles."""
import json
import pathlib
import sqlite3
import tempfile
import unittest
from portable_command import grading, observe
from portable_command.contracts import load_suite, materialize
from portable_command.test_portable_command_harness import create_audit_db

CASES = {c['id']: c for c in load_suite()['cases']}
IDENT = {'git_head': 'a' * 40}


class BB03LiveWitnessOracleTests(unittest.TestCase):
    def _base(self, root, case_id):
        case = CASES[case_id]
        proj, dbp, lp = root / 'proj', root / 'store.db', root / 'sidecar.log'
        base = materialize(case, proj)
        create_audit_db(dbp)
        lp.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
        return case, proj, base, dbp, lp

    def test_q13_stale_approval_positive_and_negative_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            case, proj, base, dbp, lp = self._base(pathlib.Path(tmp), 'stale-approval')
            db = sqlite3.connect(dbp)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN arguments_digest TEXT DEFAULT ''")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','conflict',2,'COMMAND_REVISION_CONFLICT')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,arguments_digest) VALUES ('e1','p1',1,'sed','sed','filesystem','refused','none',?)", ('d' * 64,))
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_REVISION_CONFLICT'], 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'conflict', 'status': 2, 'refusal': {'code': 'COMMAND_REVISION_CONFLICT'}})}),))
            db.commit(); db.close()
            turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            wm = {'checkpoint_id': 'cp-1', 'doc_before_rev': 1, 'human_revision': 2, 'after_revision': 2}
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta=wm)
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, IDENT)['passed'])
            # Negative control: missing checkpoint or diverged revision fails.
            obs_bad, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta={**wm, 'checkpoint_id': None, 'after_revision': 3})
            self.assertFalse(obs_bad['facts']['held_prepared_digest'])
            self.assertFalse(obs_bad['facts']['human_edit_preserved'])

    def test_q14_publication_barrier_positive_and_negative_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            case, proj, base, dbp, lp = self._base(pathlib.Path(tmp), 'publication-barrier')
            db = sqlite3.connect(dbp)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','failed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','incomplete',2,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e1','p1',1,'printf','printf','processes','completed','none','',''), ('e2','p1',2,'write','','filesystem','failed','attempted','rev2','')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e2',0,'destination','accepted_document','dir/input.txt')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'failed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'incomplete', 'status': 2, 'stdout': '', 'stderr': ''})}),))
            db.commit(); db.close()
            turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            wm = {'input_doc_before_rev': 1, 'input_doc_after_rev': 2, 'input_doc_dirty': True, 'input_held_agent_version_id': 'hv-1', 'unrelated_doc_before_rev': 2, 'unrelated_doc_after_rev': 2, 'unrelated_doc_dirty': True}
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta=wm)
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, IDENT)['passed'])
            obs_bad, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta={**wm, 'input_held_agent_version_id': ''})
            self.assertFalse(obs_bad['facts']['accepted_document_recoverable'])

    def test_q15_partial_write_unknown_http_and_q17_listener_rebind_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # Q15
            case, proj, base, dbp, lp = self._base(root / 'q15', 'partial-write-unknown-http')
            (proj / 'first.txt').write_text('first\n')
            db = sqlite3.connect(dbp)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','interrupted',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','interrupted',2,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e1','p1',1,'printf','printf','processes','completed','none','',''), ('e2','p1',2,'write','','filesystem','completed','completed','rev1','rev2'), ('e3','p1',3,'http_request','curl','network','interrupted','unknown','','')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e2',0,'destination','absent','first.txt')")
            db.execute("INSERT INTO invocation_program_publications VALUES ('p1','sess1234','committed',1)")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'interrupted', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'interrupted', 'status': 2, 'completed_effects': 1, 'uncertain_effects': 1, 'replay_available': False, 'stdout': '', 'stderr': ''})}),))
            db.commit(); db.close()
            turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            wm = {'http_requests': [{'seq': 1, 'method': 'POST', 'path': '/commit', 'body': 'payload'}]}
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta=wm)
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, IDENT)['passed'])
            obs_dup, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta={'http_requests': wm['http_requests'] * 2})
            self.assertFalse(obs_dup['facts']['http_attempts_one'])

            # Q17
            c17, p17, b17, db17, lp17 = self._base(root / 'q17', 'listener-rebind')
            db = sqlite3.connect(db17)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',1,'SANDBOX_LOOPBACK_CONNECT_DENIED')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'http_request','curl','outbound_http','refused','none')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['SANDBOX_LOOPBACK_CONNECT_DENIED'], 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'rejected', 'status': 1, 'stdout': '', 'stderr': 'curl: request: The person declined this connection to the local service [SANDBOX_LOOPBACK_CONNECT_DENIED]\n'})}),))
            db.commit(); db.close()
            t17 = [{'program': c17['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            obs17, a17 = observe.build_observation(c17, session_id='sess1234', identity=IDENT, project_dir=p17, db_path=db17, sidecar_log=lp17, baseline_hashes=b17, turn_records=t17, prelude_records=[], witness_meta={'owned_listener_succeeded': True, 'unrelated_hits': 0})
            self.assertEqual(a17['observation_gaps'], [])
            self.assertTrue(grading.grade_case(c17, obs17, p17, IDENT)['passed'])
            obs17_hit, _ = observe.build_observation(c17, session_id='sess1234', identity=IDENT, project_dir=p17, db_path=db17, sidecar_log=lp17, baseline_hashes=b17, turn_records=t17, prelude_records=[], witness_meta={'owned_listener_succeeded': True, 'unrelated_hits': 1})
            self.assertFalse(obs17_hit['facts']['no_request_sent'])
