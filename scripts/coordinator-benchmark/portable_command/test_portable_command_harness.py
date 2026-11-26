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
        CREATE TABLE invocation_receipts (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, tool_name TEXT, status TEXT, invoked INTEGER);
        CREATE TABLE invocation_programs (id TEXT PRIMARY KEY, parent_receipt_id TEXT, state TEXT, status_code INTEGER, stop_code TEXT);
        CREATE TABLE invocation_program_executions (id TEXT PRIMARY KEY, program_id TEXT, ordinal INTEGER, operation_id TEXT, native_command TEXT, owner TEXT, state TEXT, effect_certainty TEXT, accepted_revision TEXT, published_revision TEXT, native_gap_json TEXT);
        CREATE TABLE invocation_program_revision_pins (program_id TEXT, execution_id TEXT, ordinal INTEGER, role TEXT, mode TEXT, path TEXT);
        CREATE TABLE invocation_program_outputs (id TEXT PRIMARY KEY, program_id TEXT, execution_id TEXT, stream TEXT, reference TEXT, complete INTEGER, redacted INTEGER, preview TEXT, preview_truncated INTEGER, line_count INTEGER, inline_digest TEXT, blob_id TEXT);
        CREATE TABLE invocation_program_publications (program_id TEXT PRIMARY KEY, session_id TEXT, state TEXT, materialized INTEGER);
        CREATE TABLE source_command_windows (id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, state TEXT);
        CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, ord INTEGER, role TEXT, tool_result_call_id TEXT, tool_result_json TEXT);
        CREATE TABLE prompt_submissions (id TEXT PRIMARY KEY, session_id TEXT, admission_seq INTEGER, status TEXT, error_code TEXT);
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
    """In-process fake matching smoke.Application for deterministic unit coverage."""
    def __init__(self, directory, mode='publish'):
        self.directory = pathlib.Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True)
        self.url = 'http://127.0.0.1:1'
        self.token = 'tok'
        self.mode = mode
        self.process = None
        self.closed = False
        self.project_root = None
        self.session_id = 'sess-1'
        self.turn = 0
        self.pending_req = None
        self.requests = []
        (self.directory / 'sidecar.log').write_text('level=INFO msg="startup"\n')
        create_audit_db(self.directory / 'store.db')

    def ready(self):
        return None

    def close(self, cleanup=True):
        self.closed = True

    def wait_for(self, fn, phase, session=None, timeout=10):
        res = fn()
        if not res:
            raise RuntimeError('fake timeout: ' + phase)
        return res

    def submission_settled(self, session, submission):
        return True

    def request(self, path, body=None, missing=False, method=None):
        self.requests.append((path, body))
        if path == '/v1/projects':
            self.project_root = pathlib.Path(body['roots'][0]['path'])
            return {'id': 'proj-1'}
        if path == '/v1/sessions':
            return {'id': self.session_id}
        if path == f'/v1/sessions/{self.session_id}':
            return {'id': self.session_id, 'status': 'idle'}
        if path == '/harness/model-limit':
            return {'ok': True}
        if path.endswith('/prompts'):
            db = sqlite3.connect(self.directory / 'store.db')
            db.execute("INSERT INTO prompt_submissions VALUES ('sub-1',?,1,'running','')", (self.session_id,))
            db.commit(); db.close()
            self.pending_req = {'pending': True, 'id': 'req-1', 'session_id': self.session_id, 'tools': ['command', 'update_progress']}
            return {'operation_id': body['operation_id']}
        if path == '/harness/llm/pending':
            return self.pending_req or {'pending': False}
        if path == '/harness/llm/respond':
            if 'content' in body:
                self.pending_req = None
                return {'ok': True}
            self._handle_tool_calls(body['tool_calls'])
            self.turn += 1
            self.pending_req = {'pending': True, 'id': f'req-{self.turn+1}', 'session_id': self.session_id, 'tools': ['command', 'update_progress']}
            return {'ok': True}
        if path.endswith('/invocations'):
            return {'invocations': []}
        if path.endswith('/messages'):
            return {'messages': []}
        if '/program-receipts/' in path:
            return {'program_id': 'prog-1', 'activities': []}
        return {}

    def _handle_tool_calls(self, calls):
        db = sqlite3.connect(self.directory / 'store.db')
        for idx, tc in enumerate(calls, start=1):
            cid, name = tc['id'], tc['name']
            if name == 'update_progress':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'update_progress','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'completed', 'result': '{}'}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
            elif name == 'command' and self.mode == 'publish':
                (self.project_root / 'result.txt').write_bytes(b'created\n')
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','completed',1)", ('rcpt-cmd', self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','completed',0,'')")
                db.execute("INSERT INTO invocation_program_executions VALUES ('ex-1','prog-1',1,'printf','printf','textutil','completed','none','','','')")
                db.execute("INSERT INTO invocation_program_executions VALUES ('ex-2','prog-1',2,'write','write','filesystem','completed','completed','rev-1','rev-1','')")
                db.execute("INSERT INTO invocation_program_revision_pins VALUES ('prog-1','ex-2',0,'destination','absent','result.txt')")
                payload = {'program_id': 'prog-1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': '', 'completed_effects': 1, 'uncertain_effects': 0, 'native_gaps': 0}
                res = {'outcome': 'completed', 'result': json.dumps(payload)}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
            elif name == 'command' and self.mode == 'refuse':
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','rejected',0)", ('rcpt-cmd', self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-cmd','rejected',2,'COMMAND_OPTION_UNSUPPORTED')")
                payload = {
                    'program_id': 'prog-1', 'state': 'rejected', 'status': 2, 'stop_code': 'COMMAND_OPTION_UNSUPPORTED',
                    'stdout': '', 'stderr': '', 'completed_effects': 0, 'uncertain_effects': 0, 'native_gaps': 0, 'replay_available': True,
                    'refusal': {
                        'code': 'COMMAND_OPTION_UNSUPPORTED',
                        'feedback': 'Usage: cat [-n] [FILE...]',
                        'details': {'word': '--not-a-version-1-option', 'span': {'start': 4, 'end': 28}, 'expected_form': 'cat [-n] [FILE...]'},
                    },
                }
                res = {'outcome': 'rejected', 'rejection': {'code': 'COMMAND_OPTION_UNSUPPORTED'}, 'result': json.dumps(payload)}
                db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
        db.commit()
        db.close()


class PortableCommandHarnessTests(unittest.TestCase):
    def test_known_publication_run_export_and_replay(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                res = harness.run_case(
                    CASES['first-mutation'],
                    root / 'run1',
                    export_dir=root / 'export',
                    app_factory=lambda d: FakeApp(d, mode='publish'),
                    sse_factory=FakeSSE,
                )
            self.assertTrue(res['grade']['passed'])
            self.assertEqual(res['grade']['qualification'], 'native-runtime')
            self.assertTrue((root / 'export' / 'sanitized-fixture.json').is_file())
            replayed_case = harness.resolve_case(str(root / 'export' / 'sanitized-fixture.json'))
            self.assertEqual(replayed_case['id'], 'first-mutation')

    def test_typed_refusal_proves_no_external_launch_and_retains_prelude(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                res = harness.run_case(
                    CASES['native-option-no-fallback'],
                    root / 'run2',
                    app_factory=lambda d: FakeApp(d, mode='refuse'),
                    sse_factory=FakeSSE,
                )
            self.assertTrue(res['grade']['passed'])
            self.assertTrue(res['observation']['facts']['no_external_launch'])
            self.assertEqual(len(res['audit']['prelude']), 1)

    def test_cli_modes_and_unmeasured_failure_retention(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_result = {'case_id': 'first-mutation', 'session_id': 's1', 'grade': {'qualification': 'native-runtime', 'passed': True, 'dimensions': {'artifacts': True}}}
            with patch('sys.stdout', new=io.StringIO()):
                code = cli.main(['--case', 'first-mutation', '--out', str(root / 'o1')], runner=lambda c, o, export_dir=None: fake_result)
                self.assertEqual(code, 0)
                code2 = cli.main(['--replay', 'first-mutation', '--out', str(root / 'o2')], runner=lambda c, o, export_dir=None: {**fake_result, 'grade': {**fake_result['grade'], 'passed': False}})
                self.assertEqual(code2, 1)
            with self.assertRaises(ValueError):
                harness.resolve_case('nonexistent-case-id')

    def test_sidecar_launch_scanner_detects_missing_log_and_matching_launch(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = pathlib.Path(tmp) / 'sidecar.log'
            self.assertFalse(observe.scan_sidecar_launches(p, 's1')['available'])
            p.write_text('level=INFO msg="process boundary selected" tool=command session_id=s1\n')
            scan = observe.scan_sidecar_launches(p, 's1')
            self.assertTrue(scan['available'])
            self.assertEqual(scan['command_launches'], 1)
