"""Exercise selected Nix authority through the final installed command workflow."""
from __future__ import annotations

import base64
import errno
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import sys
import tempfile
import time
import uuid

from ..installed_runtime import file_identity
from .app import InstalledApp
from .approval import approve_host_execution
from .provider import ProviderFixture
from .products import require_owned_guest
from .records import CaseUnavailable, retain
from .workflow import ToolTurn, wait_settled

SOCKET = '/nix/var/nix/daemon-socket/socket'
SCRIPT = r'''import errno, json, os, pathlib, socket, subprocess, sys
client, daemon, nonce, mode, target, output = sys.argv[1:]
observation = {'nonce': nonce, 'uid': os.getuid(),
               'pid_namespace': os.stat('/proc/self/ns/pid').st_ino}
with socket.socket(socket.AF_UNIX) as connection:
    connection.settimeout(5)
    try:
        connection.connect(daemon)
    except OSError as error:
        observation['socket_errno'] = error.errno
    else:
        observation['socket_errno'] = 0
arguments = [client, '--extra-experimental-features', 'nix-command flakes',
             '--offline', '--option', 'substituters', '', '--option', 'extra-substituters', '',
             '--option', 'builders', '', '--store', 'daemon']
config = pathlib.Path.cwd() / ('nix-config-' + nonce)
config.mkdir(mode=0o700)
environment = {'HOME': str(config), 'NIX_CONF_DIR': str(config),
               'NIX_USER_CONF_FILES': '/dev/null', 'LC_ALL': 'C', 'PATH': '/usr/bin:/bin'}
if mode == 'develop':
    store = subprocess.run(arguments + ['path-info', '--json', '--json-format', '1', output],
                           capture_output=True, timeout=30, env=environment)
    if len(store.stdout) > 65536:
        raise RuntimeError('Nix store identity exceeded its evidence bound')
    observation['store_exit_code'] = store.returncode
    observation['store_stdout'] = store.stdout.decode('utf-8')
if mode == 'daemon':
    arguments += ['store', 'ping', '--json']
else:
    arguments += ['develop', '--no-write-lock-file', target, '--command', 'sh', '-c',
                  "test -x \"$(command -v go)\" && test -x \"$(command -v bun)\" && printf paintedwolf-nix-dev-shell"]
result = subprocess.run(arguments, capture_output=True, timeout=120, env=environment)
if len(result.stdout) > 65536 or len(result.stderr) > 65536:
    raise RuntimeError('Nix workflow exceeded its evidence bound')
observation.update({'exit_code': result.returncode, 'stdout': result.stdout.decode('utf-8'),
                    'stderr_sha256': __import__('hashlib').sha256(result.stderr).hexdigest()})
temporary = pathlib.Path('nix-observation-' + nonce + '.tmp')
temporary.write_text(json.dumps(observation))
temporary.replace('nix-observation.json')
'''


def client(binding: dict) -> Path:
    if binding.get('artifact', {}).get('identity', {}).get('kind') != 'nix-closure':
        raise CaseUnavailable('Nix workflow requires a selected final Nix closure')
    selected = binding.get('payload', {}).get('nix_client')
    if not isinstance(selected, dict) or not isinstance(selected.get('path'), str):
        raise CaseUnavailable('selected closure has no retained physical Nix client')
    path = Path(selected['path'])
    if not path.is_absolute() or path.resolve(strict=True) != path or not str(path).startswith('/nix/store/'):
        raise ValueError('Nix workflow client must be a physical store executable')
    actual = file_identity(path)
    actual.pop('header')
    if actual != {key: value for key, value in selected.items() if key != 'path'} or actual['mode'] & 0o222:
        raise ValueError('Nix workflow client changed after installed binding')
    info = Path(SOCKET).stat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != 0:
        raise CaseUnavailable('root-owned standard Nix daemon socket is unavailable')
    return path


def source_target(binding: dict) -> str:
    inputs = binding.get('case_inputs', {}).get('nix')
    if (not isinstance(inputs, dict) or set(inputs) != {'format_version', 'source_root'}
            or type(inputs['format_version']) is not int or inputs['format_version'] != 1
            or not isinstance(inputs['source_root'], str)):
        raise CaseUnavailable('Nix dev shell requires explicit format_version:1 and source_root')
    root = Path(inputs['source_root'])
    if not root.is_absolute() or root.resolve(strict=True) != root:
        raise ValueError('Nix source root must be a physical absolute checkout')
    source = binding['source_commit']
    if not re.fullmatch('[0-9a-f]{40}', source):
        raise ValueError('Nix source must identify a full selected commit')
    result = subprocess.run(['git', '-c', 'core.fsmonitor=false', '-C', str(root),
                             'show', source + ':flake.lock'], capture_output=True, timeout=10,
                            env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C', 'GIT_CONFIG_NOSYSTEM': '1'})
    if (result.returncode or len(result.stdout) > 1 << 20
            or hashlib.sha256(result.stdout).hexdigest() != binding['artifact']['identity']['lock_sha256']):
        raise ValueError('Nix dev shell source differs from the final artifact lock')
    return root.as_uri().replace('file:', 'git+file:', 1) + '?rev=' + source


def witness(turn, nonce: str) -> dict:
    path = turn.workspace / 'nix-observation.json'
    deadline = time.monotonic() + 150
    while not path.exists() and time.monotonic() < deadline:
        time.sleep(0.1)
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 131072:
        raise CaseUnavailable('installed Nix command produced no bounded native observation')
    row = json.loads(path.read_bytes())
    if (not isinstance(row, dict) or row.get('nonce') != nonce or row.get('uid') != os.getuid()
            or type(row.get('pid_namespace')) is not int or type(row.get('exit_code')) is not int
            or type(row.get('socket_errno')) is not int or not isinstance(row.get('stdout'), str)):
        raise ValueError('installed Nix command returned malformed native facts')
    return row


def daemon_facts(ordinary: dict, approved: dict, namespace: int) -> bool:
    try:
        ping = json.loads(approved['stdout'])
    except (TypeError, ValueError, KeyError):
        return False
    return (ordinary.get('socket_errno') in {errno.EPERM, errno.EACCES}
            and ordinary.get('exit_code') != 0 and ordinary.get('pid_namespace') != namespace
            and approved.get('socket_errno') == 0 and approved.get('exit_code') == 0
            and approved.get('pid_namespace') == namespace and isinstance(ping, dict)
            and isinstance(ping.get('version'), str) and bool(ping['version']))


def store_facts(binding: dict, observed: dict) -> bool:
    try:
        rows = json.loads(observed['store_stdout'])
        identity = binding['artifact']['identity']
        sri = 'sha256-' + base64.b64encode(bytes.fromhex(identity['sha256'])).decode()
        return (observed.get('store_exit_code') == 0 and isinstance(rows, dict)
                and set(rows) == {identity['store_path']}
                and rows[identity['store_path']].get('narHash') == sri)
    except (ValueError, KeyError, TypeError, AttributeError):
        return False


def exercise(binding: dict, mode: str) -> tuple[str, dict, dict]:
    program = client(binding)
    target = source_target(binding) if mode == 'develop' else ''
    namespace = Path('/proc/self/ns/pid').stat().st_ino
    observations = {'client': str(program), 'installation': binding['artifact']['installation'],
                    'source_target': target, 'host_pid_namespace': namespace, 'phases': []}
    with tempfile.TemporaryDirectory(prefix='paintedwolf-nix-workflow-') as directory:
        project = Path(directory)
        script = project / 'nix-workflow.py'
        script.write_text(SCRIPT)
        with InstalledApp(binding) as app:
            for approved in (False, True):
                nonce = uuid.uuid4().hex
                arguments = {'command': shlex.join([str(Path(sys.executable).resolve()), str(script),
                             str(program), SOCKET, nonce, mode, target,
                             binding['artifact']['identity']['store_path']]), 'timeout_ms': 180000}
                if approved:
                    arguments['capability_request'] = {'host_execution': True}
                with ProviderFixture([{'name': 'command', 'arguments': arguments}]) as provider:
                    with ToolTurn(app, provider, project) as turn:
                        turn.submit()
                        if approved:
                            approve_host_execution(turn, arguments)
                        results = turn.wait(timeout=240)
                        rows = [row for row in results if row.get('tool') == 'command']
                        if (len(rows) != 1 or rows[0].get('outcome') != 'completed'
                                or rows[0].get('invocation', {}).get('invoked') is not True):
                            raise CaseUnavailable('selected installed Nix command did not complete its invocation')
                        observed = witness(turn, nonce)
                        wait_settled(app, turn.session_id)
                        observations['phases'].append({'approved': approved, 'native': observed,
                                                       'turn': turn.observations, 'provider': provider.observations})
            first, second = [row['native'] for row in observations['phases']]
            if mode == 'daemon':
                passed = daemon_facts(first, second, namespace)
            else:
                passed = (first['socket_errno'] in {errno.EPERM, errno.EACCES} and first['exit_code'] != 0
                          and first['pid_namespace'] != namespace and second['socket_errno'] == 0
                          and second['exit_code'] == 0 and second['pid_namespace'] == namespace
                          and second['stdout'] == 'paintedwolf-nix-dev-shell'
                          and store_facts(binding, second))
            client(binding)
            observations['app'] = app.observations
        return 'passed' if passed else 'failed', observations, app.streams()


def execute(binding: dict, capability: str, case: str, evidence_root: Path) -> dict:
    modes = {('development', 'daemon-authority-and-attribution'): 'daemon',
             ('development', 'nix-store-and-dev-shell'): 'develop'}
    if (capability, case) not in modes:
        return retain(binding, capability, case, evidence_root, 'unverified',
                      {'reason': 'actual controlled generation, cache, remote-builder or cancellation fixture required'})
    require_owned_guest(binding)
    status, observations, streams = exercise(binding, modes[(capability, case)])
    return retain(binding, capability, case, evidence_root, status, observations, streams)
