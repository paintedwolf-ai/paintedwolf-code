"""Drive a deterministic tool turn through a genuine installed engine's public API."""
from __future__ import annotations

from pathlib import Path
import re
import time
import uuid

from .provider import MODEL
from .records import CaseUnavailable


def accepted(app, method: str, path: str, payload, statuses: set[int]) -> dict:
    response = app.request(method, path, payload)
    if response['status'] not in statuses or not isinstance(response['body'], dict):
        raise ValueError('installed API did not accept the required fixture operation')
    return response['body']


def identity(document: dict) -> str:
    value = document.get('id')
    if not isinstance(value, str) or str(uuid.UUID(value)) != value:
        raise ValueError('installed API returned an invalid resource identity')
    return value


def wait_settled(app, session_id: str):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        session = accepted(app, 'GET', '/v1/sessions/' + session_id, None, {200})
        if session.get('status') == 'idle':
            return
        if session.get('status') == 'error':
            raise ValueError('installed fixture session failed before settlement')
        time.sleep(0.05)
    raise ValueError('installed fixture session did not settle within its bounded interval')


class ToolTurn:
    def __init__(self, app, provider, project: Path, *, vision: bool = False):
        if type(vision) is not bool:
            raise ValueError('installed fixture vision capability must be explicit')
        self.capabilities = {'chat', 'streaming', 'tools'} | ({'vision'} if vision else set())
        self.app, self.provider, self.project = app, provider, project
        self.session_id = None
        self.observations = {}

    def __enter__(self):
        provider_id = 'qualification-' + uuid.uuid4().hex
        accepted(self.app, 'POST', '/v1/providers', {
            'id': provider_id, 'kind': 'openai-compatible',
            'base_url': self.provider.base_url, 'requires_api_key': False,
            'models': [{'id': MODEL, 'context_length': 131072,
                        'capabilities': {name: {'state': 'supported' if name in
                            self.capabilities else 'unsupported',
                            'sources': ['installed-fixture']} for name in
                            ('chat', 'streaming', 'tools', 'vision', 'reasoning',
                             'structured_output', 'prompt_caching')}}]}, {201})
        project = accepted(self.app, 'POST', '/v1/projects', {
            'name': 'Installed qualification', 'roots': [{'path': str(self.project), 'is_primary': True}]}, {201})
        project_id = identity(project)
        session = accepted(self.app, 'POST', '/v1/sessions', {
            'project_id': project_id, 'posture': 'build', 'provider_id': provider_id, 'model': MODEL}, {202})
        self.session_id = identity(session)
        self.observations.update({'provider_id': provider_id, 'project_id': project_id,
                                  'session_id': self.session_id})
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                session = accepted(self.app, 'GET', '/v1/sessions/' + self.session_id, None, {200})
                if session.get('status') == 'idle':
                    path = session.get('workspace_path')
                    if not isinstance(path, str) or not Path(path).is_absolute() or not Path(path).is_dir():
                        raise ValueError('installed fixture has no prepared workspace')
                    self.workspace = Path(path)
                    self.observations['workspace'] = path
                    return self
                if session.get('status') == 'error':
                    raise CaseUnavailable('installed fixture session preparation failed')
                time.sleep(0.2)
            raise CaseUnavailable('installed fixture session preparation timed out')
        except BaseException:
            self.stop()
            raise

    def submit(self):
        names = [call.get('name') for call in self.provider.calls]
        if not 1 <= len(names) <= 16 or any(not isinstance(name, str) or
                re.fullmatch(r'[a-z][a-z0-9_]{0,63}', name) is None for name in names):
            raise ValueError('installed fixture task requires bounded exact tool identifiers')
        task = 'Run the installed qualification fixture using these tools: ' + ', '.join(names) + '.'
        accepted(self.app, 'POST', f'/v1/sessions/{self.session_id}/prompts', {
            'operation_id': str(uuid.uuid4()), 'text': task}, {202})

    def results(self) -> list[dict]:
        document = accepted(self.app, 'GET', f'/v1/sessions/{self.session_id}/messages?limit=500', None, {200})
        messages = document.get('messages')
        if not isinstance(messages, list) or any(not isinstance(row, dict) for row in messages):
            raise ValueError('installed transcript returned an invalid message window')
        return [row['tool_result'] for row in messages if isinstance(row.get('tool_result'), dict)]

    def wait(self, timeout=60) -> list[dict]:
        if not 0 < timeout <= 300:
            raise ValueError('installed fixture completion timeout exceeds its bound')
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            results = self.results()
            if results:
                self.observations['results'] = [
                    {key: row[key] for key in ('tool', 'tool_call_id', 'outcome', 'codes', 'invocation') if key in row}
                    for row in results]
                return results
            session = accepted(self.app, 'GET', '/v1/sessions/' + self.session_id, None, {200})
            if session.get('status') == 'error':
                raise CaseUnavailable('installed fixture turn failed before producing a tool result')
            time.sleep(0.2)
        raise CaseUnavailable('installed fixture tool result timed out')

    def stop(self):
        if self.session_id is not None:
            accepted(self.app, 'POST', f'/v1/sessions/{self.session_id}/abort', {}, {200})
            self.session_id = None

    def __exit__(self, kind, value, traceback):
        self.stop()
