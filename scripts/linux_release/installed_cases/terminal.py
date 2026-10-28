"""Drive one actual installed PTY through its ordinary terminal tool lifecycle."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import shlex
import sys
import tempfile
import uuid

from .app import InstalledApp
from .approval import token
from .provider import MODEL, ProviderFixture
from .workflow import ToolTurn, accepted, wait_settled

INPUT = 'wolf-🐺'
SCRIPT = '''import json, os, pathlib, signal, sys
signal.alarm(30)
print('Installed terminal fixture ready.', flush=True)
value = sys.stdin.readline().rstrip('\\r\\n')
status = dict(line.split(':', 1) for line in pathlib.Path('/proc/self/status').read_text().splitlines() if ':' in line)
pathlib.Path('qualification-terminal.json').write_text(json.dumps({
    'input': value, 'stdin_tty': os.isatty(0), 'stdout_tty': os.isatty(1),
    'no_new_privs': int(status['NoNewPrivs']), 'seccomp': int(status['Seccomp']),
    'effective_capabilities': int(status['CapEff'], 16)}))
print('Installed terminal fixture finished.', flush=True)
raise SystemExit(0 if value == 'wolf-🐺' else 73)
'''
TOOLS = ('terminal_open', 'terminal_send', 'terminal_read', 'terminal_close')
CALL_TOOLS = {'terminal-fixture-open': 'terminal_open', 'terminal-fixture-send': 'terminal_send',
              **{f'terminal-fixture-read-{index}': 'terminal_read' for index in range(3)},
              'terminal-fixture-close': 'terminal_close'}


def document(raw: str, identifier: str | None = None) -> dict:
    if not isinstance(raw, str) or len(raw.encode()) > 128 << 10:
        raise ValueError('terminal fixture omitted a bounded structured result')
    value = json.loads(raw)
    if (not isinstance(value, dict) or not token(value.get('id')) or len(value['id']) > 128
            or identifier is not None and value['id'] != identifier
            or value.get('confined') is not True or value.get('host_execution', False) is not False):
        raise ValueError('terminal fixture changed its handle or lost its applied boundary')
    return value


class TerminalProvider(ProviderFixture):
    def __init__(self, command: str):
        super().__init__([{'name': 'terminal_open', 'arguments': {'command': command, 'observe': 'ack'}}])
        self.command = command

    def step(self, results: dict[str, str]) -> tuple[str, str, dict] | None:
        if not isinstance(results, dict) or not set(results) <= CALL_TOOLS.keys():
            raise ValueError('terminal fixture received undeclared phase results')
        order = list(CALL_TOOLS)
        for index, name in enumerate(order[:-1]):
            if name not in results and any(later in results for later in order[index + 1:-1]):
                raise ValueError('terminal fixture received out-of-order phase results')
        if 'terminal-fixture-close' in results and 'terminal-fixture-read-0' not in results:
            raise ValueError('terminal fixture closed before its actual PTY observation')
        if 'terminal-fixture-open' not in results:
            return 'terminal-fixture-open', 'terminal_open', {'command': self.command, 'observe': 'ack'}
        opened = document(results['terminal-fixture-open'])
        identifier = opened['id']
        if opened.get('running') is not True:
            raise ValueError('terminal fixture did not open a live confined PTY')
        if 'terminal-fixture-send' not in results:
            return 'terminal-fixture-send', 'terminal_send', {'id': identifier, 'input': INPUT + '{Enter}', 'observe': 'ack'}
        sent = document(results['terminal-fixture-send'], identifier)
        if type(sent.get('running')) is not bool:
            raise ValueError('terminal send omitted its actual running state')
        for index in range(3):
            call = f'terminal-fixture-read-{index}'
            if call not in results:
                return call, 'terminal_read', {'id': identifier, 'idle_ms': 100, 'timeout_ms': 2000, 'max_bytes': 4096}
            read = document(results[call], identifier)
            if (type(read.get('running')) is not bool or read.get('truncated', False) is not False
                    or read.get('page_continuation', False) is not False or read.get('evicted_bytes', 0) != 0):
                raise ValueError('terminal fixture lost its complete bounded observation')
            if not read['running']:
                if type(read.get('exit_code')) is not int or read['exit_code'] != 0:
                    raise ValueError('terminal fixture did not exit successfully')
                if any(f'terminal-fixture-read-{later}' in results for later in range(index + 1, 3)):
                    raise ValueError('terminal fixture received reads after its completed PTY')
                break
        else:
            raise ValueError('terminal fixture did not finish within its bounded read sequence')
        if 'terminal-fixture-close' not in results:
            return 'terminal-fixture-close', 'terminal_close', {'id': identifier}
        closed = document(results['terminal-fixture-close'], identifier)
        if closed.get('closed') is not True or type(closed.get('exit_code')) is not int or closed['exit_code'] != 0:
            raise ValueError('terminal fixture did not close its exact completed PTY')
        return None

    def completion(self, request: dict) -> dict:
        if not isinstance(request, dict) or request.get('model') != MODEL:
            raise ValueError('terminal fixture received a foreign provider request')
        messages, tools = request.get('messages'), request.get('tools')
        if (not isinstance(messages, list) or not messages or not isinstance(tools, list)
                or any(not isinstance(row, dict) or not isinstance(row.get('role'), str) for row in messages)):
            raise ValueError('terminal fixture requires structured messages and tools')
        last_user = max((i for i, row in enumerate(messages) if row['role'] == 'user'), default=-1)
        results = {}
        for row in messages[last_user + 1:]:
            if row['role'] != 'tool':
                continue
            identifier = row.get('tool_call_id')
            if not isinstance(identifier, str) or not identifier.startswith('terminal-fixture-') or identifier in results:
                raise ValueError('terminal fixture received foreign or duplicate tool results')
            results[identifier] = row.get('content')
        step = self.step(results)
        if step is None:
            message, reason = {'role': 'assistant', 'content': 'Installed terminal fixture complete.'}, 'stop'
        else:
            call, tool, arguments = step
            available = {row['function']['name'] for row in tools
                         if isinstance(row, dict) and row.get('type') == 'function'
                         and isinstance(row.get('function'), dict) and isinstance(row['function'].get('name'), str)}
            if tool not in available:
                raise ValueError('installed host did not offer the required terminal tool')
            message = {'role': 'assistant', 'content': None, 'tool_calls': [{'id': call, 'type': 'function',
                       'function': {'name': tool, 'arguments': json.dumps(arguments)}}]}
            reason = 'tool_calls'
        with self.lock:
            if len(self.observations) >= 16:
                raise ValueError('terminal fixture exceeded its bounded protocol exchanges')
            self.observations.append({'phase': 'finished' if step is None else step[1],
                                      'result_ids': sorted(results), 'model': MODEL})
        return {'id': 'terminal-fixture-completion', 'object': 'chat.completion', 'created': 1,
                'model': MODEL, 'choices': [{'index': 0, 'message': message, 'finish_reason': reason}],
                'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}}


def transcript_results(results: list[dict]) -> dict[str, str]:
    selected = {}
    for row in results:
        identifier, tool, invocation = row.get('tool_call_id'), row.get('tool'), row.get('invocation', {})
        if (not token(identifier) or not identifier.startswith('terminal-fixture-') or identifier in selected
                or CALL_TOOLS.get(identifier) != tool or row.get('outcome') != 'completed'
                or invocation.get('tool') != tool or invocation.get('invoked') is not True
                or invocation.get('status') != 'completed'):
            raise ValueError('terminal transcript omitted actual unique completed invocations')
        selected[identifier] = row.get('content')
    return selected


def terminal_interaction(binding: dict) -> tuple[str, dict, dict]:
    with tempfile.TemporaryDirectory(prefix='paintedwolf-installed-terminal-') as directory:
        project = Path(directory)
        fixture = project / 'terminal.py'
        fixture.write_text(SCRIPT)
        command = shlex.join([str(Path(sys.executable).resolve()), str(fixture)])
        with TerminalProvider(command) as provider:
            with InstalledApp(binding) as app, ToolTurn(app, provider, project) as turn:
                accepted(app, 'POST', f'/v1/sessions/{turn.session_id}/prompts', {
                    'operation_id': str(uuid.uuid4()), 'text': 'Use terminal_open, terminal_send, terminal_read and terminal_close to run the installed terminal fixture.'}, {202})
                turn.wait(timeout=90)
                wait_settled(app, turn.session_id)
                results = transcript_results(turn.results())
                if provider.step(results) is not None:
                    raise ValueError('terminal fixture settled before its full actual lifecycle')
                witness = turn.workspace / 'qualification-terminal.json'
                if not witness.is_file() or witness.is_symlink() or witness.stat().st_size > 4096:
                    raise ValueError('terminal fixture omitted its bounded actual PTY observation')
                observation = json.loads(witness.read_bytes())
                expected = {'input': INPUT, 'stdin_tty': True, 'stdout_tty': True, 'no_new_privs': 1,
                            'seccomp': 2, 'effective_capabilities': 0}
                passed = observation == expected and all(type(observation[key]) is type(value) for key, value in expected.items())
                facts = {'pty_observation': observation, 'tool_results': {key: json.loads(raw) for key, raw in results.items()},
                         'provider': provider.observations, 'turn': turn.observations, 'app': app.observations,
                         'fixture_sha256': hashlib.sha256(SCRIPT.encode()).hexdigest()}
            passed = passed and app.observations.get('capture_complete') is True and not app.observations.get('output_limit')
            return 'passed' if passed else 'failed', facts, app.streams()
