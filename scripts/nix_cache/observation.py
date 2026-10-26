"""Fresh read-only delivery observation after independent installed-release admission."""
from __future__ import annotations

import copy
import hashlib
from pathlib import Path

from .publication import encoded, record_name, verified_report
from .release_authority import authorize


PUBLIC_CACHE = 'https://downloads.paintedwolf.dev/nix-cache'


def observe(cache: Path, report: dict, *, selection: dict, trust_policy: dict, backend) -> dict:
    """Read authenticated storage controls and ordinary HTTPS; never publish or activate."""
    checked = verified_report(cache, copy.deepcopy(report))
    request = checked['request']
    if request['mode'] != 'production' or request['cache_prefix'] != 'nix-cache':
        raise ValueError('public cache observation requires the protected production namespace')
    if getattr(backend, 'public', None) != PUBLIC_CACHE:
        raise ValueError('public cache observation differs from the protected delivery origin')
    if authorize(selection, request, checked, trust_policy=trust_policy) is not True:
        raise ValueError('public cache observation requires fresh installed-release admission')
    original = record_name(request)
    withdrawn = original + '.withdrawn'
    if backend.read(withdrawn) is not None:
        raise ValueError('withdrawn cache release cannot be advertised as available')
    receipt = {'schema_version': 1, 'request': request, 'closure_sha256': checked['closure_sha256'],
               'closure': checked['closure'], 'objects': checked['objects'], 'public_keys': checked['public_keys']}
    raw = encoded(receipt)
    name = original
    if backend.read(name) != raw:
        # The publication owner retains the original record when adding signatures.
        name = original.removesuffix('.json') + '-rotation-' + hashlib.sha256(raw).hexdigest() + '.json'
        if backend.read(name) != raw:
            raise ValueError('cache availability record differs from the freshly admitted report')
    for row in checked['objects']:
        backend.verify_public(row['name'], row)
    backend.verify_public(name, {'name': name, 'phase': 'release-record', 'bytes': len(raw),
                                'sha256': hashlib.sha256(raw).hexdigest(),
                                'content_type': 'application/json',
                                'cache_control': 'no-cache, no-store, must-revalidate'})
    if backend.read(withdrawn) is not None or backend.read(name) != raw:
        raise ValueError('cache release changed or was withdrawn during public observation')
    if authorize(selection, request, checked, trust_policy=trust_policy) is not True:
        raise ValueError('installed-release admission changed during public cache observation')
    facts = {'schema_version': 1, 'request': request, 'base_url': PUBLIC_CACHE,
             'closure_sha256': checked['closure_sha256'],
             'objects_sha256': hashlib.sha256(encoded({'objects': checked['objects']})).hexdigest(),
             'record': name, 'record_sha256': hashlib.sha256(raw).hexdigest(),
             'public_keys': checked['public_keys'], 'public_bytes_verified': True}
    return {'facts': facts, 'admission_sha256': hashlib.sha256(encoded(facts)).hexdigest()}
