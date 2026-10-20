"""Signed document preparation keeps source, package identity and delivery authority separate."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from scripts.linux_release.publication import document_pipeline as owner


class DocumentPipelineTests(unittest.TestCase):
    def fixture(self, root, *, public=True):
        selected = Mock(root=root / 'linux')
        released = Mock(root=root / 'release')
        (selected.root / 'packages').mkdir(parents=True)
        released.root.mkdir()
        raw = b'actual signed installed package'
        package = selected.root / 'packages/app.deb'; package.write_bytes(raw)
        source = {'source_commit': 'a' * 40, 'source_tree': 'b' * 40, 'version': '1.2.3', 'flake_lock_sha256': 'c' * 64}
        final = {'format_version': 1, 'source_commit': source['source_commit'], 'artifacts': [
            {'id': 'app.deb', 'source_commit': source['source_commit'], 'installation': 'deb', 'architecture': 'amd64',
             'identity': {'kind': 'file', 'sha256': hashlib.sha256(raw).hexdigest()}}]}
        selected.source = source
        selected.authorize.return_value = ({'artifacts': final}, {'accepted': True})
        (released.root / 'catalog.json').write_text(json.dumps({'schema_version': 2, 'platforms': [{
            'updater_key': 'linux-x86_64', 'publication': 'public' if public else 'candidate',
            'package_extensions': ['deb'], 'updater_extension': None}]}))
        if not public:
            catalog = json.loads((released.root / 'catalog.json').read_text())
            catalog['platforms'].append({'updater_key': 'darwin-aarch64', 'publication': 'public',
                'package_extensions': ['dmg'], 'updater_extension': 'app.tar.gz'})
            (released.root / 'catalog.json').write_text(json.dumps(catalog))
            (released.root / 'artifacts').mkdir()
            (released.root / 'artifacts/painted-wolf-code_v1.2.3_darwin-aarch64.dmg').write_bytes(b'independent fixture package')
        return selected, released, source

    def test_canonical_name_keeps_exact_installed_bytes_and_content_type(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selected, released, source = self.fixture(root)
            types, _ = owner.package_files(selected, released, source, root / 'public')
            name = 'painted-wolf-code_v1.2.3_linux-x86_64.deb'
            self.assertEqual(types, {name: 'application/vnd.debian.binary-package'})
            self.assertEqual((root / 'public' / name).read_bytes(), (selected.root / 'packages/app.deb').read_bytes())
            self.assertEqual(selected.authorize.call_count, 2)

    def test_candidate_linux_cannot_be_promoted_by_an_installed_campaign(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selected, released, source = self.fixture(root, public=False)
            types, _ = owner.package_files(selected, released, source, root / 'public')
            self.assertEqual(set(types), {'painted-wolf-code_v1.2.3_darwin-aarch64.dmg'})
            self.assertFalse(any('linux' in path.name for path in (root / 'public').iterdir()))

    def test_installed_package_mutation_refuses_before_document_construction(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selected, released, source = self.fixture(root)
            (selected.root / 'packages/app.deb').write_bytes(b'changed after installation')
            with self.assertRaisesRegex(ValueError, 'installed artifact'):
                owner.package_files(selected, released, source, root / 'public')

    def test_package_and_feed_signing_generations_remain_independent(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = {'source_commit': 'a' * 40, 'source_tree': 'b' * 40, 'version': '1.2.3', 'flake_lock_sha256': 'c' * 64}
            checkout = root / 'source'; checkout.mkdir()
            released = Mock(root=root / 'selected-release')
            selected = Mock()
            producer = {'repository_id': 1, 'controller_commit': 'd' * 40}
            with patch.object(owner, 'facts', return_value=source), \
                    patch.object(owner, 'retain_campaign', return_value=selected) as retain, \
                    patch.object(owner, 'retain_release', return_value=released), \
                    patch.object(owner, 'package_files', return_value=({}, {'linux-x86_64': {'publication': 'public'}})), \
                    patch.object(owner, 'prepare', side_effect=lambda *args, **kwargs: (args[1].mkdir(), {'publication': 'nonrelease'})[1]) as prepare:
                owner.produce(None, 'owner/repo', producer, checkout, source['source_commit'], 2, 3, 4, root / 'output',
                              catalog={}, signing_generation=3, package_signing_generation=7, key_fingerprint='e' * 40,
                              repository_base_url='https://example.invalid/linux/repositories')
            self.assertEqual(retain.call_args.kwargs['signing_generation'], 7)
            request = prepare.call_args.args[2]
            self.assertEqual(request['signing_generation'], 3)
            self.assertEqual(request['repositories'], [])
            self.assertEqual(request['nix']['cache'], {'enabled': False})
            self.assertEqual(request['aur'], {'enabled': False})
            self.assertFalse(json.loads((root / 'output/preparation.json').read_text())['published'])

    def test_native_collection_keeps_bare_digest_sidecars_and_no_updater(self):
        from scripts.linux_release.publication.native_artifacts import collect
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selected, released, source = self.fixture(root)
            row = {'publication': 'public', 'package_extensions': ['deb'], 'updater_extension': None}
            result = collect(selected, {'linux-x86_64': row}, root / 'collected')
            name = result[0]['file']
            self.assertEqual((root / 'collected' / (name + '.sha256')).read_text(), result[0]['sha256'] + '\n')
            self.assertEqual(len(list((root / 'collected').iterdir())), 2)
            row['updater_extension'] = 'tar.gz'
            with self.assertRaisesRegex(ValueError, 'in-app updater'):
                collect(selected, {'linux-x86_64': row}, root / 'refused')
            self.assertFalse((root / 'refused').exists())

    def test_final_fresh_admission_failure_removes_only_owned_collection(self):
        from scripts.linux_release.publication.native_artifacts import collect
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            selected, released, source = self.fixture(root)
            admitted = selected.authorize.return_value
            selected.authorize.side_effect = [admitted, ValueError('campaign changed')]
            with self.assertRaisesRegex(ValueError, 'campaign changed'):
                collect(selected, {'linux-x86_64': {'publication': 'public', 'package_extensions': ['deb'],
                                                 'updater_extension': None}}, root / 'collected')
            self.assertFalse((root / 'collected').exists())
            self.assertTrue((selected.root / 'packages/app.deb').is_file())
