"""Bind an affected website row to the common halt plan and immutable signed identities."""
import hashlib

from .halt_plan import validate
from . import public_history
from .remote_document import ORIGIN, halt_digests, trust
from .document_schema import require


def identities(row, advertised, *, read=None, rehearsal=False, registry=None, origin=ORIGIN):
    trust(rehearsal, registry, origin)
    validate({'bad_versions': [row['bad']], 'channels': [row]})
    require(advertised['channel'] == row['channel'] and advertised['version'] == row['bad'],
            'website withdrawal requires its actually advertised bad release')
    from release_distribution import read_storage_bytes
    read = read or read_storage_bytes
    if rehearsal and read is read_storage_bytes:
        raise ValueError('rehearsal withdrawal requires its explicit isolated transport')
    updater = read('updates/releases/' + row['bad'] + '.json')
    require(isinstance(updater, bytes) and hashlib.sha256(updater).hexdigest() == advertised['manifest_sha256'],
            'website receipt differs from the selected immutable updater')
    if row['bad_legacy']:
        require(advertised.get('distribution_sha256') is None
                and advertised['manifest_sha256'] == public_history.load()['releases'][row['bad']]['updater_manifest_sha256'],
                'legacy bad release differs from the sealed publication baseline')
    legacy = {version for version, selected in ((row['bad'], row['bad_legacy']),
              (row['last_good'], row['last_good_legacy'])) if selected}
    result = halt_digests(row['bad'], row['last_good'], row['last_good_manifest_sha256'], read=read,
                         legacy_versions=legacy, rehearsal=rehearsal, registry=registry, origin=origin)
    require(result['distribution_sha256'] == row['distribution_sha256']
            and result['bad_distribution_sha256'] == advertised.get('distribution_sha256'),
            'website halt differs from the independently selected distribution identities')
    return {**result, 'last_good_legacy': row['last_good_legacy'], 'bad_legacy': row['bad_legacy']}
