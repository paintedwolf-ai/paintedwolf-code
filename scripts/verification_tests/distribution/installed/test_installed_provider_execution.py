"""The installed fixture drives ordinary provider transport without real model calls."""
import json
import unittest
import urllib.error
import urllib.request

from linux_release.installed_cases.provider import MODEL, ProviderFixture


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
            with opener.open(fixture.base_url + '/model/info', timeout=5) as response:
                row = json.load(response)['data'][0]
            self.assertEqual(row['model_name'], MODEL)
            self.assertIs(row['model_info']['supports_function_calling'], True)
            self.assertEqual(row['model_info']['mode'], 'chat')
            self.assertEqual(row['model_info']['max_input_tokens'], 131072)
            self.assertIs(row['model_info']['supports_vision'], False)
            self.assertEqual(fixture.discovery_observations, [{'kind': 'litellm-proxy',
                'model': MODEL, 'path': '/v1/model/info', 'structured_tools': True}])
            fixture.discovery_observations *= 64
            with self.assertRaises(urllib.error.HTTPError) as flooded:
                opener.open(fixture.base_url + '/model/info', timeout=5)
            self.assertEqual(flooded.exception.code, 429)
