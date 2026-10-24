"""Real SQLite projection requires durable successful invoked acquisition and command."""
from contextlib import nullcontext
import json
import sqlite3
import unittest
from unittest.mock import patch

from linux_release.installed_cases import fixture_receipt as owner

SESSION = '10000000-0000-4000-8000-000000000001'
MESSAGE = '10000000-0000-4000-8000-000000000002'
RECEIPT = '10000000-0000-4000-8000-000000000003'


class ReplyToolReceiptTests(unittest.TestCase):
    def fixture(self, tool='request_tools', *, target='command'):
        call, subsystem, args = (('call_1234abcd', 'tool_surface', {'need': target}) if tool == 'request_tools'
                                else ('call_abcd1234', 'processes', {'command': '/usr/bin/true'}) if target == 'command'
                                else ('call_abcd1234', 'browser', {'path': 'fixture.html'}))
        return {'tool': tool, 'tool_call_id': call, 'outcome': 'completed', 'tool_args': args,
            'content': json.dumps({'need': target, 'loaded': [target]} if tool == 'request_tools' else
                {'id': 'page_owned', 'live_pages': ['page_owned'], 'max_pages': 4}),
            'invocation': {'id': RECEIPT, 'tool': tool, 'tool_call_id': call,
                           'owner': subsystem, 'status': 'completed', 'invoked': True}}

    def observe(self, result, *, tool='request_tools', target='command', duplicate=False):
        with sqlite3.connect(':memory:') as database:
            database.execute('CREATE TABLE messages(id,session_id,role,tool_result_json)')
            database.execute('INSERT INTO messages VALUES(?,?,?,?)',
                             (MESSAGE, SESSION, 'tool', json.dumps(result)))
            if duplicate:
                database.execute('INSERT INTO messages VALUES(?,?,?,?)',
                                 (RECEIPT, SESSION, 'tool', json.dumps(result)))
            with patch.object(owner, '_database', return_value=nullcontext(database)):
                return owner.committed_fixture_tool(None, SESSION, tool, 'call_1234abcd' if tool == 'request_tools' else 'call_abcd1234', target=target)

    def test_actual_successful_receipts_and_fixed_args_are_required(self):
        for tool in ('request_tools', 'command'):
            found = self.observe(self.fixture(tool), tool=tool)
            self.assertEqual(found['invocation_id'], RECEIPT)
            self.assertEqual(found['message_id'], MESSAGE)
            self.assertIs(found['invoked'], True)
        for field, value in [('owner', 'foreign'), ('status', 'rejected'), ('status', 'deferred'),
                             ('invoked', False), ('invoked', 1), ('tool_call_id', 'foreign'),
                             ('tool', 'command'), ('id', 'noncanonical')]:
            with self.subTest(field=field, value=value):
                result = self.fixture()
                result['invocation'][field] = value
                with self.assertRaises(ValueError):
                    self.observe(result)
        for change in ({'tool_args': {'need': 'read'}}, {'outcome': 'rejected'},
                       {'content': json.dumps({'need': 'command', 'loaded': []})}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.observe({**self.fixture(), **change})

    def test_ambiguous_wrong_call_or_oversized_rows_never_certify_execution(self):
        with self.assertRaises(ValueError):
            self.observe(self.fixture(), duplicate=True)
        self.assertIsNone(self.observe({**self.fixture(), 'tool_call_id': 'foreign'}))
        result = self.fixture()
        result['padding'] = 'x' * (1 << 20)
        with self.assertRaises(ValueError):
            self.observe(result)

    def test_page_open_shares_call_identity_guards_but_keeps_browser_arguments_and_owner(self):
        for tool in ('request_tools', 'page_open'):
            result = self.observe(self.fixture(tool, target='page_open'), tool=tool, target='page_open')
            self.assertEqual(result['tool'], tool)
            self.assertEqual(result['owner'], 'tool_surface' if tool == 'request_tools' else 'browser')
            self.assertEqual(result['arguments'], {'need': 'page_open'} if tool == 'request_tools' else {'path': 'fixture.html'})
            if tool == 'page_open':
                self.assertEqual(result['payload']['id'], 'page_owned')
        for change in ({'tool_args': {'path': 'foreign.html'}}, {'tool_args': {'command': '/usr/bin/true'}},
                       {'invocation': {**self.fixture('page_open', target='page_open')['invocation'], 'code': 'REFUSED'}}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.observe({**self.fixture('page_open', target='page_open'), **change}, tool='page_open', target='page_open')
        with self.assertRaises(ValueError):
            self.observe(self.fixture('page_open', target='page_open'), tool='page_open', target='command')
