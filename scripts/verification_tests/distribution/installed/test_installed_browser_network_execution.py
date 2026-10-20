"""Local browser fixtures never broaden or infer loopback authority."""
import copy
import json
import unittest

from linux_release.installed_cases import browser_network

SESSION = '11111111-1111-4111-8111-111111111111'
CHECKPOINT = '22222222-2222-4222-8222-222222222222'
ARGS = {'url': 'http://127.0.0.1:12345/qualification', 'capability_request': {'loopback_connect': {'ports': [12345]}}}
CALLS = [{'id': 'fixture-call', 'name': 'page_open', 'args': ARGS}]


def checkpoint():
    return {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'pending',
            'tool_approval': {'tool_call_id': 'fixture-call', 'plan': {'stage': 'pre_spawn',
                'subject': {'kind': 'loopback_connect'}, 'options': [{'id': 'host-minted-once',
                    'kind': 'current_action', 'rung': 'once', 'decision_action': 'approve'}]}}}


class InstalledBrowserNetworkTests(unittest.TestCase):
    def test_exact_narrowed_server_call_selects_only_host_one_shot(self):
        self.assertEqual(browser_network.approval_selection(checkpoint(), SESSION, CALLS, ARGS),
                         (CHECKPOINT, 'host-minted-once'))
        changes = [lambda row: row.update(status='approved'),
                   lambda row: row.update(session_id=CHECKPOINT),
                   lambda row: row['tool_approval'].update(joined_count=2),
                   lambda row: row['tool_approval']['plan']['subject'].update(kind='host_execution'),
                   lambda row: row['tool_approval']['plan'].update(stage='pre_send'),
                   lambda row: row['tool_approval']['plan'].update(held_release={}),
                   lambda row: row['tool_approval']['plan']['options'][0].update(kind='lease'),
                   lambda row: row['tool_approval']['plan']['options'][0].update(rung='chat'),
                   lambda row: row['tool_approval']['plan']['options'][0].update(disabled=True),
                   lambda row: row['tool_approval']['plan']['options'].append(copy.deepcopy(row['tool_approval']['plan']['options'][0]))]
        for change in changes:
            row = checkpoint()
            change(row)
            with self.subTest(row=row), self.assertRaises(ValueError):
                browser_network.approval_selection(row, SESSION, CALLS, ARGS)
        for calls in [[], CALLS * 2, [dict(CALLS[0], args={'url': ARGS['url']})],
                      [dict(CALLS[0], args_truncated=True)], [dict(CALLS[0], name='command')]]:
            with self.assertRaises(ValueError):
                browser_network.approval_selection(checkpoint(), SESSION, calls, ARGS)

    def test_refusal_is_a_structured_code_and_immutable_argument_fact(self):
        arguments = {'url': ARGS['url']}
        row = {'tool': 'page_open', 'tool_call_id': 'fixture-call', 'tool_args': arguments, 'outcome': 'rejected',
               'codes': ['SANDBOX_TRY_LOOPBACK_CONNECT'], 'invocation': {'tool': 'page_open', 'status': 'rejected', 'invoked': True}}
        self.assertEqual(browser_network.refused([row], arguments)['codes'], ['SANDBOX_TRY_LOOPBACK_CONNECT'])
        for changed in [dict(row, codes=[]), dict(row, outcome='completed'), dict(row, tool_args=ARGS),
                        dict(row, invocation={}), dict(row, visual={'perceive': True}),
                        dict(row, codes=[], content='SANDBOX_TRY_LOOPBACK_CONNECT: rejected')]:
            with self.assertRaises(ValueError):
                browser_network.refused([changed], arguments)
        with self.assertRaises(ValueError):
            browser_network.refused([row, row], arguments)

    def test_owned_server_has_real_positive_control_and_stops_its_threads(self):
        with browser_network.LocalServer('public fixture') as server:
            self.assertGreater(server.port, 0)
            self.assertTrue(server.url.startswith('http://127.0.0.1:'))
            server.baseline()
            self.assertEqual(server.requests, [])
            connection = browser_network.HTTPConnection('127.0.0.1', server.port, timeout=5)
            try:
                connection.request('GET', '/qualification')
                response = connection.getresponse()
                self.assertEqual(response.status, 200)
                self.assertEqual(response.read(), b'public fixture')
            finally:
                connection.close()
        self.assertFalse(server.thread.is_alive())
        self.assertEqual(server.requests, ['/qualification'])
