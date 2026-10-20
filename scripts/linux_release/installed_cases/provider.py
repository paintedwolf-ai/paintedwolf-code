"""A bounded local provider fixture using the installed app's ordinary API transport."""
from __future__ import annotations

import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading

MODEL = 'installed-qualification'
LIMIT = 2 << 20
GUI_PORT = 50179
GUI_LABEL = 'Installed UI qualification'
GUI_ENDPOINT = f'http://127.0.0.1:{GUI_PORT}/v1'


class ProviderFixture:
    def __init__(self, calls: list[dict]):
        if not calls or len(calls) > 16:
            raise ValueError('fixture requires one to sixteen tool calls')
        for call in calls:
            if (set(call) != {'name', 'arguments'} or not isinstance(call['name'], str)
                    or not call['name'] or not isinstance(call['arguments'], dict)):
                raise ValueError('fixture tool call has an invalid typed shape')
        self.calls = copy.deepcopy(calls)
        self.observations = []
        self.lock = threading.Lock()
        self.server = None
        self.thread = None
        self.gui_purpose = False

    @classmethod
    def gui(cls, calls: list[dict]):
        fixture = cls(calls)
        fixture.gui_purpose = True
        return fixture

    def completion(self, request: dict) -> dict:
        if not isinstance(request, dict) or request.get('model') != MODEL:
            raise ValueError('fixture received a foreign model request')
        messages, tools = request.get('messages'), request.get('tools', [])
        if not isinstance(messages, list) or not messages or not isinstance(tools, list):
            raise ValueError('fixture requires structured chat messages and tools')
        if any(not isinstance(row, dict) or not isinstance(row.get('role'), str) for row in messages):
            raise ValueError('fixture received malformed chat messages')
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
        if not finished:
            if not all(call['name'] in available for call in self.calls):
                raise ValueError('required fixture tools were not offered by the installed host')
            message['tool_calls'] = [
                {'id': f'qualification-{i}', 'type': 'function', 'function': {
                    'name': call['name'], 'arguments': json.dumps(call['arguments'])}}
                for i, call in enumerate(self.calls)]
        with self.lock:
            if len(self.observations) >= 64:
                raise ValueError('fixture request bound exceeded')
            self.observations.append({'model': MODEL, 'roles': [row['role'] for row in messages],
                                      'tools': sorted(available), 'completed': finished})
        return {'id': 'qualification-completion', 'object': 'chat.completion', 'created': 1,
                'model': MODEL, 'choices': [{'index': 0, 'message': message,
                'finish_reason': 'stop' if finished else 'tool_calls'}],
                'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}}

    def __enter__(self):
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def respond(self, status, value):
                raw = json.dumps(value).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self):
                if self.path != '/v1/models':
                    return self.respond(404, {'error': 'fixture-path-refused'})
                self.respond(200, {'object': 'list', 'data': [
                    {'id': MODEL, 'object': 'model', 'created': 1, 'owned_by': 'qualification'}]})

            def do_POST(self):
                self.connection.settimeout(10)
                try:
                    length = int(self.headers.get('Content-Length', '0'))
                    if self.path != '/v1/chat/completions' or not 0 < length <= LIMIT:
                        raise ValueError('fixture request boundary refused')
                    raw = self.rfile.read(length)
                    if len(raw) != length:
                        raise ValueError('fixture request was truncated')
                    request = json.loads(raw)
                    completion = fixture.completion(request)
                    if not request.get('stream', False):
                        return self.respond(200, completion)
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
                except (ValueError, TypeError, OSError):
                    self.respond(400, {'error': 'fixture-request-refused'})

        try:
            self.server = ThreadingHTTPServer(('127.0.0.1', GUI_PORT if self.gui_purpose else 0), Handler)
        except OSError:
            if self.gui_purpose:
                from .records import CaseUnavailable
                raise CaseUnavailable('fixed-GUI-provider-port-unavailable') from None
            raise
        self.server.daemon_threads = True
        self.base_url = f'http://127.0.0.1:{self.server.server_port}/v1'
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return self

    def __exit__(self, kind, value, traceback):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        if self.thread.is_alive():
            raise RuntimeError('fixture provider failed to stop')
