"""Observe the kernel facts of one captured process tree as the administrator.

Protected and confined processes hide their namespace links and executables
from the launching user, so a bounded root observer reads them and the
validators here decide from those facts alone."""
from __future__ import annotations

import errno
import json
import os
from pathlib import Path
import subprocess
import sys

from .app import process_identity

NAMESPACES = ('user', 'mnt', 'pid', 'net')
CAPABILITIES = ('CapInh', 'CapPrm', 'CapEff', 'CapAmb')
FULL_MAP = [[0, 0, 4294967295]]
MAX_PROCESSES = 256
OBSERVER = '''import fcntl, json, os, pathlib, sys
NS_GET_OWNER_UID = 0xb704
request = json.loads(sys.argv[1])
root = request['root']
def identity(pid):
    fields = pathlib.Path(f'/proc/{pid}/stat').read_text().rpartition(') ')[2].split()
    return {'pid': pid, 'parent_pid': int(fields[1]), 'start_time_ticks': int(fields[19])}
def namespaces(pid):
    result = {}
    for name in ('user', 'mnt', 'pid', 'net'):
        info = os.stat(f'/proc/{pid}/ns/{name}')
        result[name] = [info.st_dev, info.st_ino]
    return result
def owner(pid):
    descriptor = os.open(f'/proc/{pid}/ns/user', os.O_RDONLY)
    try:
        return int.from_bytes(fcntl.ioctl(descriptor, NS_GET_OWNER_UID, bytes(4)), sys.byteorder)
    finally:
        os.close(descriptor)
def id_map(pid, name):
    return [[int(value) for value in line.split()] for line in pathlib.Path(f'/proc/{pid}/{name}').read_text().splitlines()]
if identity(root['pid']) != root:
    raise SystemExit(73)
rows = {}
for entry in pathlib.Path('/proc').iterdir():
    if entry.name.isdecimal():
        try:
            row = identity(int(entry.name))
            rows[row['pid']] = row
        except (FileNotFoundError, ProcessLookupError, IndexError, ValueError):
            pass
owned = {root['pid']}
for _ in range(64):
    children = {pid for pid, row in rows.items() if row['parent_pid'] in owned}
    if children <= owned:
        break
    owned |= children
observed = []
for pid in sorted(owned)[:request['limit'] + 1]:
    try:
        status = dict(line.split(':', 1) for line in pathlib.Path(f'/proc/{pid}/status').read_text().splitlines() if ':' in line)
        image = os.stat(f'/proc/{pid}/exe')
        row = {'identity': rows[pid], 'exe': os.readlink(f'/proc/{pid}/exe'),
               'image': [image.st_dev, image.st_ino, image.st_size], 'namespaces': namespaces(pid),
               'user_namespace_owner': owner(pid), 'uid_map': id_map(pid, 'uid_map'), 'gid_map': id_map(pid, 'gid_map'),
               'uids': [int(value) for value in status['Uid'].split()],
               'no_new_privs': int(status['NoNewPrivs']), 'seccomp': int(status['Seccomp']),
               'capabilities': {name: int(status[name], 16) for name in ('CapInh', 'CapPrm', 'CapEff', 'CapAmb')},
               'capability_bounding_set': int(status['CapBnd'], 16)}
        if request['mounts'] and pid in request['mounts']:
            row['mounts'] = [line.split(' - ')[0].split()[4:6] for line in pathlib.Path(f'/proc/{pid}/mountinfo').read_text().splitlines()]
        if identity(pid) != rows[pid]:
            continue
        observed.append(row)
    except (FileNotFoundError, ProcessLookupError):
        pass
initial = {'namespaces': namespaces(1), 'user_namespace_owner': owner(1)}
if identity(root['pid']) != root:
    raise SystemExit(74)
print(json.dumps({'root': root, 'initial': initial, 'processes': observed}))
'''


def observe(root: dict, *, mounts: tuple[int, ...] = ()) -> dict:
    """Reads every live descendant of root, including root itself."""
    if process_identity(root['pid']) != root:
        raise ValueError('process tree observer lost its captured root before observation')
    request = {'root': root, 'limit': MAX_PROCESSES, 'mounts': list(mounts)}
    result = subprocess.run(['/usr/bin/sudo', '-n', str(Path(sys.executable).resolve()), '-I', '-c', OBSERVER,
                             json.dumps(request)], capture_output=True, timeout=20, check=False,
                            env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C'})
    if result.returncode or len(result.stdout) > 4 << 20:
        raise OSError('administrator process tree observer is unavailable or exceeded its bound')
    return validate(json.loads(result.stdout), root)


def pair(value) -> bool:
    return isinstance(value, list) and len(value) == 2 and all(type(item) is int and item > 0 for item in value)


def validate(document: object, root: dict) -> dict:
    """Refuses any observer output that is not the exact typed tree of root."""
    if (not isinstance(document, dict) or set(document) != {'root', 'initial', 'processes'}
            or document['root'] != root or not isinstance(document['processes'], list)
            or not 1 <= len(document['processes']) <= MAX_PROCESSES):
        raise ValueError('process tree observer did not return its bounded captured tree')
    initial = document['initial']
    if (not isinstance(initial, dict) or set(initial) != {'namespaces', 'user_namespace_owner'}
            or not namespace_set(initial['namespaces']) or type(initial['user_namespace_owner']) is not int):
        raise ValueError('process tree observer returned malformed initial namespaces')
    seen = set()
    for row in document['processes']:
        required = {'identity', 'exe', 'image', 'namespaces', 'user_namespace_owner', 'uid_map', 'gid_map',
                    'uids', 'no_new_privs', 'seccomp', 'capabilities', 'capability_bounding_set'}
        if not isinstance(row, dict) or not required <= set(row) <= required | {'mounts'}:
            raise ValueError('process tree observer returned a malformed process')
        identity = row['identity']
        if (not isinstance(identity, dict) or set(identity) != {'pid', 'parent_pid', 'start_time_ticks'}
                or any(type(value) is not int or value < 0 for value in identity.values())
                or identity['pid'] <= 1 or identity['pid'] in seen):
            raise ValueError('process tree observer returned a reused or malformed identity')
        seen.add(identity['pid'])
        image = row['image']
        if (not isinstance(row['exe'], str) or not row['exe'].startswith('/')
                or not isinstance(image, list) or len(image) != 3 or any(type(v) is not int or v < 0 for v in image)
                or not namespace_set(row['namespaces'])
                or type(row['user_namespace_owner']) is not int or not id_map(row['uid_map']) or not id_map(row['gid_map'])
                or not isinstance(row['uids'], list) or len(row['uids']) != 4 or any(type(v) is not int for v in row['uids'])
                or type(row['no_new_privs']) is not int or type(row['seccomp']) is not int
                or not isinstance(row['capabilities'], dict) or set(row['capabilities']) != set(CAPABILITIES)
                or any(type(value) is not int or value < 0 for value in row['capabilities'].values())
                or type(row['capability_bounding_set']) is not int or row['capability_bounding_set'] < 0):
            raise ValueError('process tree observer returned malformed kernel facts')
        mounts = row.get('mounts', [])
        if (not isinstance(mounts, list) or len(mounts) > 4096
                or any(not isinstance(item, list) or len(item) != 2 or not all(isinstance(v, str) for v in item) for item in mounts)):
            raise ValueError('process tree observer returned a malformed mount table')
    if root['pid'] not in seen:
        raise ValueError('process tree observer omitted its live captured root')
    return document


def namespace_set(value) -> bool:
    return isinstance(value, dict) and set(value) == set(NAMESPACES) and all(pair(item) for item in value.values())


def id_map(value) -> bool:
    return (isinstance(value, list) and 1 <= len(value) <= 340
            and all(isinstance(line, list) and len(line) == 3 and all(type(v) is int and v >= 0 for v in line) for line in value))


def matching(document: dict, images: list[list[int]]) -> list[dict]:
    """The observed processes executing one of the sealed image identities."""
    return [row for row in document['processes'] if row['image'] in images]


def boundary_failures(row: dict, outer: dict, uid: int) -> list[str]:
    """Typed reasons a helper process lacks its applied boundary, empty when it holds."""
    failures = [f'namespace-shared-{name}' for name in NAMESPACES if row['namespaces'][name] == outer[name]]
    if row['seccomp'] != 2:
        failures.append('seccomp-filter-absent')
    if row['no_new_privs'] != 1:
        failures.append('no-new-privileges-absent')
    if any(row['capabilities'].values()):
        failures.append('capabilities-held')
    if row['uids'] != [uid] * 4:
        failures.append('identity-changed')
    return failures


def memory_open_errno(pid: int) -> str:
    """The user's /proc/<pid>/mem open outcome: an errno name, or 'opened'."""
    try:
        descriptor = os.open(f'/proc/{pid}/mem', os.O_RDONLY)
    except OSError as error:
        return errno.errorcode.get(error.errno, str(error.errno))
    os.close(descriptor)
    return 'opened'
