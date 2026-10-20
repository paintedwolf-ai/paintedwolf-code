"""Observe installed custody and session controls in an owned graphical guest."""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import errno
import json
import os
from pathlib import Path
import socket
import stat
import struct
import subprocess
import time

from ..installed_runtime import unchanged
from .app import InstalledApp
from .ipc import system_v_isolation
from .lifecycle import background_cancel
from .crash import detached_cleanup, host_execution_lifetime
from .kernel import private_shared_memory, restricted_namespaces, helper_boundary
from .process import run
from .terminal import terminal_interaction
from .browser import browser_sandbox
from .browser_network import denied_local_services, allowed_local_server
from .products import require_owned_guest
from .records import CaseUnavailable, CaseFailed, retain
from .security_boundary import filesystem_egress

SOCKET = Path('/run/painted-wolf-code/vault-identity.sock')
UNIT = 'paintedwolf-vault-identity.service'


def observe(arguments: list[str]) -> bytes:
    result = subprocess.run(arguments, stdin=subprocess.DEVNULL, capture_output=True, timeout=10,
                            env={**os.environ, 'LC_ALL': 'C'})
    if result.returncode or len(result.stdout) > 1 << 20:
        raise OSError('native observer unavailable or exceeded its evidence bound')
    return result.stdout


def bus_value(arguments: list[str], expected: str):
    row = json.loads(observe(['busctl', '--system', '--json=short', *arguments]))
    if not isinstance(row, dict) or row.get('type') != expected or not isinstance(row.get('data'), list):
        raise ValueError('native D-Bus observer returned an invalid typed value')
    return row['data']


def inhibitors() -> list:
    data = bus_value(['call', 'org.freedesktop.login1', '/org/freedesktop/login1',
                      'org.freedesktop.login1.Manager', 'ListInhibitors'], 'a(ssssuu)')
    if len(data) != 1 or not isinstance(data[0], list):
        raise ValueError('logind inhibitor result has an invalid array shape')
    rows = data[0]
    if any(not isinstance(row, list) or len(row) != 6
           or any(not isinstance(value, str) for value in row[:4])
           or any(type(value) is not int or value < 0 for value in row[4:]) for row in rows):
        raise ValueError('logind inhibitor row has an invalid typed shape')
    return rows


def inhibitor_lifetime(binding: dict) -> tuple[str, dict, dict]:
    baseline = inhibitors()
    observed = []
    with ThreadPoolExecutor(max_workers=1) as executor:
        future = executor.submit(run, binding, 'pw', ['diagnostics', 'power-inhibit', '2s'], timeout=15)
        while not future.done():
            observed.extend(inhibitors())
            time.sleep(0.1)
        result = future.result()
    identity = result.get('launch_identity')
    if not isinstance(identity, dict) or type(identity.get('pid')) is not int or identity['pid'] <= 1:
        raise ValueError('installed inhibitor omitted its captured process identity')
    pid = identity['pid']
    held = [row for row in observed if row[5] == pid and row[4] == os.getuid()
            and 'sleep' in row[0].split(':') and row[3] == 'block']
    final = inhibitors()
    passed = (result['exit_code'] == 0 and not result['timed_out'] and not result['output_limit']
              and bool(held) and not any(row[5] == pid for row in final))
    return ('passed' if passed else 'failed',
            {'launch_identity': identity, 'before': baseline, 'held': held, 'after': final,
             'exit_code': result['exit_code'], 'timed_out': result['timed_out'],
             'output_limit': result['output_limit']},
            {name: result[name] for name in ('stdout', 'stderr')})


def receive(stream, size):
    chunks = bytearray()
    while len(chunks) < size:
        chunk = stream.recv(size - len(chunks))
        if not chunk:
            raise ValueError('identity service closed an incomplete protocol frame')
        chunks.extend(chunk)
    return bytes(chunks)


def refusal(stream) -> dict:
    body = b'{"op":"probe"}'
    try:
        stream.sendall(struct.pack('!I', len(body)) + body)
    except BrokenPipeError:
        pass
    length = struct.unpack('!I', receive(stream, 4))[0]
    if not 0 < length <= 128 << 10:
        raise ValueError('identity service returned an invalid frame length')
    row = json.loads(receive(stream, length))
    if not isinstance(row, dict):
        raise ValueError('identity service returned an invalid response')
    # Retain only protocol classifications, never an identity or diagnostic prose.
    return {'status': row.get('status'), 'code': row.get('code'),
            'identity_returned': bool(row.get('value')), 'launcher_pid': row.get('launcher_pid', 0)}


def service_pid() -> int:
    unit = bus_value(['call', 'org.freedesktop.systemd1', '/org/freedesktop/systemd1',
                      'org.freedesktop.systemd1.Manager', 'GetUnit', 's', UNIT], 'o')
    if len(unit) != 1 or not isinstance(unit[0], str) or not unit[0].startswith('/org/freedesktop/systemd1/unit/'):
        raise ValueError('identity service unit has an invalid object path')
    value = bus_value(['get-property', 'org.freedesktop.systemd1', unit[0],
                      'org.freedesktop.systemd1.Service', 'MainPID'], 'u')
    if len(value) != 1 or type(value[0]) is not int or value[0] <= 1:
        raise OSError('identity service is not running')
    return value[0]


def process_identity(pid: int) -> tuple[str, list[int], int]:
    root = Path('/proc') / str(pid)
    fields = (root / 'stat').read_text().rpartition(') ')[2].split()
    status = dict(line.split(':', 1) for line in (root / 'status').read_text().splitlines() if ':' in line)
    if len(fields) < 20 or not fields[19].isdigit():
        raise ValueError('identity service has an invalid process start time')
    return fields[19], [int(value) for value in status['Uid'].split()], int(status['CapEff'].strip(), 16)


def memory_read_denied(pid: int) -> bool:
    try:
        descriptor = os.open(f'/proc/{pid}/mem', os.O_RDONLY)
    except OSError as error:
        if error.errno not in {errno.EACCES, errno.EPERM}:
            raise
        return True
    os.close(descriptor)
    return False


def wrong_launcher_memory(binding: dict) -> tuple[str, dict, dict]:
    info, parent = SOCKET.lstat(), SOCKET.parent.stat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != 0 or parent.st_uid != 0 or parent.st_mode & 0o022:
        raise ValueError('identity socket is not selected by the administrator')
    with socket.socket(socket.AF_UNIX) as stream:
        stream.settimeout(5)
        stream.connect(str(SOCKET))
        outcome = refusal(stream)
    pid = service_pid()
    start, uids, capabilities = process_identity(pid)
    image = observe(['sudo', '-n', 'readlink', '-e', f'/proc/{pid}/exe']).decode().strip()
    expected = unchanged(binding, 'pw-vault-identity')
    if Path(image).resolve() != expected.resolve():
        raise ValueError('identity service is not the selected installed image')
    denied = memory_read_denied(pid)
    stable = service_pid() == pid and process_identity(pid) == (start, uids, capabilities)
    passed = (outcome['status'] == 'refused' and outcome['code'] == 'executable_mismatch'
              and not outcome['identity_returned'] and outcome['launcher_pid'] == 0
              and all(uid not in {0, os.getuid()} for uid in uids)
              and capabilities == 1 << 19 and denied and stable)
    with InstalledApp(binding) as app:
        engine = app.observations['engine_identity']
        engine_start, engine_uids, engine_capabilities = process_identity(engine['pid'])
        engine_denied = memory_read_denied(engine['pid'])
        engine_stable = process_identity(engine['pid']) == (engine_start, engine_uids, engine_capabilities)
        engine_passed = (engine_start == str(engine['start_time_ticks'])
                         and engine_uids == [os.getuid()] * 4 and engine_capabilities == 0
                         and engine_denied and engine_stable)
        engine_observations = {'identity': engine, 'uids': engine_uids,
                               'effective_capabilities': engine_capabilities,
                               'memory_read_denied': engine_denied, 'identity_stable': engine_stable}
    return ('passed' if passed and engine_passed else 'failed',
            {'engine': engine_observations, 'app': app.observations, 'refusal': outcome, 'service_pid': pid, 'start_time_ticks': start,
             'service_uids': uids, 'effective_capabilities': capabilities,
             'memory_read_denied': denied, 'service_identity_stable': stable}, app.streams())


PROBES = {('browser', 'denied-local-services'): denied_local_services,
          ('browser', 'allowed-local-server'): allowed_local_server,
          ('browser', 'browser-sandbox-composition'): browser_sandbox,
          ('processes', 'terminal-interaction'): terminal_interaction,
          ('processes', 'host-execution-lifetime'): host_execution_lifetime,
          ('processes', 'background-cancel'): background_cancel,
          ('processes', 'setsid-and-double-fork'): detached_cleanup,
          ('confinement', 'system-v-ipc-isolation'): system_v_isolation,
          ('confinement', 'private-shared-memory'): private_shared_memory,
          ('confinement', 'restricted-user-namespaces'): restricted_namespaces,
          ('confinement', 'helper-boundary-and-descendants'): helper_boundary,
          ('confinement', 'filesystem-and-egress-denial'): filesystem_egress,
          ('session', 'inhibitor-lifetime'): inhibitor_lifetime,
          ('vault', 'wrong-launcher-and-memory'): wrong_launcher_memory}


def execute(binding: dict, capability: str, case: str, evidence_root: Path) -> dict:
    require_owned_guest(binding)
    selected = binding.get('case_inputs', {}).get('security', {})
    if (not isinstance(selected, dict) or selected and
            (set(selected) != {'format_version'} or type(selected.get('format_version')) is not int
             or selected['format_version'] != 1)):
        raise ValueError('security case inputs permit only format_version 1')
    probe = PROBES.get((capability, case))
    if probe is None:
        return retain(binding, capability, case, evidence_root, 'unverified',
                      {'reason': 'installed-app-interaction-driver-required'})
    try:
        status, observations, streams = probe(binding)
    except CaseFailed as error:
        status, observations, streams = ('failed', getattr(error, 'observations', {'reason': str(error)}),
                                         getattr(error, 'streams', {}))
    except CaseUnavailable as error:
        status, observations, streams = ('unverified',
            getattr(error, 'observations', {'reason': 'installed-app-interaction-unavailable'}),
            getattr(error, 'streams', {}))
    except (OSError, subprocess.SubprocessError):
        status, observations, streams = 'unverified', {'reason': 'native-security-observer-unavailable'}, {}
    except (ValueError, TypeError, KeyError, json.JSONDecodeError):
        status, observations, streams = 'failed', {'reason': 'native-security-contract-refused'}, {}
    return retain(binding, capability, case, evidence_root, status, observations, streams)
