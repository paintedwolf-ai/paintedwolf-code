"""Unit controls for declared read-only workflow profiles and Q07/Q08/Q09 authority oracles."""
import json
import pathlib
import sqlite3
import tempfile
import unittest
from portable_command import observe, profile_session
from portable_command.contracts import load_suite, materialize
from portable_command.test_portable_command_harness import create_audit_db

CASES = {c['id']: c for c in load_suite()['cases']}


class PortableCommandReadonlyAuthorityTests(unittest.TestCase):
    def _env(self, root, case_id):
        db_path, log_path, proj = root / f'store-{case_id}.db', root / f'sidecar-{case_id}.log', root / f'proj-{case_id}'
        create_audit_db(db_path)
        log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
        base = materialize(CASES[case_id], proj)
        return db_path, log_path, proj, base

    def _surface(self, profile, offered=('command', 'read', 'search')):
        return {
            'requested_profile': profile, 'requested_posture': 'build', 'effective_posture': 'build',
            'offered_tools': list(offered), 'declared_profile_bound': True, 'workflow_id': f'pc-{profile}',
        }

    def test_q07_static_and_dynamic_readonly_positive_and_negative_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # 1) read-only-static-refusal positive
            db_path, log_path, proj, base = self._env(root, 'read-only-static-refusal')
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_EFFECT_NOT_ALLOWED')")
            payload = {
                'program_id': 'p1', 'state': 'rejected', 'status': 2, 'completed_effects': 0, 'uncertain_effects': 0,
                'refusal': {'code': 'COMMAND_EFFECT_NOT_ALLOWED', 'feedback': 'worker_readonly forbids mutation; inspect only.', 'details': {'source_line': 1}},
            }
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_EFFECT_NOT_ALLOWED'], 'content': '[command#1]\n' + json.dumps(payload)}),))
            db.commit(); db.close()
            obs, audit = observe.build_observation(
                CASES['read-only-static-refusal'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes=base, session_surface=self._surface('worker_readonly'),
                turn_records=[{'program': CASES['read-only-static-refusal']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[],
            )
            self.assertEqual(audit['observation_gaps'], [])
            for fact in CASES['read-only-static-refusal']['required_evidence']:
                self.assertTrue(obs['facts'][fact], fact)
            # Negative control: leaked mutation tool 'write' in offered_tools -> readonly_profile & reachable_readonly_remedy MUST be False.
            obs_leak, _ = observe.build_observation(
                CASES['read-only-static-refusal'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes=base, session_surface=self._surface('worker_readonly', ('command', 'write')),
                turn_records=[{'program': CASES['read-only-static-refusal']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[],
            )
            self.assertFalse(obs_leak['facts']['readonly_profile'])
            self.assertFalse(obs_leak['facts']['reachable_readonly_remedy'])
            # 2) read-only-dynamic-refusal positive
            db_d, log_d, proj_d, base_d = self._env(root, 'read-only-dynamic-refusal')
            db = sqlite3.connect(db_d)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_EFFECT_NOT_ALLOWED')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'read','cat','filesystem','completed','none'), ('e2','p1',2,'delete','rm','filesystem','refused','none')")
            payload_d = {
                'program_id': 'p1', 'state': 'rejected', 'status': 2, 'completed_effects': 0, 'uncertain_effects': 0,
                'refusal': {'code': 'COMMAND_EFFECT_NOT_ALLOWED', 'feedback': 'explore_readonly cannot delete; inspect or close out.', 'details': {'operation_source_line': 1}},
            }
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_EFFECT_NOT_ALLOWED'], 'content': '[command#1]\n' + json.dumps(payload_d)}),))
            db.commit(); db.close()
            obs_d, audit_d = observe.build_observation(
                CASES['read-only-dynamic-refusal'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj_d,
                db_path=db_d, sidecar_log=log_d, baseline_hashes=base_d, session_surface=self._surface('explore_readonly'),
                turn_records=[{'program': CASES['read-only-dynamic-refusal']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[],
            )
            self.assertEqual(audit_d['observation_gaps'], [])
            self.assertEqual([(r['operation'], r['outcome']) for r in obs_d['receipts']], [('read', 'completed'), ('delete', 'rejected')])
            self.assertEqual(obs_d['recovery']['cause'], 'readonly_profile')
            self.assertEqual(obs_d['recovery']['remedy'], 'readonly_or_closeout')
            for fact in CASES['read-only-dynamic-refusal']['required_evidence']:
                self.assertTrue(obs_d['facts'][fact], fact)

    def test_q09_sed_awk_and_q08_oar_unavailable_remedy_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # 1) embedded-sed-write-refusal
            db_s, log_s, proj_s, base_s = self._env(root, 'embedded-sed-write-refusal')
            db = sqlite3.connect(db_s)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_EFFECT_NOT_ALLOWED')")
            p_sed = {'program_id': 'p1', 'state': 'rejected', 'status': 2, 'completed_effects': 0, 'uncertain_effects': 0,
                     'refusal': {'code': 'COMMAND_EFFECT_NOT_ALLOWED', 'feedback': 'worker_readonly forbids sed w.', 'details': {'source_line': 1}}}
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_EFFECT_NOT_ALLOWED'], 'content': '[command#1]\n' + json.dumps(p_sed)}),))
            db.commit(); db.close()
            obs_s, audit_s = observe.build_observation(
                CASES['embedded-sed-write-refusal'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj_s,
                db_path=db_s, sidecar_log=log_s, baseline_hashes=base_s, session_surface=self._surface('worker_readonly'),
                turn_records=[{'program': CASES['embedded-sed-write-refusal']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[],
            )
            self.assertEqual(audit_s['observation_gaps'], [])
            for fact in CASES['embedded-sed-write-refusal']['required_evidence']:
                self.assertTrue(obs_s['facts'][fact], fact)
            # 2) embedded-awk-exec-refusal
            db_a, log_a, proj_a, base_a = self._env(root, 'embedded-awk-exec-refusal')
            db = sqlite3.connect(db_a)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_NATIVE_FORM_UNSUPPORTED')")
            p_awk = {'program_id': 'p1', 'state': 'rejected', 'status': 2, 'completed_effects': 0, 'uncertain_effects': 0,
                     'refusal': {'code': 'COMMAND_NATIVE_FORM_UNSUPPORTED', 'feedback': 'awk system() disabled.', 'details': {'expected': 'no system()', 'source_line': 1}}}
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_NATIVE_FORM_UNSUPPORTED'], 'content': '[command#1]\n' + json.dumps(p_awk)}),))
            db.commit(); db.close()
            obs_a, audit_a = observe.build_observation(
                CASES['embedded-awk-exec-refusal'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj_a,
                db_path=db_a, sidecar_log=log_a, baseline_hashes=base_a, session_surface=self._surface('worker_readonly'),
                turn_records=[{'program': CASES['embedded-awk-exec-refusal']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[],
            )
            self.assertEqual(audit_a['observation_gaps'], [])
            for fact in CASES['embedded-awk-exec-refusal']['required_evidence']:
                self.assertTrue(obs_a['facts'][fact], fact)
            # 3) oar-unavailable-remedy positive + negative (mutating remedy suggested)
            db_o, log_o, proj_o, base_o = self._env(root, 'oar-unavailable-remedy')
            db = sqlite3.connect(db_o)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_EFFECT_NOT_ALLOWED')")
            p_oar = {'program_id': 'p1', 'state': 'rejected', 'status': 2, 'completed_effects': 0, 'uncertain_effects': 0,
                     'refusal': {'code': 'COMMAND_EFFECT_NOT_ALLOWED', 'feedback': 'plan_review_readonly cannot modify files; report limitation.', 'details': {'source_line': 1}}}
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_EFFECT_NOT_ALLOWED'], 'content': '[command#1]\n' + json.dumps(p_oar)}),))
            db.commit(); db.close()
            obs_o, audit_o = observe.build_observation(
                CASES['oar-unavailable-remedy'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj_o,
                db_path=db_o, sidecar_log=log_o, baseline_hashes=base_o, session_surface=self._surface('plan_review_readonly'),
                turn_records=[{'program': CASES['oar-unavailable-remedy']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[],
            )
            self.assertEqual(audit_o['observation_gaps'], [])
            self.assertEqual(obs_o['recovery']['cause'], 'unavailable_remedy')
            self.assertEqual(obs_o['recovery']['remedy'], 'honest_blocked_closeout')
            for fact in CASES['oar-unavailable-remedy']['required_evidence']:
                self.assertTrue(obs_o['facts'][fact], fact)
            self.assertFalse(profile_session.has_no_mutating_remedy('Please use edit instead.'))
            self.assertFalse(profile_session.has_refusal_source_span({'source_line': 0}))
            # Refused sed/awk map to rejected while touch stays refused (preserving progress-then-denial).
            rows, _, _ = observe._receipt_rows({
                'invocation_program_executions': [
                    {'id': 'e1', 'program_id': 'p1', 'operation_id': 'sed', 'native_command': 'sed', 'owner': 'filesystem', 'state': 'refused', 'effect_certainty': 'none'},
                    {'id': 'e2', 'program_id': 'p1', 'operation_id': 'awk', 'native_command': 'awk', 'owner': 'processes', 'state': 'refused', 'effect_certainty': 'none'},
                    {'id': 'e3', 'program_id': 'p1', 'operation_id': 'touch', 'native_command': 'touch', 'owner': 'filesystem', 'state': 'refused', 'effect_certainty': 'none'},
                ],
                'invocation_program_publications': [], 'invocation_program_revision_pins': [],
            }, [])
            self.assertEqual([(r['operation'], r['outcome']) for r in rows], [('sed', 'rejected'), ('awk', 'rejected'), ('touch', 'refused')])
