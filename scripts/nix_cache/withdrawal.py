"""Prepare every cache availability action before applying a common halt plan."""
import hashlib
import json

from .admission import validate_request, validate_scope, SYSTEMS
from .publication import encoded, validate_availability, withdraw as retract
from .r2 import R2Transport


def identity(raw):
    return hashlib.sha256(raw).hexdigest() if raw is not None else None


def prepare(distribution, *, rehearsal=False, policy=None, cache_prefix='nix-cache', backend_factory=None):
    from release_distribution_contract.halt_plan import validate
    validate(distribution)
    if type(rehearsal) is not bool or rehearsal and (policy is None or backend_factory is None):
        raise ValueError('cache halt requires explicit trust and isolated rehearsal transport')
    from .release_lane import trust_policy
    maintained = trust_policy() if not rehearsal else policy
    if not rehearsal and policy is not None and policy != maintained:
        raise ValueError('production cache halt cannot replace maintained trust')
    policy = maintained
    mode = 'rehearsal' if rehearsal else 'production'
    validate_scope(mode, cache_prefix, policy)
    backend_factory = backend_factory or R2Transport
    backend = backend_factory({'mode': mode, 'cache_prefix': cache_prefix})
    rows = []
    for bad in distribution['bad_versions']:
        for system in SYSTEMS:
            name = f'release-records/{bad}/{system}.json'
            raw, marker = backend.read(name), backend.read(name + '.withdrawn')
            request = validate_availability(json.loads(raw), policy)['request'] if raw is not None else None
            if marker is not None:
                control = json.loads(marker)
                if (not isinstance(control, dict) or set(control) != {'schema_version', 'request', 'withdrawn'}
                        or type(control['schema_version']) is not int or control['schema_version'] != 1 or control['withdrawn'] is not True):
                    raise ValueError('cache withdrawal control is inconsistent')
                if request is not None and control['request'] != request:
                    raise ValueError('cache withdrawal control differs from its availability record')
                request = control['request']
            if request is not None:
                validate_request(request, policy)
                if request['version'] != bad or request['system'] != system or request['mode'] != mode or request['cache_prefix'] != cache_prefix:
                    raise ValueError('Nix cache release record differs from the halted release')
            rows.append({'version': bad, 'system': system, 'action': 'withdraw' if raw is not None and marker is None else 'keep',
                         'request': request, 'record_sha256': identity(raw), 'withdrawn_sha256': identity(marker)})
    return {'schema_version': 1, 'mode': mode, 'cache_prefix': cache_prefix,
            'bad_versions': distribution['bad_versions'], 'rows': rows}


def apply(prepared, *, rehearsal=False, policy=None, backend_factory=None, withdraw=retract):
    if type(rehearsal) is not bool or rehearsal and (policy is None or backend_factory is None):
        raise ValueError('cache halt requires explicit trust and isolated rehearsal transport')
    from .release_lane import trust_policy
    maintained = trust_policy() if not rehearsal else policy
    if not rehearsal and policy is not None and policy != maintained:
        raise ValueError('production cache halt cannot replace maintained trust')
    mode = 'rehearsal' if rehearsal else 'production'
    if (not isinstance(prepared, dict) or set(prepared) != {'schema_version', 'mode', 'cache_prefix', 'bad_versions', 'rows'}
            or type(prepared['schema_version']) is not int or prepared['schema_version'] != 1 or prepared['mode'] != mode):
        raise ValueError('cache halt differs from the prepared trust mode')
    validate_scope(mode, prepared['cache_prefix'], maintained)
    backend_factory = backend_factory or R2Transport
    from scripts.release_semver import parse
    if (not isinstance(prepared["bad_versions"], list) or not isinstance(prepared["rows"], list)
            or any(not isinstance(bad, str) for bad in prepared["bad_versions"])
            or len(prepared["bad_versions"]) != len(set(prepared["bad_versions"]))):
        raise ValueError("cache halt requires typed distinct release identities")
    for bad in prepared["bad_versions"]:
        parse(bad)
    expected = {(bad, system) for bad in prepared['bad_versions'] for system in SYSTEMS}
    seen = set()
    for row in prepared['rows']:
        if not isinstance(row, dict) or set(row) != {"version", "system", "action", "request", "record_sha256", "withdrawn_sha256"}:
            raise ValueError("cache halt availability action has an unsupported shape")
        key = (row['version'], row['system'])
        if key not in expected or key in seen or row['action'] not in {'withdraw', 'keep'}:
            raise ValueError('cache halt has an unexpected or duplicate availability action')
        seen.add(key)
        if row['request'] is not None:
            validate_request(row['request'], maintained)
            if any(row["request"][key] != value for key, value in {"version": row["version"], "system": row["system"],
                "mode": mode, "cache_prefix": prepared["cache_prefix"]}.items()):
                raise ValueError("cache halt request differs from its selected availability")
        from release_distribution_contract.document_schema import digest
        for name in ("record_sha256", "withdrawn_sha256"):
            if row[name] is not None:
                digest(row[name])
        if row["action"] != ("withdraw" if row["record_sha256"] is not None and row["withdrawn_sha256"] is None else "keep"):
            raise ValueError("cache action differs from its prepared published state")
    if seen != expected:
        raise ValueError("cache halt preparation omits an availability row")
    backend = backend_factory({"mode": mode, "cache_prefix": prepared["cache_prefix"]})
    for row in prepared["rows"]:
        name = f"release-records/{row['version']}/{row['system']}.json"
        marker = backend.read(name + '.withdrawn')
        completed = encoded({'schema_version': 1, 'request': row['request'], 'withdrawn': True})
        if (identity(backend.read(name)) != row['record_sha256']
                or identity(marker) != row['withdrawn_sha256'] and not (row['action'] == 'withdraw' and marker == completed)):
            raise ValueError('cache availability changed after complete halt preparation')
        if row['action'] == 'withdraw' and (row['record_sha256'] is None or row['withdrawn_sha256'] is not None or row['request'] is None):
            raise ValueError('cache withdrawal lacks its selected published availability')
    result = []
    for row in prepared['rows']:
        if row['action'] == 'withdraw':
            selected = row['request']
            withdraw(selected, authorize=lambda value, selected=selected: value == selected, backend_factory=backend_factory)
        result.append({'version': row['version'], 'system': row['system'], 'action': row['action']})
    return result
