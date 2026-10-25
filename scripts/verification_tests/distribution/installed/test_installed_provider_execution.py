"""The installed fixture drives ordinary provider transport without real model calls."""
import json
from concurrent.futures import ThreadPoolExecutor
import unittest
import urllib.error
import urllib.request
import uuid

from linux_release.installed_cases.provider import (MODEL, GUI_API_KEY, ProviderFixture,
                                                    FixtureRefusal, request_shape)
from linux_release.installed_cases.provider_barrier import ResponseBarrier
from verification_tests.distribution.installed.test_provider_acquisition_execution import framed, receipt


class InstalledProviderTests(unittest.TestCase):
    def request(self, result=False):
        messages = [{'role': 'user', 'content': 'private fixture prompt'}]
        if result:
            messages += [{'role': 'assistant', 'tool_calls': []},
                         {'role': 'tool', 'tool_call_id': 'qualification-0', 'content': 'private output'}]
        return {'model': MODEL, 'messages': messages, 'tools': [
            {'type': 'function', 'function': {'name': 'command'}}]}

    def test_completion_requires_actual_tool_identity_and_structured_result(self):
        fixture = ProviderFixture([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}])
        first = fixture.completion(self.request())['choices'][0]
        self.assertEqual(first['finish_reason'], 'tool_calls')
        self.assertEqual(json.loads(first['message']['tool_calls'][0]['function']['arguments']),
                         {'command': '/usr/bin/true'})
        self.assertEqual(fixture.completion(self.request(True))['choices'][0]['finish_reason'], 'stop')
        self.assertNotIn('private', json.dumps(fixture.observations))
        request = self.request()
        request['messages'][0]['content'] = 'tool result qualification-0'
        self.assertEqual(fixture.completion(request)['choices'][0]['finish_reason'], 'tool_calls')
        request['tools'] = []
        with self.assertRaises(ValueError):
            fixture.completion(request)

    def test_http_stream_and_models_use_bounded_owned_loopback_server(self):
        with ProviderFixture([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]) as fixture:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(fixture.base_url + '/models', timeout=5) as response:
                self.assertEqual(json.load(response)['data'][0]['id'], MODEL)
            request = urllib.request.Request(fixture.base_url + '/chat/completions',
                data=json.dumps(dict(self.request(), stream=True)).encode(),
                headers={'Content-Type': 'application/json'})
            with opener.open(request, timeout=5) as response:
                raw = response.read()
                self.assertIn(b'"object": "chat.completion.chunk"', raw)
                self.assertIn(b'"index": 0', raw)
                self.assertTrue(raw.endswith(b'data: [DONE]\n\n'))
            with self.assertRaises(urllib.error.HTTPError) as error:
                opener.open(fixture.base_url + '/unknown', timeout=5)
            self.assertEqual(error.exception.code, 404)
        self.assertFalse(fixture.thread.is_alive())

    def test_foreign_model_shapes_and_request_flood_are_refused(self):
        fixture = ProviderFixture([{'name': 'command', 'arguments': {}}])
        for request in [{}, {'model': MODEL, 'messages': ['invalid']}, dict(self.request(), model='foreign')]:
            with self.assertRaises(ValueError):
                fixture.completion(request)
        for _ in range(64):
            fixture.completion(self.request())
        with self.assertRaises(ValueError):
            fixture.completion(self.request())

    def test_gui_discovery_uses_existing_typed_litellm_model_info_protocol_only(self):
        with ProviderFixture([{'name': 'command', 'arguments': {}}]) as fixture:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with self.assertRaises(urllib.error.HTTPError) as refused:
                opener.open(fixture.base_url + '/model/info', timeout=5)
            self.assertEqual(refused.exception.code, 404)
            fixture.gui_purpose = True
            request = urllib.request.Request(fixture.base_url + '/model/info',
                headers={'Authorization': 'Bearer ' + GUI_API_KEY})
            with opener.open(request, timeout=5) as response:
                row = json.load(response)['data'][0]
            self.assertEqual(row['model_name'], MODEL)
            self.assertIs(row['model_info']['supports_function_calling'], True)
            self.assertEqual(row['model_info']['mode'], 'chat')
            self.assertEqual(row['model_info']['max_input_tokens'], 131072)
            self.assertIs(row['model_info']['supports_vision'], False)
            self.assertEqual(fixture.discovery_observations, [{'kind': 'litellm-proxy',
                'model': MODEL, 'path': '/v1/model/info', 'structured_tools': True,
                'fixture_credential_observed': True}])
            fixture.discovery_observations *= 64
            with self.assertRaises(urllib.error.HTTPError) as flooded:
                opener.open(request, timeout=5)
            self.assertEqual(flooded.exception.code, 429)

    def test_missing_tool_has_closed_reason_without_acceptance_change(self):
        fixture = ProviderFixture([{'name': 'command', 'arguments': {}}])
        request = dict(self.request(), tools=[])
        with self.assertRaises(FixtureRefusal) as error:
            fixture.completion(request)
        self.assertEqual(error.exception.code, 'required-tools-missing')
        self.assertEqual(fixture.observations, [])

    def test_actual_http_refusal_retains_only_bounded_structural_evidence(self):
        with ProviderFixture.gui([{'name': 'command', 'arguments': {}}]) as fixture:
            request = dict(self.request(), tools=[{'type': 'function', 'function': {'name': 'page_open',
                          'arguments': 'PRIVATE TOOL ARGUMENTS'}}])
            request['messages'].append({'role': 'PRIVATE ROLE', 'content': 'PRIVATE MESSAGE'})
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            wire = urllib.request.Request(fixture.base_url + '/chat/completions',
                data=json.dumps(request).encode(), headers={'Authorization': 'Bearer ' + GUI_API_KEY})
            with self.assertRaises(urllib.error.HTTPError) as refused:
                opener.open(wire, timeout=5)
            self.assertEqual(refused.exception.code, 400)
            self.assertEqual(json.load(refused.exception), {'error': 'fixture-request-refused'})
            record, = fixture.transport_observations
            self.assertEqual(record['refusal_code'], 'required-tools-missing')
            self.assertEqual(record['request_shape']['offered_tools'], ['page_open'])
            self.assertEqual(record['request_shape']['role_counts']['unknown_count'], 1)
            self.assertIs(record['request_shape']['expected_model'], True)
            self.assertEqual(record['request_shape']['field_types'],
                             {'model': 'string', 'messages': 'array', 'tools': 'array'})
            self.assertNotIn('PRIVATE', json.dumps(record))
            self.assertNotIn('private fixture prompt', json.dumps(record))
            self.assertEqual(fixture.observations, [])

    def test_refusal_shape_clips_counts_and_refuses_arbitrary_identifiers(self):
        request = {'model': 'PRIVATE MODEL', 'messages': [{'role': 'PRIVATE'}] * 257,
                   'tools': [{'function': {'name': 'PRIVATE SECRET/'}}] * 257}
        shape = request_shape(request)
        self.assertIs(shape['expected_model'], False)
        self.assertTrue(shape['messages_clipped'])
        self.assertTrue(shape['tools_clipped'])
        self.assertEqual(shape['invalid_tool_identifiers'], 64)
        self.assertEqual(shape['offered_tools'], [])
        self.assertNotIn('PRIVATE', json.dumps(shape))
        self.assertEqual(request_shape(None), {'request_type': 'null'})

    def test_two_real_authenticated_sse_turns_have_fresh_bounded_transport_observations(self):
        barrier = ResponseBarrier()
        with ProviderFixture.gui([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}],
                acquire='command', response_barrier=barrier) as fixture:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            def send(messages, available):
                request = urllib.request.Request(fixture.base_url + '/chat/completions',
                    data=json.dumps({'model': MODEL, 'messages': messages, 'stream': True,
                        'tools': [{'type': 'function', 'function': {'name': name}} for name in available]}).encode(),
                    headers={'Authorization': 'Bearer ' + GUI_API_KEY})
                with opener.open(request, timeout=5) as response:
                    self.assertEqual(response.headers['Content-Type'], 'text/event-stream')
                    raw = response.read().decode()
                self.assertTrue(raw.endswith('data: [DONE]\n\n'))
                return json.loads(raw.split('\n\n')[0].removeprefix('data: '))['choices'][0]
            histories = []
            for number in range(2):
                prior_count = len(fixture.transport_observations)
                acquisition = fixture.acquisition
                history = [{'role': 'user', 'content': 'PRIVATE FIXTURE PROMPT'}]
                with ThreadPoolExecutor(max_workers=1) as pool:
                    pending = pool.submit(send, history, ['request_tools'])
                    barrier.await_held()
                    self.assertEqual(len(fixture.transport_observations), prior_count)
                    barrier.release()
                    load = pending.result(timeout=5)['delta']['tool_calls'][0]
                barrier.await_response_written()
                load['id'] = f'call_{number * 2 + 1:08x}'
                load.pop('index', None)
                history.extend([{'role': 'assistant', 'tool_calls': [load]},
                    {'role': 'tool', 'tool_call_id': load['id'], 'content': framed(invocation=receipt(
                        id=str(uuid.uuid4()), tool_call_id=load['id']))}])
                command = send(history, ['command'])['delta']['tool_calls'][0]
                command['id'] = f'call_{number * 2 + 2:08x}'
                command.pop('index', None)
                history.extend([{'role': 'assistant', 'tool_calls': [command]},
                    {'role': 'tool', 'tool_call_id': command['id'], 'content': framed(
                        payload={'private': 'PRIVATE OUTPUT'}, invocation=receipt(id=str(uuid.uuid4()),
                            tool='command', tool_call_id=command['id']))}])
                self.assertEqual(send(history, ['command'])['finish_reason'], 'stop')
                self.assertTrue(acquisition.completed)
                rows = fixture.transport_observations[prior_count:]
                self.assertEqual(len(rows), 3)
                self.assertTrue(all(row == {'method': 'POST', 'path': '/v1/chat/completions',
                    'status': 200, 'fixture_credential_observed': True} for row in rows))
                histories.append(list(fixture.transport_observations))
                if number == 0:
                    barrier = fixture.begin_next_turn()
                    self.assertIsNot(fixture.acquisition, acquisition)
                    self.assertEqual(fixture.transport_observations, histories[0])
            self.assertEqual(len(fixture.transport_observations), 6)
            self.assertEqual(fixture.transport_observations[:3], histories[0])
            self.assertNotIn(GUI_API_KEY, json.dumps(fixture.transport_observations))
            self.assertNotIn('PRIVATE', json.dumps(fixture.transport_observations))
            self.assertFalse(fixture.transport_clipped)

    def test_sse_wrong_credential_is_refused_once_without_acquisition_or_payload_evidence(self):
        with ProviderFixture.gui([{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}],
                acquire='command') as fixture:
            request = urllib.request.Request(fixture.base_url + '/chat/completions',
                data=json.dumps(dict(self.request(), stream=True)).encode(),
                headers={'Authorization': 'Bearer PRIVATE WRONG KEY'})
            with self.assertRaises(urllib.error.HTTPError) as failed:
                urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request, timeout=5)
            self.assertEqual(failed.exception.code, 401)
            self.assertEqual(fixture.transport_observations, [{'method': 'POST',
                'path': '/v1/chat/completions', 'status': 401, 'fixture_credential_observed': False}])
            self.assertFalse(fixture.acquisition.issued)
            self.assertNotIn('PRIVATE', json.dumps(fixture.transport_observations))
