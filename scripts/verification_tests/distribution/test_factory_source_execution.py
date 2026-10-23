"""Image factories retain independent host pins and the unchanged application graph."""

import hashlib
import json
from pathlib import Path
import tempfile
import subprocess
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

    def test_lock_preparation_stages_the_complete_factory_source_without_mutating_pins(self):
        root = Path(__file__).resolve().parents[3]
        source = root / 'packaging/nix/qualification-factories'
        for channel in factory.CHANNELS:
            with self.subTest(channel=channel), tempfile.TemporaryDirectory() as artifacts:
                descriptor = json.loads((source / channel / 'factory-inputs.json').read_text())
                lock = (source / channel / 'flake.lock').read_bytes()
                pins = {name: (source / channel / name).read_bytes()
                        for name in ('flake.lock', 'factory-inputs.json')}
                report = {'factory': descriptor.copy()}
                def committed(_root, _commit, path):
                    return (root / path).read_bytes()
                def lock_factory(command, **_kwargs):
                    candidate = Path(command[-1])
                    self.assertEqual((candidate.parent / 'factory.nix').read_bytes(),
                                     (source / 'factory.nix').read_bytes())
                    self.assertEqual((candidate / 'factory-inputs.json').read_bytes(),
                                     pins['factory-inputs.json'])
                    self.assertFalse((candidate / 'flake.lock').exists())
                    (candidate / 'flake.lock').write_bytes(lock)
                    return subprocess.CompletedProcess(command, 0)
                with patch.object(factory, 'committed', side_effect=committed), \
                        patch.object(nix, 'artifact_root', return_value=Path(artifacts)), \
                        patch.object(nix.subprocess, 'run', side_effect=lock_factory), \
                        patch.object(factory, 'validate_lock', return_value={
                            'lock_sha256': hashlib.sha256(lock).hexdigest()}) as validate:
                    self.assertEqual(nix.prepare_lock(root, root, 'c' * 40,
                        {'subdirectory': 'packaging/nix/qualification-factories/' + channel,
                         **descriptor}, {}, report), 0)
                validate.assert_called_once_with(root, {'subdirectory':
                    'packaging/nix/qualification-factories/' + channel, **descriptor}, lock)
                for name, value in pins.items():
                    self.assertEqual((source / channel / name).read_bytes(), value)
