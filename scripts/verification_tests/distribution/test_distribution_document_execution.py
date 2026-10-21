"""Distribution facts bind freshly admitted bytes and keep source-only Nix explicit."""
from copy import deepcopy
import base64
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.release_distribution_contract.document import construct
from scripts.release_distribution_contract import document_signing
from scripts.release_distribution_contract.document_schema import canonical, decode, validate


def feed_public_key(number):
    key = b'Ed' + number.to_bytes(8, 'little') + bytes([number]) * 32
    return base64.b64encode(b'untrusted comment: test\n' + base64.b64encode(key) + b'\n').decode()


def feed_signature(number, file, version):
    body = b'ED' + number.to_bytes(8, 'little') + bytes(64)
    document = ('untrusted comment: test\n' + base64.b64encode(body).decode()
                + f'\ntrusted comment: timestamp:1800000000\tfile:{file}\tversion:{version}\nunused\n')
    return base64.b64encode(document.encode()).decode()


class DocumentFixture(unittest.TestCase):
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


class DistributionDocumentTests(DocumentFixture):

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

    def test_aur_origins_are_fixed_and_channel_bound(self):
        stable = self.construct()
        preview = json.loads(json.dumps(stable).replace('1.2.3', '1.2.4-rc.1'))
        preview['channel'] = 'preview'
        for document, names in [(stable, ['painted-wolf-code', 'painted-wolf-code-bin']),
                                 (preview, ['painted-wolf-code-preview-bin'])]:
            for name in names:
                document['aur'] = {'enabled': True, 'git_url': 'https://aur.archlinux.org/' + name + '.git',
                                   'commit': 'e' * 40, 'source_sha256': 'f' * 64, 'package_sha256': 'a' * 64}
                self.assertIs(validate(document), document)
            for url in ['https://foreign.invalid/package.git',
                        'https://aur.archlinux.org/' + ('painted-wolf-code-preview-bin' if document is stable else 'painted-wolf-code-bin') + '.git']:
                document['aur']['git_url'] = url
                with self.assertRaises(ValueError):
                    validate(document)


class DistributionSigningTests(DocumentFixture):
    """Real signature-document and registry checks; a minisign shim stands in for the absent tool."""

    def setUp(self):
        super().setUp()
        self.registry = {'generations': [{'generation': 1, 'feed_public_key': feed_public_key(1)},
                                         {'generation': 2, 'feed_public_key': feed_public_key(2)}]}
        tools = self.root / 'tools'
        tools.mkdir()
        self.verification = tools / 'status'
        self.verification.write_text('0')
        minisign = tools / 'minisign'
        minisign.write_text(f"#!/bin/sh\necho \"$*\" >> '{tools}/calls'\nexit $(cat '{self.verification}')\n")
        minisign.chmod(0o755)
        self.calls = tools / 'calls'
        context = patch.dict(os.environ, {'PATH': f"{tools}{os.pathsep}{os.environ['PATH']}"})
        context.start()
        self.addCleanup(context.stop)

    def prepare(self, name, signer=None, authorize=lambda binding: True):
        def signed(path, number, registry):
            return feed_signature(number, path.name, '1.2.3')
        return document_signing.prepare(self.root / name, self.request, self.catalog, self.updater, self.artifacts,
                                        authorize=authorize, registry=self.registry, signer=signer or signed)

    def test_signing_repeats_fresh_fact_admission_and_retains_nonpublishing_identity(self):
        observed = []
        result = self.prepare('signed', authorize=lambda binding: observed.append(binding) or True)
        self.assertEqual(len(observed), 2)
        raw = (self.root / 'signed/distribution.v1.json').read_bytes()
        self.assertEqual(result['distribution_sha256'], hashlib.sha256(raw).hexdigest())
        self.assertEqual(result['publication'], 'nonrelease')
        self.assertEqual((self.root / 'signed/distribution.v1.json.sig').read_text(),
                         feed_signature(1, 'distribution.v1.json', '1.2.3'))
        self.assertEqual(len(self.calls.read_text().splitlines()), 1)
        self.assertIn('distribution.v1.json', self.calls.read_text())

    def test_signature_bound_to_another_file_version_or_key_is_refused(self):
        for index, document in enumerate([feed_signature(1, 'other.json', '1.2.3'), feed_signature(1, 'distribution.v1.json', '1.2.4'),
                                          feed_signature(2, 'distribution.v1.json', '1.2.3'), 'not a signature']):
            with self.subTest(document=document), self.assertRaises(ValueError):
                self.prepare(f'bound-{index}', lambda path, number, registry, document=document: document)
            self.assertFalse((self.root / f'bound-{index}/distribution-identity.json').exists())
            self.assertFalse((self.root / f'bound-{index}/distribution.v1.json.sig').exists())
        self.assertFalse(self.calls.exists())

    def test_cryptographic_verification_failure_refuses_identity(self):
        self.verification.write_text('1')
        with self.assertRaisesRegex(ValueError, 'feed signature verification failed'):
            self.prepare('invalid')
        self.assertFalse((self.root / 'invalid/distribution-identity.json').exists())

    def test_package_change_after_signing_refuses_identity(self):
        def mutate(path, number, registry):
            next(self.artifacts.iterdir()).write_bytes(b'changed after signing')
            return feed_signature(number, path.name, '1.2.3')
        with self.assertRaisesRegex(ValueError, 'changed while signing'):
            self.prepare('mutated', mutate)
        self.assertFalse((self.root / 'mutated/distribution-identity.json').exists())

    def test_unregistered_generation_and_reused_destination_are_refused(self):
        self.request['signing_generation'] = 3
        with self.assertRaisesRegex(ValueError, 'unknown signing key generation'):
            self.prepare('unknown')
        self.assertFalse((self.root / 'unknown').exists())
        self.request['signing_generation'] = 1
        (self.root / 'existing').mkdir()
        with self.assertRaisesRegex(ValueError, 'fresh absolute directory'):
            self.prepare('existing')
        with self.assertRaisesRegex(ValueError, 'fresh absolute directory'):
            document_signing.prepare(Path('relative'), self.request, self.catalog, self.updater, self.artifacts,
                                     authorize=lambda binding: True, registry=self.registry, signer=lambda *args: '')

    def test_production_cannot_replace_the_maintained_signing_registry(self):
        self.request['mode'] = 'production'
        self.request['origin'] = 'https://downloads.paintedwolf.dev'
        with self.assertRaises(ValueError):
            self.prepare('foreign-registry')
