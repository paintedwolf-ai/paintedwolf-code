"""Focused unit controls for Q11, Q18, Q19, Q20, Q21, and Q22 audit and repository oracles."""
import io
import json
import os
import pathlib
import sqlite3
import subprocess
import tarfile
import tempfile
import unittest
import zipfile
from portable_command import artifacts, case_oracles, observe
from portable_command.contracts import load_suite, materialize
from portable_command.test_portable_command_harness import create_audit_db

CASES = {c['id']: c for c in load_suite()['cases']}


class PortableCommandQSliceTests(unittest.TestCase):
    def _base_env(self, root):
        db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
        create_audit_db(db_path)
        log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
        proj.mkdir()
        return db_path, log_path, proj

    def test_q20_first_mutation_missing_progress_zero_effects_and_resubmission_ref(self):
        case = CASES['first-mutation-missing-progress']
        with tempfile.TemporaryDirectory() as tmp:
            db_path, log_path, proj = self._base_env(pathlib.Path(tmp))
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'PROGRESS_MISSING')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'touch','touch','filesystem','refused','none')")
            payload = {
                'program_id': 'p1', 'detail_ref': 'output:program-source:p1', 'state': 'rejected', 'status': 2,
                'stop_code': 'PROGRESS_MISSING', 'completed_effects': 0, 'uncertain_effects': 0, 'replay_available': False,
                'refusal': {'code': 'PROGRESS_MISSING', 'details': {'expected_form': 'update_progress'}},
            }
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'rejected', 'codes': ['PROGRESS_MISSING'], 'content': '[command#1]\n' + json.dumps(payload)}),))
            db.commit(); db.close()
            obs, audit = observe.build_observation(
                case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes={},
                turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[],
            )
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(obs['facts']['missing_progress_owner_code'])
            self.assertTrue(obs['facts']['zero_program_effects'])
            self.assertEqual(obs['recovery']['cause'], 'missing_progress')
            self.assertEqual(obs['recovery']['remedy'], 'update_progress')
            self.assertTrue(obs['recovery']['zero_program_effects'] and obs['recovery']['effect_certainty_required'])
            self.assertTrue(obs['facts']['validated_resubmission_reference'])

    def test_q21_external_argv_disk_provenance_and_confined_launch_gap(self):
        case = CASES['external-argv']
        with tempfile.TemporaryDirectory() as tmp:
            db_path, log_path, proj = self._base_env(pathlib.Path(tmp))
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_revision_pins ADD COLUMN basis TEXT DEFAULT ''")
            db.execute("ALTER TABLE invocation_program_revision_pins ADD COLUMN disk_sha256 TEXT DEFAULT ''")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'command','external','processes','completed','none')")
            db.execute("INSERT INTO invocation_program_revision_pins (program_id,execution_id,ordinal,role,mode,path,basis,disk_sha256) VALUES ('p1','e1',0,'source','published_disk','bin/fixture-argv','declared_at_launch',?)", ('b' * 64,))
            payload = {'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '["a b","x;touch never","https://example.test/a?b=c"]\n', 'stderr': ''}
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps(payload)}),))
            db.commit(); db.close()
            obs, audit = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs['facts']['exact_argv'])
            self.assertTrue(obs['facts']['disk_provenance'])
            self.assertIn('missing_observation:confined_launch:process_owner', audit['observation_gaps'])

    def test_q19_archive_cross_decoder_hostile_zip_and_q11_temp_namespace(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = self._base_env(root)
            (proj / 'out').mkdir()
            (proj / 'out' / 'input.txt').write_text('hello\n')
            with tarfile.open(proj / 'bundle.tar.bz2', 'w:bz2') as tf:
                info = tarfile.TarInfo('input.txt')
                info.size = 6
                tf.addfile(info, io.BytesIO(b'hello\n'))
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN canonical TEXT DEFAULT ''")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,canonical) VALUES ('e1','p1',1,'extract_archive','tar','filesystem','completed','completed','tar -cjf ./bundle.tar.bz2 ./input.txt'), ('e2','p1',2,'mkdir','mkdir','filesystem','completed','completed','mkdir -p ./out;'), ('e3','p1',3,'extract_archive','tar','filesystem','completed','completed','tar -xjf ./bundle.tar.bz2 -C ./out')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e1',0,'destination','absent','bundle.tar.bz2')")
            db.execute("INSERT INTO invocation_program_publications VALUES ('p1','sess1234','committed',1)")
            db.commit(); db.close()
            obs, _ = observe.build_observation(CASES['archive-create-extract'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': CASES['archive-create-extract']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs['facts']['real_bzip2_cross_decoder'] and obs['facts']['confined_archive_entries'] and obs['facts']['recoverable_publication'])
            self.assertEqual([e.get('path') for e in obs['effects']], ['bundle.tar.bz2', 'out', 'out/input.txt'])
            ok, bindings = case_oracles.materialize_host_facts(CASES['hostile-archive']['setup']['host_facts'], proj)
            self.assertTrue(ok and bindings['archive_fixture']['injected'])
            with zipfile.ZipFile(proj / 'hostile.zip') as zf:
                self.assertEqual(zf.namelist(), ['../escape.txt'])
            audit_tmp = {
                'invocation_program_executions': [
                    {'id': 'w1', 'program_id': 'p1', 'operation_id': 'program $()', 'native_command': '', 'owner': 'processes', 'state': 'completed', 'effect_certainty': 'none'},
                    {'id': 'e1', 'program_id': 'p1', 'operation_id': 'mktemp', 'native_command': 'mktemp', 'owner': 'filesystem', 'state': 'completed', 'effect_certainty': 'completed'},
                ],
                'invocation_program_publications': [],
                'invocation_program_revision_pins': [{'execution_id': 'e1', 'role': 'destination', 'mode': 'absent', 'path': '/tmp/app/scratch/sess1234/tmp.abc'}],
            }
            rows, effs, wrappers = observe._receipt_rows(audit_tmp, [])
            self.assertEqual((len(wrappers), rows[0]['operation'], effs[0]['namespace']), (1, 'mktemp', 'session-temp'))

    def test_q18_git_concurrent_index_positive_and_negative_controls(self):
        case = CASES['git-concurrent-index']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, _ = self._base_env(root)
            proj = root / 'git-proj'
            materialize(case, proj)
            git_setup = case_oracles.materialize_git_setup(case['setup']['git'], proj, case['setup']['files'])
            self.assertTrue(git_setup['initialized'] and len(git_setup['initial_head']) == 40)
            self.assertTrue(artifacts.grade_artifacts(proj, case))
            iso_env = {**os.environ, 'GIT_INDEX_FILE': str(root / 'isolated.index')}
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'read-tree', 'HEAD'], env=iso_env, check=True)
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'add', '--', 'authored.txt'], env=iso_env, check=True)
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'commit', '-m', 'Fixture change'], env=iso_env, check=True)
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'git add','git add','git','completed','completed'), ('e2','p1',2,'git_commit','git commit','git','completed','completed')")
            db.commit(); db.close()
            obs, audit = observe.build_observation(
                case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path,
                sidecar_log=log_path, baseline_hashes={}, git_setup=git_setup,
                turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[],
            )
            self.assertEqual([r['operation'] for r in obs['receipts']], ['git_add', 'git_commit'])
            self.assertEqual([e.get('paths') for e in obs['effects']], [['authored.txt'], ['authored.txt']])
            self.assertTrue(obs['facts']['foreign_index_preserved'] and obs['facts']['session_authored_paths'] and obs['facts']['immutable_ref_evidence'])
            self.assertIn('missing_observation:invocation_fsmonitor_policy:git_owner', audit['observation_gaps'])
            subprocess.run(['git', '-C', str(proj), '-c', 'core.fsmonitor=false', 'commit', '-m', 'Accidental foreign commit'], check=True)
            obs_bad, _ = observe.build_observation(
                case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path,
                sidecar_log=log_path, baseline_hashes={}, git_setup=git_setup,
                turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[],
            )
            self.assertFalse(obs_bad['facts']['foreign_index_preserved'])
            self.assertFalse(obs_bad['facts']['session_authored_paths'])

    def test_q22_paired_manifest_contract_zero_model_controls(self):
        case = CASES['paired-manifest-contract']
        with tempfile.TemporaryDirectory() as tmp:
            db_path, log_path, proj = self._base_env(pathlib.Path(tmp))
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'true','true','processes','completed','none')")
            db.commit(); db.close()
            good_id = {'git_head': 'a' * 40, 'git_tree': 'b' * 40, 'suite_sha256': 'c' * 64, 'engine_provenance': {'provenance_verified': True}}
            obs, audit = observe.build_observation(
                case, session_id='sess1234', identity=good_id, project_dir=proj, db_path=db_path,
                sidecar_log=log_path, baseline_hashes={},
                turn_records=[{'program': 'true', 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[],
            )
            self.assertEqual(audit['observation_gaps'], [])
            for fact in case['required_evidence']:
                self.assertTrue(obs['facts'][fact], fact)
            bad_id = {**good_id, 'engine_provenance': {'provenance_verified': False}}
            obs_bad, _ = observe.build_observation(
                case, session_id='sess1234', identity=bad_id, project_dir=proj, db_path=db_path,
                sidecar_log=log_path, baseline_hashes={},
                turn_records=[{'program': 'true', 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[],
            )
            self.assertFalse(obs_bad['facts']['immutable_identity'])
