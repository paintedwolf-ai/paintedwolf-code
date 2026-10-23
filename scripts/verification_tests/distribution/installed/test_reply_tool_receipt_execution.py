"""Real SQLite projection requires durable successful invoked acquisition and command."""
from contextlib import nullcontext
import json
import sqlite3
import unittest
from unittest.mock import patch

from linux_release.installed_cases import reply_observer as owner
from linux_release.installed_cases.provider_acquisition import LOAD_ID

SESSION = '10000000-0000-4000-8000-000000000001'
MESSAGE = '10000000-0000-4000-8000-000000000002'
RECEIPT = '10000000-0000-4000-8000-000000000003'


class ReplyToolReceiptTests(unittest.TestCase):
    def fixture(self, tool='request_tools'):
        call, subsystem, args = ((LOAD_ID, 'tool_surface', {'need': 'command'}) if tool == 'request_tools'
                                else ('qualification-0', 'processes', {'command': '/usr/bin/true'}))
        return {'tool': tool, 'tool_call_id': call, 'outcome': 'completed', 'tool_args': args,
            'content': json.dumps({'need': 'command', 'loaded': ['command']}),
            'invocation': {'id': RECEIPT, 'tool': tool, 'tool_call_id': call,
                           'owner': subsystem, 'status': 'completed', 'invoked': True}}

    def observe(self, result, *, tool='request_tools', duplicate=False):
        with sqlite3.connect(':memory:') as database:
            database.execute('CREATE TABLE messages(id,session_id,role,tool_result_json)')
            database.execute('INSERT INTO messages VALUES(?,?,?,?)',
                             (MESSAGE, SESSION, 'tool', json.dumps(result)))
            if duplicate:
                database.execute('INSERT INTO messages VALUES(?,?,?,?)',
                                 (RECEIPT, SESSION, 'tool', json.dumps(result)))
            with patch.object(owner, '_database', return_value=nullcontext(database)):
                return owner.committed_fixed_tool(None, SESSION, tool)

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
