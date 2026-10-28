"""One installed smoke per confinement boundary: shared memory, user namespaces
and helper descendants, each exercised through a real model-driven command in
the installed app. The full confinement contract (capabilities, no-new-privs,
namespace identities, filter content) is proven by ./task test:confine:linux;
these cases show the installed package actually applies it."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import uuid

from .app import InstalledApp
from ..installed_process import process_identity
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


HELPER = '''import errno, json, os, pathlib, sys
outside = pathlib.Path(sys.argv[1])
read, write = os.pipe()
pid = os.fork()
if pid == 0:
    os.close(read)
    status = dict(line.split(':', 1) for line in pathlib.Path('/proc/self/status').read_text().splitlines() if ':' in line)
    child = {'seccomp': int(status['Seccomp'])}
    try:
        outside.write_bytes(b'forbidden descendant write')
    except OSError as error:
        child['outside_write_denied'] = error.errno in (errno.EPERM, errno.EACCES, errno.EROFS)
    else:
        child['outside_write_denied'] = False
    os.write(write, json.dumps(child).encode())
    os.close(write)
    os._exit(0)
os.close(write)
raw = os.read(read, 4096)
os.close(read)
_, status = os.waitpid(pid, 0)
pathlib.Path('qualification-observation.json').write_text(json.dumps({
    'child': json.loads(raw), 'child_reaped': os.WIFEXITED(status) and os.WEXITSTATUS(status) == 0}))
'''


def namespace_observer(identity: dict) -> dict:
    """The selected engine's namespace identities, read while its identity holds."""
    if process_identity(identity['pid']) != identity:
        raise ValueError('helper observer lost its selected engine identity')
    names = ('user', 'mnt', 'pid', 'net')
    arguments = ['/usr/bin/sudo', '-n', '/usr/bin/stat', '-L', '-c', '%i',
                 *[f"/proc/{identity['pid']}/ns/{name}" for name in names]]
    result = subprocess.run(arguments, capture_output=True, timeout=10, check=False,
                            env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C'})
    rows = result.stdout.splitlines()
    if (result.returncode or len(rows) != len(names) or len(result.stdout) > 1024
            or any(not row.isdigit() or int(row) <= 0 for row in rows)
            or process_identity(identity['pid']) != identity):
        raise ValueError('helper observer did not retain exact engine namespace identities')
    return dict(zip(names, map(int, rows)))


def helper_observation(observation: dict) -> bool:
    """A descendant of the confined command is still filtered and cannot write outside."""
    if not isinstance(observation, dict) or set(observation) != {'child', 'child_reaped'} or observation['child_reaped'] is not True:
        return False
    child = observation['child']
    return (isinstance(child, dict) and set(child) == {'seccomp', 'outside_write_denied'}
            and type(child['seccomp']) is int and child['seccomp'] == 2 and child['outside_write_denied'] is True)


def helper_boundary(binding: dict) -> tuple[str, dict, dict]:
    outside = Path.home() / ('paintedwolf-helper-outside-' + uuid.uuid4().hex)
    with outside.open('xb') as stream:
        stream.write(b'public helper boundary fixture')
    try:
        baseline = outside.read_bytes() == b'public helper boundary fixture'
        with tempfile.TemporaryDirectory(prefix='paintedwolf-helper-boundary-') as directory:
            project = Path(directory)
            fixture = project / 'helper.py'
            fixture.write_text(HELPER)
            command = shlex.join([str(Path(sys.executable).resolve()), str(fixture), str(outside)])
            with ProviderFixture([{'name': 'command', 'arguments': {'command': command}}]) as provider:
                with InstalledApp(binding) as app, ToolTurn(app, provider, project) as turn:
                    turn.submit()
                    results = turn.wait()
                    witness = turn.workspace / 'qualification-observation.json'
                    if not witness.is_file() or witness.is_symlink() or witness.stat().st_size > 4096:
                        raise ValueError('helper boundary omitted its bounded kernel observation')
                    observation = json.loads(witness.read_bytes())
                    passed = (baseline and successful_invocation(results) and helper_observation(observation)
                              and outside.read_bytes() == b'public helper boundary fixture')
                    facts = {'kernel_observation': observation, 'turn': turn.observations,
                             'app': app.observations, 'fixture_sha256': hashlib.sha256(HELPER.encode()).hexdigest(),
                             'unconfined_marker_read': baseline, 'outside_marker_preserved': outside.read_bytes() == b'public helper boundary fixture'}
                passed = passed and app.observations.get('capture_complete') is True and not app.observations.get('output_limit')
                return 'passed' if passed else 'failed', facts, app.streams()
    finally:
        outside.unlink(missing_ok=True)
