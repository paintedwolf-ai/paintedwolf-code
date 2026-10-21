"""The settings observer is required ordinary package payload with final-byte sealing."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), ROOT / 'scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class DesktopSettingsPayloadTests(unittest.TestCase):
    def test_required_observer_is_hashed_as_ordinary_native_executable(self):
        manifest = load('linux-package-manifest')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = 'x86_64-unknown-linux-gnu'
            tauri = root / 'lycaon-den/src-tauri'
            paths = [tauri / 'target' / target / 'release/painted-wolf-code']
            paths += [tauri / 'binaries' / (helper + '-' + target) for helper in manifest.HELPERS]
            paths += [root / 'packaging/linux/bin' / helper for helper in manifest.ENTRY_POINTS]
            for path in paths:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'final fixture executable ' + path.name.encode())
                path.chmod(0o755)
            args = argparse.Namespace(root=root, target=target, version='1.2.3', output=root / 'manifest.json')
            manifest.generate(args)
            row = next(row for row in json.loads(args.output.read_text())['executables']
                       if row['path'] == '/opt/painted-wolf-code/bin/pw-desktop-settings')
            observer = tauri / 'binaries' / ('pw-desktop-settings-' + target)
            self.assertEqual(set(row), {'path', 'sha256', 'size'})
            self.assertEqual(row['sha256'], hashlib.sha256(observer.read_bytes()).hexdigest())
            observer.unlink()
            with self.assertRaises(FileNotFoundError):
                manifest.generate(args)

    def test_package_staging_refuses_missing_observer(self):
        config = load('linux-package-config')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tauri = root / 'lycaon-den/src-tauri'
            target = 'aarch64-unknown-linux-gnu'
            for helper in config.HELPERS:
                if helper == 'pw-desktop-settings':
                    continue
                path = tauri / 'binaries' / (helper + '-' + target)
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'fixture')
            with self.assertRaisesRegex(ValueError, 'pw-desktop-settings'):
                config.required_files(root, tauri, target, root / 'manifest.json')
