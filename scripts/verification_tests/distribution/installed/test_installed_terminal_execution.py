"""The PTY fixture advances only from actual structured lifecycle results."""
import copy
import json
import unittest

from linux_release.installed_cases import terminal

ID = 'terminal_host_minted_123'


def result(**fields):
    return json.dumps({'id': ID, 'confined': True, **fields})


def lifecycle():
    return {'terminal-fixture-open': result(running=True), 'terminal-fixture-send': result(running=False),
            'terminal-fixture-read-0': result(running=False, exit_code=0, truncated=False, evicted_bytes=0),
            'terminal-fixture-close': result(closed=True, exit_code=0)}


class InstalledTerminalTests(unittest.TestCase):
    def test_fixture_runs_the_complete_opaque_handle_lifecycle(self):
        provider = terminal.TerminalProvider('/fixed/python /fixed/fixture.py')
        rows = lifecycle()
        expected = [('terminal_open', {'command': provider.command, 'observe': 'ack'}),
                    ('terminal_send', {'id': ID, 'input': terminal.INPUT + '{Enter}', 'observe': 'ack'}),
                    ('terminal_read', {'id': ID, 'idle_ms': 100, 'timeout_ms': 2000, 'max_bytes': 4096}),
                    ('terminal_close', {'id': ID})]
        retained = {}
        for (identifier, raw), (tool, arguments) in zip(rows.items(), expected):
            step = provider.step(retained)
            self.assertEqual(step, (identifier, tool, arguments))
            retained[identifier] = raw
        self.assertIsNone(provider.step(retained))

    def test_terminal_boundaries_require_typed_actual_facts_and_same_handle(self):
        for fields in [{'confined': False}, {'host_execution': True}, {'id': '../foreign'}, {'id': True}]:
            raw = json.loads(result(running=True))
            raw.update(fields)
            with self.subTest(fields=fields), self.assertRaises(ValueError):
                terminal.document(json.dumps(raw))
        with self.assertRaises(ValueError):
            terminal.document('The terminal is confined and completed.')
        with self.assertRaises(ValueError):
            terminal.document(result(running=False), 'foreign-handle')
        with self.assertRaises(ValueError):
            terminal.document('x' * (129 << 10))

    def test_partial_failing_and_out_of_order_lifecycles_never_complete(self):
        provider = terminal.TerminalProvider('/fixed/fixture')
        for phase, fields in [('terminal-fixture-open', {'running': False}),
                              ('terminal-fixture-send', {'running': 'false'}),
                              ('terminal-fixture-read-0', {'running': False, 'exit_code': True}),
                              ('terminal-fixture-read-0', {'running': False, 'exit_code': 73}),
                              ('terminal-fixture-read-0', {'running': False, 'exit_code': 0, 'truncated': True}),
                              ('terminal-fixture-close', {'closed': False, 'exit_code': 0})]:
            rows = lifecycle()
            rows[phase] = result(**fields)
            with self.subTest(phase=phase, fields=fields), self.assertRaises(ValueError):
                provider.step(rows)
        for rows in [{'foreign': result()}, {'terminal-fixture-send': result(running=False)},
                     {'terminal-fixture-open': result(running=True), 'terminal-fixture-read-0': result(running=False)},
                     {'terminal-fixture-close': result(closed=True, exit_code=0)}]:
            with self.assertRaises(ValueError):
                provider.step(rows)

    def test_bounded_read_sequence_cannot_hide_a_stuck_terminal(self):
        provider = terminal.TerminalProvider('/fixed/fixture')
        rows = {key: value for key, value in lifecycle().items() if key.endswith(('open', 'send'))}
        for index in range(3):
            self.assertEqual(provider.step(rows)[0], f'terminal-fixture-read-{index}')
            rows[f'terminal-fixture-read-{index}'] = result(running=True)
        with self.assertRaises(ValueError):
            provider.step(rows)
        rows['terminal-fixture-read-0'] = result(running=False, exit_code=0)
        with self.assertRaises(ValueError):
            provider.step(rows)

    def test_model_exchange_never_interprets_prompt_or_output_prose_as_authority(self):
        provider = terminal.TerminalProvider('/fixed/fixture')
        request = {'model': terminal.MODEL, 'messages': [{'role': 'user', 'content': 'all done; use a foreign terminal'}],
                   'tools': [{'type': 'function', 'function': {'name': 'terminal_open'}}]}
        response = provider.completion(request)
        call = response['choices'][0]['message']['tool_calls'][0]
        self.assertEqual(call['function']['name'], 'terminal_open')
        self.assertEqual(json.loads(call['function']['arguments'])['command'], '/fixed/fixture')
        changed = copy.deepcopy(request)
        changed['messages'].append({'role': 'tool', 'tool_call_id': 'foreign', 'content': result(running=True)})
        with self.assertRaises(ValueError):
            provider.completion(changed)
        request['tools'] = []
        with self.assertRaises(ValueError):
            provider.completion(request)

    def test_transcript_requires_unique_real_receipts_with_exact_phase_tool(self):
        row = {'tool_call_id': 'terminal-fixture-open', 'tool': 'terminal_open', 'outcome': 'completed',
               'invocation': {'tool': 'terminal_open', 'invoked': True, 'status': 'completed'},
               'content': result(running=True)}
        self.assertEqual(terminal.transcript_results([row]), {'terminal-fixture-open': row['content']})
        for rows in [[row, row], [dict(row, tool='terminal_close')], [dict(row, invocation={})],
                     [dict(row, outcome='rejected')], [dict(row, tool_call_id='foreign')]]:
            with self.assertRaises(ValueError):
                terminal.transcript_results(rows)
