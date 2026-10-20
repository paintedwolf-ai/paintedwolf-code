"""Public native packages remain separate from the signed in-app updater set."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), ROOT / 'scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class NativePublicationBoundaryTests(unittest.TestCase):
    def test_public_native_linux_copies_all_packages_without_inventing_updater(self):
        module = load('release-collect-public-artifacts')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary); inputs = root / 'input'; inputs.mkdir()
            (inputs / 'opengrep').mkdir()
            stem = 'painted-wolf-code_v1.2.3_linux-x86_64'
            for extension in ('deb', 'rpm', 'pkg.tar.zst'):
                (inputs / (stem + '.' + extension)).write_bytes(b'actual selected fixture')
                (inputs / (stem + '.' + extension + '.sha256')).write_text('a' * 64)
            (inputs / 'opengrep' / (stem + '.json')).write_text('{}')
            catalog = root / 'catalog.json'
            catalog.write_text(json.dumps({'schema_version': 2, 'platforms': [{
                'updater_key': 'linux-x86_64', 'goos': 'linux', 'publication': 'public',
                'package_extensions': ['deb', 'rpm', 'pkg.tar.zst'], 'updater_extension': None}]}))
            output = root / 'output'
            with patch('sys.argv', ['collect', '--catalog', str(catalog), '--input', str(inputs),
                                    '--output', str(output), '--version', '1.2.3']):
                self.assertEqual(module.main(), 0)
            self.assertEqual(len(list(output.iterdir())), 7)
            self.assertFalse(any(path.name.endswith('.sig') for path in output.iterdir()))

    def updater(self, root, rows):
        module = load('release-assemble-updater-manifest')
        catalog, notes, output = root / 'catalog.json', root / 'notes', root / 'updater.json'
        fragments = root / 'fragments'; fragments.mkdir()
        catalog.write_text(json.dumps({'schema_version': 2, 'platforms': rows}))
        notes.write_text('Actual fixture release notes')
        (fragments / 'darwin.json').write_text(json.dumps({'platform': 'darwin-aarch64', 'signature': 'fixture',
            'updater_artifact': 'painted-wolf-code_v1.2.3_darwin-aarch64.app.tar.gz', 'update_keys': {}}))
        with patch('sys.argv', ['assemble', '--catalog', str(catalog), '--fragments', str(fragments),
                               '--version', '1.2.3', '--notes', str(notes), '--pub-date', '2026-10-20T12:00:00Z',
                               '--base-url', 'https://example.invalid', '--output', str(output)]), \
                patch.object(module, 'load_registry', return_value={}), \
                patch.object(module, 'release_binding', return_value={}), \
                patch.object(module, 'require_version'):
            code = module.main()
        return code, output

    def test_public_linux_needs_no_updater_fragment_but_non_linux_still_does(self):
        rows = [{'updater_key': 'linux-x86_64', 'goos': 'linux', 'publication': 'public', 'updater_extension': None},
                {'updater_key': 'darwin-aarch64', 'goos': 'darwin', 'publication': 'public', 'updater_extension': 'app.tar.gz'}]
        with tempfile.TemporaryDirectory() as temporary:
            code, output = self.updater(Path(temporary), rows)
            self.assertEqual(code, 0)
            self.assertEqual(set(json.loads(output.read_text())['platforms']), {'darwin-aarch64'})

    def test_linux_updater_and_non_linux_missing_updater_refuse(self):
        for key, goos, extension in [('linux-x86_64', 'linux', 'deb'), ('darwin-aarch64', 'darwin', None)]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as temporary:
                code, output = self.updater(Path(temporary), [{'updater_key': key, 'goos': goos,
                    'publication': 'public', 'updater_extension': extension}])
                self.assertEqual(code, 1)
                self.assertFalse(output.exists())
