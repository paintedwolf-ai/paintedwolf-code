"""Bind the sealed browser to the exact installed launch and engine."""
from __future__ import annotations

from . import process_tree
from .app import process_identity
from .process import probe_path

NAMES = {'user', 'mnt', 'pid', 'net'}
CAPS = {'CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb'}


def require_chain(chain: object, identity: dict, launch: dict) -> None:
    if (not isinstance(chain, list) or not 1 <= len(chain) <= 64 or chain[0] != identity
            or any(not isinstance(member, dict) or set(member) != {'pid', 'parent_pid', 'start_time_ticks'}
                   or any(type(value) is not int or value <= 0 for value in member.values()) for member in chain)
            or len({member['pid'] for member in chain}) != len(chain)
            or any(member['pid'] == launch['pid'] for member in chain)
            or any(child['parent_pid'] != parent['pid'] for child, parent in zip(chain, chain[1:]))
            or chain[-1]['parent_pid'] != launch['pid']):
        raise ValueError('browser observer did not retain a complete captured launch ancestry')


def validate(document: dict, engine: dict, launch: dict, namespaces: dict, uid: int) -> list[dict]:
    if (not isinstance(document, dict) or set(document) != {'launch', 'engine', 'engine_ancestors', 'browsers'} or document['engine'] != engine
            or document['launch'] != launch
            or not isinstance(document['browsers'], list) or not 1 <= len(document['browsers']) <= 64
            or not isinstance(namespaces, dict) or set(namespaces) != NAMES
            or any(type(value) is not int or value <= 0 for value in namespaces.values())):
        raise ValueError('browser observer did not capture its selected engine and live browser processes')
    require_chain(document['engine_ancestors'], engine, launch)
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
        require_chain(row.get('ancestors'), identity, launch)
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


def observe(binding: dict, engine: dict, launch: dict) -> dict:
    image = probe_path(binding, 'browser-observer')
    expected = binding['images'][str(image)]
    if process_identity(engine['pid']) != engine or process_identity(launch['pid']) != launch:
        raise ValueError('browser observation lost its engine or launch identity')
    tree = process_tree.observe(launch)
    rows = {row['identity']['pid']: row for row in tree['processes']}

    def ancestry(identity):
        chain, current = [], identity
        for _ in range(64):
            chain.append(current)
            if current['parent_pid'] == launch['pid']:
                require_chain(chain, identity, launch)
                return chain
            parent = rows.get(current['parent_pid'])
            if parent is None or parent['identity'] in chain:
                break
            current = parent['identity']
        raise ValueError('browser observation could not bind its captured launch ancestry')

    if rows.get(engine['pid'], {}).get('identity') != engine:
        raise ValueError('browser observation omitted its selected engine')
    document = {'launch': launch, 'engine': engine, 'engine_ancestors': ancestry(engine), 'browsers': []}
    selected = [expected['device'], expected['inode'], expected['bytes']]
    # The broker starts the browser outside the protected engine's namespace;
    # both branches must still terminate at this verified launch instance.
    for row in tree['processes']:
        if row['image'] != selected:
            continue
        document['browsers'].append({
            'identity': row['identity'], 'ancestors': ancestry(row['identity']),
            'namespaces': {name: pair[1] for name, pair in row['namespaces'].items()},
            'uids': row['uids'], 'no_new_privs': row['no_new_privs'], 'seccomp': row['seccomp'],
            'capabilities': {**row['capabilities'], 'CapBnd': row['capability_bounding_set']}})
    if process_identity(engine['pid']) != engine or process_identity(launch['pid']) != launch:
        raise ValueError('browser observation changed its engine or launch identity')
    probe_path(binding, 'browser-observer')
    return document
