"""Unit tests for the zero-model Portable Command harness, observation adapter, and CLI."""
import io
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import cli, harness, observe
from portable_command.contracts import load_suite

CASES = {c['id']: c for c in load_suite()['cases']}


def create_audit_db(db_path):
    db = sqlite3.connect(db_path)
    db.executescript('''
        CREATE TABLE invocation_receipts (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, tool_name TEXT, owner TEXT, status TEXT, invoked INTEGER);
        CREATE TABLE invocation_programs (id TEXT PRIMARY KEY, parent_receipt_id TEXT, state TEXT, status_code INTEGER, stop_code TEXT);
        CREATE TABLE invocation_program_executions (id TEXT PRIMARY KEY, program_id TEXT, ordinal INTEGER, operation_id TEXT, native_command TEXT, owner TEXT, state TEXT, effect_certainty TEXT, accepted_revision TEXT, published_revision TEXT, native_gap_json TEXT);
        CREATE TABLE invocation_program_revision_pins (program_id TEXT, execution_id TEXT, ordinal INTEGER, role TEXT, mode TEXT, path TEXT);
        CREATE TABLE invocation_program_outputs (id TEXT PRIMARY KEY, program_id TEXT, execution_id TEXT, stream TEXT, reference TEXT, complete INTEGER, redacted INTEGER, preview TEXT, preview_truncated INTEGER, line_count INTEGER, inline_digest TEXT, blob_id TEXT);
        CREATE TABLE invocation_program_publications (program_id TEXT PRIMARY KEY, session_id TEXT, state TEXT, materialized INTEGER);
        CREATE TABLE invocation_program_progress (execution_id TEXT PRIMARY KEY, program_id TEXT, session_id TEXT, state TEXT, owner_invoked INTEGER, detail_ref TEXT);
        CREATE TABLE source_command_windows (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, state TEXT);
        CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, ord INTEGER, role TEXT, tool_result_call_id TEXT, tool_result_json TEXT);
    ''')
    db.commit()
    db.close()


class FakeSSE:
    def __init__(self, url, token, out_path):
        self.out_path = pathlib.Path(out_path)
        self.closed = False

    def start(self):
        self.out_path.write_text('{"topic":"activity"}\n')

    def close(self):
        self.closed = True


class FakeApp:
    """In-process fake matching smoke.Application for deterministic unit & defect-control coverage."""
    def __init__(self, directory, mode='publish'):
        self.directory = pathlib.Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.url = 'http://127.0.0.1:1'
        self.token = 'tok'
        self.mode = mode
        self.process = None
        self.closed = False
        self.project_root = None
        self.session_id = 'sess1234-0000-0000-0000-000000000001'
        self.turn = 0
        self.pending_req = None
        if mode == 'incomplete-log':
            (self.directory / 'sidecar.log').write_text('unrelated noise\n')
        elif mode == 'truncated-startup':
            (self.directory / 'sidecar.log').write_text('level=INFO msg="server listening" addr=127.0.0.1:1\n')
        elif mode == 'truncated-session':
            (self.directory / 'sidecar.log').write_text(
                'level=INFO msg="server listening" addr=127.0.0.1:1\n'
                'level=INFO msg="session active" session_id=sess1234\n'
            )
        elif mode != 'missing-log':
            log_line = 'level=INFO msg="server listening" addr=127.0.0.1:1\n'
            if mode == 'external-launch':
                log_line += 'level=INFO msg="process boundary selected" tool=command session_id=sess1234…\n'
            (self.directory / 'sidecar.log').write_text(log_line)
        create_audit_db(self.directory / 'store.db')

    def ready(self):
        return None

    def close(self, cleanup=True):
        self.closed = True
        if self.mode not in {'missing-log', 'incomplete-log', 'truncated-startup', 'truncated-session'}:
            with (self.directory / 'sidecar.log').open('a') as f:
                f.write('level=INFO msg="shutdown signal received" signal=terminated\n')

    def wait_for(self, fn, phase, session=None, timeout=10):
        res = fn()
        if not res:
            raise RuntimeError('fake timeout: ' + phase)
        return res

    def request(self, path, body=None, missing=False, method=None):
        if path == '/v1/projects':
            self.project_root = pathlib.Path(body['roots'][0]['path'])
            return {'id': 'proj-1'}
        if path == '/v1/sessions':
            return {'id': self.session_id}
        if path == f'/v1/sessions/{self.session_id}':
            return {'id': self.session_id, 'status': 'idle'}
        if path.endswith('/prompts'):
            self.pending_req = {'pending': True, 'id': 'req-1', 'session_id': self.session_id, 'tools': ['command', 'update_progress']}
            return {'operation_id': body['operation_id']}
        if path == '/harness/llm/pending':
            return self.pending_req or {'pending': False}
        if path == '/harness/llm/respond':
            self._handle_tool_calls(body['tool_calls'])
            self.turn += 1
            self.pending_req = {'pending': True, 'id': f'req-{self.turn+1}', 'session_id': self.session_id, 'tools': ['command', 'update_progress']}
            return {'ok': True}
        if path.endswith('/invocations'):
            return {'invocations': []}
        if path.endswith('/messages'):
            return {'messages': []}
        return {}

    def _handle_tool_calls(self, calls):
        db = sqlite3.connect(self.directory / 'store.db')
        for idx, tc in enumerate(calls, start=1):
            cid, name = tc['id'], tc['name']
            if name == 'update_progress':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'update_progress','progress','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps({'outcome': 'completed', 'content': '{}'})))
            elif name == 'command' and self.mode in {'publish', 'missing-pub', 'injected-extra'}:
                (self.project_root / 'result.txt').write_bytes(b'created\n')
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','completed',1)", ('rcpt-cmd', self.session_id, cid))
                if self.mode == 'injected-extra':
                    db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-extra',?,'unissued-call-id','command','processes','completed',1)", (self.session_id,))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','completed',0,'')")
                if 'progress_update' in (tc.get('args') or {}):
                    db.execute("INSERT INTO invocation_program_progress VALUES (?,'prog-1',?,'committed',1,'')", (f'prog-exec-{cid}', self.session_id))
                db.execute("INSERT INTO invocation_program_executions VALUES ('ex-1','prog-1',1,'printf','printf','processes','completed','none','','','')")
                pub_rev = 'rev-1' if self.mode != 'missing-pub' else ''
                db.execute("INSERT INTO invocation_program_executions VALUES ('ex-2','prog-1',2,'write','>','filesystem','completed','completed','',?,'')", (pub_rev,))
                db.execute("INSERT INTO invocation_program_revision_pins VALUES ('prog-1','ex-2',0,'destination','absent','result.txt')")
                if self.mode != 'missing-pub':
                    db.execute("INSERT INTO invocation_program_publications VALUES ('prog-1',?,'committed',1)", (self.session_id,))
                payload = {'program_id': 'prog-1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': '', 'completed_effects': 1, 'uncertain_effects': 0, 'native_gaps': 0}
                res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps(payload)}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
            elif name == 'command':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", ('rcpt-cmd', self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','rejected',2,'COMMAND_OPTION_UNSUPPORTED')")
                payload = {
                    'program_id': 'prog-1', 'state': 'rejected', 'status': 2, 'stop_code': 'COMMAND_OPTION_UNSUPPORTED',
                    'stdout': '', 'stderr': '', 'completed_effects': 0, 'uncertain_effects': 0, 'native_gaps': 0, 'replay_available': True,
                    'refusal': {'code': 'COMMAND_OPTION_UNSUPPORTED', 'feedback': 'Usage: cat [-n] [FILE...]',
                                'details': {'source_start_byte': 4, 'source_end_byte': 28, 'expected_form': 'cat [-n] [FILE...]'}},
                }
                res = {'outcome': 'rejected', 'codes': ['COMMAND_OPTION_UNSUPPORTED'], 'content': '[command#1]\n' + json.dumps(payload)}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
        db.commit()
        db.close()


class PortableCommandHarnessTests(unittest.TestCase):
    def test_known_publication_same_turn_batch_and_bundle_validation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                res = harness.run_case(
                    CASES['first-mutation'], root / 'run1', export_dir=root / 'export',
                    app_factory=lambda d: FakeApp(d, mode='publish'), sse_factory=FakeSSE,
                )
            self.assertTrue(res['grade']['passed'])
            self.assertEqual(res['audit']['turn_batches'][0]['batch_plan'], ['command'])
            self.assertEqual(res['audit']['turn_batches'][0]['envelope_mode'], 'native_command_envelope')
            bundle_path = root / 'export' / 'sanitized-fixture.json'
            self.assertEqual(harness.resolve_case(str(bundle_path))['id'], 'first-mutation')
            for mutate in (
                lambda b: b['turns'][0].update(program='printf hacked > ./result.txt'),
                lambda b: b.pop('baseline_hashes'),
                lambda b: b.update(baseline_hashes=['not-a-dict']),
            ):
                bad = json.loads(bundle_path.read_text())
                mutate(bad)
                bad_path = root / 'bad.json'
                bad_path.write_text(json.dumps(bad))
                with self.assertRaises(ValueError):
                    harness.resolve_case(str(bad_path))
            # Mismatched baseline_hashes rejected before engine startup.
            mismatch = json.loads(bundle_path.read_text())
            mismatch['baseline_hashes'] = {'extra.txt': 'deadbeef'}
            mis_path = root / 'mismatch.json'
            mis_path.write_text(json.dumps(mismatch))
            started = []
            with self.assertRaises(ValueError):
                harness.run_case(harness.resolve_case(str(mis_path)), root / 'misrun', app_factory=lambda d: started.append(d))
            self.assertEqual(started, [])

    def test_missing_publication_and_injected_extra_command_fail_grades(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                mp = harness.run_case(CASES['first-mutation'], root / 'mp', app_factory=lambda d: FakeApp(d, mode='missing-pub'), sse_factory=FakeSSE)
                extra = harness.run_case(CASES['first-mutation'], root / 'ex', app_factory=lambda d: FakeApp(d, mode='injected-extra'), sse_factory=FakeSSE)
            self.assertFalse(mp['grade']['dimensions']['receipts'])
            self.assertFalse(mp['grade']['dimensions']['authority'])
            self.assertTrue(extra['grade']['forbidden_replay'])
            self.assertFalse(extra['grade']['passed'])
            self.assertEqual(extra['audit']['unexpected_receipt_ids'], ['rcpt-extra'])

    def test_refusal_positive_launch_missing_and_incomplete_observer_controls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                clean = harness.run_case(CASES['native-option-no-fallback'], root / 'clean', app_factory=lambda d: FakeApp(d, mode='refuse'), sse_factory=FakeSSE)
                launched = harness.run_case(CASES['native-option-no-fallback'], root / 'launched', app_factory=lambda d: FakeApp(d, mode='external-launch'), sse_factory=FakeSSE)
                missing = harness.run_case(CASES['native-option-no-fallback'], root / 'missing', app_factory=lambda d: FakeApp(d, mode='missing-log'), sse_factory=FakeSSE)
                incomp = harness.run_case(CASES['native-option-no-fallback'], root / 'incomp', app_factory=lambda d: FakeApp(d, mode='incomplete-log'), sse_factory=FakeSSE)
                trunc_start = harness.run_case(CASES['native-option-no-fallback'], root / 'ts', app_factory=lambda d: FakeApp(d, mode='truncated-startup'), sse_factory=FakeSSE)
                trunc_sess = harness.run_case(CASES['native-option-no-fallback'], root / 'tss', app_factory=lambda d: FakeApp(d, mode='truncated-session'), sse_factory=FakeSSE)
            self.assertTrue(clean['observation']['facts']['no_external_launch'])
            self.assertTrue((root / 'clean' / 'raw' / 'window.json').is_file())
            self.assertFalse(launched['observation']['facts']['no_external_launch'])
            self.assertFalse(missing['observation']['facts']['no_external_launch'])
            self.assertIn('missing_sidecar_log', missing['audit']['observation_gaps'])
            for bad in (incomp, trunc_start, trunc_sess):
                self.assertFalse(bad['observation']['facts']['no_external_launch'])
                self.assertIn('incomplete_sidecar_log', bad['audit']['observation_gaps'])

    def test_cli_modes_and_unknown_case(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_result = {'case_id': 'first-mutation', 'session_id': 's1', 'grade': {'qualification': 'native-runtime', 'passed': True, 'dimensions': {'artifacts': True}}}
            with patch('sys.stdout', new=io.StringIO()):
                self.assertEqual(cli.main(['--case', 'first-mutation', '--out', str(root / 'o1')], runner=lambda c, o, export_dir=None: fake_result), 0)
                self.assertEqual(cli.main(['--replay', 'first-mutation', '--out', str(root / 'o2')], runner=lambda c, o, export_dir=None: {**fake_result, 'grade': {**fake_result['grade'], 'passed': False}}), 1)
            with self.assertRaises(ValueError):
                harness.resolve_case('nonexistent-case-id')

    def test_multi_header_parse_draft_input_revision_null_sink_helpers_and_readonly(self):
        # Multi-header [read#1 ...]\n[command#2]\n{...} parses stdout cleanly.
        _, parsed = observe.parse_tool_result({'tool_result_json': json.dumps({
            'outcome': 'completed', 'content': '[read#1 input.txt:1]\n[command#2]\n{"status":0,"stdout":"alpha\\n"}',
        })})
        self.assertEqual(parsed['stdout'], 'alpha\n')
        # Draft input_revision bound strictly from accepted_document pin + draft_bindings.
        audit = {
            'invocation_program_executions': [{'id': 'e1', 'program_id': 'p1', 'operation_id': 'read', 'native_command': 'cat', 'state': 'completed', 'effect_certainty': 'none'}],
            'invocation_program_publications': [],
            'invocation_program_revision_pins': [{'execution_id': 'e1', 'role': 'source', 'mode': 'accepted_document', 'path': 'settings.json', 'document_id': 'doc-1', 'document_revision': 2}],
        }
        r_ok, _, _ = observe._receipt_rows(audit, [], draft_bindings={'settings.json': {'injected': True, 'document_id': 'doc-1', 'revision': 2, 'alias': 'draft-r2'}})
        self.assertEqual(r_ok[0]['input_revision'], 'draft-r2')
        r_bad, _, _ = observe._receipt_rows(audit, [], draft_bindings={'settings.json': {'injected': True, 'document_id': 'doc-1', 'revision': 9, 'alias': 'draft-r2'}})
        self.assertNotIn('input_revision', r_bad[0])
        # Helper executable materialization + literal-null-sink + readonly surface facts.
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            hdir = harness.materialize_helpers({'fixture-producer': {'stdout': 'alpha', 'stderr': '', 'status': 1}, 'fixture-argv': {'kind': 'argv-json'}}, root / 'helpers')
            self.assertTrue((hdir / 'fixture-producer').is_file() and (hdir / 'fixture-argv').is_file())
            db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
            create_audit_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            proj.mkdir()
            obs, _ = observe.build_observation(
                CASES['literal-null-sink'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes={},
                turn_records=[{'program': 'printf hidden > /dev/null; printf kept', 'command_call_id': 'c1', 'call_ids': ['c1']}],
                prelude_records=[], session_surface={'effective_posture': 'vet'},
            )
            self.assertTrue(obs['facts']['typed_null_sink'])
            self.assertTrue(obs['facts']['readonly_profile'])

    def test_structural_wrapper_audit_and_uncorrelated_progress_receipt_flagged(self):
        # Wrapper with an effect is NOT silently excluded; clean wrapper IS audited.
        audit_clean = {
            'invocation_program_executions': [{'id': 'w1', 'program_id': 'p1', 'operation_id': 'program |', 'native_command': '', 'owner': 'processes', 'state': 'completed', 'effect_certainty': 'none'}],
            'invocation_program_publications': [], 'invocation_program_revision_pins': [],
        }
        r_clean, _, wrappers = observe._receipt_rows(audit_clean, [])
        self.assertEqual(r_clean, [])
        self.assertEqual(len(wrappers), 1)
        audit_eff = {**audit_clean, 'invocation_program_executions': [dict(audit_clean['invocation_program_executions'][0], effect_certainty='completed')]}
        r_eff, _, w_eff = observe._receipt_rows(audit_eff, [])
        self.assertEqual(len(r_eff), 1)
        self.assertEqual(w_eff, [])
        # Extra uncorrelated standalone update_progress receipt is flagged as unexpected even on a progress_update case.
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
            create_audit_db(db_path)
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-uncorr','sess1234','rogue-call','update_progress','progress','completed',1)")
            db.commit()
            db.close()
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            proj.mkdir()
            _, audit_out = observe.build_observation(
                CASES['first-mutation'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[], prelude_records=[],
            )
            self.assertIn('rcpt-uncorr', audit_out['unexpected_receipt_ids'])

    def test_slice2c_source_later_syntax_external_argv_and_tar_mappings(self):
        # Canonical owner mappings for external_exec, tar_create, tar_extract, and source refused->rejected.
        audit = {
            'invocation_program_executions': [
                {'id': 'e1', 'program_id': 'p1', 'operation_id': 'command', 'native_command': 'external', 'owner': 'processes', 'state': 'completed', 'effect_certainty': 'none'},
                {'id': 'e2', 'program_id': 'p1', 'operation_id': 'extract_archive', 'native_command': 'tar', 'owner': 'filesystem', 'canonical': 'tar -cjf ./b.tar.bz2 ./a', 'state': 'completed', 'effect_certainty': 'none'},
                {'id': 'e3', 'program_id': 'p1', 'operation_id': 'extract_archive', 'native_command': 'tar', 'owner': 'filesystem', 'canonical': 'tar -xjf ./b.tar.bz2 -C ./out', 'state': 'completed', 'effect_certainty': 'none'},
                {'id': 'e4', 'program_id': 'p1', 'operation_id': 'source', 'native_command': 'source', 'owner': 'processes', 'state': 'refused', 'effect_certainty': 'none'},
            ],
            'invocation_program_publications': [], 'invocation_program_revision_pins': [],
        }
        rows, _, _ = observe._receipt_rows(audit, [])
        self.assertEqual([(r['operation'], r['outcome']) for r in rows], [
            ('external_exec', 'completed'), ('tar_create', 'completed'), ('tar_extract', 'completed'), ('source', 'rejected'),
        ])
        # source-later-syntax positive + negative controls for no_source_statement_started (child or suffix).
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path, log_path, proj = root / 'store.db', root / 'sidecar.log', root / 'proj'
            create_audit_db(db_path)
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN parent_execution_id TEXT DEFAULT ''")
            db.execute("CREATE TABLE invocation_program_sources (program_id TEXT, source_unit_id TEXT, source_id TEXT, revision TEXT, sha256 TEXT)")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','rejected',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','rejected',2,'COMMAND_SYNTAX_INVALID')")
            db.execute("INSERT INTO invocation_program_sources VALUES ('p1','bad.sh','bad.sh','rev-1',?)", ('a' * 64,))
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,parent_execution_id) VALUES ('e1','p1',1,'printf','printf','processes','completed','none','')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,parent_execution_id) VALUES ('e2','p1',2,'source','source','processes','refused','none','')")
            db.commit()
            db.close()
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            proj.mkdir()
            obs_ok, _ = observe.build_observation(CASES['source-later-syntax'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, turn_records=[{'program': CASES['source-later-syntax']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertTrue(obs_ok['facts']['captured_source_identity'] and obs_ok['facts']['no_source_statement_started'])
            # Injecting a suffix execution after ordinal 2 must fail no_source_statement_started.
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,parent_execution_id) VALUES ('e3','p1',3,'printf','printf','processes','completed','none','')")
            db.commit()
            db.close()
            obs_bad, _ = observe.build_observation(CASES['source-later-syntax'], session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj, db_path=db_path, sidecar_log=log_path, turn_records=[{'program': CASES['source-later-syntax']['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}], prelude_records=[])
            self.assertFalse(obs_bad['facts']['no_source_statement_started'])

    def test_running_command_handle_awaits_to_terminal_and_flags_timeout(self):
        class RunningApp(FakeApp):
            def __init__(self, directory, *, resolve=True):
                super().__init__(directory, mode='publish')
                self.resolve = resolve
            def request(self, path, body=None, missing=False, method=None):
                res = super().request(path, body=body, missing=missing, method=method)
                if path == '/harness/llm/pending' and isinstance(res, dict) and res.get('pending'):
                    res['tools'] = ['command', 'update_progress', 'wait', 'command_output']
                return res
            def _handle_tool_calls(self, calls):
                db = sqlite3.connect(self.directory / 'store.db')
                for idx, tc in enumerate(calls, start=1):
                    cid, name = tc['id'], tc['name']
                    if name == 'command':
                        db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-cmd',?,?,'command','processes','running',1)", (self.session_id, cid))
                        db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','running',NULL,'')")
                        res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'prog-1', 'state': 'running', 'handle': 'command-1'})}
                    elif name == 'wait':
                        db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'wait','processes','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                        res = {'outcome': 'completed', 'content': json.dumps({'outcome': 'process_done' if self.resolve else 'timeout', 'timed_out': not self.resolve})}
                    elif name == 'command_output':
                        db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command_output','processes','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                        if self.resolve:
                            db.execute("UPDATE invocation_programs SET state='completed', status_code=0 WHERE id='prog-1'")
                        res = {'outcome': 'completed', 'content': json.dumps({'state': 'completed', 'status': 0, 'stdout': 'ok\n'} if self.resolve else {'state': 'running'})}
                    else:
                        db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'update_progress','progress','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                        res = {'outcome': 'completed', 'content': '{}'}
                    db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
                db.commit(); db.close()
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                ok_res = harness.run_case(CASES['literal-null-sink'], root / 'ok', app_factory=lambda d: RunningApp(d, resolve=True), sse_factory=FakeSSE)
                to_res = harness.run_case(CASES['literal-null-sink'], root / 'to', app_factory=lambda d: RunningApp(d, resolve=False), sse_factory=FakeSSE)
            self.assertTrue(ok_res['audit']['turn_batches'][0]['handle_await']['terminal'] and ok_res['observation']['program']['complete'])
            self.assertEqual((ok_res['observation']['program']['status'], ok_res['observation']['forbidden_replay'], ok_res['audit']['unexpected_receipt_ids']), (0, False, []))
            self.assertFalse(to_res['audit']['turn_batches'][0]['handle_await']['terminal'])
            self.assertIn('command_wait_timeout', to_res['audit']['observation_gaps'])
