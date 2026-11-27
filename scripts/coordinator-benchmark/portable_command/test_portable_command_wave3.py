"""Unit tests and negative controls for Wave 3: Q18 behavioral fsmonitor policy & Q16 job handle lifecycle."""
import json
import os
import pathlib
import sqlite3
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import focused_outcomes as oracle
from portable_command import case_oracles, grading, observe
from portable_command.contracts import load_suite, materialize
from portable_command.test_portable_command_harness import create_audit_db
from test_focused_outcomes import local_execute

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
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e0','p1',1,'git','git','git','refused','none'), ('e1','p2',1,'git add','git add','git','completed','completed'), ('e2','p2',2,'git_commit','git commit','git','completed','completed')")
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
            self.assertTrue(grading.exact_records(obs['receipts'], case['expected']['receipts']))
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
            good_probe = {
                'handles': ['bg-42'], 'call_ids': [], 'wrong_handle_rejected': True,
                'cross_session_refused': True, 'other_session_usable': True,
                'handle_outputs': {'bg-42': {'outcome': 'completed', 'returned_handle': 'bg-42', 'stdout': f'before\n{proj.resolve()}\n', 'running': False, 'exit_code': 0}},
            }
            ident = {'git_head': 'a' * 40}
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': good_probe}], prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, ident)['passed'])
            # Negative control (a): cross-session handle NOT refused -> owned_descendants_only MUST be False.
            obs_x, _ = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': {**good_probe, 'cross_session_refused': False}}], prelude_records=[])
            self.assertFalse(obs_x['facts']['owned_descendants_only'])
            # Negative control (b): wrong cwd in handle output -> captured_environment_cwd MUST be False.
            bad_cwd = {**good_probe, 'handle_outputs': {'bg-42': {**good_probe['handle_outputs']['bg-42'], 'stdout': 'before\n/wrong/cwd\n'}}}
            obs_cwd, _ = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': bad_cwd}], prelude_records=[])
            self.assertFalse(obs_cwd['facts']['captured_environment_cwd'])
            # Negative control (c): process_control=true escalation -> owned_descendants_only MUST be False.
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=false process_control=true component=sandbox tool=command session_id=sess1234\nlevel=INFO msg="shutdown signal received"\n')
            obs_pc, _ = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': good_probe}], prelude_records=[])
            self.assertFalse(obs_pc['facts']['owned_descendants_only'])

    def test_large_tree_catalog_generation_positive_and_negative_controls(self):
        case = CASES['large-tree-discovery']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
            proj.mkdir()
            create_audit_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db_path)
            db.execute("CREATE TABLE sessions (id TEXT PRIMARY KEY, project_id TEXT)")
            db.execute("CREATE TABLE source_inventory_state (project_id TEXT, requested_generation INTEGER, completed_generation INTEGER, phase TEXT, file_count INTEGER, snapshot_id TEXT)")
            db.execute("CREATE TABLE source_snapshots (id TEXT PRIMARY KEY, file_count INTEGER, capture_quality TEXT)")
            db.execute("CREATE TABLE source_snapshot_roots (snapshot_id TEXT, root_path TEXT, ordinal INTEGER)")
            db.execute("INSERT INTO sessions VALUES ('sess1234','proj1')")
            db.execute("INSERT INTO source_inventory_state VALUES ('proj1',2,2,'ready',10000,'snap1')")
            db.execute("INSERT INTO source_snapshots VALUES ('snap1',10000,'exact')")
            db.execute("INSERT INTO source_snapshot_roots VALUES ('snap1',?,0)", (str(proj.resolve()),))
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.commit(); db.close()
            obs, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs['facts']['catalog_generation_matches_live'])
            db = sqlite3.connect(db_path)
            db.execute("UPDATE source_inventory_state SET completed_generation=1 WHERE project_id='proj1'")
            db.commit(); db.close()
            obs_stale, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs_stale['facts']['catalog_generation_matches_live'])
            # bounded_visits parsed from sidecar.log "command discovery completed" by execution_id.
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN complete INTEGER DEFAULT 1")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,complete,effect_certainty) VALUES ('fe1','p1',1,'find','find','filesystem','completed',1,'none')")
            db.commit(); db.close()
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="command discovery completed" execution_id=fe1 entries=10001\nlevel=INFO msg="shutdown signal received"\n')
            obs_ok, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs_ok['facts']['bounded_visits'])
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="command discovery completed" execution_id=fe1 entries=999999\nlevel=INFO msg="shutdown signal received"\n')
            obs_over, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs_over['facts']['bounded_visits'])

    def _seed_task_env(self, root, case_id, task_arg):
        from portable_command.artifacts import project_hashes
        from portable_command.provenance import materialize_helpers
        case = CASES[case_id]
        proj, helpers = root / f'proj-{task_arg}', root / f'bin-{task_arg}'
        proj.mkdir()
        hdir = materialize_helpers(None, helpers, case_id=case_id)
        subprocess.run([str(hdir / 'fixture-task'), task_arg], cwd=proj, check=True)
        db_path, log_path = root / f'store-{task_arg}.db', root / f'sidecar-{task_arg}.log'
        create_audit_db(db_path)
        log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
        hashes = project_hashes(proj)
        db = sqlite3.connect(db_path)
        db.execute("CREATE TABLE sessions (id TEXT PRIMARY KEY, project_id TEXT)")
        db.execute("CREATE TABLE source_snapshots (id TEXT PRIMARY KEY, roots_key TEXT, capture_quality TEXT)")
        db.execute("CREATE TABLE source_snapshot_roots (snapshot_id TEXT, root_path TEXT, ordinal INTEGER, manifest_id TEXT)")
        db.execute("CREATE TABLE source_manifest_layout (manifest_id TEXT, chunk_id TEXT, first_path TEXT)")
        db.execute("CREATE TABLE source_manifest_entries (chunk_id TEXT, path TEXT, sha256 TEXT, git_oid TEXT, identity TEXT)")
        db.execute("INSERT INTO sessions VALUES ('sess1234','proj1')")
        db.execute("INSERT INTO source_snapshots VALUES ('snap-1','rk-1','exact')")
        db.execute("INSERT INTO source_snapshot_roots VALUES ('snap-1','.',0,'man-1')")
        db.execute("INSERT INTO source_manifest_layout VALUES ('man-1','chk-1','a')")
        for rel, sha in sorted(hashes.items()):
            db.execute("INSERT INTO source_manifest_entries VALUES ('chk-1',?,?,?,'hashed')", (rel, sha, ''))
        db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1), ('r2','sess1234','v1','verify','processes','completed',1)")
        db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,''), ('p2','r2','completed',0,'')")
        db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'command','external','processes','completed','unknown'), ('ev1','p2',1,'command','external','processes','completed','none')")
        db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),))
        db.commit(); db.close()
        ev_dir = root / 'projects' / 'proj1' / 'evidence'
        ev_dir.mkdir(parents=True, exist_ok=True)
        rec = {'type': 'verify', 'slot': 'tests', 'run_id': 'sess1234', 'verdict': 'passed', 'artifacts': {'session_id': 'sess1234', 'is_check': True, 'exit_code': 0, 'producer': 'verify', 'check_id': 'chk-1', 'source_revision': 'snap-1', 'source_root_digest': 'rk-1', 'cwd': '.', 'command': 'python3 -B -m unittest discover'}}
        (ev_dir / 'sess1234.jsonl').write_text(json.dumps(rec) + '\n')
        return case, proj, db_path, log_path

    def test_q20_fractional_logstat_and_wordle_positive_and_negative_controls(self):
        ident = {'git_head': 'a' * 40}
        with patch.object(oracle, 'execute', local_execute), tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # 1) fractional-logstat positive + negative controls
            case, proj, db_path, log_path = self._seed_task_env(root, 'fractional-logstat', 'logstat')
            turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'await_call_ids': ['v1']}]
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            grade = grading.grade_case(case, obs, proj, ident)
            self.assertTrue(grade['passed'], grade['dimensions'])
            (proj / 'result.json').write_text('{"count":3,"mean_ms":1,"levels":{"info":2}}\n')
            obs_bad, _ = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_bad['facts']['independent_artifact_oracle'])
            self.assertFalse(obs_bad['facts']['verification_source_match'])
            # 2) wordle-explicit-history positive + negative controls
            wcase, wproj, wdb, wlog = self._seed_task_env(root, 'wordle-explicit-history', 'wordle')
            wturns = [{'program': wcase['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'await_call_ids': ['v1']}]
            wobs, waudit = observe.build_observation(wcase, session_id='sess1234', identity=ident, project_dir=wproj, db_path=wdb, sidecar_log=wlog, baseline_hashes={}, turn_records=wturns, prelude_records=[])
            self.assertEqual(waudit['observation_gaps'], [])
            wgrade = grading.grade_case(wcase, wobs, wproj, ident)
            self.assertTrue(wgrade['passed'], wgrade['dimensions'])
            (wproj / 'history.json').write_text('{"secret":"apple","guesses":[{"word":"allee","marks":"GYYXG"},{"word":"apple","marks":"GGGGG"}],"attempts":2,"status":"won"}\n')
            wobs_bad, _ = observe.build_observation(wcase, session_id='sess1234', identity=ident, project_dir=wproj, db_path=wdb, sidecar_log=wlog, baseline_hashes={}, turn_records=wturns, prelude_records=[])
            self.assertFalse(wobs_bad['facts']['duplicate_aware_scoring'])
            (wproj / 'test_wordle.py').write_text('import unittest\nclass Bad(unittest.TestCase):\n def test_x(self): self.fail("bad")\n')
            wobs_tbad, _ = observe.build_observation(wcase, session_id='sess1234', identity=ident, project_dir=wproj, db_path=wdb, sidecar_log=wlog, baseline_hashes={}, turn_records=wturns, prelude_records=[])
            self.assertFalse(wobs_tbad['facts']['delivered_tests_separate'])

    def test_q21_external_launch_log_disk_provenance_controls(self):
        import hashlib
        case = CASES['external-argv']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            proj, db_path, log_path, helper = root / 'proj', root / 'store.db', root / 'sidecar.log', root / 'fixture-argv'
            proj.mkdir()
            helper.write_bytes(b'#!/bin/sh\necho ok\n')
            sha = hashlib.sha256(helper.read_bytes()).hexdigest()
            create_audit_db(db_path)
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'command','external','processes','completed','unknown')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '["a b"]\n', 'stderr': ''})}),))
            db.commit(); db.close()
            turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
            # Positive: matching execution_id + resolved path + on-disk SHA-256.
            log_path.write_text(f'level=INFO msg="server listening"\nlevel=INFO msg="command external launch" execution_id=e1 executable_path="{helper}" executable_sha256={sha}\nlevel=INFO msg="shutdown signal received"\n')
            obs, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertTrue(obs['facts']['disk_provenance'])
            # Negative (a): mismatched SHA-256 -> False.
            log_path.write_text(f'level=INFO msg="server listening"\nlevel=INFO msg="command external launch" execution_id=e1 executable_path="{helper}" executable_sha256={"0" * 64}\nlevel=INFO msg="shutdown signal received"\n')
            obs_bad, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_bad['facts']['disk_provenance'])
            # Negative (b): unattributed / foreign execution_id -> unobserved.
            log_path.write_text(f'level=INFO msg="server listening"\nlevel=INFO msg="command external launch" execution_id=other executable_path="{helper}" executable_sha256={sha}\nlevel=INFO msg="shutdown signal received"\n')
            obs_un, audit_un = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertNotIn('disk_provenance', obs_un['facts'])
            self.assertTrue(any('disk_provenance' in g for g in audit_un['observation_gaps']))
