"""Protected native result retention never copies Linux or invents signature proof."""
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release.publication.release import release_producer as owner
from verification_tests.distribution.publication import signed_release as fixture


class ReleaseProducerTests(unittest.TestCase):
    def fixture(self, root):
        files = root / 'files'; files.mkdir()
        catalog = json.dumps({'schema_version': 2, 'platforms': [
            {'updater_key': 'darwin-aarch64', 'goos': 'darwin', 'goarch': 'arm64', 'artifact_arch': 'aarch64',
             'runner': 'macos-15', 'publication': 'public', 'package_extensions': ['dmg'], 'updater_extension': 'app.tar.gz'},
            {'updater_key': 'linux-x86_64', 'goos': 'linux', 'goarch': 'amd64', 'artifact_arch': 'x86_64',
             'runner': 'ubuntu-24.04', 'publication': 'public', 'package_extensions': ['deb'], 'updater_extension': None}]})
        source = root / 'source'
        commit, tree = fixture.source_repository(source, {'packaging/release-platforms.json': catalog})
        stem = 'painted-wolf-code_v1.2.3_darwin-aarch64'
        package = files / (stem + '.dmg'); package.write_bytes(b'actual fixture')
        (files / (stem + '.dmg.sha256')).write_text(hashlib.sha256(package.read_bytes()).hexdigest() + '\n')
        (files / (stem + '.app.tar.gz')).write_bytes(b'updater fixture')
        (files / (stem + '.app.tar.gz.sig')).write_text('signature fixture\n')
        (files / 'painted-wolf-code_v1.2.3_linux-x86_64.deb').write_bytes(b'Linux must not enter independent scope')
        updater = root / 'updater.json'
        updater.write_text(json.dumps({'version': '1.2.3', 'notes': 'Notes', 'pub_date': '2026-10-20T00:00:00Z',
            'platforms': {'darwin-aarch64': {'signature': 'signature fixture', 'url': 'https://example.invalid/' + stem + '.app.tar.gz'}}}))
        producer = {'repository_id': 1, 'run_id': 2, 'run_attempt': 1, 'controller_commit': 'a' * 40}
        return source, commit, tree, files, source / 'packaging/release-platforms.json', updater, producer

    def execute(self, root, *, changed=False, wrong_catalog=False):
        source, commit, tree, files, catalog, updater, producer = self.fixture(root)
        if wrong_catalog:
            catalog.write_bytes(catalog.read_bytes() + b'\n')
        original = shutil.copyfile
        def copy(incoming, destination):
            result = original(incoming, destination)
            if changed:
                Path(incoming).write_bytes(b'changed during retention')
            return result
        output = root / 'retained'
        # Only the copy is instrumented, to change a signed result while it is being retained.
        with patch.object(owner.shutil, 'copyfile', side_effect=copy):
            report = owner.produce(source, commit, catalog, updater, files, output, producer)
        self.assertEqual((report['source']['source_commit'], report['source']['source_tree']), (commit, tree))
        return report, output

    def test_only_exact_non_linux_packages_checksums_and_signed_updater_are_retained(self):
        with tempfile.TemporaryDirectory() as temporary:
            report, output = self.execute(Path(temporary))
            self.assertFalse(report['published'])
            self.assertEqual(report['retained_files'], 4)
            self.assertFalse(any('linux' in path.name for path in (output / 'artifacts').iterdir()))
            self.assertEqual(json.loads((output / 'producer.json').read_text())['run_attempt'], 1)

    def test_mutated_payload_cannot_receive_protected_retention(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaisesRegex(ValueError, 'changed during'):
                self.execute(root, changed=True)
            self.assertFalse((root / 'retained').exists())

    def test_modified_worktree_catalog_cannot_replace_selected_git_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaisesRegex(ValueError, 'immutable application source'):
                self.execute(root, wrong_catalog=True)
            self.assertFalse((root / 'retained').exists())
