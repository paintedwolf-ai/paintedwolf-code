"""Image factories retain independent host pins and the unchanged application graph."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import factory_source as factory
from scripts.linux_release import nix


class FactorySourceExecutionTests(unittest.TestCase):
    def fixture(self):
        application = {'root': 'root', 'nodes': {'root': {'inputs': {'nixpkgs': 'p', 'tool': 't'}},
            'p': {'locked': {'rev': 'a' * 40}},
            't': {'locked': {'rev': 'b' * 40}, 'inputs': {'nixpkgs': ['nixpkgs']}}}}
        lock = {'root': 'root', 'nodes': {'root': {'inputs': {'application': 'app', 'nixpkgs': 'host'}},
            'app': {'locked': {'type': 'git', 'url': 'https://github.com/paintedwolf-ai/paintedwolf-code',
                               'rev': 'c' * 40}, 'inputs': {'nixpkgs': 'app-p', 'tool': 'app-t'}},
            'app-p': application['nodes']['p'],
            'app-t': {'locked': {'rev': 'b' * 40}, 'inputs': {'nixpkgs': ['application', 'nixpkgs']}},
            'host': {'original': {'owner': 'NixOS', 'repo': 'nixpkgs', 'ref': 'nixos-26.05', 'type': 'github'},
                     'locked': {'rev': 'd' * 40}}}}
        return application, lock, {'application_commit': 'c' * 40, 'channel': 'nixos-26.05'}

    def test_host_pin_is_independent_and_application_follows_are_preserved(self):
        app, lock, descriptor = self.fixture()
        with patch.object(factory, 'committed', return_value=json.dumps(app).encode()):
            result = factory.validate_lock(Path('/source'), descriptor, json.dumps(lock).encode())
            self.assertEqual(result['nixpkgs_revision'], 'd' * 40)
            lock['nodes']['app']['inputs']['nixpkgs'] = 'host'
            with self.assertRaisesRegex(ValueError, 'dependency graph'):
                factory.validate_lock(Path('/source'), descriptor, json.dumps(lock).encode())

    def test_wrong_source_channel_and_unlocked_host_are_refused(self):
        for field, value in [('rev', 'e' * 40), ('url', 'https://example.invalid/repo')]:
            app, lock, descriptor = self.fixture()
            lock['nodes']['app']['locked'][field] = value
            with self.assertRaises(ValueError):
                factory.validate_lock(Path('/source'), descriptor, json.dumps(lock).encode())
        app, lock, descriptor = self.fixture()
        lock['nodes']['host']['original']['ref'] = 'nixos-unstable'
        with self.assertRaises(ValueError):
            factory.validate_lock(Path('/source'), descriptor, json.dumps(lock).encode())

    def test_role_cannot_change_normal_application_or_use_mutable_selector(self):
        with tempfile.TemporaryDirectory() as root:
            for component, commit in [('default', 'c' * 40), ('nixos-gnome-vm', 'HEAD')]:
                with self.assertRaises(ValueError):
                    factory.select(Path(root), commit, 'nixos-26.05', component)
            with self.assertRaises(ValueError):
                nix.execute('build', Path(root), factory_commit='c' * 40)
            with self.assertRaises(ValueError):
                nix.execute('check', Path(root), prepare_factory_lock=True)

    def test_factory_uri_is_immutable_and_build_remains_sandboxed(self):
        source = 'git+file:///factory?rev=' + 'c' * 40 + '&dir=packaging/nix/qualification-factories/nixos-26.05'
        with patch.object(nix.platform, 'system', return_value='Linux'), \
                patch.object(nix.platform, 'machine', return_value='aarch64'):
            command = nix.command('build', Path('/controller'), 'nixos-plasma-vm', source)
        self.assertEqual(command[-1], source + '#packages.aarch64-linux.nixos-plasma-vm')
        self.assertIn('sandbox', command)
        self.assertIn('--no-write-lock-file', command)
