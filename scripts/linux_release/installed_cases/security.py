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

from ..campaign.runtime import unchanged
from .app import InstalledApp
from .ipc import system_v_isolation
from .lifecycle import background_cancel
from .crash import detached_cleanup, host_execution_lifetime
from .kernel import private_shared_memory, restricted_namespaces, helper_boundary
from .terminal import terminal_interaction
from .presence import unchallenged_presence
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
    row = json.loads(observe(['busctl', '--system', '--json=short', 'get-property',
                             'org.freedesktop.systemd1', unit[0], 'org.freedesktop.systemd1.Service', 'MainPID']))
    if not isinstance(row, dict) or row.get('type') != 'u' or type(row.get('data')) is not int:
        raise ValueError('identity service PID property has an invalid typed scalar shape')
    if row['data'] <= 1:
        raise OSError('identity service is not running')
    return row['data']



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


def protected_process(pid: int, expected_start: int) -> tuple[bool, dict]:
    start, uids, capabilities = process_identity(pid)
    denied = memory_read_denied(pid)
    stable = process_identity(pid) == (start, uids, capabilities)
    passed = (start == str(expected_start) and uids == [os.getuid()] * 4
              and capabilities == 0 and denied and stable)
    return passed, {'pid': pid, 'start_time_ticks': start, 'uids': uids,
                    'effective_capabilities': capabilities, 'memory_read_denied': denied,
                    'identity_stable': stable}


def wrong_launcher_memory(binding: dict) -> tuple[str, dict, dict]:
    phase = "service-socket-ownership"
    try:
        info, parent = SOCKET.lstat(), SOCKET.parent.stat()
        if not stat.S_ISSOCK(info.st_mode) or info.st_uid != 0 or parent.st_uid != 0 or parent.st_mode & 0o022:
            raise ValueError('identity socket is not selected by the administrator')
        phase = "service-protocol-refusal"
        with socket.socket(socket.AF_UNIX) as stream:
            stream.settimeout(5)
            stream.connect(str(SOCKET))
            outcome = refusal(stream)
        phase = "service-owner"
        pid = service_pid()
        phase = "service-process-credentials"
        start, uids, capabilities = process_identity(pid)
        phase = "service-image"
        image = observe(['sudo', '-n', 'readlink', '-e', f'/proc/{pid}/exe']).decode().strip()
        expected = unchanged(binding, 'pw-vault-identity')
        if Path(image).resolve() != expected.resolve():
            raise ValueError('identity service is not the selected installed image')
        phase = "service-memory-and-stability"
        denied = memory_read_denied(pid)
        stable = service_pid() == pid and process_identity(pid) == (start, uids, capabilities)
        passed = (outcome['status'] == 'refused' and outcome['code'] == 'executable_mismatch'
                  and not outcome['identity_returned'] and outcome['launcher_pid'] == 0
                  and all(uid not in {0, os.getuid()} for uid in uids)
                  and capabilities == 1 << 19 and denied and stable)
        phase = "installed-gui-and-engine"
        with InstalledApp(binding) as app:
            engine = app.observations['engine_identity']
            engine_passed, engine_observations = protected_process(engine['pid'], engine['start_time_ticks'])
            shell = app.observations['launch_identity']
            shell_passed, shell_observations = protected_process(shell['pid'], shell['start_time_ticks'])
        return ('passed' if passed and engine_passed and shell_passed else 'failed',
                {'engine': engine_observations, 'shell': shell_observations, 'app': app.observations, 'refusal': outcome, 'service_pid': pid, 'start_time_ticks': start,
                 'service_uids': uids, 'effective_capabilities': capabilities,
                 'memory_read_denied': denied, 'service_identity_stable': stable}, app.streams())
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        error.observations = {"observer_phase": phase, "error_type": type(error).__name__,
                              **getattr(error, "observations", {})}
        raise


PROBES = {('vault', 'presence-cancel-and-expiry'): unchallenged_presence,
          ('browser', 'denied-local-services'): denied_local_services,
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
    except (OSError, subprocess.SubprocessError) as error:
        status, observations, streams = 'unverified', {'reason': 'native-security-observer-unavailable',
            **getattr(error, 'observations', {})}, getattr(error, 'streams', {})
    except (ValueError, TypeError, KeyError, json.JSONDecodeError) as error:
        status, observations, streams = 'failed', {'reason': 'native-security-contract-refused',
            **getattr(error, 'observations', {})}, getattr(error, 'streams', {})
    return retain(binding, capability, case, evidence_root, status, observations, streams)
