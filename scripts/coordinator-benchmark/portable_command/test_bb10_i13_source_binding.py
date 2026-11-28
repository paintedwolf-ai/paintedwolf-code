"""BB10 I13 systemic invariant tests: claims bind to actual source and scope."""
import hashlib
import json
import pathlib
import sqlite3
import tempfile
import unittest
from portable_command import grading, observe, reporting
from portable_command.contracts import load_suite, materialize
from portable_command.delivery_oracles import extract_deliveries
from portable_command.test_portable_command_harness import create_audit_db

CASES = {c['id']: c for c in load_suite()['cases']}
IDENT = {'git_head': 'a' * 40}


def _sha(text):
    return hashlib.sha256(text.encode('utf-8')).hexdigest()


def _init_delivery_db(db_path):
    create_audit_db(db_path)
    db = sqlite3.connect(db_path)
    db.execute("CREATE TABLE invocation_program_deliveries (id TEXT PRIMARY KEY, output_id TEXT, response_id TEXT, delivery_id TEXT, start_byte INTEGER, end_byte INTEGER, source_owner_ref TEXT, mappings_json TEXT)")
    db.execute("ALTER TABLE invocation_program_revision_pins ADD COLUMN disk_sha256 TEXT DEFAULT ''")
    db.execute("ALTER TABLE invocation_program_revision_pins ADD COLUMN content_sha256 TEXT DEFAULT ''")
    db.commit()
    db.close()


class BB10I13SourceBindingTests(unittest.TestCase):
    def _seed_q05(self, root, *, read_path='input.txt', edit_src_path='input.txt', read_sha=None, edit_sha=None,
                  owner_ref='owner:1', src_id='root-1/input.txt', m1_end=6, m1_out_end=8):
        proj = root / 'proj'
        base = materialize(CASES['literal-delivery-edit'], proj)
        (proj / 'other.txt').write_text('alpha\nbeta\n')  # Equal bytes at a different path
        (proj / 'input.txt').write_text('alpha\ngamma\n')
        db_path, log_path = root / 'store.db', root / 'sidecar.log'
        _init_delivery_db(db_path)
        log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
        r_sha = read_sha if read_sha is not None else base['input.txt']
        e_sha = edit_sha if edit_sha is not None else base['input.txt']
        db = sqlite3.connect(db_path)
        db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1), ('r2','sess1234','c2','command','processes','completed',1)")
        db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,''), ('p2','r2','completed',0,'')")
        db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e1','p1',1,'read','cat','filesystem','completed','none','',''), ('e2','p2',1,'edit','edit','filesystem','completed','completed','rev1','rev2')")
        db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e1',0,'source','published_disk',?,?,?), ('p2','e2',0,'source','published_disk',?,?,?), ('p2','e2',1,'destination','published_disk','input.txt',?,?)",
                   (read_path, r_sha, r_sha, edit_src_path, e_sha, e_sha, e_sha, e_sha))
        db.execute("INSERT INTO invocation_program_publications VALUES ('p2','sess1234','committed',1)")
        db.execute("INSERT INTO invocation_program_outputs VALUES ('o1','p1','e1','stdout','ref:o1',1,0,'1\\talpha\\n2\\tbeta\\n',0,2,'',?)", ('b' * 64,))
        m1 = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': r_sha, 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 0, 'SourceEndByte': m1_end, 'OutputStartByte': 2, 'OutputEndByte': m1_out_end, 'PrintedLine': 1}])
        m2 = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': r_sha, 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 6, 'SourceEndByte': 11, 'OutputStartByte': 10, 'OutputEndByte': 15, 'PrintedLine': 2}])
        db.execute("INSERT INTO invocation_program_deliveries VALUES ('d1','o1','resp1','del1',2,8,?,?), ('d2','o1','resp1','del1#1',10,15,?,?)", (owner_ref, m1, owner_ref, m2))
        db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?), ('m2','sess1234',2,'tool','c2',?)", (
            json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '1\talpha\n2\tbeta\n', 'stderr': ''})}),
            json.dumps({'outcome': 'completed', 'content': '[command#2]\n' + json.dumps({'program_id': 'p2', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),
        ))
        db.commit(); db.close()
        (proj / 'other.txt').unlink()  # Remove extra file for clean Q05 grade check unless testing path mismatch
        turns = [
            {'program': CASES['literal-delivery-edit']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'envelope_mode': 'command_only'},
            {'program': CASES['literal-delivery-edit']['turns'][1]['program'], 'command_call_id': 'c2', 'call_ids': ['c2'], 'envelope_mode': 'command_only'},
        ]
        return proj, base, db_path, log_path, turns

    def test_numbered_literal_delivery_and_synthetic_mutation_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # Positive control: exact numbered-literal delivery + matching read/edit source pins.
            proj, base, db_path, log_path, turns = self._seed_q05(root / 'ok')
            obs, audit = observe.build_observation(CASES['literal-delivery-edit'], session_id='sess1234', identity=IDENT, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=turns, prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            self.assertTrue(grading.grade_case(CASES['literal-delivery-edit'], obs, proj, IDENT)['passed'])

            # Mutation controls: equal bytes at different path, stale revision, mutated range, missing source owner.
            cases = [
                ('equal_bytes_different_read_path', {'read_path': 'other.txt'}, 'anchored_edit'),
                ('equal_bytes_different_edit_path', {'edit_src_path': 'other.txt'}, 'anchored_edit'),
                ('stale_edit_revision_pin', {'edit_sha': 'f' * 64}, 'anchored_edit'),
                ('missing_source_owner_ref', {'owner_ref': ''}, 'delivery_owner_identity'),
                ('transformed_range_length_mismatch', {'m1_end': 6, 'm1_out_end': 9}, 'exact_mapped_ranges'),
                ('wrong_source_identity_path', {'src_id': 'root-2/other.txt'}, 'anchored_edit'),
            ]
            for label, kwargs, fact in cases:
                with self.subTest(mutation=label):
                    mproj, mbase, mdb, mlog, mturns = self._seed_q05(root / label, **kwargs)
                    mobs, _ = observe.build_observation(CASES['literal-delivery-edit'], session_id='sess1234', identity=IDENT, project_dir=mproj, db_path=mdb, sidecar_log=mlog, baseline_hashes=mbase, turn_records=mturns, prelude_records=[])
                    self.assertFalse(mobs['facts'].get(fact), f'{label} must fail {fact}')
                    self.assertFalse(grading.grade_case(CASES['literal-delivery-edit'], mobs, mproj, IDENT)['passed'])

    def test_clipped_delivery_whole_doc_conversational_rewrite_vs_partial_read(self):
        case = CASES['unseen-output-no-edit-base']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            proj, db_path, log_path = root / 'proj', root / 'store.db', root / 'sidecar.log'
            base = materialize(case, proj)
            _init_delivery_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            src_id = 'root-1/input.txt'
            m_clip = json.dumps([{'Source': {'SourceID': src_id, 'SHA256': base['input.txt'], 'Representation': 'utf8-no-bom'}, 'SourceStartByte': 0, 'SourceEndByte': 6, 'OutputStartByte': 0, 'OutputEndByte': 6}])
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1), ('r2','sess1234','c2','code_rewrite','filesystem','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,''), ('p2','r2','rejected',2,'EDITOR_DOCUMENT_READ_REQUIRED')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty) VALUES ('e1','p1',1,'read','cat','filesystem','completed','none')")
            db.execute("INSERT INTO invocation_program_outputs VALUES ('o1','p1','e1','stdout','ref:o1',1,0,'alpha\\n',1,202,'',?)", ('b' * 64,))
            db.execute("INSERT INTO invocation_program_deliveries VALUES ('d1','o1','resp1','del1',0,6,'owner:1',?)", (m_clip,))
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?), ('m2','sess1234',2,'tool','c2',?)", (
                json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': 'alpha\n', 'stderr': ''})}),
                json.dumps({'outcome': 'rejected', 'codes': ['EDITOR_DOCUMENT_READ_REQUIRED'], 'content': '[code_rewrite#2]\n' + json.dumps({'program_id': 'p2', 'state': 'rejected', 'status': 2, 'refusal': {'code': 'EDITOR_DOCUMENT_READ_REQUIRED'}})}),
            ))
            db.commit(); db.close()
            turns = [{'program': 'cat ./input.txt', 'command_call_id': 'c1', 'call_ids': ['c1']}, {'program': 'code-rewrite', 'command_call_id': 'c2', 'call_ids': ['c2']}]
            obs_refused, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=turns, prelude_records=[])
            self.assertTrue(obs_refused['facts']['unseen_ranges_not_read_bases'])
            # Product discrepancy control: if whole-doc conversational rewrite accepts unseen span, oracle MUST reject.
            db = sqlite3.connect(db_path)
            db.execute("UPDATE invocation_programs SET state='completed', status_code=0, stop_code='' WHERE id='p2'")
            db.execute("UPDATE messages SET tool_result_json=? WHERE id='m2'", (json.dumps({'outcome': 'completed', 'content': '[code_rewrite#2]\n' + json.dumps({'program_id': 'p2', 'state': 'completed', 'status': 0})}),))
            db.commit(); db.close()
            obs_leaked, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_leaked['facts']['unseen_ranges_not_read_bases'])

    def test_verified_source_binding_rejects_equal_bytes_wrong_path_or_foreign_exec(self):
        case = CASES['draft-read-transform']
        draft_sha = _sha(case['setup']['drafts']['settings.json']['bytes'])
        base_sha = _sha(case['setup']['files']['settings.json'])
        execs = [{'id': 'e1'}]
        good_pin = [{'execution_id': 'e1', 'role': 'source', 'mode': 'accepted_document', 'path': 'settings.json', 'content_sha256': draft_sha}]
        self.assertTrue(reporting.verified_source_binding(case, good_pin, execs, {'settings.json': base_sha})[0])
        for bad in (
            [{'execution_id': 'e1', 'role': 'source', 'mode': 'accepted_document', 'path': 'other.json', 'content_sha256': draft_sha}],
            [{'execution_id': 'foreign', 'role': 'source', 'mode': 'accepted_document', 'path': 'settings.json', 'content_sha256': draft_sha}],
            [{'execution_id': 'e1', 'role': 'source', 'mode': 'accepted_document', 'path': 'settings.json', 'content_sha256': '0' * 64}],
        ):
            self.assertFalse(reporting.verified_source_binding(case, bad, execs, {'settings.json': base_sha})[0])

    def test_full_vs_clipped_extract_deliveries_rejects_non_literal_transform(self):
        case = CASES['literal-delivery-edit']
        sha = _sha('alpha\nbeta\n')
        audit = {
            'invocation_program_outputs': [{'id': 'o1', 'stream': 'stdout', 'preview_truncated': 0}],
            'invocation_program_deliveries': [{'output_id': 'o1', 'source_owner_ref': 'owner:1', 'mappings_json': json.dumps([
                {'Source': {'SourceID': 'root/input.txt', 'SHA256': sha}, 'SourceStartByte': 0, 'SourceEndByte': 11, 'OutputStartByte': 0, 'OutputEndByte': 11},
            ])}],
        }
        deliveries, facts = extract_deliveries(case, audit, {'input.txt': sha})
        self.assertEqual(deliveries[0]['lines'], [1, 2])
        self.assertTrue(facts['exact_mapped_ranges'] and facts['delivery_owner_identity'])
        # Transformed byte span (OutputEndByte != SourceEndByte) fails exact_mapped_ranges.
        audit['invocation_program_deliveries'][0]['mappings_json'] = json.dumps([
            {'Source': {'SourceID': 'root/input.txt', 'SHA256': sha}, 'SourceStartByte': 0, 'SourceEndByte': 11, 'OutputStartByte': 0, 'OutputEndByte': 15},
        ])
        _, bad_facts = extract_deliveries(case, audit, {'input.txt': sha})
        self.assertFalse(bad_facts.get('exact_mapped_ranges', False))

    def test_clean_open_draft_read_input_revision_requires_exact_pin_digest(self):
        from portable_command.db_audit import receipt_rows
        sha = _sha('alpha\nbeta\n')
        base_audit = {
            'invocation_receipts': [], 'invocation_programs': [], 'invocation_program_publications': [],
            'invocation_program_executions': [{'id': 'e1', 'program_id': 'p1', 'operation_id': 'read', 'native_command': 'head', 'owner': 'filesystem', 'state': 'completed', 'effect_certainty': 'none'}],
            'invocation_program_revision_pins': [{'execution_id': 'e1', 'role': 'source', 'mode': 'published_disk', 'path': 'input.txt', 'content_sha256': sha, 'disk_sha256': sha}],
        }
        good_bind = {'input.txt': {'injected': True, 'document_id': 'd1', 'revision': 2, 'alias': 'draft-r1', 'sha256': sha}}
        rows, _, _ = receipt_rows(base_audit, [], draft_bindings=good_bind)
        self.assertEqual(rows[0].get('input_revision'), 'draft-r1')
        bad_bind = {'input.txt': {**good_bind['input.txt'], 'sha256': '0' * 64}}
        rows_bad, _, _ = receipt_rows(base_audit, [], draft_bindings=bad_bind)
        self.assertNotIn('input_revision', rows_bad[0])
