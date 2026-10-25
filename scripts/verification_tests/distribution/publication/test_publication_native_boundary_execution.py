"""Public native packages remain separate from the signed in-app updater set."""
from contextlib import redirect_stderr
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[4]


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

    def updater(self, root, rows, *, signed_version='1.2.3', binding=None,
                fragment_platform='darwin-aarch64', rejection=None):
        module = load('release-assemble-updater-manifest')
        from updater_signature import rehearsal_signature
        catalog, notes, output = root / 'catalog.json', root / 'notes', root / 'updater.json'
        fragments = root / 'fragments'; fragments.mkdir()
        catalog.write_text(json.dumps({'schema_version': 2, 'platforms': rows}))
        notes.write_text('Actual fixture release notes')
        artifact = f'painted-wolf-code_v1.2.3_{fragment_platform}.app.tar.gz'
        keys = module.release_binding(module.load_registry(), '1.2.3') if binding is None else binding
        (fragments / 'darwin.json').write_text(json.dumps({'platform': fragment_platform,
            'signature': rehearsal_signature(signed_version, artifact), 'updater_artifact': artifact, 'update_keys': keys}))
        with patch('sys.argv', ['assemble', '--catalog', str(catalog), '--fragments', str(fragments),
                               '--version', '1.2.3', '--notes', str(notes), '--pub-date', '2026-10-20T12:00:00Z',
                               '--base-url', 'https://example.invalid', '--output', str(output)]), \
                redirect_stderr(io.StringIO()) as errors:
            try:
                code = module.main()
            except SystemExit as exit:
                code = exit.code
        if rejection is not None:
            self.assertEqual(errors.getvalue(), 'error: ' + rejection + '\n')
        return code, output

    def test_fragment_signed_for_another_version_or_key_generation_refuses(self):
        rows = [{'updater_key': 'darwin-aarch64', 'goos': 'darwin', 'publication': 'public',
                 'package_extensions': ['dmg'], 'updater_extension': 'app.tar.gz'}]
        for keywords in (
                {'signed_version': '1.2.4',
                 'rejection': 'darwin.json signature is signed for version 1.2.4, not 1.2.3'},
                {'binding': {'signing_generation': 9},
                 'rejection': 'darwin.json signing and embedded key generations disagree'}):
            with self.subTest(keywords=keywords), tempfile.TemporaryDirectory() as temporary:
                code, output = self.updater(Path(temporary), rows, **keywords)
                self.assertNotEqual(code, 0)
                self.assertFalse(output.exists())

    def test_public_linux_needs_no_updater_fragment_but_non_linux_still_does(self):
        rows = [{'updater_key': 'linux-x86_64', 'goos': 'linux', 'publication': 'public',
                 'package_extensions': ['deb', 'rpm', 'pkg.tar.zst'], 'updater_extension': None},
                {'updater_key': 'darwin-aarch64', 'goos': 'darwin', 'publication': 'public',
                 'package_extensions': ['dmg'], 'updater_extension': 'app.tar.gz'}]
        with tempfile.TemporaryDirectory() as temporary:
            code, output = self.updater(Path(temporary), rows)
            self.assertEqual(code, 0)
            self.assertEqual(set(json.loads(output.read_text())['platforms']), {'darwin-aarch64'})

    def test_package_managed_platform_refuses_an_updater_fragment_regardless_of_goos(self):
        for key, goos, packages in [('linux-x86_64', 'linux', ['deb']), ('darwin-aarch64', 'darwin', ['dmg'])]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as temporary:
                code, output = self.updater(Path(temporary), [{'updater_key': key, 'goos': goos,
                    'publication': 'public', 'package_extensions': packages, 'updater_extension': None}],
                    fragment_platform=key,
                    rejection=f'darwin.json offers an updater artifact for package-managed platform {key}')
                self.assertEqual(code, 1)
                self.assertFalse(output.exists())
