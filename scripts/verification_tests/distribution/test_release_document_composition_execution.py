"""The production document composition uses its same constructor with isolated rehearsal trust."""
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from scripts.release_distribution_contract.release_lane import ReleaseDocument
from scripts.release_distribution_contract.document_schema import decode
from scripts.verification_tests.distribution.distribution_document_fixtures import ORIGIN, signing_identity


class ReleaseDocumentCompositionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.checkout = self.root / 'checkout'
        self.checkout.mkdir()
        self.registry, self.sign = signing_identity(self.root)
        data = {'VERSION': '1.2.3\n', 'flake.lock': '{}',
                'packaging/update-keys.json': json.dumps(self.registry),
                'packaging/linux/signing/keys.json': '{}',
                'packaging/linux/nix-cache-trust.json': json.dumps({'schema_version': 1, 'mode': 'rehearsal', 'public_keys': []}),
                'packaging/release-platforms.json': json.dumps({'schema_version': 2, 'platforms': [
                    {'updater_key': 'linux-x86_64', 'goos': 'linux', 'artifact_arch': 'x86_64',
                     'publication': 'public', 'package_extensions': ['deb'], 'updater_extension': None}]})}
        source_root = Path(__file__).resolve().parents[3]
        for name in ('packaging/distribution-legacy-platforms.json', 'packaging/distribution-legacy-releases.json'):
            data[name] = (source_root / name).read_text()
        for name, raw in data.items():
            path = self.checkout / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(raw)
        for args in (['init', '-q'], ['add', '--', *data],
                     ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', '-c', 'commit.gpgsign=false',
                      'commit', '-qm', 'fixture source']):
            subprocess.run(['git', '-C', str(self.checkout), *args], check=True, capture_output=True)
        self.source = subprocess.check_output(['git', '-C', str(self.checkout), 'rev-parse', 'HEAD'], text=True).strip()
        self.artifacts = self.root / 'artifacts'
        self.artifacts.mkdir()
        self.package = self.artifacts / 'painted-wolf-code_v1.2.3_linux-x86_64.deb'
        self.package.write_bytes(b'exact collected package bytes')
        self.checksum = self.artifacts / (self.package.name + '.sha256')
        self.checksum.write_text(hashlib.sha256(self.package.read_bytes()).hexdigest() + '\n')
        self.updater = self.root / 'updater.json'
        self.updater.write_text(json.dumps({'version': '1.2.3', 'notes': 'Fixture release',
                                           'pub_date': '2026-10-28T00:00:00Z', 'platforms': {}}))
        from scripts.release_distribution_contract.channels import select
        self.plan = self.root / 'channel-plan.json'
        selected = select('stable', '1.2.3', 1, self.source, read=lambda key: None)
        self.plan.write_text(json.dumps(selected))
        self.admissions = []

    def selected(self, signer=None):
        return ReleaseDocument(self.artifacts, self.updater, self.root / 'native.json', self.root / 'nix.json',
            self.source, 'c' * 40, self.plan, root=self.checkout, admission=self.admissions.append,
            read=lambda key: None, public=lambda *args: b'', rehearsal=True, origin=ORIGIN,
            repository_prefix='release-system-tests/fixture/linux', cache_prefix='release-system-tests/fixture/nix-cache',
            signer=signer or self.sign)

    def prepare(self, signer=None):
        # Delivery effect admission is exercised independently with the same real owner.
        nix = {'source': {'git_url': 'github:paintedwolf-ai/paintedwolf-code/' + self.source,
                          'commit': self.source, 'flake_lock_sha256': hashlib.sha256(b'{}').hexdigest()},
               'cache': {'enabled': False}}
        with patch('scripts.release_distribution_contract.release_facts.DeliveryFacts.repositories', return_value=[]) as native, \
             patch('scripts.release_distribution_contract.release_facts.DeliveryFacts.cache', return_value=nix):
            result = self.selected(signer).prepare(self.root / 'signed')
        self.assertTrue(all(call.kwargs['rehearsal'] is True for call in native.call_args_list))
        return result

    def test_same_composition_prepares_real_signature_with_fresh_source_admission(self):
        result = self.prepare()
        raw = (self.root / 'signed/distribution.v1.json').read_bytes()
        document = decode(raw)
        self.assertEqual(document['mode'], 'rehearsal')
        self.assertEqual(document['source']['commit'], self.source)
        self.assertEqual(result['distribution_sha256'], hashlib.sha256(raw).hexdigest())
        self.assertGreaterEqual(len(self.admissions), 2)
        self.assertEqual(set(self.admissions), {self.source})

    def test_changed_source_or_collected_checksum_refuses_before_completion(self):
        self.checksum.write_text('f' * 64 + '\n')
        with self.assertRaisesRegex(ValueError, 'collected final checksum'):
            self.prepare()
        self.assertFalse((self.root / 'signed').exists())
        self.checksum.write_text(hashlib.sha256(self.package.read_bytes()).hexdigest() + '\n')
        def change(path, number, registry):
            signed = self.sign(path, number, registry)
            (self.checkout / 'flake.lock').write_text('changed committed input')
            return signed
        with self.assertRaisesRegex(ValueError, 'committed tree'):
            self.prepare(change)
        self.assertFalse((self.root / 'signed/distribution-identity.json').exists())

    def test_production_has_no_rehearsal_origin_or_signer_override(self):
        with self.assertRaisesRegex(ValueError, 'maintained trust'):
            ReleaseDocument(self.artifacts, self.updater, self.root / 'native', self.root / 'nix',
                            self.source, 'c' * 40, self.plan, origin=ORIGIN, signer=self.sign)
