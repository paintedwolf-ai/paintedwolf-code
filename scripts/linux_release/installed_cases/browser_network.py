"""Test actual loopback refusal and a narrowly approved owned development server."""
from __future__ import annotations

from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
import time
import tempfile
import urllib.parse
import uuid

from .app import InstalledApp
from .approval import token
from .browser import HTML, opened
from .provider import ProviderFixture
from .records import CaseUnavailable
from .workflow import ToolTurn, accepted, wait_settled


class LocalServer:
    def __init__(self, body: str):
        self.body, self.requests, self.lock = body.encode(), [], threading.Lock()
        self.server = self.thread = None

    def __enter__(self):
        fixture = self
        class Handler(BaseHTTPRequestHandler):
            def setup(self):
                super().setup()
                self.connection.settimeout(5)
            def log_message(self, *args):
                pass
            def do_GET(self):
                with fixture.lock:
                    if len(fixture.requests) >= 64 or len(self.path) > 4096:
                        self.send_error(429)
                        return
                    fixture.requests.append(self.path)
                self.send_response(200 if self.path == '/qualification' else 404)
                self.send_header('Content-Type', 'text/html; charset=utf-8')
                self.send_header('Content-Length', str(len(fixture.body)))
                self.send_header('Cache-Control', 'no-store')
                self.end_headers()
                self.wfile.write(fixture.body)
        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.server.daemon_threads = False
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        return self

    @property
    def port(self):
        return self.server.server_address[1]

    @property
    def url(self):
        return f'http://127.0.0.1:{self.port}/qualification'

    def baseline(self):
        connection = HTTPConnection('127.0.0.1', self.port, timeout=5)
        try:
            connection.request('GET', '/qualification')
            response = connection.getresponse()
            if response.status != 200 or response.read((1 << 20) + 1) != self.body:
                raise ValueError('owned local server failed its unconfined positive control')
        finally:
            connection.close()
        with self.lock:
            self.requests.clear()

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        if self.thread.is_alive():
            raise ValueError('owned local server did not stop within its fixture bound')


def approval_selection(checkpoint: dict, session: str, calls: list[dict], arguments: dict) -> tuple[str, str]:
    checkpoint_id = checkpoint.get('id')
    if (not isinstance(checkpoint_id, str) or str(uuid.UUID(checkpoint_id)) != checkpoint_id
            or checkpoint.get('session_id') != session or checkpoint.get('kind') != 'tool_approval'
            or checkpoint.get('status') != 'pending'):
        raise ValueError('local server checkpoint is not pending for the selected fixture session')
    approval = checkpoint.get('tool_approval', {})
    plan, call_id = approval.get('plan', {}), approval.get('tool_call_id')
    rows = [row for row in calls if row.get('id') == call_id]
    if (not token(call_id) or len(rows) != 1 or rows[0].get('name') != 'page_open'
            or rows[0].get('args') != arguments or rows[0].get('args_malformed') or rows[0].get('args_truncated')
            or approval.get('joined_count', 1) != 1 or approval.get('joined_tool_call_ids')
            or plan.get('held_release') is not None or plan.get('stage') != 'pre_spawn'
            or plan.get('subject', {}).get('kind') != 'loopback_connect'):
        raise ValueError('local server approval is not bound to the exact narrowed fixture operation')
    options = plan.get('options')
    if not isinstance(options, list) or any(not isinstance(row, dict) for row in options):
        raise ValueError('local server approval omitted typed options')
    selected = [row for row in options if row.get('kind') == 'current_action' and row.get('rung') == 'once'
                and row.get('decision_action') == 'approve' and row.get('disabled', False) is False]
    if (len(selected) != 1 or not token(selected[0].get('id'))
            or sum(row.get('id') == selected[0]['id'] for row in options) != 1):
        raise ValueError('local server fixture has no unique enabled one-shot option')
    return checkpoint_id, selected[0]['id']


def approve(turn, arguments: dict) -> dict:
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        pending = accepted(turn.app, 'GET', f'/v1/sessions/{turn.session_id}/checkpoints?status=pending', None, {200})
        rows = pending.get('checkpoints')
        if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
            raise ValueError('local server checkpoint list is malformed')
        if rows:
            if len(rows) != 1:
                raise ValueError('local server fixture has ambiguous pending approvals')
            transcript = accepted(turn.app, 'GET', f'/v1/sessions/{turn.session_id}/messages?limit=500', None, {200})
            messages = transcript.get('messages')
            if not isinstance(messages, list) or any(not isinstance(row, dict) for row in messages):
                raise ValueError('local server transcript is malformed')
            calls = []
            for message in messages:
                entries = message.get('tool_calls', [])
                if not isinstance(entries, list) or any(not isinstance(row, dict) for row in entries):
                    raise ValueError('local server transcript calls are malformed')
                calls.extend(entries)
            checkpoint, option = approval_selection(rows[0], turn.session_id, calls, arguments)
            result = accepted(turn.app, 'POST', f'/v1/sessions/{turn.session_id}/checkpoints/{checkpoint}',
                              {'kind': 'tool_approval', 'action': 'approve', 'option_id': option}, {200})
            if (result.get('id') != checkpoint or result.get('session_id') != turn.session_id
                    or result.get('kind') != 'tool_approval' or result.get('status') != 'approved'):
                raise ValueError('local server approval did not resolve its exact one-shot checkpoint')
            return {'checkpoint_id': checkpoint, 'option_id': option, 'status': 'approved'}
        time.sleep(0.1)
    raise CaseUnavailable('installed local server operation produced no reviewable approval')


def refused(results: list[dict], arguments: dict) -> dict:
    rows = [row for row in results if row.get('tool') == 'page_open']
    if len(rows) != 1:
        raise ValueError('local service fixture omitted its unique page refusal')
    row = rows[0]
    invocation = row.get('invocation', {})
    if (row.get('outcome') != 'rejected' or row.get('tool_args') != arguments
            or 'SANDBOX_TRY_LOOPBACK_CONNECT' not in row.get('codes', [])
            or invocation.get('tool') != 'page_open' or invocation.get('status') != 'rejected'
            or type(invocation.get('invoked')) is not bool or row.get('visual')):
        raise ValueError('local service fixture did not retain its typed capability refusal')
    return {key: row[key] for key in ('tool', 'tool_call_id', 'tool_args', 'codes', 'invocation', 'outcome') if key in row}


def denied_local_services(binding: dict) -> tuple[str, dict, dict]:
    with LocalServer(HTML) as server, tempfile.TemporaryDirectory(prefix='paintedwolf-denied-server-') as directory:
        server.baseline()
        arguments = {'url': server.url}
        with ProviderFixture([{'name': 'page_open', 'arguments': arguments}]) as provider:
            with InstalledApp(binding) as app, ToolTurn(app, provider, Path(directory)) as turn:
                turn.submit()
                result = refused(turn.wait(timeout=60), arguments)
                facts = {'refusal': result, 'unconfined_server_reachable': True,
                         'turn': turn.observations, 'provider': provider.observations, 'app': app.observations}
            streams = app.streams()
    # Close and join the owned server before judging the absence of any requests.
    facts['server_requests'] = list(server.requests)
    passed = not server.requests and app.observations.get('capture_complete') is True and not app.observations.get('output_limit')
    return 'passed' if passed else 'failed', facts, streams


def allowed_local_server(binding: dict) -> tuple[str, dict, dict]:
    with LocalServer(HTML) as other:
        other.baseline()
        body = HTML + f'<img src="{other.url}" alt="unapproved sibling service">'
        with LocalServer(body) as server, tempfile.TemporaryDirectory(prefix='paintedwolf-approved-server-') as directory:
            server.baseline()
            arguments = {'url': server.url, 'capability_request': {'loopback_connect': {'ports': [server.port]}}}
            with ProviderFixture([{'name': 'page_open', 'arguments': arguments}]) as provider:
                with InstalledApp(binding) as app, ToolTurn(app, provider, Path(directory)) as turn:
                    turn.submit()
                    authority = approve(turn, arguments)
                    page = opened(turn.wait(timeout=90))
                    wait_settled(app, turn.session_id)
                    url = urllib.parse.urlsplit(page['page'].get('final_url', ''))
                    correct = (url.scheme == 'http' and url.hostname == '127.0.0.1' and url.port == server.port
                               and url.path == '/qualification' and not url.query and not url.fragment)
                    facts = {'approval': authority, 'page': page, 'unconfined_servers_reachable': True,
                             'requested_ports': [server.port], 'unapproved_sibling_port': other.port,
                             'turn': turn.observations, 'provider': provider.observations, 'app': app.observations}
                streams = app.streams()
    facts.update({'approved_server_requests': list(server.requests), 'unapproved_server_requests': list(other.requests)})
    passed = (correct and '/qualification' in server.requests and not other.requests
              and app.observations.get('capture_complete') is True and not app.observations.get('output_limit'))
    return 'passed' if passed else 'failed', facts, streams
