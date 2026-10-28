"""The sealed public release baseline preceding signed distribution documents."""
import hashlib
import json
from pathlib import Path

from release_semver import compare, parse
from .document_schema import digest, exact, require

ROOT = Path(__file__).resolve().parents[2]
LEGACY = ROOT / 'packaging/distribution-legacy-releases.json'
LEGACY_SHA256 = '5f17294d797e15e99f205b63097eb30a64217e1d731d325d4e1a1df19290ef41'


def load(path=LEGACY):
    raw = path.read_bytes()
    require(hashlib.sha256(raw).hexdigest() == LEGACY_SHA256, 'legacy release enumeration differs from its sealed baseline')
    document = json.loads(raw)
    exact(document, {'schema_version', 'document_release_floor', 'releases'})
    require(type(document['schema_version']) is int and document['schema_version'] == 1
            and isinstance(document['releases'], list), 'unsupported legacy baseline')
    floor, seen = parse(document['document_release_floor']), {}
    for row in document['releases']:
        exact(row, {'version', 'updater_manifest_sha256', 'dmg_sha256'})
        require(row['version'] not in seen and compare(parse(row['version']), floor) < 0,
                'legacy enumeration crosses the document release boundary')
        digest(row['updater_manifest_sha256'])
        digest(row['dmg_sha256'])
        seen[row['version']] = row
    return {'document_release_floor': document['document_release_floor'], 'releases': seen}
