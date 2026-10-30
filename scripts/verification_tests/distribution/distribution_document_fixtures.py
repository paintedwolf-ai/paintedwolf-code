"""Disposable minisign identities and real shared document preparation."""
import base64
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import uuid

from scripts.release_distribution_contract.document_signing import prepare

ORIGIN = 'https://distribution.example.invalid'


def signing_identity(root):
    tool = shutil.which('minisign')
    if not tool:
        raise RuntimeError('distribution fixtures require minisign; provision repository-tools before verification')
    public = {}
    for role in ('artifact', 'feed'):
        key, secret = root / (role + '.pub'), root / (role + '.key')
        subprocess.run([tool, '-G', '-W', '-p', str(key), '-s', str(secret)], check=True, capture_output=True)
        public[role] = base64.b64encode(key.read_bytes()).decode()
    registry = {'schema_version': 1, 'signing_generation': 1, 'embedded_generation': 1, 'generations': [
        {'generation': 1, 'public_key': public['artifact'], 'feed_public_key': public['feed'],
         'successor': None, 'bridge_version': None}]}
    def sign(path, number, protected):
        raw = root / ('signature-' + uuid.uuid4().hex + '.minisig')
        version = json.loads(path.read_bytes())['version']
        comment = f'timestamp:1800000000\tfile:{path.name}\tversion:{version}'
        subprocess.run([tool, '-S', '-s', str(root / 'feed.key'), '-m', str(path), '-x', str(raw), '-t', comment],
                       check=True, capture_output=True)
        return base64.b64encode(raw.read_bytes()).decode()
    return registry, sign


def prepared_document(root, registry, sign, version='1.2.3', *, updater_bytes=None):
    catalog, updater, artifacts = root / 'platforms.json', root / 'updater.json', root / 'artifacts'
    artifacts.mkdir()
    rows = [{'updater_key': 'darwin-aarch64', 'goos': 'darwin', 'artifact_arch': 'aarch64',
             'publication': 'public', 'package_extensions': ['dmg'], 'updater_extension': 'app.tar.gz'}]
    rows += [{'updater_key': key, 'goos': 'linux', 'artifact_arch': arch, 'publication': 'public',
              'package_extensions': ['deb', 'rpm', 'pkg.tar.zst'], 'updater_extension': None}
             for key, arch in [('linux-x86_64', 'x86_64'), ('linux-aarch64', 'aarch64')]]
    catalog.write_text(json.dumps({'schema_version': 2, 'platforms': rows}))
    updater.write_text(json.dumps({'version': version, 'notes': 'Fixture release', 'pub_date': '2026-10-28T00:00:00Z',
                                  'platforms': {'darwin-aarch64': {'url': ORIGIN + f'/releases/v{version}/painted-wolf-code_v{version}_darwin-aarch64.app.tar.gz', 'signature': 'fixture'}}}))
    if updater_bytes is not None:
        updater.write_bytes(updater_bytes)
    content = {}
    for row in rows:
        for extension in row['package_extensions']:
            name = f'painted-wolf-code_v{version}_{row["updater_key"]}.{extension}'
            (artifacts / name).write_bytes(name.encode())
            content[name] = 'application/octet-stream'
    request = {'mode': 'rehearsal', 'version': version, 'origin': ORIGIN, 'source_commit': 'a' * 40,
        'source_tree': 'b' * 40, 'controller_commit': 'c' * 40, 'signing_generation': 1,
        'package_content_types': content, 'repositories': [],
        'nix': {'source': {'git_url': 'github:paintedwolf-ai/paintedwolf-code/' + 'a' * 40,
                          'commit': 'a' * 40, 'flake_lock_sha256': 'd' * 64}, 'cache': {'enabled': False}}}
    prepare((root / 'signed').resolve(), request, catalog, updater, artifacts,
            authorize=lambda binding: binding['request'] == request, registry=registry, signer=sign)
    raw = (root / 'signed/distribution.v1.json').read_bytes()
    signature = (root / 'signed/distribution.v1.json.sig').read_bytes()
    objects = {f'releases/v{version}/distribution.v1.json': raw,
               f'releases/v{version}/distribution.v1.json.sig': signature,
               f'updates/releases/{version}.json': updater.read_bytes()}
    receipt = {'version': version, 'manifest_sha256': hashlib.sha256(updater.read_bytes()).hexdigest(),
               'distribution_sha256': hashlib.sha256(raw).hexdigest(), 'channel': 'stable', 'withdrawn_version': None}
    return objects, receipt


def cache_receipt(request, public_keys):
    """A complete declared availability record; no claim of native NAR verification."""
    from scripts.nix_cache.publication import availability
    closure = {output: {'nar_hash': 'sha256-' + base64.b64encode(bytes(32)).decode(), 'nar_bytes': 1,
                        'references': [], 'derivation': output + '.drv'} for output in request['outputs']}
    objects = []
    names = [('nar/content.nar.xz', 'nar'), ('nix-cache-info', 'cache-info')]
    names += [(Path(path).name.split('-', 1)[0] + '.narinfo', 'narinfo') for path in closure]
    for name, phase in names:
        objects.append({'name': name, 'phase': phase, 'bytes': 1, 'sha256': 'a' * 64,
            'content_type': 'application/octet-stream' if phase == 'nar' else 'text/plain; charset=utf-8',
            'cache_control': 'public, max-age=31536000, immutable' if phase == 'nar' else 'no-cache, no-store, must-revalidate'})
    return availability({'request': request, 'public_keys': public_keys, 'objects': objects, 'closure': closure,
        'closure_sha256': hashlib.sha256(json.dumps(closure, sort_keys=True, separators=(',', ':')).encode()).hexdigest()})
