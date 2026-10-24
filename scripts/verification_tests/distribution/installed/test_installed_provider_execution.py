"""The installed fixture drives ordinary provider transport without real model calls."""
import json
import unittest
import urllib.error
import urllib.request

from linux_release.installed_cases.provider import (MODEL, GUI_API_KEY, ProviderFixture,
                                                    FixtureRefusal, request_shape)


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
