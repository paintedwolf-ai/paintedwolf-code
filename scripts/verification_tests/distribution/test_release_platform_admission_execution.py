"""Preflight permits a complete native Linux release without activating candidates."""
import copy
import json
from pathlib import Path
import unittest

from release_distribution_contract.platforms import package_urls, public_updaters, validate_release_catalog
from linux_release.candidates import matrix
from scripts.linux_release.release_repositories import public_linux


ROOT = Path(__file__).resolve().parents[3]


class ReleasePlatformAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.repository_catalog = json.loads((ROOT / 'packaging/release-platforms.json').read_bytes())
        self.catalog = copy.deepcopy(self.repository_catalog)
        for row in self.linux():
            row['publication'] = 'candidate'

    def validate(self):
        return validate_release_catalog(json.dumps(self.catalog).encode())

    def linux(self):
        return [row for row in self.catalog['platforms'] if row['goos'] == 'linux']

    def test_repository_catalog_has_supported_adapters_and_mac_os_updater(self):
        platforms = validate_release_catalog(json.dumps(self.repository_catalog).encode())
        self.assertEqual(set(public_updaters(platforms)), {'darwin-aarch64'})
        self.assertEqual({key for key, row in platforms.items() if row['publication'] == 'public'},
                         {'darwin-aarch64', 'linux-x86_64', 'linux-aarch64'})

    def test_current_catalog_drives_public_downloads_and_independent_candidate_builds(self):
        catalog = ROOT / 'packaging/release-platforms.json'
        urls = package_urls('1.2.3', catalog)
        expected = {'darwin-aarch64': ['dmg'], 'linux-x86_64': ['deb', 'rpm', 'pkg.tar.zst'],
                    'linux-aarch64': ['deb', 'rpm', 'pkg.tar.zst']}
        self.assertEqual(urls, sorted('https://downloads.paintedwolf.dev/releases/v1.2.3/'
                         f'painted-wolf-code_v1.2.3_{platform}.{extension}'
                         for platform, extensions in expected.items() for extension in extensions))
        native = {row['updater_key'] for row in public_linux(catalog)}
        self.assertEqual(native, {'linux-x86_64', 'linux-aarch64'})
        self.assertEqual({row['updater_key'] for row in matrix(self.repository_catalog)['include']}, native)

    def test_candidate_state_does_not_require_linux_publication(self):
        platforms = self.validate()
        self.assertEqual({key for key, row in platforms.items() if row['publication'] == 'public'},
                         {'darwin-aarch64'})
        self.assertEqual(set(public_updaters(platforms)), {'darwin-aarch64'})

    def test_both_linux_architectures_publish_all_packages_without_updater(self):
        for row in self.linux():
            row['publication'] = 'public'
        platforms = self.validate()
        self.assertEqual({key for key, row in platforms.items() if row['publication'] == 'public'},
                         {'darwin-aarch64', 'linux-x86_64', 'linux-aarch64'})
        self.assertEqual(set(public_updaters(platforms)), {'darwin-aarch64'})
        for key in ('linux-x86_64', 'linux-aarch64'):
            self.assertEqual(platforms[key]['package_extensions'], ['deb', 'rpm', 'pkg.tar.zst'])
            self.assertIsNone(platforms[key]['updater_extension'])

    def test_partial_architecture_package_and_updater_changes_refuse(self):
        original = copy.deepcopy(self.catalog)
        for change in ('one-public', 'missing-architecture', 'missing-package', 'linux-updater',
                       'windows-public', 'macos-candidate', 'unknown-field', 'missing-runner',
                       'swapped-linux-cpu', 'wrong-artifact-cpu', 'wrong-os', 'extra-candidate'):
            with self.subTest(change=change):
                self.catalog = copy.deepcopy(original)
                if change == 'one-public':
                    self.linux()[0]['publication'] = 'public'
                elif change == 'missing-architecture':
                    self.catalog['platforms'].remove(self.linux()[0])
                elif change == 'missing-package':
                    self.linux()[0]['package_extensions'].pop()
                elif change == 'linux-updater':
                    self.linux()[0]['updater_extension'] = 'unsupported-updater'
                elif change == 'windows-public':
                    self.catalog['platforms'][-1]['publication'] = 'public'
                elif change == 'macos-candidate':
                    self.catalog['platforms'][0]['publication'] = 'candidate'
                elif change == 'swapped-linux-cpu':
                    for row in self.linux():
                        row['goarch'] = 'amd64' if row['goarch'] == 'arm64' else 'arm64'
                        row['artifact_arch'] = 'x86_64' if row['artifact_arch'] == 'aarch64' else 'aarch64'
                elif change == 'wrong-artifact-cpu':
                    self.linux()[0]['artifact_arch'] = 'aarch64'
                elif change == 'wrong-os':
                    self.linux()[0]['goos'] = 'darwin'
                elif change == 'extra-candidate':
                    self.catalog['platforms'].append({**self.catalog['platforms'][0],
                        'updater_key': 'darwin-x86_64', 'publication': 'candidate'})
                elif change == 'unknown-field':
                    self.linux()[0]['allow_partial'] = True
                else:
                    del self.linux()[0]['runner']
                with self.assertRaises(ValueError):
                    self.validate()
