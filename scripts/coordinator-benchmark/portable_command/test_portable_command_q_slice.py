"""Focused unit controls for Q04, Q20, and Q21 owner-backed observations."""
import hashlib
import io
import json
import pathlib
import sqlite3
import tarfile
import tempfile
import unittest
import zipfile
from portable_command import observe, provenance
from portable_command.contracts import load_suite
from portable_command.test_portable_command_harness import create_audit_db

CASES = {c['id']: c for c in load_suite()['cases']}


class PortableCommandQSliceTests(unittest.TestCase):
    def _base_env(self, root):
        db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
        create_audit_db(db_path)
        log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
        proj.mkdir()
        return db_path, log_path, proj

    def test_first_mutation_missing_progress_zero_effects_and_resubmission_ref(self):
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
            self.assertTrue(obs['facts']['validated_resubmission_reference'])

    def test_masked_upstream_failure_catches_ineligible_published_write(self):
        case = CASES['masked-upstream-failure']
        with tempfile.TemporaryDirectory() as tmp:
            db_path, log_path, proj = self._base_env(pathlib.Path(tmp))
            (proj / 'result.txt').write_text('old\n')
            base = {'result.txt': hashlib.sha256(b'old\n').hexdigest()}
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN publication_eligible INTEGER DEFAULT 1")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',1,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,publication_eligible) VALUES ('e1','p1',1,'command','external','processes','completed','none',0)")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,publication_eligible) VALUES ('e2','p1',2,'read','cat','filesystem','completed','none',1)")
            db.commit(); db.close()
            obs_ok, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs_ok['facts']['every_producer_eligible'])
            self.assertTrue(obs_ok['facts']['destination_unchanged'])
            # If a write WAS published despite ineligible producer, every_producer_eligible MUST be False.
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,published_revision,publication_eligible) VALUES ('e3','p1',3,'write','>','filesystem','completed','completed','rev-2',1)")
            db.execute("INSERT INTO invocation_program_publications VALUES ('p1','sess1234','committed',1)")
            db.commit(); db.close()
            obs_bad, _ = observe.build_observation(case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs_bad['facts']['every_producer_eligible'])

    def test_external_argv_disk_provenance_and_screening_remain_owner_backed(self):
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
            # confined_launch stays an explicit named missing_observation when not emitted by owner.
            self.assertIn('missing_observation:confined_launch:process_owner', audit['observation_gaps'])

    def test_q19_archive_cross_decoder_hostile_zip_and_temp_namespace(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = self._base_env(root)
            # 1) archive-create-extract: real bzip2 cross-decoder + confined entries + tar_extract path.
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
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,canonical) VALUES ('e1','p1',1,'extract_archive','tar','filesystem','completed','completed','tar -cjf ./bundle.tar.bz2 ./input.txt'), ('e2','p1',2,'mkdir','mkdir','filesystem','completed','completed','mkdir -p ./out'), ('e3','p1',3,'extract_archive','tar','filesystem','completed','completed','tar -xjf ./bundle.tar.bz2 -C ./out')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e1',0,'destination','absent','bundle.tar.bz2')")
            db.execute("INSERT INTO invocation_program_publications VALUES ('p1','sess1234','committed',1)")
            db.commit(); db.close()
            obs, audit = observe.build_observation(CASES['archive-create-extract'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': CASES['archive-create-extract']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs['facts']['real_bzip2_cross_decoder'] and obs['facts']['confined_archive_entries'] and obs['facts']['recoverable_publication'])
            self.assertEqual([e.get('path') for e in obs['effects']], ['bundle.tar.bz2', 'out', 'out/input.txt'])
            # 2) hostile-archive: materialize_host_facts writes hostile.zip with '../escape.txt'.
            ok, bindings = provenance.materialize_host_facts(CASES['hostile-archive']['setup']['host_facts'], proj)
            self.assertTrue(ok and bindings['archive_fixture']['injected'])
            with zipfile.ZipFile(proj / 'hostile.zip') as zf:
                self.assertEqual(zf.namelist(), ['../escape.txt'])
            # 3) temp-native-process-handoff: program $() wrapper + session-temp namespace.
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
