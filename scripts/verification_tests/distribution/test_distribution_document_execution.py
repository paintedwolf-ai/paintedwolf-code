"""Distribution facts bind freshly admitted bytes and keep source-only Nix explicit."""
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.release_distribution_contract.document import construct
from scripts.release_distribution_contract import document_signing
from scripts.release_distribution_contract.document_schema import canonical, decode, validate


class DistributionDocumentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.catalog = self.root / 'catalog.json'
        self.updater = self.root / 'updater.json'
        self.artifacts = self.root / 'packages'
        self.artifacts.mkdir()
        row = {'updater_key': 'linux-x86_64', 'goos': 'linux', 'artifact_arch': 'x86_64',
               'publication': 'public', 'package_extensions': ['deb', 'rpm', 'pkg.tar.zst'], 'updater_extension': None}
        self.catalog.write_text(json.dumps({'schema_version': 2, 'platforms': [row]}))
        self.updater.write_text(json.dumps({'version': '1.2.3', 'notes': 'Linux release',
                                           'pub_date': '2026-10-20T00:00:00Z', 'platforms': {}}))
        self.request = {'mode': 'rehearsal', 'version': '1.2.3', 'origin': 'https://test.invalid',
            'source_commit': 'a' * 40, 'source_tree': 'b' * 40, 'controller_commit': 'c' * 40,
            'signing_generation': 1, 'package_content_types': {}, 'repositories': [],
            'nix': {'source': {'git_url': 'github:paintedwolf-ai/paintedwolf-code/' + 'a' * 40,
                              'commit': 'a' * 40, 'flake_lock_sha256': 'd' * 64}, 'cache': {'enabled': False}},
            'aur': {'enabled': False}}
        for extension in row['package_extensions']:
            name = 'painted-wolf-code_v1.2.3_linux-x86_64.' + extension
            (self.artifacts / name).write_bytes(('actual signed ' + extension).encode())
            self.request['package_content_types'][name] = 'application/octet-stream'

    def construct(self, authorize=lambda binding: True):
        return construct(self.request, self.catalog, self.updater, self.artifacts, authorize=authorize)

    def test_constructor_retains_actual_final_bytes_and_separate_updater_identity(self):
        observed = []
        result = self.construct(lambda binding: observed.append(binding) or True)
        self.assertEqual(len(observed), 1)
        self.assertEqual(result['updater_manifest_sha256'], hashlib.sha256(self.updater.read_bytes()).hexdigest())
        self.assertEqual(result['source']['catalog_sha256'], hashlib.sha256(self.catalog.read_bytes()).hexdigest())
        for package in result['platforms']['linux-x86_64']['packages'].values():
            self.assertEqual(package['sha256'], hashlib.sha256((self.artifacts / package['file']).read_bytes()).hexdigest())
        self.assertEqual(result['nix']['cache'], {'enabled': False})
        self.assertEqual(decode(canonical(result)), result)

    def test_fresh_admission_missing_artifacts_and_foreign_source_refuse(self):
        with self.assertRaises(ValueError):
            self.construct(lambda binding: False)
        with self.assertRaises(ValueError):
            self.construct(None)
        path = next(self.artifacts.iterdir())
        raw = path.read_bytes()
        path.unlink()
        with self.assertRaises(FileNotFoundError):
            self.construct()
        outside = self.root / 'outside'
        outside.write_bytes(raw)
        path.symlink_to(outside)
        with self.assertRaises(OSError):
            self.construct()

    def test_disabled_cache_cannot_smuggle_urls_keys_or_other_source_identity(self):
        original = self.construct()
        for field, value in [('base_url', 'https://test.invalid/cache'), ('public_keys', ['untrusted'])]:
            document = deepcopy(original)
            document['nix']['cache'][field] = value
            with self.assertRaises(ValueError):
                validate(document)
        document = deepcopy(original)
        document['nix']['source']['commit'] = 'f' * 40
        with self.assertRaises(ValueError):
            validate(document)
        with self.assertRaises(ValueError):
            decode(canonical(original).replace(b'"schema_version":1', b'"schema_version":1,"schema_version":1'))
        with self.assertRaises(ValueError):
            decode(json.dumps(original, indent=2).encode())

    def test_production_origin_and_package_url_names_are_fixed(self):
        document = self.construct()
        document['mode'] = 'production'
        with self.assertRaises(ValueError):
            validate(document)
        document = self.construct()
        document['platforms']['linux-x86_64']['packages']['deb']['url'] += '?redirect=1'
        with self.assertRaises(ValueError):
            validate(document)

    def prepare(self, name, signer=None):
        return document_signing.prepare(self.root / name, self.request, self.catalog, self.updater, self.artifacts,
            authorize=lambda binding: True, registry={}, signer=signer or (lambda path, number, registry: 'fixture signature'))

    def test_signing_repeats_fresh_fact_admission_and_retains_nonpublishing_identity(self):
        observed = []
        with patch.object(document_signing, 'generation', return_value={'feed_public_key': 'maintained public key'}), \
                patch.object(document_signing, 'check') as metadata, patch.object(document_signing, 'verify') as crypto:
            result = document_signing.prepare(self.root / 'signed', self.request, self.catalog, self.updater, self.artifacts,
                authorize=lambda binding: observed.append(binding) or True, registry={},
                signer=lambda path, number, registry: 'fixture signature')
        self.assertEqual(len(observed), 2)
        raw = (self.root / 'signed/distribution.v1.json').read_bytes()
        self.assertEqual(result['distribution_sha256'], hashlib.sha256(raw).hexdigest())
        self.assertEqual(result['publication'], 'nonrelease')
        metadata.assert_called_once()
        crypto.assert_called_once_with(self.root / 'signed/distribution.v1.json', 'fixture signature', 'maintained public key')
        self.assertEqual((self.root / 'signed/distribution.v1.json.sig').read_text(), 'fixture signature')

    def test_after_signing_package_changes_and_crypto_failure_refuse_identity(self):
        def mutate(path, number, registry):
            next(self.artifacts.iterdir()).write_bytes(b'changed after signing')
            return 'fixture signature'
        with patch.object(document_signing, 'generation', return_value={'feed_public_key': 'fixture'}), \
                patch.object(document_signing, 'check'), patch.object(document_signing, 'verify'):
            with self.assertRaises(ValueError):
                self.prepare('mutated', mutate)
        self.assertFalse((self.root / 'mutated/distribution-identity.json').exists())
        with patch.object(document_signing, 'generation', return_value={'feed_public_key': 'fixture'}), \
                patch.object(document_signing, 'check'), patch.object(document_signing, 'verify', side_effect=ValueError('bad signature')):
            with self.assertRaises(ValueError):
                self.prepare('invalid')
        self.assertFalse((self.root / 'invalid/distribution-identity.json').exists())

    def test_production_cannot_replace_the_maintained_signing_registry(self):
        self.request['mode'] = 'production'
        self.request['origin'] = 'https://downloads.paintedwolf.dev'
        with self.assertRaises(ValueError):
            self.prepare('foreign-registry')
