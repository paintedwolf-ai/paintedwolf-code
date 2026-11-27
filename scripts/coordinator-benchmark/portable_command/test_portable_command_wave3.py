"""Unit tests and negative controls for Wave 3: Q18 behavioral fsmonitor policy & Q16 job handle lifecycle."""
import json
import os
import pathlib
import sqlite3
import subprocess
import tempfile
import unittest
from portable_command import case_oracles, grading, observe
from portable_command.contracts import load_suite, materialize
from portable_command.test_portable_command_harness import create_audit_db

CASES = {c['id']: c for c in load_suite()['cases']}


class PortableCommandWave3Tests(unittest.TestCase):
    def test_q18_behavioral_fsmonitor_policy_positive_and_negative_controls(self):
        case = CASES['git-concurrent-index']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'git-proj'
            create_audit_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            materialize(case, proj)
            git_setup = case_oracles.materialize_git_setup(case['setup']['git'], proj, case['setup']['files'])
            self.assertTrue(git_setup['marker_file'] and git_setup['config_sha256'])
            iso_env = {**os.environ, 'GIT_INDEX_FILE': str(root / 'isolated.index')}
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'read-tree', 'HEAD'], env=iso_env, check=True)
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'add', '--', 'authored.txt'], env=iso_env, check=True)
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'commit', '-m', 'Fixture change'], env=iso_env, check=True)
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1), ('r2','sess1234','c2','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_OPTION_UNSUPPORTED'), ('p2','r2','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p2',1,'git add','git add','git','completed','completed'), ('e2','p2',2,'git_commit','git commit','git','completed','completed')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?), ('m2','sess1234',2,'tool','c2',?)", (
                json.dumps({'outcome': 'rejected', 'codes': ['COMMAND_OPTION_UNSUPPORTED'], 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'rejected', 'status': 2, 'refusal': {'code': 'COMMAND_OPTION_UNSUPPORTED'}})}),
                json.dumps({'outcome': 'completed', 'content': '[command#2]\n' + json.dumps({'program_id': 'p2', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),
            ))
            db.commit(); db.close()
            turns = [
                {'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']},
                {'program': case['turns'][1]['program'], 'command_call_id': 'c2', 'call_ids': ['c2']},
            ]
            # Positive control: marker uninvoked, .git/config unchanged, -c core.fsmonitor refused, git exec bound.
            obs, audit = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, git_setup=git_setup, turn_records=turns, prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(obs['facts']['invocation_fsmonitor_policy'])
            # Negative control (a): marker helper invoked -> invocation_fsmonitor_policy MUST be False.
            marker = pathlib.Path(git_setup['marker_file'])
            marker.write_text('invoked\n')
            obs_m, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, git_setup=git_setup, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_m['facts']['invocation_fsmonitor_policy'])
            marker.unlink()
            # Negative control (b): .git/config mutated -> invocation_fsmonitor_policy MUST be False.
            cfg = proj / '.git' / 'config'
            orig_cfg = cfg.read_bytes()
            cfg.write_bytes(orig_cfg + b'\n# tampered\n')
            obs_c, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, git_setup=git_setup, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_c['facts']['invocation_fsmonitor_policy'])
            cfg.write_bytes(orig_cfg)
            # Negative control (c): override NOT refused -> invocation_fsmonitor_policy MUST be False.
            obs_no_ref, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, git_setup=git_setup, turn_records=[turns[1]], prelude_records=[])
            self.assertFalse(obs_no_ref['facts']['invocation_fsmonitor_policy'])

    def test_q16_job_handle_output_process_control_and_wrong_handle_controls(self):
        case = CASES['owned-background-state']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
            proj.mkdir()
            create_audit_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=false process_control=false component=sandbox tool=command session_id=sess1234\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN job_handle TEXT DEFAULT ''")
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN status_code INTEGER DEFAULT 0")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,status_code,effect_certainty,job_handle) VALUES ('w1','p1',0,'program &','','processes','completed',0,'none',''), ('e1','p1',1,'command','external','processes','completed',0,'unknown','bg-42'), ('e2','p1',2,'wait','wait','processes','completed',0,'none','')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),))
            db.commit(); db.close()
            good_probe = {'handles': ['bg-42'], 'call_ids': [], 'handle_outputs': {'bg-42': {'outcome': 'completed', 'returned_handle': 'bg-42', 'stdout': 'before\n', 'running': False, 'exit_code': 0}}, 'wrong_handle_rejected': True}
            ident = {'git_head': 'a' * 40}
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': good_probe}], prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, ident)['passed'])
            # Negative control (a): wrong-handle control NOT rejected -> owned_descendants_only MUST be False.
            bad_probe = {**good_probe, 'wrong_handle_rejected': False}
            obs_w, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': bad_probe}], prelude_records=[])
            self.assertFalse(obs_w['facts']['owned_descendants_only'])
            # Negative control (b): process_control=true escalation -> owned_descendants_only MUST be False.
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=false process_control=true component=sandbox tool=command session_id=sess1234\nlevel=INFO msg="shutdown signal received"\n')
            obs_pc, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': good_probe}], prelude_records=[])
            self.assertFalse(obs_pc['facts']['owned_descendants_only'])
