"""Distribution delivery facts come from completed effects and fresh selected bytes."""
import base64
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.pacman_publication import configuration
from scripts.linux_release.repository_objects import encoded
from scripts.linux_release.repository_operations import identity, transition
from scripts.linux_release.repository_setup_identity import package_identity
from scripts.release_distribution_contract.release_facts import DeliveryFacts

SOURCE, VERSION, ORIGIN = 'a' * 40, '1.2.3', 'https://downloads.example.invalid'


class CompletedDeliveryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.artifacts = self.root / 'artifacts'
        self.artifacts.mkdir()
        self.storage, self.setups = {}, {}
        self.signer = {'generation': 1, 'fingerprint': 'A' * 40, 'public_key': 'public.asc'}
        self.catalog = self.root / 'keys.json'
        self.catalog.write_text(json.dumps({'format_version': 1, 'signing_generation': 1, 'generations': [self.signer]}))
        (self.root / 'public.asc').write_bytes(b'selected public key')
        self.store('releases/v' + VERSION + '/linux-package-key.asc', b'selected public key')
        (self.artifacts / 'linux-package-key.asc').write_bytes(b'selected public key')
        packages = {}
        for family, extension in [('deb', 'deb'), ('rpm', 'rpm'), ('pacman', 'pkg.tar.zst')]:
            packages[family] = []
            for platform, arch in [('linux-x86_64', 'x86_64'), ('linux-aarch64', 'aarch64')]:
                name = f'painted-wolf-code_v{VERSION}_{platform}.{extension}'
                raw = name.encode()
                (self.artifacts / name).write_bytes(raw)
                digest = hashlib.sha256(raw).hexdigest()
                (self.artifacts / (name + '.sha256')).write_text(digest + '\n')
                packages[family].append({'file': name, 'sha256': digest, 'architecture': arch})
        self.completed = []
        for family, arch in [('deb', None), ('rpm', None), ('pacman', 'x86_64'), ('pacman', 'aarch64')]:
            selected = packages[family] if arch is None else [p for p in packages[family] if p['architecture'] == arch]
            manifest = {'format_version': 1, 'family': family, 'version': VERSION, 'source_commit': SOURCE,
                        'channel': 'stable', 'rehearsal': False, 'packages': selected,
                        'signing_generation': 1, 'key_fingerprint': 'A' * 40, 'inventory_sha256': 'b' * 64}
            if arch is not None:
                manifest.update(architecture=arch, repository='painted-wolf-code')
            generation = identity(manifest)
            record = transition(manifest, None, None)
            if family != 'pacman':
                record['configuration'] = 'generations/' + generation + '/client.conf'
            channel = f'linux/channels/{family}/stable' + ('/' + arch if arch else '')
            self.store(channel + '/current.json', encoded(record))
            self.store('linux/generations/' + generation + '/generation.json', encoded(manifest))
            if family == 'pacman':
                raw = configuration(manifest, ORIGIN + '/' + channel, 'A' * 40).encode()
                self.store('linux/generations/' + generation + '/client.conf', raw)
            else:
                name = package_identity(VERSION, family, 'stable', 1)['file']
                raw = (family + ' setup payload').encode()
                (self.artifacts / name).write_bytes(raw)
                for suffix in ('.SHA256SUMS', '.SHA256SUMS.asc'):
                    (self.artifacts / (name + suffix)).write_bytes(b'fixture signature boundary')
                setup = {'family': family, 'source_commit': SOURCE, 'repository_generation': generation}
                (self.artifacts / (name + '.setup.json')).write_bytes(encoded(setup))
                self.setups[family] = setup
                self.store('releases/v' + VERSION + '/' + name, raw)
            self.completed.append({**record, 'storage_prefix': 'linux', 'public_verified': True,
                                   'publication_phase': 'activated',
                                   **({'pair_consistency': 'required-signature-fail-closed-retry'} if family == 'pacman' else {})})
        self.native, self.nix, self.lock = self.root / 'native.json', self.root / 'nix.json', self.root / 'flake.lock'
        self.native.write_bytes(encoded(self.completed))
        self.lock.write_bytes(b'{}')
        public = 'paintedwolf-cache-1:' + base64.b64encode(bytes(32)).decode()
        self.policy = {'schema_version': 1, 'mode': 'production', 'public_keys': [public]}
        self.receipts = []
        for system in ('x86_64-linux', 'aarch64-linux'):
            output = '/nix/store/' + 'a' * 32 + '-painted-wolf-code'
            request = {'schema_version': 1, 'mode': 'production', 'version': VERSION,
                       'application_commit': SOURCE, 'flake_lock_sha256': hashlib.sha256(b'{}').hexdigest(),
                       'system': system, 'outputs': [output], 'cache_prefix': 'nix-cache'}
            from scripts.verification_tests.distribution.distribution_document_fixtures import cache_receipt
            receipt = cache_receipt(request, [public])
            self.store('nix-cache/release-records/' + VERSION + '/' + system + '.json', encoded(receipt))
            self.receipts.append(receipt)
        self.nix.write_bytes(encoded(self.receipts))
        self.facts = DeliveryFacts(self.artifacts, self.native, self.nix, SOURCE, VERSION, 'stable', ORIGIN,
                                  self.storage.get, self.public, ["stable"])

    def store(self, key, raw):
        self.storage[key] = raw

    def public(self, url, maximum):
        raw = self.storage[url.removeprefix(ORIGIN + '/')]
        self.assertLessEqual(len(raw), maximum)
        return raw

    def repositories(self):
        def admit(package, checksum, signature, catalog, **authority):
            self.assertEqual(catalog, self.catalog)
            self.assertEqual(authority['source_commit'], SOURCE)
            self.assertEqual(authority['repository_generation'], self.setups[authority['family']]['repository_generation'])
            return deepcopy(self.setups[authority['family']])
        with patch('scripts.release_distribution_contract.release_facts.admit_setup', side_effect=admit):
            return self.facts.repositories(catalog=self.catalog)

    def test_completed_native_effects_select_exact_control_assets_and_activation_bytes(self):
        rows = self.repositories()
        self.assertEqual({(r['family'], r['architecture']) for r in rows},
                         {('deb', 'both'), ('rpm', 'both'), ('pacman', 'x86_64'), ('pacman', 'aarch64')})
        for row in rows:
            key = row['base_url'].removeprefix(ORIGIN + '/') + '/current.json'
            self.assertEqual(row['activation_sha256'], hashlib.sha256(self.storage[key]).hexdigest())
            self.assertEqual(row['setup']['sha256'], hashlib.sha256(self.public(row['setup']['url'], 2 << 20)).hexdigest())
        self.assertEqual({r['setup']['file'] for r in rows if r['family'] == 'pacman'}, {'client.conf'})

    def test_changed_channel_source_key_or_public_bytes_refuses(self):
        key = 'linux/channels/deb/stable/current.json'
        original = self.storage[key]
        changed = json.loads(original)
        changed['source_commit'] = 'd' * 40
        self.store(key, encoded(changed))
        with self.assertRaisesRegex(ValueError, 'changed after'):
            self.repositories()
        self.store(key, original)
        self.store('releases/v' + VERSION + '/linux-package-key.asc', b'foreign key')
        with self.assertRaisesRegex(ValueError, 'package trust'):
            self.repositories()

    def test_cache_source_trust_and_live_withdrawal_control_are_required(self):
        result = self.facts.cache(self.lock, policy=self.policy)
        self.assertTrue(result['cache']['enabled'])
        self.assertEqual({row['system'] for row in result['cache']['closures']}, {'x86_64-linux', 'aarch64-linux'})
        key = 'nix-cache/release-records/' + VERSION + '/x86_64-linux.json'
        self.store(key + '.withdrawn', b'{}')
        with self.assertRaisesRegex(ValueError, 'withdrawn'):
            self.facts.cache(self.lock, policy=self.policy)
        del self.storage[key + '.withdrawn']
        changed = deepcopy(self.receipts)
        changed[0]['request']['application_commit'] = 'd' * 40
        self.nix.write_bytes(encoded(changed))
        with self.assertRaisesRegex(ValueError, 'selected source'):
            self.facts.cache(self.lock, policy=self.policy)

    def test_observed_bytes_and_production_mode_cannot_be_replaced_by_rehearsal(self):
        self.facts.public = lambda url, maximum: b'cached different bytes'
        with self.assertRaisesRegex(ValueError, 'public metadata'):
            self.repositories()
        with self.assertRaisesRegex(ValueError, 'trust mode'):
            self.facts.repositories(catalog=self.catalog, rehearsal=1)
