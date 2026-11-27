"""Focused unit controls for Q11, Q18, Q19, Q20, Q21, and Q22 audit and repository oracles."""
import hashlib
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
from portable_command import artifacts, case_oracles, grading, observe, provenance
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
            (proj / 'never').write_text('')
            prog_sha = hashlib.sha256(b'touch ./never').hexdigest()
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN status_code INTEGER DEFAULT 0")
            db.execute("CREATE TABLE invocation_program_sources (program_id TEXT, source_unit_id TEXT, source_id TEXT, revision TEXT, sha256 TEXT)")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1), ('r2','sess1234','c2','update_progress','progress','completed',1), ('r3','sess1234','c3','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'PROGRESS_MISSING'), ('p2','r3','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,status_code,effect_certainty) VALUES ('e1','p1',1,'touch','touch','filesystem','refused',2,'none'), ('e2','p2',1,'touch','touch','filesystem','completed',0,'completed')")
            db.execute("INSERT INTO invocation_program_sources VALUES ('p1','u1','inline','1',?)", (prog_sha,))
            db.execute("INSERT INTO invocation_program_outputs VALUES ('o1','p1','e1','source','output:program-source:p1',1,0,'touch ./never',0,1,?,'')", (prog_sha,))
            payload1 = {
                'program_id': 'p1', 'detail_ref': 'output:program-source:p1', 'state': 'rejected', 'status': 2, 'executed_children': 0,
                'stderr': 'touch: PROGRESS_MISSING [PROGRESS_MISSING]\n', 'stop_code': 'PROGRESS_MISSING',
                'completed_effects': 0, 'uncertain_effects': 0, 'replay_available': False,
                'refusal': {'code': 'PROGRESS_MISSING', 'feedback': 'Cause: Edits and delegated work start only after the deliverables are recorded in the progress checklist. Next: Record the checklist, then retry the same rejected call.\nCode: PROGRESS_MISSING', 'details': {'source_start_byte': 0, 'source_end_byte': 13}},
            }
            payload3 = {'program_id': 'p2', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''}
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?), ('m2','sess1234',2,'tool','c2',?), ('m3','sess1234',3,'tool','c3',?)", (
                json.dumps({'outcome': 'rejected', 'codes': ['PROGRESS_MISSING'], 'content': '[command#1]\n' + json.dumps(payload1)}),
                json.dumps({'outcome': 'completed', 'content': 'progress updated'}),
                json.dumps({'outcome': 'completed', 'content': '[command#2]\n' + json.dumps(payload3)}),
            ))
            db.commit(); db.close()
            turns = [
                {'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']},
                {'program': case['turns'][1]['program'], 'command_call_id': 'c2', 'call_ids': ['c2']},
                {'program': case['turns'][2]['program'], 'command_call_id': 'c3', 'call_ids': ['c3']},
            ]
            ident = {'git_head': 'a' * 40}
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs, proj, ident)['passed'])
            # Negative control: prefix-only detail_ref without matching source sha256 MUST fail validated_resubmission_reference.
            db = sqlite3.connect(db_path)
            db.execute("UPDATE invocation_program_sources SET sha256=? WHERE program_id='p1'", ('0' * 64,))
            db.commit(); db.close()
            obs_bad, _ = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_bad['facts']['validated_resubmission_reference'])
            # Negative control: unrelated refusal feedback MUST NOT fabricate remedy='update_progress'.
            db = sqlite3.connect(db_path)
            p_unrel = {**payload1, 'refusal': {'code': 'PROGRESS_MISSING', 'feedback': 'Command failed.', 'details': {}}}
            db.execute("UPDATE messages SET tool_result_json=? WHERE id='m1'", (json.dumps({'outcome': 'rejected', 'codes': ['PROGRESS_MISSING'], 'content': '[command#1]\n' + json.dumps(p_unrel)}),))
            db.commit(); db.close()
            obs_unrel, _ = observe.build_observation(case, session_id='sess1234', identity=ident, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertNotEqual(obs_unrel['recovery']['remedy'], 'update_progress')

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
            hdir = provenance.materialize_helpers(case['setup']['helpers'], pathlib.Path(tmp) / 'bin')
            out = subprocess.run([str(hdir / 'fixture-argv'), 'a b', 'x;touch never', 'https://example.test/a?b=c'], capture_output=True, text=True, check=True).stdout
            self.assertEqual(out, case['expected']['stdout'])

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
            hproj = root / 'hostile-proj'
            base_hostile = materialize(CASES['hostile-archive'], hproj)
            ok, bindings = case_oracles.materialize_host_facts(CASES['hostile-archive']['setup']['host_facts'], hproj)
            self.assertTrue(ok and bindings['archive_fixture']['injected'])
            self.assertTrue(artifacts.grade_artifacts(hproj, CASES['hostile-archive']))
            self.assertEqual(artifacts.project_hashes(hproj), base_hostile)
            with zipfile.ZipFile(hproj / 'hostile.zip') as zf:
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
            self.assertFalse(obs['facts']['invocation_fsmonitor_policy'])
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

    def test_q05_literal_delivery_edit_and_q06_unseen_output_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, _ = self._base_env(root)
            proj = root / 'lit-proj'
            base = materialize(CASES['literal-delivery-edit'], proj)
            (proj / 'input.txt').write_text('alpha\ngamma\n')
            db = sqlite3.connect(db_path)
            db.execute("CREATE TABLE invocation_program_deliveries (id TEXT PRIMARY KEY, output_id TEXT, response_id TEXT, delivery_id TEXT, start_byte INTEGER, end_byte INTEGER, source_owner_ref TEXT, mappings_json TEXT)")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1), ('r2','sess1234','c2','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,''), ('p2','r2','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e1','p1',1,'read','cat','filesystem','completed','none','',''), ('e2','p2',1,'write','edit','filesystem','completed','completed','rev1','rev2')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p2','e2',0,'destination','published_disk','input.txt')")
            db.execute("INSERT INTO invocation_program_publications VALUES ('p2','sess1234','committed',1)")
            db.execute("INSERT INTO invocation_program_outputs VALUES ('o1','p1','e1','stdout','ref:o1',1,0,'1\\talpha\\n2\\tbeta\\n',0,2,'','b'*64)")
            src_id = 'disk:' + str((proj / 'input.txt').resolve())
            m1 = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': base['input.txt'], 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 0, 'SourceEndByte': 6, 'OutputStartByte': 2, 'OutputEndByte': 8, 'PrintedLine': 1}])
            m2 = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': base['input.txt'], 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 6, 'SourceEndByte': 11, 'OutputStartByte': 10, 'OutputEndByte': 15, 'PrintedLine': 2}])
            db.execute("INSERT INTO invocation_program_deliveries VALUES ('d1','o1','resp1','del1',2,8,'owner:1',?), ('d2','o1','resp1','del1#1',10,15,'owner:1',?)", (m1, m2))
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?), ('m2','sess1234',2,'tool','c2',?)", (
                json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '1\talpha\n2\tbeta\n', 'stderr': ''})}),
                json.dumps({'outcome': 'completed', 'content': '[command#2]\n' + json.dumps({'program_id': 'p2', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),
            ))
            db.commit(); db.close()
            ident = {'git_head': 'a' * 40}
            obs, audit = observe.build_observation(
                CASES['literal-delivery-edit'], session_id='sess1234', identity=ident, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes=base,
                turn_records=[
                    {'program': CASES['literal-delivery-edit']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'envelope_mode': 'command_only'},
                    {'program': CASES['literal-delivery-edit']['turns'][1]['program'], 'command_call_id': 'c2', 'call_ids': ['c2'], 'envelope_mode': 'command_only'},
                ],
                prelude_records=[],
            )
            self.assertEqual(audit['observation_gaps'], [])
            grade = grading.grade_case(CASES['literal-delivery-edit'], obs, proj, ident)
            self.assertTrue(grade['passed'], grade['dimensions'])
            # Q06 unseen-output-no-edit-base: line 1 delivered -> unseen_ranges_not_read_bases=True; full file delivered -> False.
            db_q6 = root / 'q6.db'
            create_audit_db(db_q6)
            db = sqlite3.connect(db_q6)
            db.execute("CREATE TABLE invocation_program_deliveries (id TEXT PRIMARY KEY, output_id TEXT, response_id TEXT, delivery_id TEXT, start_byte INTEGER, end_byte INTEGER, source_owner_ref TEXT, mappings_json TEXT)")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'read','cat','filesystem','completed','none')")
            db.execute("INSERT INTO invocation_program_outputs VALUES ('o1','p1','e1','stdout','ref:o1',1,0,'alpha\\n',1,2,'',?)", ('b' * 64,))
            m_line1 = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': base['input.txt'], 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 0, 'SourceEndByte': 6, 'OutputStartByte': 0, 'OutputEndByte': 6}])
            db.execute("INSERT INTO invocation_program_deliveries VALUES ('d1','o1','resp1','del1',0,6,'owner:1',?)", (m_line1,))
            db.commit(); db.close()
            obs_q6, _ = observe.build_observation(CASES['unseen-output-no-edit-base'], session_id='sess1234', identity=ident, project_dir=proj, db_path=db_q6, sidecar_log=log_path, baseline_hashes=base, turn_records=[{'program': 'cat ./input.txt', 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertEqual(obs_q6['deliveries'], [{'path': 'input.txt', 'revision': 'baseline:input.txt', 'lines': [1, 1], 'representation': 'literal', 'delivered': True}])
            self.assertTrue(obs_q6['facts']['unseen_ranges_not_read_bases'])
            db = sqlite3.connect(db_q6)
            full_len = len(CASES['unseen-output-no-edit-base']['setup']['files']['input.txt'].encode('utf-8'))
            m_full = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': base['input.txt'], 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 0, 'SourceEndByte': full_len, 'OutputStartByte': 0, 'OutputEndByte': full_len}])
            db.execute("UPDATE invocation_program_deliveries SET end_byte=?, mappings_json=? WHERE id='d1'", (full_len, m_full))
            db.commit(); db.close()
            obs_q6_bad, _ = observe.build_observation(CASES['unseen-output-no-edit-base'], session_id='sess1234', identity=ident, project_dir=proj, db_path=db_q6, sidecar_log=log_path, baseline_hashes=base, turn_records=[{'program': 'cat ./input.txt', 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs_q6_bad['facts']['unseen_ranges_not_read_bases'])
            # Non-delivery cases (e.g. oar-option-correction) must keep deliveries=[] even when cat records a delivery row.
            obs_oar, _ = observe.build_observation(CASES['oar-option-correction'], session_id='sess1234', identity=ident, project_dir=proj, db_path=db_q6, sidecar_log=log_path, baseline_hashes=base, turn_records=[{'program': 'cat ./input.txt', 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertEqual(obs_oar['deliveries'], [])

    def test_q20_bounded_visits_and_q21_confined_launch_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = self._base_env(root)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=false component=sandbox tool=command session_id=sess1234 network=deny\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN complete INTEGER DEFAULT 0")
            db.execute("CREATE TABLE invocation_program_revision_pin_segments (program_id TEXT, execution_id TEXT, first_ordinal INTEGER, pin_count INTEGER, body_sha256 TEXT)")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,complete) VALUES ('e1','p1',1,'find','find','filesystem','completed','none',1), ('e2','p1',2,'command','external','processes','completed','none',1)")
            db.execute("INSERT INTO invocation_program_revision_pin_segments VALUES ('p1','e1',0,10000,?)", ('c' * 64,))
            db.commit(); db.close()
            obs, _ = observe.build_observation(CASES['large-tree-discovery'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': CASES['large-tree-discovery']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs['facts']['bounded_visits'])
            self.assertTrue(obs['facts']['confined_launch'])
            # Negative controls: >250000 pins -> bounded_visits=False; host_execution=true -> confined_launch=False.
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=true component=sandbox tool=command session_id=sess1234 network=deny\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db_path)
            db.execute("UPDATE invocation_program_revision_pin_segments SET pin_count=250001 WHERE execution_id='e1'")
            db.commit(); db.close()
            obs_bad, _ = observe.build_observation(CASES['large-tree-discovery'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': CASES['large-tree-discovery']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs_bad['facts']['bounded_visits'])
            self.assertFalse(obs_bad['facts']['confined_launch'])

    def test_q11_temp_handoff_and_q16_owned_background_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # 1) Helper materialization for Q11 fixture-reader & Q16 fixture-job.
            h_q11 = provenance.materialize_helpers(None, root / 'bin-q11', case_id='temp-native-process-handoff')
            sample = root / 'sample.txt'
            sample.write_text('same\n')
            self.assertEqual(subprocess.run([str(h_q11 / 'fixture-reader'), str(sample)], capture_output=True, text=True, check=True).stdout, 'same\n')
            h_q16 = provenance.materialize_helpers(None, root / 'bin-q16', case_id='owned-background-state')
            q16_out = subprocess.run([str(h_q16 / 'fixture-job')], cwd=root, env={**os.environ, 'FIXTURE': 'before'}, capture_output=True, text=True, check=True).stdout.splitlines()
            self.assertEqual(q16_out, ['before', str(root.resolve())])
            # 2) Q11 positive + negative (two different scratch paths -> same_physical_identity=False).
            db_path, log_path, proj = self._base_env(root)
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_revision_pins ADD COLUMN basis TEXT DEFAULT ''")
            db.execute("ALTER TABLE invocation_program_revision_pins ADD COLUMN disk_sha256 TEXT DEFAULT ''")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'mktemp','mktemp','filesystem','completed','completed'), ('e2','p1',2,'write','>','filesystem','completed','completed'), ('e3','p1',3,'command','external','processes','completed','none')")
            db.execute("INSERT INTO invocation_program_revision_pins (program_id,execution_id,ordinal,role,mode,path,basis,disk_sha256) VALUES ('p1','e1',0,'destination','absent','@scratch/tmp.1','captured',''), ('p1','e2',0,'destination','published_disk','@scratch/tmp.1','captured',''), ('p1','e3',0,'source','published_disk','bin/fixture-reader','declared_at_launch',?)", ('d' * 64,))
            db.commit(); db.close()
            obs11, audit11 = observe.build_observation(CASES['temp-native-process-handoff'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': CASES['temp-native-process-handoff']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertEqual(audit11['observation_gaps'], [])
            self.assertTrue(obs11['facts']['temp_path_owned'] and obs11['facts']['same_physical_identity'] and obs11['facts']['external_disk_provenance'])
            # 3) Q16 positive + negative (mismatched exit / stdout='after\n' -> False).
            db16 = root / 'q16.db'
            create_audit_db(db16)
            log16 = root / 'q16.log'
            log16.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=false process_control=false component=sandbox tool=command session_id=sess1234\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db16)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN job_handle TEXT DEFAULT ''")
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN status_code INTEGER DEFAULT 0")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,status_code,effect_certainty,job_handle) VALUES ('e1','p1',1,'command','external','processes','completed',0,'none','job-1'), ('e2','p1',2,'wait','wait','processes','completed',0,'none','')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': 'before\n', 'stderr': ''})}),))
            db.commit(); db.close()
            probe16 = {
                'handles': ['job-1'], 'call_ids': [], 'wrong_handle_rejected': True,
                'cross_session_refused': True, 'other_session_usable': True,
                'handle_outputs': {'job-1': {'outcome': 'completed', 'returned_handle': 'job-1', 'stdout': f'before\n{proj.resolve()}\n', 'running': False, 'exit_code': 0}},
            }
            obs16, audit16 = observe.build_observation(CASES['owned-background-state'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db16, sidecar_log=log16, baseline_hashes={}, turn_records=[{'program': CASES['owned-background-state']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': probe16}], prelude_records=[])
            self.assertEqual(audit16['observation_gaps'], [])
            for fact in CASES['owned-background-state']['required_evidence']:
                self.assertTrue(obs16['facts'][fact], fact)
            # Negative control: absent job_probe MUST fail (no fallback to foreground stdout).
            obs16_noprobe, _ = observe.build_observation(CASES['owned-background-state'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db16, sidecar_log=log16, baseline_hashes={}, turn_records=[{'program': CASES['owned-background-state']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs16_noprobe['facts']['captured_environment_cwd'])
            self.assertFalse(obs16_noprobe['facts']['actual_exit'])
            self.assertFalse(obs16_noprobe['facts']['owned_descendants_only'])
