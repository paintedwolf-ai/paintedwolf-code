"""Prepare and retain the common negative release ledger separately from feed-pointer actions."""
from pathlib import Path
import subprocess
import tempfile

from release_semver import parse
from .document_schema import exact, require

ROOT = Path(__file__).resolve().parents[2]


def require_publishable(version, *, read=None):
    from release_distribution import read_storage
    read = read or read_storage
    parse(version)
    if read(f'release-metadata/{version}/withdrawn.json') is not None:
        raise ValueError('a withdrawn release cannot be published again')


def is_marker(value, version):
    return (isinstance(value, dict) and set(value) == {'version', 'withdrawn'}
            and isinstance(value['version'], str) and value['version'] == version
            and value['withdrawn'] is True)


def prepare(versions, *, read=None):
    from release_distribution import read_storage
    read = read or read_storage
    require(isinstance(versions, list) and all(isinstance(version, str) for version in versions)
            and len(versions) == len(set(versions)), 'negative ledger requires distinct typed versions')
    for version in versions:
        parse(version)
    rows = []
    for version in versions:
        current = read(f'release-metadata/{version}/withdrawn.json')
        require(current is None or is_marker(current, version),
                'withdrawal record has unexpected content')
        rows.append({'version': version, 'action': 'withdraw' if current is None else 'keep'})
    return rows


def validate(rows, versions, *, read=None):
    from release_distribution import read_storage
    read = read or read_storage
    require(isinstance(rows, list), 'negative ledger preparation requires explicit rows')
    seen, current = set(), {}
    for row in rows:
        exact(row, {'version', 'action'})
        version = row['version']
        require(version in versions and version not in seen and row['action'] in {'withdraw', 'keep'},
                'negative ledger preparation differs from the selected halt intent')
        parse(version)
        seen.add(version)
        current[version] = read(f'release-metadata/{version}/withdrawn.json')
        require(is_marker(current[version], version) or current[version] is None and row['action'] == 'withdraw',
                'negative ledger changed after preparation')
    require(seen == set(versions), 'negative ledger preparation omits a selected halted version')
    return current


def record(rows, versions, *, read=None):
    from release_distribution import encoded
    current = validate(rows, versions, read=read)
    for row in rows:
        version = row['version']
        if current[version] is not None:
            continue
        with tempfile.TemporaryDirectory(prefix='release-withdrawal-') as temporary:
            source = Path(temporary) / 'withdrawn.json'
            source.write_bytes(encoded({'version': version, 'withdrawn': True}))
            subprocess.run(['bash', str(ROOT / 'scripts/release-r2-immutable-put.sh'),
                            '--key', f'release-metadata/{version}/withdrawn.json', '--file', str(source),
                            '--content-type', 'application/json'], check=True)
