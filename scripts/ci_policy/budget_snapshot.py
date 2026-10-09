"""Complete maintainability observations, bound to the measured source tree."""
import hashlib
import json
import re

CATEGORIES = frozenset({'source_files', 'test_files', 'source_directories', 'test_directories',
                        'go_struct_fields', 'go_receiver_methods', 'go_receiver_lines', 'ts_local_dependencies'})
SHA = re.compile(r'[0-9a-f]{40}')


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode()).hexdigest()


def key(category, artifact):
    return digest(['maintainability', category, artifact])


def seal(report, source_sha, source_tree, base_sha):
    tracking = report['tracking']
    tracking.update(source_sha=source_sha, source_tree=source_tree, base_sha=base_sha)
    tracking['digest'] = digest({k: v for k, v in tracking.items() if k != 'digest'})
    return tracking


def relative(value):
    return (isinstance(value, str) and bool(value) and len(value) <= 1024
            and not value.startswith('/') and all(part not in {'', '.', '..'} for part in value.split('/'))
            and not any(ord(c) < 32 or c in '`\\' for c in value))


def validate(report, sha, tree):
    if not isinstance(report, dict) or report.get('suite') != 'maintainability':
        raise ValueError('not a maintainability report')
    snapshot = report.get('tracking')
    if not isinstance(snapshot, dict) or snapshot.get('schema_version') != 1 or snapshot.get('complete') is not True:
        raise ValueError('missing complete tracking snapshot')
    for name, expected in [('source_sha', sha), ('source_tree', tree)]:
        if not isinstance(expected, str) or not SHA.fullmatch(expected) or snapshot.get(name) != expected:
            raise ValueError('snapshot source identity mismatch')
    if not isinstance(snapshot.get('base_sha'), str) or not SHA.fullmatch(snapshot['base_sha']):
        raise ValueError('invalid snapshot base')
    if snapshot.get('digest') != digest({k: v for k, v in snapshot.items() if k != 'digest'}):
        raise ValueError('snapshot digest mismatch')
    artifacts = snapshot.get('artifacts')
    if not isinstance(artifacts, list) or len(artifacts) > 10000:
        raise ValueError('invalid tracking artifacts')
    out = {}
    for row in artifacts:
        if not isinstance(row, dict) or row.get('category') not in CATEGORIES or not relative(row.get('id')):
            raise ValueError('invalid artifact identity')
        if any(type(row.get(field)) is not int for field in ['measured', 'warn', 'limit', 'effective_cap']):
            raise ValueError('invalid artifact measurement')
        if not 0 < row['warn'] < row['limit'] <= row['effective_cap'] or row['measured'] <= row['warn']:
            raise ValueError('invalid artifact thresholds')
        reason, sources = row.get('exception_reason'), row.get('sources')
        if not isinstance(reason, str) or len(reason) > 4096 or row['effective_cap'] > row['limit'] and not reason.strip():
            raise ValueError('invalid exception reason')
        if not isinstance(sources, list) or not sources or len(sources) > 1000 or not all(relative(s) for s in sources):
            raise ValueError('invalid artifact sources')
        identity = key(row['category'], row['id'])
        if identity in out:
            raise ValueError('duplicate tracking artifact')
        out[identity] = row
    return snapshot, out
