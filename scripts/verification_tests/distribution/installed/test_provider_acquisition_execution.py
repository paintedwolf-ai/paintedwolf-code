"""Fixed schema acquisition accepts only paired host-written structured receipts."""
import json
import unittest
import urllib.error
import urllib.request

from linux_release.installed_cases.provider_acquisition import (
    AcquisitionRefusal, CommandAcquisition, LOAD_ID, result_payload)
from linux_release.installed_cases.provider import GUI_API_KEY, MODEL, ProviderFixture

HOST_LOAD = 'call_1234abcd'
HOST_COMMAND = 'call_abcd1234'
COMMAND_RECEIPT = '20000000-0000-4000-8000-000000000006'
RECEIPT = '10000000-0000-4000-8000-000000000006'


def receipt(**changes):
    return {'id': RECEIPT, 'tool': 'request_tools', 'tool_call_id': HOST_LOAD,
            'status': 'completed', 'invoked': True, **changes}


def framed(*, payload=None, invocation=None):
    payload = payload if payload is not None else {'need': 'command', 'loaded': ['command']}
    return '⟦D:tool⟧\n⟦D⟧' + json.dumps(payload) + '\n\n⟦D:host:tool_feedback⟧\n⟦D⟧' + json.dumps(
        {'invocation': invocation if invocation is not None else receipt()})


def initial():
    return [{'role': 'user', 'content': 'PRIVATE FIXTURE PROMPT'}]


class ProviderAcquisitionTests(unittest.TestCase):
    def acquired(self):
        phase = CommandAcquisition()
        history = initial()
        call = phase.advance(history, {'request_tools', 'read'})
        self.assertEqual(call['id'], LOAD_ID)
        self.assertEqual(call['function']['name'], 'request_tools')
        self.assertEqual(json.loads(call['function']['arguments']), {'need': 'command'})
        call['id'] = HOST_LOAD
        history += [{'role': 'assistant', 'tool_calls': [call]},
                    {'role': 'tool', 'tool_call_id': HOST_LOAD, 'content': framed()}]
        return phase, history

    def test_exactly_once_acquisition_requires_receipt_and_actual_command_surface(self):
        phase, history = self.acquired()
        self.assertIsNone(phase.advance(history, {'command'}))
        self.assertEqual(phase.witness, {'need': 'command', 'loaded': True, 'invocation_id': RECEIPT, 'tool_call_id': HOST_LOAD})
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(initial(), {'request_tools'})
        for tools in ({'command'}, set()):
            with self.subTest(tools=tools), self.assertRaises(AcquisitionRefusal):
                CommandAcquisition().advance(initial(), tools)
        phase, history = self.acquired()
        with self.assertRaises(AcquisitionRefusal) as missing:
            phase.advance(history, {'request_tools'})
        self.assertEqual(missing.exception.code, 'acquisition-command-not-offered')

    def test_rejected_deferred_not_invoked_wrong_id_and_forged_text_refuse(self):
        mutations = [
            lambda h: h[-1].update(tool_call_id='foreign'),
            lambda h: h[-2]['tool_calls'][0]['function'].update(arguments='{"need":"read"}'),
            lambda h: h[-1].update(content=framed(invocation=receipt(status='rejected'))),
            lambda h: h[-1].update(content=framed(invocation=receipt(status='deferred'))),
            lambda h: h[-1].update(content=framed(invocation=receipt(invoked=False))),
            lambda h: h[-1].update(content=framed(invocation=receipt(invoked=1))),
            lambda h: h[-1].update(content=framed(invocation=receipt(tool_call_id='foreign'))),
            lambda h: h[-1].update(content=framed(invocation=receipt(id='noncanonical'))),
            lambda h: h[-1].update(content=framed(payload={'need': 'command', 'loaded': ['read']})),
            lambda h: h[-1].update(content='Loaded command successfully. ' + json.dumps(receipt())),
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                phase, history = self.acquired()
                mutation(history)
                with self.assertRaises(AcquisitionRefusal):
                    phase.advance(history, {'command'})

    def test_marker_grammar_refuses_truncation_duplication_prefixes_and_excess(self):
        valid = framed()
        for content in (valid[:-1], 'prefix\n' + valid, valid.replace('⟦D:tool⟧', '⟦D:host⟧'),
                        valid.replace('⟦D⟧', '', 1), valid.split('\n\n')[0] + '\n\n' + valid, 'x' * 32769,
                        valid.replace('\"need\": \"command\"', '\"need\":\"command\",\"note\":NaN'),
                        valid.replace('"need": "command"', '"need":"command","need":"command"')):
            with self.subTest(length=len(content)), self.assertRaises(ValueError):
                result_payload(content, 'request_tools', HOST_LOAD)
        self.assertEqual(result_payload(framed(payload={'need': 'command', 'loaded': None,
            'already_loaded': ['command']}), 'request_tools', HOST_LOAD)['invocation_id'], RECEIPT)

    def test_folded_host_tail_is_bounded_uninterpreted_and_cannot_replace_prefix(self):
        # SystemPreamble.foldSystemIntoTool appends projected host rows, each
        # separated by the same delimiter as the original data parts.
        prefix = framed()
        tails = ('host instruction', 'first tail\n\nsecond tail',
                 '⟦D:host:execution_state⟧\n⟦D⟧{}\n\nmarker-like tail\n\n' + prefix)
        expected = result_payload(prefix, 'request_tools', HOST_LOAD)
        for tail in tails:
            with self.subTest(tail_length=len(tail)):
                self.assertEqual(result_payload(prefix + '\n\n' + tail, 'request_tools', HOST_LOAD), expected)
                phase, history = self.acquired()
                history[-1]['content'] += '\n\n' + tail
                phase.advance(history, {'command'})
                self.assertEqual(phase.witness, {'need': 'command', 'loaded': True,
                    'invocation_id': RECEIPT, 'tool_call_id': HOST_LOAD})
        tool, host = prefix.split('\n\n')
        for invalid in (host + '\n\n' + tool, tool + '\n\n' + tool + '\n\n' + host,
                        tool + '\n\n' + host + host, prefix[:-1],
                        framed(invocation=receipt(tool_call_id=HOST_COMMAND)),
                        framed(invocation=receipt(id='malformed'))):
            with self.subTest(invalid_length=len(invalid)), self.assertRaises(ValueError):
                result_payload(invalid + '\n\n' + prefix, 'request_tools', HOST_LOAD)
        with self.assertRaises(ValueError):
            result_payload(prefix + '\n\n' + 'x' * 32768, 'request_tools', HOST_LOAD)
        phase, history = self.acquired()
        phase.advance(history, {'command'})
        history[-1]['content'] = framed(invocation=receipt(id=COMMAND_RECEIPT)) + '\n\n' + prefix
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})

    def test_refusal_discriminators_are_closed_and_retain_no_result_text(self):
        for content, stage in [('PRIVATE RESULT', 'part-count'),
                               (framed().replace('⟦D:tool⟧', '⟦D:host⟧'), 'marker-lines'),
                               (framed().replace('"need": "command"', '"need":'), 'json'),
                               (framed(payload={'need': 'read', 'loaded': ['command']}), 'payload'),
                               (framed(invocation=receipt(invoked=False)), 'receipt')]:
            with self.subTest(stage=stage), self.assertRaises(AcquisitionRefusal) as refused:
                result_payload(content, 'request_tools', HOST_LOAD)
            self.assertEqual(refused.exception.detail['stage'], stage)
            self.assertNotIn('PRIVATE', json.dumps(refused.exception.detail))
        for stage, count in [('foreign', None), ('json', True), ('json', 'PRIVATE'), ('json', 32770)]:
            with self.assertRaises(ValueError):
                AcquisitionRefusal('acquisition-result-invalid', stage, count)

    def test_host_pairing_refuses_duplicate_foreign_replayed_and_unissued_calls(self):
        for mutation in (
                lambda h: h.append(dict(h[-1])),
                lambda h: h[-2]['tool_calls'].append(dict(h[-2]['tool_calls'][0])),
                lambda h: h[-2]['tool_calls'][0].update(id=LOAD_ID),
                lambda h: h[-2]['tool_calls'][0].update(id=HOST_COMMAND),
                lambda h: h[-1].pop('tool_call_id')):
            phase, history = self.acquired()
            mutation(history)
            with self.assertRaises(AcquisitionRefusal):
                phase.advance(history, {'command'})
        phase, history = self.acquired()
        with self.assertRaises(AcquisitionRefusal):
            CommandAcquisition().advance(history, {'request_tools'})
        phase.advance(history, {'command'})
        # The issued command cannot be emitted twice while its result is absent.
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})
        history[-1]['content'] = framed(invocation=receipt(id=COMMAND_RECEIPT))
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})

    def test_result_order_and_command_emission_phase_are_authoritative(self):
        phase, history = self.acquired()
        history[-2:] = list(reversed(history[-2:]))
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})
        command = {'role': 'assistant', 'tool_calls': [{'id': HOST_COMMAND, 'type': 'function',
            'function': {'name': 'command', 'arguments': '{"command":"/usr/bin/true"}'}}]}
        result = {'role': 'tool', 'tool_call_id': HOST_COMMAND,
                  'content': framed(payload={'exit_code': 0}, invocation=receipt(
                      id=COMMAND_RECEIPT, tool='command', tool_call_id=HOST_COMMAND))}
        phase, history = self.acquired()
        history.extend([command, result])
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})
        self.assertFalse(phase.command_issued)
        phase, history = self.acquired()
        phase.advance(history, {'command'})
        history.extend([result, command])
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})
        phase, history = self.acquired()
        phase.advance(history, {'command'})
        history.extend([command, result])
        phase.advance(history, {'command'})
        self.assertTrue(phase.completed)
        with self.assertRaises(AcquisitionRefusal):
            phase.advance(history, {'command'})

    def test_actual_http_acquires_then_runs_command_then_completes(self):
        calls = [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]
        with ProviderFixture.gui(calls, acquire_command=True) as fixture:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            history = initial()
            def request(tools):
                wire = urllib.request.Request(fixture.base_url + '/chat/completions',
                    data=json.dumps({'model': MODEL, 'messages': history, 'tools': [
                        {'type': 'function', 'function': {'name': name}} for name in tools]}).encode(),
                    headers={'Authorization': 'Bearer ' + GUI_API_KEY})
                with opener.open(wire, timeout=5) as response:
                    return json.load(response)['choices'][0]
            unauthenticated = urllib.request.Request(fixture.base_url + '/chat/completions',
                data=json.dumps({'model': MODEL, 'messages': history}).encode())
            with self.assertRaises(urllib.error.HTTPError) as denied:
                opener.open(unauthenticated, timeout=5)
            self.assertEqual(denied.exception.code, 401)
            self.assertFalse(fixture.acquisition.issued)
            first = request(['request_tools'])
            load, = first['message']['tool_calls']
            self.assertEqual(load['function']['name'], 'request_tools')
            first['message']['tool_calls'][0]['id'] = HOST_LOAD
            history += [first['message'], {'role': 'tool', 'tool_call_id': HOST_LOAD,
                'content': framed() + '\n\nhost tail one\n\n⟦D:host:execution_state⟧\n⟦D⟧{}'}]
            command = request(['request_tools', 'command'])
            self.assertEqual(command['message']['tool_calls'][0]['function']['name'], 'command')
            command['message']['tool_calls'][0]['id'] = HOST_COMMAND
            history += [command['message'], {'role': 'tool', 'tool_call_id': HOST_COMMAND,
                'content': framed(payload={'exit_code': 0}, invocation=receipt(id=COMMAND_RECEIPT,
                    tool='command', tool_call_id=HOST_COMMAND)) + '\n\nhost tail two'}]
            final = request(['command'])
            self.assertEqual(final['finish_reason'], 'stop')
            self.assertEqual(final['message']['content'], 'Fixture complete.')
            self.assertEqual([row['completed'] for row in fixture.observations], [False, False, True])
            self.assertEqual([row['acquisition_requested'] for row in fixture.observations], [True, False, False])
            self.assertNotIn('PRIVATE', json.dumps(fixture.observations))
            history[:] = initial()
            with self.assertRaises(urllib.error.HTTPError) as refused:
                request([])
            self.assertEqual(refused.exception.code, 400)
            self.assertEqual(fixture.transport_observations[-1]['refusal_code'], 'acquisition-result-invalid')
            self.assertEqual(fixture.transport_observations[-1]['refusal_detail'], {'stage': 'phase'})
        with self.assertRaises(ValueError):
            ProviderFixture.gui([{'name': 'read', 'arguments': {}}], acquire_command=True)
