"""Admit fixed tiny Nix derivations and observe only their owned daemon descendants."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

from ..installed_runtime import nix_command
from .records import CaseUnavailable

MARKER = Path('/etc/paintedwolf-nix-fixture.json')
STORE = re.compile(r'/nix/store/[a-z0-9]{32}-[A-Za-z0-9+._?=-]+')
HEX = re.compile('[0-9a-f]{64}')


def cancellation_script(builder: str, token: str) -> str:
    return (f'fixture_token={token}; {builder} mkdir -p "$out"; {builder} sleep 300; '
            f'{builder} echo paintedwolf-cancel-completed > "$out/result"')


def read_marker() -> dict:
    try:
        descriptor = os.open(MARKER, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError as error:
        raise CaseUnavailable('fixed root-owned Nix fixture is not installed') from error
    with os.fdopen(descriptor, 'rb') as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_mode & 0o022 or before.st_size > 65536:
            raise ValueError('Nix fixture descriptor is not bounded root-controlled input')
        raw = stream.read(65537)
        after = os.fstat(stream.fileno())
        if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) != (
                after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) or len(raw) != before.st_size:
            raise ValueError('Nix fixture descriptor changed during inspection')
    return json.loads(raw)


def store_file(value: dict, fields: set[str]) -> Path:
    if (not isinstance(value, dict) or set(value) != fields or not isinstance(value.get('path'), str)
            or STORE.fullmatch(value['path']) is None and not re.fullmatch(
                r'/nix/store/[a-z0-9]{32}-[A-Za-z0-9+._?=-]+/bin/(nix|nix-daemon|busybox)', value['path'])
            or not isinstance(value.get('sha256'), str) or HEX.fullmatch(value['sha256']) is None):
        raise ValueError('Nix fixture has an invalid fixed store identity')
    path = Path(value['path'])
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, 'rb') as stream:
        before = os.fstat(stream.fileno())
        if not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_mode & 0o222:
            raise ValueError('Nix fixture requires an immutable root-owned store file')
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        after = os.fstat(stream.fileno())
        if (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) != (
                after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) or digest != value['sha256']:
            raise ValueError('Nix fixture store content changed')
    return path


def derivation(graph: dict, record: dict, role: str = 'cancellation') -> dict:
    if role not in {'cancellation', 'remote_builder'}:
        raise ValueError('Nix fixture derivative role is not declared')
    selected = record[role]
    extra = {'uri', 'ssh_key', 'known_hosts', 'known_hosts_sha256', 'server'} if role == 'remote_builder' else set()
    if (not isinstance(selected, dict) or set(selected) != {
            'derivation', 'derivation_sha256', 'output', 'token', 'arguments'} | extra
            or not re.fullmatch('[0-9a-f]{32}', selected.get('token', ''))
            or STORE.fullmatch(selected.get('derivation', '')) is None
            or not selected['derivation'].endswith('.drv') or STORE.fullmatch(selected.get('output', '')) is None):
        raise ValueError('Nix fixture has an invalid derivative identity')
    builder, token = record['static_builder']['path'], selected['token']
    script = cancellation_script(builder, token) if role == 'cancellation' else (
        f'{builder} mkdir -p \"$out\"; {builder} echo {token} > \"$out/result\"')
    arguments = ['sh', '-c', script]
    rows = graph.get('derivations')
    name = Path(selected['derivation']).name
    if (graph.get('version') != 4 or not isinstance(rows, dict) or set(rows) != {name}
            or not isinstance(rows[name], dict)):
        raise ValueError('Nix fixture requires the exact pinned derivation graph')
    row = rows[name]
    prefix = 'paintedwolf-nix-cancel-' if role == 'cancellation' else 'paintedwolf-nix-remote-'
    expected_env = {'allowSubstitutes': '', 'builder': builder, 'name': prefix + token,
                    'out': selected['output'], 'system': record['system']}
    if role == 'cancellation':
        expected_env['preferLocalBuild'] = '1'
    if (row.get('version') != 4 or row.get('system') != record['system'] or row.get('builder') != builder
            or row.get('args') != arguments or selected['arguments'] != arguments
            or row.get('env') != expected_env or row.get('inputs') != {'drvs': {}, 'srcs': [Path(builder).parts[3]]}
            or row.get('outputs') != {'out': {'path': Path(selected['output']).name}}):
        raise ValueError('Nix derivative differs from its fixed sandboxed fixture')
    return selected


def path_presence(document: dict, output: str) -> bool:
    if not isinstance(document, dict) or set(document) != {output}:
        raise ValueError('Nix store query did not identify the exact selected output')
    row = document[output]
    if row is None:
        return False
    if not isinstance(row, dict) or not isinstance(row.get('narHash'), str):
        raise ValueError('Nix store query omitted its typed output metadata')
    return True


def realized(program: Path, output: str) -> bool:
    return path_presence(json.loads(nix_command(program, 'path-info', '--json', '--json-format', '1', output)), output)


def load(binding: dict) -> dict:
    record = read_marker()
    if (not isinstance(record, dict) or set(record) != {'format_version', 'purpose', 'system', 'client',
            'daemon', 'static_builder', 'cancellation', 'remote_builder'}
            or type(record['format_version']) is not int or record['format_version'] != 1
            or record['purpose'] != 'installed-nix-workflow-fixture'
            or record['system'] != {'amd64': 'x86_64-linux', 'arm64': 'aarch64-linux'}[binding['artifact']['architecture']]):
        raise ValueError('Nix fixture does not match this selected native installation')
    program = store_file(record['client'], {'path', 'sha256'})
    store_file(record['daemon'], {'path', 'sha256'})
    builder = store_file(record['static_builder'], {'path', 'sha256', 'nar_hash'})
    builder_store = '/nix/store/' + builder.parts[3]
    paths = json.loads(nix_command(program, 'path-info', '--json', '--json-format', '1', builder_store))
    if (not isinstance(paths, dict) or set(paths) != {builder_store}
            or paths[builder_store].get('narHash') != record['static_builder']['nar_hash']):
        raise ValueError('Nix static fixture builder differs from its actual store NAR')
    selected = binding.get('payload', {}).get('nix_client', {})
    if (record['client']['path'], record['client']['sha256']) != (selected.get('path'), selected.get('sha256')):
        raise ValueError('Nix fixture client differs from the actual installed binding')
    target = record['cancellation']
    if not isinstance(target, dict):
        raise ValueError('Nix fixture omitted its fixed cancellation derivative')
    store_file({'path': target.get('derivation'), 'sha256': target.get('derivation_sha256')}, {'path', 'sha256'})
    graph = json.loads(nix_command(program, 'derivation', 'show', target['derivation']))
    derivation(graph, record)
    return record


# The observer starts only at the root-owned daemon that this fixture installed.
# It never scans other host processes or infers ownership from command prose.
OBSERVER = r'''import grp, json, os, pathlib, pwd, signal, stat, subprocess, sys
operation, token = sys.argv[1:]
if operation not in {'observe','state','cleanup'} or len(token) != 32 or any(c not in '0123456789abcdef' for c in token):
    raise ValueError('invalid fixed observer operation')
marker = pathlib.Path('/etc/paintedwolf-nix-fixture.json')
info = marker.lstat()
if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or info.st_size > 65536:
    raise ValueError('fixture marker lost root control')
record = json.loads(marker.read_bytes())
fixture = record['cancellation']
if fixture['token'] != token:
    raise ValueError('observer token differs from controlled derivative')
root = pathlib.Path('/run/paintedwolf-nix-fixture')
root.mkdir(mode=0o700, exist_ok=True)
if root.is_symlink() or root.stat().st_uid != 0 or stat.S_IMODE(root.stat().st_mode) != 0o700:
    raise ValueError('observer lease directory lost root control')
lease = root / (token + '.json')
def identity(pid):
    try:
        before = pathlib.Path('/proc', str(pid), 'stat').read_text().rpartition(') ')[2].split()
        uid = pathlib.Path('/proc', str(pid)).stat().st_uid
        after = pathlib.Path('/proc', str(pid), 'stat').read_text().rpartition(') ')[2].split()
        if before[19] != after[19]: return None
        return {'pid':pid,'start_ticks':before[19],'uid':uid,'parent':int(before[1]),'state':before[0]}
    except (FileNotFoundError, ProcessLookupError): return None
def descendants(pid):
    rows, pending = [], [pid]
    while pending:
        current = pending.pop()
        row = identity(current)
        if row is None: continue
        rows.append(row)
        if len(rows) > 128: raise ValueError('fixture descendants exceeded observation bound')
        try: pending += [int(p) for p in pathlib.Path('/proc',str(current),'task',str(current),'children').read_text().split()]
        except FileNotFoundError: pass
    return rows
if operation == 'observe':
    if lease.exists(): raise ValueError('fixture observer lease already exists')
    result = subprocess.run(['/usr/bin/systemctl','show','--property=MainPID','--value','nix-daemon.service'],capture_output=True,check=True,timeout=10)
    pid = int(result.stdout)
    daemon = identity(pid)
    expected = pathlib.Path(record['daemon']['path'])
    actual = pathlib.Path('/proc',str(pid),'exe').stat()
    declared = expected.stat()
    if daemon is None or daemon['uid'] != 0 or (actual.st_dev,actual.st_ino) != (declared.st_dev,declared.st_ino):
        raise ValueError('fixture daemon differs from its controlled image')
    matches = []
    build_uids = {pwd.getpwnam(name).pw_uid for name in grp.getgrnam('nixbld').gr_mem}
    for row in descendants(pid)[1:]:
        try:
            executable = pathlib.Path('/proc',str(row['pid']),'exe').stat()
            args = pathlib.Path('/proc',str(row['pid']),'cmdline').read_bytes().rstrip(b'\0').split(b'\0')
        except (FileNotFoundError, ProcessLookupError): continue
        selected = pathlib.Path(record['static_builder']['path']).stat()
        if row['uid'] in build_uids and (executable.st_dev,executable.st_ino) == (selected.st_dev,selected.st_ino) and args[-2:] == [b'-c',fixture['arguments'][-1].encode()]:
            matches.append(row)
    if len(matches) > 1: raise ValueError('fixture derivative has ambiguous builder processes')
    rows = descendants(matches[0]['pid']) if matches else []
    sleeping = False
    for child in rows[1:]:
        try:
            args = pathlib.Path('/proc',str(child['pid']),'cmdline').read_bytes().rstrip(b'\0').split(b'\0')
            executable = pathlib.Path('/proc',str(child['pid']),'exe').stat()
        except (FileNotFoundError, ProcessLookupError): continue
        if args[-2:] == [b'sleep',b'300'] and (executable.st_dev,executable.st_ino) == (selected.st_dev,selected.st_ino):
            sleeping = True
    if not sleeping: rows = []
    if rows:
        with lease.open('x') as stream: json.dump(rows,stream)
        lease.chmod(0o600)
else:
    info = lease.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError('fixture observer lease lost root ownership')
    rows = json.loads(lease.read_bytes())
    if operation == 'cleanup':
        for selected in reversed(rows):
            current = identity(selected['pid'])
            if current and all(current[k] == selected[k] for k in ('pid','start_ticks','uid')):
                try: os.kill(current['pid'],signal.SIGKILL)
                except ProcessLookupError: pass
live = []
for selected in rows:
    current = identity(selected['pid'])
    if current and all(current[k] == selected[k] for k in ('pid','start_ticks','uid')): live.append(current)
print(json.dumps({'owned':rows,'live':live}))
'''


def observe(record: dict, operation: str) -> dict:
    if operation not in {'observe', 'state', 'cleanup'}:
        raise ValueError('Nix observer operation is not a fixed lifecycle action')
    result = subprocess.run(['sudo', '-n', str(Path(sys.executable).resolve()), '-c', OBSERVER,
                             operation, record['cancellation']['token']],
                            capture_output=True, timeout=15,
                            env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C'})
    if result.returncode or len(result.stdout) > 65536:
        raise CaseUnavailable('fixed Nix daemon observer could not establish owned process facts')
    value = json.loads(result.stdout)
    if not isinstance(value, dict) or set(value) != {'owned', 'live'} or not isinstance(value['owned'], list) or not isinstance(value['live'], list):
        raise ValueError('Nix fixture observer returned invalid process facts')
    return value
