"""Observe only the selected engine's sealed, live browser descendants."""
from __future__ import annotations

import json
from pathlib import Path
import subprocess

from .app import process_identity
from .process import probe_path

NAMES = {'user', 'mnt', 'pid', 'net'}
CAPS = {'CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb'}
OBSERVER = '''import json, os, pathlib, sys
expected = json.loads(sys.argv[1])
engine = expected['engine']
def identity(pid):
    fields = pathlib.Path(f'/proc/{pid}/stat').read_text().rpartition(') ')[2].split()
    return {'pid': pid, 'parent_pid': int(fields[1]), 'start_time_ticks': int(fields[19])}
if identity(engine['pid']) != engine:
    raise SystemExit(73)
rows = {}
for root in pathlib.Path('/proc').iterdir():
    if root.name.isdecimal():
        try:
            row = identity(int(root.name))
            rows[row['pid']] = row
        except (FileNotFoundError, ProcessLookupError):
            pass
owned = {engine['pid']}
for _ in range(64):
    children = {pid for pid, row in rows.items() if row['parent_pid'] in owned}
    if children <= owned:
        break
    owned |= children
else:
    raise SystemExit(74)
observed = []
for pid in sorted(owned - {engine['pid']}):
    try:
        image = os.stat(f'/proc/{pid}/exe')
        if [image.st_dev, image.st_ino, image.st_size] != expected['image']:
            continue
        current = pid
        chain = []
        while current != engine['pid']:
            row = rows[current]
            if identity(current) != row or len(chain) >= 64:
                raise SystemExit(75)
            chain.append(row)
            current = row['parent_pid']
        status = dict(line.split(':', 1) for line in pathlib.Path(f'/proc/{pid}/status').read_text().splitlines() if ':' in line)
        namespaces = {name: os.stat(f'/proc/{pid}/ns/{name}').st_ino for name in ('user', 'mnt', 'pid', 'net')}
        final = os.stat(f'/proc/{pid}/exe')
        if identity(pid) != rows[pid] or [final.st_dev, final.st_ino, final.st_size] != expected['image']:
            raise SystemExit(76)
        observed.append({'identity': rows[pid], 'ancestors': chain, 'namespaces': namespaces,
                         'uids': [int(value) for value in status['Uid'].split()],
                         'no_new_privs': int(status['NoNewPrivs']), 'seccomp': int(status['Seccomp']),
                         'capabilities': {name: int(status[name], 16) for name in ('CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb')}})
    except (FileNotFoundError, ProcessLookupError):
        pass
if identity(engine['pid']) != engine:
    raise SystemExit(77)
print(json.dumps({'engine': engine, 'browsers': observed}))
'''


def validate(document: dict, engine: dict, namespaces: dict, uid: int) -> list[dict]:
    if (not isinstance(document, dict) or set(document) != {'engine', 'browsers'} or document['engine'] != engine
            or not isinstance(document['browsers'], list) or not 1 <= len(document['browsers']) <= 64
            or not isinstance(namespaces, dict) or set(namespaces) != NAMES
            or any(type(value) is not int or value <= 0 for value in namespaces.values())):
        raise ValueError('browser observer did not capture its selected engine and live browser processes')
    identities, seen = [], set()
    for row in document['browsers']:
        if not isinstance(row, dict):
            raise ValueError('browser observer returned a malformed process')
        identity = row.get('identity', {})
        if (not isinstance(identity, dict) or set(identity) != {'pid', 'parent_pid', 'start_time_ticks'}
                or any(type(value) is not int or value <= 0 for value in identity.values())
                or identity['pid'] <= 1 or identity['pid'] in seen or identity['pid'] == engine['pid']):
            raise ValueError('browser observer returned a reused or malformed process identity')
        seen.add(identity['pid'])
        chain = row.get('ancestors')
        if (not isinstance(chain, list) or not 1 <= len(chain) <= 64 or chain[0] != identity
                or any(not isinstance(member, dict) or set(member) != set(identity)
                       or any(type(value) is not int or value <= 0 for value in member.values()) for member in chain)
                or len({member['pid'] for member in chain}) != len(chain)
                or any(child['parent_pid'] != parent['pid'] for child, parent in zip(chain, chain[1:]))
                or chain[-1]['parent_pid'] != engine['pid']):
            raise ValueError('browser observer did not retain a complete owned ancestry')
        actual, caps = row.get('namespaces'), row.get('capabilities')
        if (row.get('uids') != [uid] * 4 or any(type(value) is not int for value in row['uids'])
                or type(row.get('no_new_privs')) is not int or row['no_new_privs'] != 1
                or type(row.get('seccomp')) is not int or row['seccomp'] != 2
                or not isinstance(caps, dict) or set(caps) != CAPS
                or any(type(value) is not int or value != 0 for value in caps.values())
                or not isinstance(actual, dict) or set(actual) != NAMES
                or any(type(value) is not int or value <= 0 or value == namespaces[name] for name, value in actual.items())):
            raise ValueError('live installed browser lost a required kernel boundary')
        identities.append({'pid': identity['pid'], 'start_ticks': str(identity['start_time_ticks'])})
    return identities


def observe(binding: dict, engine: dict) -> dict:
    image = probe_path(binding, 'browser-observer')
    expected = binding['images'][str(image)]
    if process_identity(engine['pid']) != engine:
        raise ValueError('browser observation lost its engine identity')
    selected = {'engine': engine, 'image': [expected['device'], expected['inode'], expected['bytes']]}
    result = subprocess.run(['/usr/bin/sudo', '-n', '/usr/bin/python3', '-c', OBSERVER, json.dumps(selected)],
        capture_output=True, timeout=20, check=False, env={'PATH': '/usr/bin:/bin', 'LC_ALL': 'C'})
    if result.returncode or len(result.stdout) > 256 << 10 or process_identity(engine['pid']) != engine:
        raise ValueError('browser observer could not bind live sealed descendants to its engine')
    probe_path(binding, 'browser-observer')
    return json.loads(result.stdout)
