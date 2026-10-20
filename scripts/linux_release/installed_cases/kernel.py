"""Fixed installed command scenarios for private shared memory and namespace denial."""
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import shlex
import signal
import sys
import tempfile
import uuid

from .app import InstalledApp
from .provider import ProviderFixture
from .security_boundary import successful_invocation
from .workflow import ToolTurn

SHARED_MEMORY = '''import errno, json, pathlib, sys
host, private = map(pathlib.Path, sys.argv[1:])
try:
    host.read_bytes()
except OSError as error:
    isolated = error.errno in (errno.ENOENT, errno.EACCES, errno.EPERM)
else:
    isolated = False
with private.open('xb') as stream:
    stream.write(b'private shared memory fixture')
result = {'host_marker_hidden': isolated,
          'private_write_read': private.read_bytes() == b'private shared memory fixture'}
pathlib.Path('qualification-observation.json').write_text(json.dumps(result))
'''

NAMESPACE = '''import ctypes, json, os, pathlib, signal
pid = os.fork()
if pid == 0:
    libc = ctypes.CDLL(None, use_errno=True)
    libc.unshare(0x10000000)
    os._exit(73)
_, status = os.waitpid(pid, 0)
result = {'child_reaped': True, 'namespace_escape_killed':
          os.WIFSIGNALED(status) and os.WTERMSIG(status) == signal.SIGSYS}
pathlib.Path('qualification-observation.json').write_text(json.dumps(result))
'''


def run_script(binding: dict, script: str, arguments: list[str]) -> tuple[bool, dict, dict]:
    with tempfile.TemporaryDirectory(prefix='paintedwolf-kernel-') as directory:
        project = Path(directory)
        fixture = project / 'kernel.py'
        fixture.write_text(script)
        command = shlex.join([str(Path(sys.executable).resolve()), str(fixture), *arguments])
        with ProviderFixture([{'name': 'command', 'arguments': {'command': command}}]) as provider:
            with InstalledApp(binding) as app, ToolTurn(app, provider, project) as turn:
                turn.submit()
                results = turn.wait()
                witness = turn.workspace / 'qualification-observation.json'
                if not witness.is_file() or witness.is_symlink() or witness.stat().st_size > 4096:
                    observation = {}
                else:
                    observation = json.loads(witness.read_bytes())
                    if not isinstance(observation, dict):
                        raise ValueError('installed kernel fixture returned an invalid observation')
                facts = {'kernel_observation': observation, 'turn': turn.observations,
                         'provider': provider.observations, 'app': app.observations,
                         'fixture_sha256': hashlib.sha256(script.encode()).hexdigest()}
                invoked = successful_invocation(results)
            return invoked, facts, app.streams()


def private_shared_memory(binding: dict) -> tuple[str, dict, dict]:
    identifier = uuid.uuid4().hex
    host = Path('/dev/shm') / ('paintedwolf-host-' + identifier)
    private = Path('/dev/shm') / ('paintedwolf-private-' + identifier)
    if private.exists():
        raise ValueError('installed shared memory fixture path already exists')
    with host.open('xb') as stream:
        stream.write(b'public shared memory fixture')
    try:
        baseline = host.read_bytes() == b'public shared memory fixture'
        invoked, facts, streams = run_script(binding, SHARED_MEMORY, [str(host), str(private)])
        observation = facts['kernel_observation']
        passed = (baseline and invoked and observation.get('host_marker_hidden') is True
                  and observation.get('private_write_read') is True and not private.exists()
                  and host.read_bytes() == b'public shared memory fixture')
        facts['host_marker_preserved'] = host.read_bytes() == b'public shared memory fixture'
        facts['private_marker_absent_on_host'] = not private.exists()
        facts['unconfined_marker_read'] = baseline
        return 'passed' if passed else 'failed', facts, streams
    finally:
        host.unlink(missing_ok=True)
        private.unlink(missing_ok=True)


def restricted_namespaces(binding: dict) -> tuple[str, dict, dict]:
    invoked, facts, streams = run_script(binding, NAMESPACE, [])
    observation = facts['kernel_observation']
    passed = invoked and observation.get('child_reaped') is True and observation.get('namespace_escape_killed') is True
    facts['required_signal'] = signal.SIGSYS
    return 'passed' if passed else 'failed', facts, streams
