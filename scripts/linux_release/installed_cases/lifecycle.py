"""Bind command cancellation and detached-descendant cleanup to installed process identities."""
from __future__ import annotations

from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import select
import shlex
import signal
import subprocess
import sys
import tempfile
import time

from .app import InstalledApp
from .provider import ProviderFixture
from .workflow import ToolTurn, accepted

SCRIPT = '''import json, os, pathlib, resource, sys, time
resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
def identity():
    row = pathlib.Path('/proc/self/stat').read_text().rpartition(') ')[2].split()
    return {'inner_pid': os.getpid(), 'start_ticks': row[19]}
root = pathlib.Path.cwd()
namespace = os.stat('/proc/self/ns/pid').st_ino
primary = identity()
if sys.argv[1] == 'detached':
    read, write = os.pipe()
    child = os.fork()
    if child == 0:
        os.close(read)
        os.setsid()
        if os.fork():
            os._exit(0)
        daemon = identity()
        for name in os.listdir('/proc/self/fd'):
            descriptor = int(name)
            if descriptor != write:
                try:
                    os.close(descriptor)
                except OSError:
                    pass
        os.write(write, json.dumps(daemon).encode())
        os.close(write)
        time.sleep(90)
        os._exit(73)
    os.close(write)
    raw = os.read(read, 4096)
    os.close(read)
    _, first_status = os.waitpid(child, 0)
    members = [primary, json.loads(raw)]
else:
    members, first_status = [primary], 0
temporary = root / 'qualification-observation.tmp'
temporary.write_text(json.dumps({
    'namespace': namespace, 'members': members, 'first_child_reaped': first_status == 0}))
temporary.replace(root / 'qualification-observation.json')
deadline = time.monotonic() + 60
while not (root / 'qualification-release').exists():
    if time.monotonic() >= deadline:
        raise SystemExit(99)
    time.sleep(0.05)
'''

OBSERVER = '''import json, pathlib, sys
namespace = int(sys.argv[1])
expected = {(row['inner_pid'], row['start_ticks']) for row in json.loads(sys.argv[2])}
result = []
for directory in pathlib.Path('/proc').iterdir():
    if not directory.name.isdecimal():
        continue
    try:
        before = (directory / 'stat').read_text().rpartition(') ')[2].split()
        status = dict(line.split(':', 1) for line in (directory / 'status').read_text().splitlines() if ':' in line)
        inner = int(status['NSpid'].split()[-1])
        observed_namespace = (directory / 'ns/pid').stat().st_ino
        after = (directory / 'stat').read_text().rpartition(') ')[2].split()
        if before[19] != after[19] or observed_namespace != namespace or (inner, before[19]) not in expected:
            continue
        result.append({'pid': int(directory.name), 'start_ticks': before[19],
                       'namespace': namespace, 'inner_pid': inner,
                       'uids': [int(value) for value in status['Uid'].split()]})
    except (FileNotFoundError, ProcessLookupError):
        pass
print(json.dumps(result))
'''


def state(identity: dict):
    try:
        row = (Path('/proc') / str(identity['pid']) / 'stat').read_text().rpartition(') ')[2].split()
    except (FileNotFoundError, ProcessLookupError):
        return None
    if len(row) < 20:
        raise ValueError('owned lifecycle process has an invalid stat record')
    return row[0] if row[19] == identity['start_ticks'] else None


def capture(witness: dict) -> list[dict]:
    namespace, members = witness.get('namespace'), witness.get('members')
    if (type(namespace) is not int or namespace <= 0
            or namespace == Path('/proc/self/ns/pid').stat().st_ino
            or not isinstance(members, list) or len(members) not in {1, 2}):
        raise ValueError('installed command omitted its private process namespace identity')
    for member in members:
        if (not isinstance(member, dict) or set(member) != {'inner_pid', 'start_ticks'}
                or type(member['inner_pid']) is not int or member['inner_pid'] <= 0
                or not isinstance(member['start_ticks'], str) or not member['start_ticks'].isdecimal()):
            raise ValueError('installed command returned an invalid process identity')
    result = subprocess.run(['sudo', '-n', str(Path(sys.executable).resolve()), '-c', OBSERVER,
                             str(namespace), json.dumps(members)], capture_output=True, timeout=10)
    if result.returncode or len(result.stdout) > 65536:
        raise OSError('owned process namespace observer unavailable')
    rows = json.loads(result.stdout)
    expected = {(row['inner_pid'], row['start_ticks']) for row in members}
    if (not isinstance(rows, list) or len(rows) > len(expected)
            or any(not isinstance(row, dict) or type(row.get('pid')) is not int or row['pid'] <= 1
                   or row.get('namespace') != namespace or row.get('uids') != [os.getuid()] * 4 for row in rows)
            or not {(row.get('inner_pid'), row.get('start_ticks')) for row in rows}.issubset(expected)
            or len({row['pid'] for row in rows}) != len(rows)):
        raise ValueError('observer did not bind every live fixture process to its installed command')
    return rows


def handle(results: list[dict]) -> str:
    matches = [row for row in results if row.get('tool') == 'command']
    if len(matches) != 1:
        raise ValueError('installed command did not produce one process result')
    process, invocation = matches[0].get('process', {}), matches[0].get('invocation', {})
    value = process.get('handle')
    if (process.get('running') is not True or not isinstance(value, str)
            or re.fullmatch('[A-Za-z0-9_-]+', value) is None
            or invocation.get('invoked') is not True or invocation.get('tool') != 'command'):
        raise ValueError('installed command omitted its live invocation identity')
    return value


def mirror(app, session: str, process: str) -> dict:
    document = accepted(app, 'GET', f'/v1/sessions/{session}/background', None, {200})
    rows = document.get('processes')
    if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
        raise ValueError('installed process mirror has an invalid shape')
    matches = [row for row in rows if row.get('process_id') == process]
    if len(matches) != 1 or type(matches[0].get('running')) is not bool:
        raise ValueError('installed process mirror lost or duplicated the owned process')
    return matches[0]


def witness(turn) -> dict:
    path = turn.workspace / 'qualification-observation.json'
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        if path.exists():
            if path.is_symlink() or path.stat().st_size > 4096:
                raise ValueError('installed process witness exceeds its boundary')
            return json.loads(path.read_bytes())
        time.sleep(0.05)
    raise ValueError('installed process did not produce a bounded identity witness')


def cleanup(owned: list[dict]):
    for row in owned:
        try:
            descriptor = os.pidfd_open(row['pid'], 0)
        except ProcessLookupError:
            continue
        try:
            if state(row) is None:
                continue
            try:
                signal.pidfd_send_signal(descriptor, signal.SIGKILL, None, 0)
            except ProcessLookupError:
                continue
            waiter = select.poll()
            waiter.register(descriptor, select.POLLIN)
            if not waiter.poll(3000):
                raise OSError('owned lifecycle fixture did not exit after exact pidfd cleanup')
        finally:
            os.close(descriptor)


def scenario(binding: dict, cancel: bool) -> tuple[str, dict, dict]:
    owned = []
    try:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-lifecycle-') as directory, ExitStack() as stack:
            root = Path(directory)
            app = stack.enter_context(InstalledApp(binding))
            turns = []
            for index, mode in enumerate(('single', 'detached')):
                project = root / str(index)
                project.mkdir()
                script = project / 'lifecycle.py'
                script.write_text(SCRIPT)
                command = shlex.join([str(Path(sys.executable).resolve()), str(script), mode])
                provider = stack.enter_context(ProviderFixture([{'name': 'command', 'arguments': {
                    'command': command, 'background': index == 0 or cancel, 'wait_ms': 1,
                    'allow_concurrent': True, 'timeout_ms': 90000}}]))
                turn = stack.enter_context(ToolTurn(app, provider, project))
                turn.submit()
                process = handle(turn.wait())
                observation = witness(turn)
                members = capture(observation)
                owned.extend(members)
                if len(members) != len(observation['members']):
                    raise ValueError('installed command lost a fixture process before ownership capture')
                turns.append((turn, process, observation, members))
            auxiliary, target = turns
            turn, process, observation, members = target
            if cancel:
                stop = accepted(app, 'POST', f'/v1/sessions/{turn.session_id}/background/{process}/stop', {}, {200})
                if stop.get('process_id') != process or stop.get('stop_requested') is not True:
                    raise ValueError('installed stop did not acknowledge the selected process')
            else:
                (turn.workspace / 'qualification-release').write_text('release fixed fixture\n')
                stop = None
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                final = mirror(app, turn.session_id, process)
                if final['running'] is False and all(state(row) is None for row in members):
                    break
                time.sleep(0.1)
            auxiliary_state = mirror(app, auxiliary[0].session_id, auxiliary[1])
            unrelated_survives = auxiliary_state['running'] is True and all(state(row) is not None for row in auxiliary[3])
            exit_code = final.get('exit_code')
            passed = (final['running'] is False and type(exit_code) is int
                      and (exit_code != 0 if cancel else exit_code == 0)
                      and all(state(row) is None for row in members) and unrelated_survives
                      and observation.get('first_child_reaped') is True)
            facts = {'target': turn.observations, 'target_processes': members, 'terminal_process': final,
                     'stop': stop, 'unrelated_survives': unrelated_survives,
                     'unrelated_processes': auxiliary[3], 'app': app.observations}
        return 'passed' if passed else 'failed', facts, app.streams()
    finally:
        # These PIDs were bound while alive to the fixed fixture's private namespace.
        cleanup(owned)


def detached_cleanup(binding):
    return scenario(binding, False)


def background_cancel(binding):
    return scenario(binding, True)
