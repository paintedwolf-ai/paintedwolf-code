"""The fixture a helper family runs inside its boundary, and how its report is judged.

The probe reports its own namespaces, seccomp mode, no_new_privs and
capabilities, and whether a write outside its roots, a read of the engine's
control plane and creating a protected name absent at launch (#1113) fail,
while an ordinary sibling write in the project succeeds."""
from __future__ import annotations

import json
from pathlib import Path
import sys
import time
import uuid

from . import process_tree
from .records import CaseUnavailable

DENIED = {'EACCES', 'EPERM', 'EROFS', 'ENOENT'}
WITNESS = 'qualification-family.json'
PROBE = '''import errno, json, os, pathlib, sys
mode, outside, control = sys.argv[1], pathlib.Path(sys.argv[2]), pathlib.Path(sys.argv[3])
if mode == 'terminal':
    received = sys.stdin.readline().rstrip('\\r\\n')
def denied(action):
    try:
        action()
    except OSError as error:
        return errno.errorcode.get(error.errno, str(error.errno))
    return None
status = dict(line.split(':', 1) for line in pathlib.Path('/proc/self/status').read_text().splitlines() if ':' in line)
result = {'namespaces': {name: os.stat('/proc/self/ns/' + name).st_ino for name in ('user', 'mnt', 'pid', 'net')},
          'seccomp': int(status['Seccomp']), 'no_new_privs': int(status['NoNewPrivs']),
          'capabilities': {name: int(status[name], 16) for name in ('CapInh', 'CapPrm', 'CapEff', 'CapAmb')},
          'outside_write': denied(lambda: outside.write_bytes(b'escaped')),
          'control_read': denied(control.read_bytes)}
if mode == 'mcp':
    tool = {'name': 'qualification_observation', 'description': json.dumps(result, separators=(',', ':')),
            'inputSchema': {'type': 'object', 'properties': {}}}
    for line in sys.stdin:
        request = json.loads(line)
        if 'id' not in request:
            continue
        method = request.get('method')
        if method == 'initialize':
            answer = {'protocolVersion': request['params']['protocolVersion'], 'capabilities': {'tools': {}},
                      'serverInfo': {'name': 'qualification', 'version': '1'}}
        elif method == 'tools/list':
            answer = {'tools': [tool]}
        elif method == 'tools/call':
            answer = {'content': [{'type': 'text', 'text': tool['description']}]}
        else:
            answer = {}
        print(json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': answer}), flush=True)
    raise SystemExit(0)
project = pathlib.Path.cwd()
result['future_protected_create'] = denied(lambda: (project / '.paintedwolf').mkdir())
result['sibling_write'] = denied(lambda: (project / 'qualification-sibling').write_bytes(b'permitted'))
temporary = project / 'qualification-family.tmp'
temporary.write_text(json.dumps(result))
temporary.replace(project / 'qualification-family.json')
if mode == 'terminal':
    raise SystemExit(0 if received == sys.argv[4] else 73)
'''



def domain_failures(observation: object, outer: dict, *, project: bool) -> list[str]:
    """Typed reasons the in-domain report shows a missing boundary."""
    fields = {'namespaces', 'seccomp', 'no_new_privs', 'capabilities', 'outside_write', 'control_read'}
    if project:
        fields |= {'future_protected_create', 'sibling_write'}
    if not isinstance(observation, dict) or set(observation) != fields:
        return ['observation-malformed']
    namespaces, capabilities = observation['namespaces'], observation['capabilities']
    if (not isinstance(namespaces, dict) or set(namespaces) != set(process_tree.NAMESPACES)
            or any(type(value) is not int for value in namespaces.values())
            or not isinstance(capabilities, dict) or set(capabilities) != set(process_tree.CAPABILITIES)
            or any(type(value) is not int for value in capabilities.values())):
        return ['observation-malformed']
    failures = [f'namespace-shared-{name}' for name in process_tree.NAMESPACES if namespaces[name] == outer[name]]
    if observation['seccomp'] != 2:
        failures.append('seccomp-filter-absent')
    if observation['no_new_privs'] != 1:
        failures.append('no-new-privileges-absent')
    if any(capabilities.values()):
        failures.append('capabilities-held')
    if observation['outside_write'] not in DENIED:
        failures.append('outside-write-admitted')
    if observation['control_read'] not in DENIED:
        failures.append('control-plane-read-admitted')
    if project and observation['future_protected_create'] not in DENIED:
        failures.append('future-protected-name-created')
    if project and observation['sibling_write'] is not None:
        failures.append('permitted-sibling-write-refused')
    return failures


def read_witness(path: Path, limit: float = 30) -> dict:
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        if path.exists():
            if path.is_symlink() or path.stat().st_size > 4096:
                raise ValueError('helper family witness exceeds its boundary')
            return json.loads(path.read_bytes())
        time.sleep(0.05)
    raise CaseUnavailable('helper family fixture produced no witness')


class Fixtures:
    """The shared fixture paths a confined family must not reach."""

    def __init__(self):
        self.outside = Path.home() / ('paintedwolf-family-outside-' + uuid.uuid4().hex)
        self.control = Path.home() / '.config/paintedwolf/daemon.json'

    def __enter__(self):
        with self.outside.open('xb') as stream:
            stream.write(b'public helper family fixture')
        return self

    def __exit__(self, *_):
        self.outside.unlink(missing_ok=True)

    def intact(self) -> bool:
        return self.outside.read_bytes() == b'public helper family fixture'

    def command(self, mode: str, *extra: str) -> list[str]:
        return [str(Path(sys.executable).resolve()), '-I', '-c', PROBE, mode, str(self.outside), str(self.control), *extra]
