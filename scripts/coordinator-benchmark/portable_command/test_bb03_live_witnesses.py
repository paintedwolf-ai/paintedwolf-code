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
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,arguments_digest) VALUES ('e1','p1',1,'sed','sed','filesystem','refused','unknown',?)", ('d' * 64,))
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_REVISION_CONFLICT'], 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'conflict', 'status': 2, 'stderr': case['expected']['stderr'], 'refusal': {'code': 'COMMAND_REVISION_CONFLICT'}})}),))
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
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_PUBLICATION_PENDING')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e1','p1',1,'printf','printf','processes','completed','none','',''), ('e2','p1',2,'write','','filesystem','refused','attempted','rev2','')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e2',0,'destination','accepted_document','dir/input.txt')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_PUBLICATION_PENDING'], 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'rejected', 'status': 2, 'stdout': '', 'stderr': case['expected']['stderr'], 'refusal': {'code': 'COMMAND_PUBLICATION_PENDING'}})}),))
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
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',52,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e1','p1',1,'printf','printf','processes','completed','none','',''), ('e2','p1',2,'write','','filesystem','completed','completed','rev1','rev2'), ('e3','p1',3,'http_request','curl','network','failed','unknown','','')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e2',0,'destination','absent','first.txt')")
            db.execute("INSERT INTO invocation_program_publications VALUES ('p1','sess1234','committed',1)")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 52, 'completed_effects': 1, 'uncertain_effects': 1, 'replay_available': False, 'stdout': '', 'stderr': 'curl: (52) Empty reply from server\n'})}),))
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
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'http_request','curl','outbound_http','refused','unknown')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['SANDBOX_LOOPBACK_CONNECT_DENIED'], 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'rejected', 'status': 1, 'stdout': '', 'stderr': 'curl: request: The person declined this connection to the local service [SANDBOX_LOOPBACK_CONNECT_DENIED]\n'})}),))
            db.commit(); db.close()
            t17 = [{'program': c17['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            obs17, a17 = observe.build_observation(c17, session_id='sess1234', identity=IDENT, project_dir=p17, db_path=db17, sidecar_log=lp17, baseline_hashes=b17, turn_records=t17, prelude_records=[], witness_meta={'owned_listener_succeeded': True, 'unrelated_hits': 0})
            self.assertEqual(a17['observation_gaps'], [])
            self.assertTrue(grading.grade_case(c17, obs17, p17, IDENT)['passed'])
            obs17_hit, _ = observe.build_observation(c17, session_id='sess1234', identity=IDENT, project_dir=p17, db_path=db17, sidecar_log=lp17, baseline_hashes=b17, turn_records=t17, prelude_records=[], witness_meta={'owned_listener_succeeded': True, 'unrelated_hits': 1})
            self.assertFalse(obs17_hit['facts']['no_request_sent'])

    def test_cancellation_abort_witness_positive_and_negative_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            case, proj, base, dbp, lp = self._base(pathlib.Path(tmp), 'cancellation')
            db = sqlite3.connect(dbp)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','interrupted',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','interrupted',130,'TOOL_OWNER_INTERRUPTED')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'sleep','sleep','processes','completed','none'), ('e2','p1',2,'sleep','sleep','processes','completed','none'), ('e3','p1',3,'sleep','sleep','processes','interrupted','none')")
            db.commit(); db.close()
            turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            wm = {
                'abort_barrier': 'active_child_running_verified', 'aborted_child_running_pre_abort': True,
                'aborted_child_started': True, 'aborted_child_invoked': True,
                'aborted_child_certainty': 'none', 'aborted_child_status': 130,
                'db_program': {'state': 'interrupted', 'status_code': 130, 'stop_code': 'TOOL_OWNER_INTERRUPTED'},
                'exec_count_at_idle': 3, 'exec_count_after_wait': 3, 'unsettled_children': 0, 'recovery_ok': True,
                'st01_st03_stop_fence_separate': True,
            }
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta=wm)
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, IDENT)['passed'])
            # Pre-start cancellation control (started=0, invoked=0, certainty=unknown): distinct schedule MUST NOT pass active-child barrier or loosen authority.
            db = sqlite3.connect(dbp)
            db.execute("UPDATE invocation_program_executions SET effect_certainty='unknown' WHERE id='e3'")
            db.commit(); db.close()
            pre_wm = {**wm, 'aborted_child_started': False, 'aborted_child_invoked': False, 'aborted_child_certainty': 'unknown', 'aborted_child_status': 1}
            obs_pre, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta=pre_wm)
            self.assertFalse(obs_pre['facts']['admission_closed'])
            g_pre = grading.grade_case(case, obs_pre, proj, IDENT)
            self.assertFalse(g_pre['dimensions']['authority'])
            self.assertFalse(g_pre['passed'])
            db = sqlite3.connect(dbp)
            db.execute("UPDATE invocation_program_executions SET effect_certainty='none' WHERE id='e3'")
            db.commit(); db.close()
            # Negative controls: later child admitted, unsettled child, or 'after' file leaked -> fail.
            obs_later, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta={**wm, 'exec_count_after_wait': 4})
            self.assertFalse(obs_later['facts']['no_later_child'])
            obs_unsettled, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta={**wm, 'unsettled_children': 1})
            self.assertFalse(obs_unsettled['facts']['owned_resources_reclaimed'])
            (proj / 'after').write_text('leaked\n')
            obs_leak, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes=base, turn_records=turns, prelude_records=[], witness_meta=wm)
            self.assertFalse(obs_leak['facts']['no_later_child'])
