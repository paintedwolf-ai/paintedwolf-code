"""A bounded local provider fixture using the installed app's ordinary API transport."""
from __future__ import annotations

import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import re
import threading

from ..installed_evidence import IDENTIFIER
from .provider_acquisition import AcquisitionRefusal, CommandAcquisition

MODEL = 'installed-qualification'
LIMIT = 2 << 20
GUI_LABEL = 'Installed UI qualification'
GUI_KIND = 'litellm-proxy'
GUI_API_KEY = 'installed-local-fixture-key'


REFUSALS = frozenset({'foreign-model', 'messages-or-tools-shape', 'message-role-shape',
                      'required-tools-missing', 'request-count-bound', 'request-boundary',
                      'request-truncated', 'request-invalid'})


class FixtureRefusal(ValueError):
    def __init__(self, code):
        if code not in REFUSALS:
            raise ValueError('fixture refusal code is not declared')
        self.code = code
        super().__init__(code)


def request_shape(request):
    if not isinstance(request, dict):
        return {'request_type': json_type(request)}
    result = {'field_types': {name: json_type(request.get(name)) for name in ('model', 'messages', 'tools')},
              'expected_model': request.get('model') == MODEL}
    messages, tools = request.get('messages'), request.get('tools')
    if isinstance(messages, list):
        counts = dict.fromkeys(('system', 'developer', 'user', 'assistant', 'tool', 'function', 'unknown_count'), 0)
        for row in messages[:256]:
            role = row.get('role') if isinstance(row, dict) else None
            key = role if isinstance(role, str) and role in counts and role != 'unknown_count' else 'unknown_count'
            counts[key] += 1
        result.update(message_count=len(messages), role_counts=counts, messages_clipped=len(messages) > 256)
    if isinstance(tools, list):
        names, invalid = set(), 0
        for row in tools[:64]:
            function = row.get('function') if isinstance(row, dict) and row.get('type') == 'function' else None
            name = function.get('name') if isinstance(function, dict) else None
            if isinstance(name, str) and 1 <= len(name) <= 128 and IDENTIFIER.fullmatch(name):
                names.add(name)
            else:
                invalid += 1
        result.update(tool_count=len(tools), offered_tools=sorted(names),
                      invalid_tool_identifiers=invalid, tools_clipped=len(tools) > 64)
    return result


def json_type(value):
    return {dict: 'object', list: 'array', str: 'string', int: 'number', float: 'number',
            bool: 'boolean', type(None): 'null'}.get(type(value), 'other')


def gui_endpoint(port: int) -> str:
    """The loopback endpoint a GUI-purpose fixture serves on its bound port."""
    if type(port) is not int or not 1024 <= port <= 65535:
        raise ValueError('GUI provider fixture port is outside the unprivileged range')
    return f'http://127.0.0.1:{port}/v1'


class _FixtureServer(ThreadingHTTPServer):
    allow_reuse_address = False
    allow_reuse_port = False


class ProviderFixture:
    def __init__(self, calls: list[dict], *, response_barrier=None):
        if not calls or len(calls) > 16:
            raise ValueError('fixture requires one to sixteen tool calls')
        for call in calls:
            if (set(call) != {'name', 'arguments'} or not isinstance(call['name'], str)
                    or not call['name'] or not isinstance(call['arguments'], dict)):
                raise ValueError('fixture tool call has an invalid typed shape')
        self.calls = copy.deepcopy(calls)
        self.observations = []
        self.discovery_observations = []
        self.transport_observations = []
        self.transport_clipped = False
        self.transport_lock = threading.Lock()
        self.lock = threading.Lock()
        self.server = None
        self.thread = None
        self.gui_purpose = False
        self.response_barrier = response_barrier
        self.port = 0
        self.acquisition = None

    @classmethod
    def gui(cls, calls: list[dict], *, endpoint: str | None = None,
            acquire_command: bool = False, response_barrier=None):
        fixture = cls(calls, response_barrier=response_barrier)
        fixture.gui_purpose = True
        if type(acquire_command) is not bool:
            raise ValueError('GUI acquisition mode must be explicit')
        if acquire_command:
            if calls != [{'name': 'command', 'arguments': {'command': '/usr/bin/true'}}]:
                raise ValueError('GUI acquisition requires the fixed protected command fixture')
            fixture.acquisition = CommandAcquisition()
        if endpoint is not None:
            match = re.fullmatch(r'http://127\.0\.0\.1:([0-9]{4,5})/v1', endpoint)
            if match is None or gui_endpoint(int(match.group(1))) != endpoint:
                raise ValueError('GUI fixture recovery requires its canonical loopback endpoint')
            fixture.port = int(match.group(1))
        return fixture

    def completion(self, request: dict) -> dict:
        if not isinstance(request, dict) or request.get('model') != MODEL:
            raise FixtureRefusal('foreign-model')
        messages, tools = request.get('messages'), request.get('tools', [])
        if not isinstance(messages, list) or not messages or not isinstance(tools, list):
            raise FixtureRefusal('messages-or-tools-shape')
        if any(not isinstance(row, dict) or not isinstance(row.get('role'), str) for row in messages):
            raise FixtureRefusal('message-role-shape')
        available = {row['function']['name'] for row in tools
                     if isinstance(row, dict) and row.get('type') == 'function'
                     and isinstance(row.get('function'), dict)
                     and isinstance(row['function'].get('name'), str)}
        last_user = max((i for i, row in enumerate(messages) if row['role'] == 'user'), default=-1)
        results = {row.get('tool_call_id') for row in messages[last_user + 1:]
                   if row['role'] == 'tool' and isinstance(row.get('tool_call_id'), str)}
        expected = {f'qualification-{i}' for i in range(len(self.calls))}
        finished = expected.issubset(results)
        message = {'role': 'assistant', 'content': 'Fixture complete.' if finished else None}
        acquisition = self.acquisition.advance(messages, available) if self.acquisition is not None else None
        if self.acquisition is not None:
            finished = self.acquisition.completed
            message['content'] = 'Fixture complete.' if finished else None
        if acquisition is not None:
            if finished:
                raise AcquisitionRefusal('acquisition-result-invalid')
            message['tool_calls'] = [acquisition]
        elif not finished:
            if not all(call['name'] in available for call in self.calls):
                raise FixtureRefusal('required-tools-missing')
            message['tool_calls'] = [
                {'id': f'qualification-{i}', 'type': 'function', 'function': {
                    'name': call['name'], 'arguments': json.dumps(call['arguments'])}}
                for i, call in enumerate(self.calls)]
        with self.lock:
            if len(self.observations) >= 64:
                raise FixtureRefusal('request-count-bound')
            self.observations.append({'model': MODEL, 'roles': [row['role'] for row in messages],
                                      'tools': sorted(available), 'completed': finished,
                                      **({'schema_acquisition': self.acquisition.witness,
                                          'acquisition_requested': acquisition is not None}
                                         if self.acquisition is not None else {})})
        return {'id': 'qualification-completion', 'object': 'chat.completion', 'created': 1,
                'model': MODEL, 'choices': [{'index': 0, 'message': message,
                'finish_reason': 'stop' if finished else 'tool_calls'}],
                'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}}

    def __enter__(self):
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def respond(self, status, value, *, refusal=None, shape=None):
                with fixture.transport_lock:
                    if len(fixture.transport_observations) < 64:
                        record = {'method': self.command,
                            'path': self.path if self.path in {'/v1/model/info', '/v1/models', '/v1/chat/completions'} else 'other',
                            'status': status, 'fixture_credential_observed': self.headers.get('Authorization') == 'Bearer ' + GUI_API_KEY}
                        if refusal is not None:
                            record.update(refusal_code=refusal, request_shape=shape)
                        fixture.transport_observations.append(record)
                    else:
                        fixture.transport_clipped = True
                raw = json.dumps(value).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                if fixture.gui_purpose and self.path == '/v1/model/info':
                    if self.headers.get('Authorization') != 'Bearer ' + GUI_API_KEY:
                        return self.respond(401, {'error': 'fixture-credential-required'})
                    with fixture.lock:
                        if len(fixture.discovery_observations) >= 64:
                            return self.respond(429, {'error': 'fixture-discovery-bound'})
                        fixture.discovery_observations.append({'kind': GUI_KIND, 'model': MODEL,
                            'path': '/v1/model/info', 'structured_tools': True, 'fixture_credential_observed': True})
                    return self.respond(200, {'data': [{'model_name': MODEL, 'model_info': {
                        'mode': 'chat', 'max_input_tokens': 131072, 'supports_function_calling': True,
                        'supports_vision': False, 'supports_reasoning': False,
                        'supports_response_schema': False, 'supports_prompt_caching': False}}]})
                if self.path != '/v1/models':
                    return self.respond(404, {'error': 'fixture-path-refused'})
                self.respond(200, {'object': 'list', 'data': [
                    {'id': MODEL, 'object': 'model', 'created': 1, 'owned_by': 'qualification'}]})

            def do_POST(self):
                self.connection.settimeout(10)
                lease, written, shape = None, False, None
                barrier = fixture.response_barrier
                try:
                    length = int(self.headers.get('Content-Length', '0'))
                    if self.path != '/v1/chat/completions' or not 0 < length <= LIMIT:
                        raise FixtureRefusal('request-boundary')
                    if fixture.gui_purpose and self.headers.get('Authorization') != 'Bearer ' + GUI_API_KEY:
                        return self.respond(401, {'error': 'fixture-credential-required'})
                    raw = self.rfile.read(length)
                    if len(raw) != length:
                        raise FixtureRefusal('request-truncated')
                    request = json.loads(raw)
                    shape = request_shape(request)
                    completion = fixture.completion(request)
                    if barrier is not None:
                        lease = barrier.capture(self.connection, raw)
                        if lease is not None and not barrier.wait(lease):
                            return
                    if not request.get('stream', False):
                        self.respond(200, completion)
                        written = True
                        return
                    choice = completion['choices'][0]
                    delta = choice['message'].copy()
                    if 'tool_calls' in delta:
                        delta['tool_calls'] = [dict(row, index=i) for i, row in enumerate(delta['tool_calls'])]
                    chunk = {**completion, 'object': 'chat.completion.chunk',
                             'choices': [{'index': 0, 'delta': delta, 'finish_reason': choice['finish_reason']}]}
                    raw = ('data: ' + json.dumps(chunk) + '\n\ndata: [DONE]\n\n').encode()
                    self.send_response(200)
                    self.send_header('Content-Type', 'text/event-stream')
                    self.send_header('Content-Length', str(len(raw)))
                    self.end_headers()
                    self.wfile.write(raw)
                    written = True
                except (ValueError, TypeError, OSError) as error:
                    if lease is None:
                        code = error.code if isinstance(error, (FixtureRefusal, AcquisitionRefusal)) else 'request-invalid'
                        self.respond(400, {'error': 'fixture-request-refused'}, refusal=code, shape=shape)
                finally:
                    if lease is not None:
                        barrier.finish(lease, written)

        # Fresh fixtures use a kernel-selected port. Recovery exclusively binds
        # the exact prior admitted endpoint; an occupied port refuses without interference.
        self.server = _FixtureServer(('127.0.0.1', self.port), Handler)
        self.server.daemon_threads = True
        self.base_url = gui_endpoint(self.server.server_port)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return self

    def __exit__(self, kind, value, traceback):
        try:
            if self.response_barrier is not None:
                self.response_barrier.close()
        finally:
            self.server.shutdown()
            self.server.server_close()
            self.thread.join(timeout=5)
            if self.thread.is_alive():
                raise RuntimeError('fixture provider failed to stop')
