"""Select and validate the shared per-channel halt facts before any effect prepares."""
import hashlib
import json

from release_semver import compare, parse
from .document_schema import exact, require
from . import public_history
from .remote_document import ORIGIN, trust, verify_metadata

FIELDS = {'channel', 'bad', 'last_good'}
FACTS = {'last_good_legacy', 'last_good_manifest_sha256', 'last_good_dmg_sha256', 'distribution_sha256', 'bad_legacy'}


def validate_request(distribution):
    exact(distribution, {'bad_versions', 'channels'})
    bad = distribution['bad_versions']
    require(isinstance(bad, list) and all(isinstance(value, str) for value in bad) and len(bad) == len(set(bad)),
            'halt plan requires distinct typed bad versions')
    for value in bad:
        parse(value)
    rows = distribution['channels']
    require(isinstance(rows, list), 'halt plan requires explicit channel rows')
    seen = set()
    for row in rows:
        require(isinstance(row, dict) and set(row) in (FIELDS, FIELDS | FACTS), 'unsupported halt channel fields')
        channel, version, good = row['channel'], row['bad'], row['last_good']
        require(channel in {'stable', 'preview'} and channel not in seen and version in bad
                and (good is None or isinstance(good, str)), 'halt plan has an invalid or duplicate channel row')
        selected = parse(version)
        require(channel == 'preview' or selected.channel == 'stable', 'stable cannot halt a Preview-only release')
        if good is not None:
            replacement = parse(good)
            require(good not in bad and compare(replacement, selected) < 0
                    and (channel == 'preview' or replacement.channel == 'stable'),
                    'halt replacement must precede bad without regressing channel authority')
        seen.add(channel)
    return distribution


def validate(distribution):
    validate_request(distribution)
    baseline = public_history.load()["releases"]
    from .document_schema import digest
    for row in distribution['channels']:
        exact(row, FIELDS | FACTS)
        good = row['last_good']
        require(type(row['last_good_legacy']) is bool and type(row['bad_legacy']) is bool
                and row['last_good_legacy'] == (good in baseline) and row['bad_legacy'] == (row['bad'] in baseline),
                'halt legacy fact differs from the closed publication baseline')
        if good is None:
            require(all(row[key] is None for key in ('last_good_manifest_sha256', 'last_good_dmg_sha256', 'distribution_sha256')),
                    'empty replacement cannot retain release identities')
        elif row['last_good_legacy']:
            require(row['last_good_manifest_sha256'] == baseline[good]['updater_manifest_sha256']
                    and row['last_good_dmg_sha256'] == baseline[good]['dmg_sha256'] and row['distribution_sha256'] is None,
                    'legacy replacement differs from its sealed immutable identities')
        else:
            digest(row['last_good_manifest_sha256'])
            digest(row['distribution_sha256'])
            require(row['last_good_dmg_sha256'] is None, 'new replacement cannot select legacy package facts')
    return distribution


def classify(distribution, *, read=None, rehearsal=False, registry=None, origin=ORIGIN):
    """Only this plan owner classifies legacy; every consumer receives the same verified fact."""
    validate_request(distribution)
    if any(FACTS <= set(row) for row in distribution["channels"]):
        validate(distribution)
    trust(rehearsal, registry, origin)
    from release_distribution import read_storage_bytes
    read = read or read_storage_bytes
    if rehearsal and read is read_storage_bytes:
        raise ValueError('rehearsal halt selection requires its explicit isolated transport')
    baseline, selected = public_history.load()["releases"], {}
    def release(version):
        if version in selected:
            return selected[version]
        raw = read('updates/releases/' + version + '.json')
        require(isinstance(raw, bytes) and json.loads(raw).get('version') == version,
                'halt replacement lacks its exact immutable updater')
        updater = hashlib.sha256(raw).hexdigest()
        if version in baseline:
            checksum = read(f'releases/v{version}/painted-wolf-code_v{version}_darwin-aarch64.dmg.sha256')
            require(updater == baseline[version]['updater_manifest_sha256'] and isinstance(checksum, bytes)
                    and checksum.decode().strip() == baseline[version]['dmg_sha256'],
                    'legacy release differs from its sealed immutable identities')
            facts = {'last_good_legacy': True, 'last_good_manifest_sha256': updater,
                     'last_good_dmg_sha256': baseline[version]['dmg_sha256'], 'distribution_sha256': None}
        else:
            key = 'releases/v' + version + '/distribution.v1.json'
            document, signature = read(key), read(key + '.sig')
            require(isinstance(document, bytes) and isinstance(signature, bytes),
                    'non-legacy replacement requires its complete immutable distribution')
            verify_metadata(document, signature.decode(), version, updater, rehearsal=rehearsal, registry=registry, origin=origin)
            facts = {'last_good_legacy': False, 'last_good_manifest_sha256': updater, 'last_good_dmg_sha256': None,
                     'distribution_sha256': hashlib.sha256(document).hexdigest()}
        selected[version] = facts
        return facts
    rows = []
    from .ledger import require_publishable
    for row in distribution['channels']:
        if row['last_good'] is not None and row['last_good'] not in selected:
            require_publishable(row['last_good'], read=read)
        facts = release(row['last_good']) if row['last_good'] is not None else {
            'last_good_legacy': False, 'last_good_manifest_sha256': None, 'last_good_dmg_sha256': None, 'distribution_sha256': None}
        facts = {**facts, 'bad_legacy': row['bad'] in baseline}
        if row['bad'] in baseline:
            release(row['bad'])
        require(not FACTS <= set(row) or all(row[key] == value for key, value in facts.items()),
                'reviewed halt facts differ from the independently selected release')
        rows.append({**row, **facts})
    return validate({'bad_versions': distribution['bad_versions'], 'channels': rows})
