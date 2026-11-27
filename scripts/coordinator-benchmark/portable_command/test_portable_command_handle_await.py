"""Unit controls for real-shaped chunked command_output awaiting, raw byte cursors, and gaps."""
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import harness, reporting
from portable_command.contracts import load_suite
from portable_command.test_portable_command_harness import FakeApp, FakeSSE

CASES = {c['id']: c for c in load_suite()['cases']}


class ChunkedOutputFakeApp(FakeApp):
    """Fake app emitting real commandOutput JSON shape (raw byte cursors + chunks array)."""
    def __init__(self, directory, *, pages, reject_output=False):
        super().__init__(directory, mode='publish')
        self.pages = pages
        self.reject_output = reject_output
        self.requested_cursors = []

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
                res = {'outcome': 'completed', 'content': json.dumps({'outcome': 'process_done'})}
            elif name == 'command_output':
                cur = (tc.get('args') or {}).get('cursor', 0)
                self.requested_cursors.append(cur)
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command_output','processes','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                if self.reject_output:
                    res = {'outcome': 'rejected', 'codes': ['BACKGROUND_HANDLE_NOT_FOUND'], 'content': '{}'}
                else:
                    page = self.pages[min(len(self.requested_cursors) - 1, len(self.pages) - 1)]
                    if page.get('running') is False:
                        db.execute("UPDATE invocation_programs SET state='completed', status_code=? WHERE id='prog-1'", (page.get('exit_code', 0),))
                    res = {'outcome': 'completed', 'content': json.dumps(page)}
            else:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'update_progress','progress','completed',1)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'completed', 'content': '{}'}
            db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
        db.commit()
        db.close()


class PortableCommandHandleAwaitTests(unittest.TestCase):
    def test_multibyte_screened_and_empty_terminal_command_output_controls(self):
        # 1) Multi-byte raw byte offsets across pages (6 bytes + 5 bytes = cursor 11, then 3 bytes = cursor 14).
        multibyte_pages = [
            {
                'handle': 'command-1', 'cursor': '0', 'next_cursor': '11', 'running': True,
                'chunks': [{'cursor': 0, 'stream': 'stdout', 'text': '10000\n'}, {'cursor': 6, 'stream': 'stderr', 'text': 'warn\n'}],
            },
            {
                'handle': 'command-1', 'cursor': '11', 'next_cursor': '14', 'running': False, 'exit_code': 0,
                'chunks': [{'cursor': 11, 'stream': 'stdout', 'text': 'ok\n'}],
            },
            {'handle': 'command-1', 'cursor': '14', 'next_cursor': '14', 'running': False, 'exit_code': 0, 'chunks': []},
        ]
        # 2) Screened/length-changed chunks: raw offset jumps 0 -> 24 (24 raw bytes) while visible redacted text is 10 chars.
        screened_pages = [
            {
                'handle': 'command-1', 'cursor': '0', 'next_cursor': '30', 'running': False, 'exit_code': 0,
                'chunks': [{'cursor': 0, 'stream': 'stdout', 'text': '[REDACTED]'}, {'cursor': 24, 'stream': 'stdout', 'text': ' done\n'}],
            },
            {'handle': 'command-1', 'cursor': '30', 'next_cursor': '30', 'running': False, 'exit_code': 0, 'chunks': []},
        ]
        # 3) Empty-terminal output: running=False, chunks=[], cursor == next_cursor == "0".
        empty_pages = [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '0', 'running': False, 'exit_code': 0, 'chunks': []}]

        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(harness, 'checkpoint_closed_store', return_value={}):
                app_mb = []
                def mb_factory(d):
                    a = ChunkedOutputFakeApp(d, pages=multibyte_pages)
                    app_mb.append(a)
                    return a
                r_mb = harness.run_case(CASES['literal-null-sink'], root / 'mb', app_factory=mb_factory, sse_factory=FakeSSE)
                r_sc = harness.run_case(CASES['literal-null-sink'], root / 'sc', app_factory=lambda d: ChunkedOutputFakeApp(d, pages=screened_pages), sse_factory=FakeSSE)
                r_em = harness.run_case(CASES['literal-null-sink'], root / 'em', app_factory=lambda d: ChunkedOutputFakeApp(d, pages=empty_pages), sse_factory=FakeSSE)
            self.assertEqual(app_mb[0].requested_cursors, [0, 11, 14])
            self.assertEqual(r_mb['observation']['program']['stdout'], '10000\nok\n')
            self.assertEqual(r_mb['observation']['program']['stderr'], 'warn\n')
            self.assertEqual(r_mb['audit']['observation_gaps'], [])
            self.assertEqual(r_sc['observation']['program']['stdout'], '[REDACTED] done\n')
            self.assertEqual(r_sc['audit']['observation_gaps'], [])
            self.assertEqual(r_em['observation']['program']['stdout'], '')
            self.assertTrue(r_em['observation']['program']['complete'])
            self.assertEqual(r_em['audit']['observation_gaps'], [])

    def test_truncation_combined_unavailable_cursor_gap_and_missing_fields(self):
        cases = [
            ('truncated', [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '1', 'running': False, 'exit_code': 0, 'truncated': True, 'chunks': [{'cursor': 0, 'stream': 'stdout', 'text': 'x'}]}], False, 'command_output_truncated'),
            ('combined_unavailable', [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '12', 'running': False, 'exit_code': 0, 'chunks': [{'cursor': 0, 'stream': 'combined', 'text': '[unavailable]'}]}], False, 'command_output_unavailable'),
            ('out_of_range_cursor', [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '5', 'running': False, 'exit_code': 0, 'chunks': [{'cursor': 5, 'stream': 'stdout', 'text': 'oob'}]}], False, 'command_output_cursor_gap'),
            ('non_monotonic_cursor', [{'handle': 'command-1', 'cursor': '0', 'next_cursor': '10', 'running': False, 'exit_code': 0, 'chunks': [{'cursor': 4, 'stream': 'stdout', 'text': 'a'}, {'cursor': 4, 'stream': 'stdout', 'text': 'b'}]}], False, 'command_output_cursor_gap'),
            ('handle_mismatch', [{'handle': 'other-9', 'cursor': '0', 'next_cursor': '0', 'running': False, 'exit_code': 0, 'chunks': []}], False, 'command_output_cursor_gap'),
            ('missing_fields', [{'stdout': 'legacy-without-handle'}], False, 'command_output_missing'),
            ('read_rejected', [], True, 'command_output_rejected'),
        ]
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for label, pages, rej, expected_gap in cases:
                with self.subTest(defect=label), patch.object(harness, 'checkpoint_closed_store', return_value={}):
                    res = harness.run_case(CASES['literal-null-sink'], root / label, app_factory=lambda d, p=pages, r=rej: ChunkedOutputFakeApp(d, pages=p, reject_output=r), sse_factory=FakeSSE)
                    self.assertIn(expected_gap, res['audit']['observation_gaps'])
                    disp = reporting.classify_case_disposition({'attempt': 'measured', 'qualification': 'native-runtime', 'passed': True, 'observation_gaps': res['audit']['observation_gaps']})
                    self.assertEqual(disp['disposition'], 'unqualified')
                    self.assertEqual(disp['discrepancy_category'], 'missing_observation')
