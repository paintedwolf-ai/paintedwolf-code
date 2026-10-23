"""Copied runtime inodes require an exact administrator-selected final generation."""
from dataclasses import replace
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from scripts.linux_release import installed_generation as generation
from scripts.linux_release.installed_runtime import file_identity
from scripts.linux_release.installed_cases.package_payload import Entry, verify


class InstalledGenerationTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory).resolve() / 'installed'
        root.mkdir()
        bootstrap = Path(directory).resolve() / 'pw-launch'
        bootstrap.write_bytes(b'bootstrap')
        bootstrap.chmod(0o4755)
        names = ['bin/pw', 'bin/pw-launch-entry', 'bin/painted-wolf-code',
                 'engine-root/browser/chrome-headless-shell']
        entries, images = {}, {}
        for name in names:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(name.encode())
            path.chmod(0o755)
            row = file_identity(path)
            row.pop('header')
            images[str(path)] = row
            package_name = str(path.relative_to('/'))
            entries[package_name] = Entry(package_name, 0o755, 0, 0, 'file', row['bytes'], row['sha256'])
        seal = root / 'manifest.json'
        seal.write_bytes(b'exact final artifact manifest')
        seal.chmod(0o644)
        digest = hashlib.sha256(seal.read_bytes()).hexdigest()
        name = str(seal.relative_to('/'))
        entries[name] = Entry(name, 0o644, 0, 0, 'file', seal.stat().st_size, digest)
        selected = root / 'generations' / digest
        selected.mkdir(parents=True)
        for name in [*names, 'manifest.json']:
            target = selected / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(root / name, target)
        # Fixture authority must not inherit a group-writable host umask.
        for path in [root, *root.rglob('*')]:
            if path.is_dir():
                path.chmod(0o755)
        policy = Path(directory).resolve() / 'policy.json'
        policy.write_text(json.dumps({'version': 1, 'logical_install_root': str(root),
                                     'generation_root': str(selected), 'manifest_sha256': digest}))
        policy.chmod(0o644)
        row = file_identity(bootstrap)
        row.pop('header')
        images[str(bootstrap)] = row
        return root, images, entries, policy, selected, bootstrap

    def bind(self, fixture):
        root, images, entries, policy, selected, bootstrap = fixture
        # Authority metadata is simulated in these unprivileged byte/inode fixtures;
        # the actual installed case exercises the real root-owned paths.
        def controlled(path, **kwargs):
            if path.is_symlink() or path.stat().st_mode & 0o022:
                raise ValueError('uncontrolled fixture')
        def verify_owned(entries, root):
            return verify({name: replace(row, uid=os.getuid(), gid=os.getgid()) for name, row in entries.items()}, root)
        with patch.object(generation, 'SETUID_LAUNCHER', bootstrap), \
                patch.object(generation, 'controlled', side_effect=controlled), \
                patch.object(generation, 'verify', side_effect=verify_owned):
            return generation.bind(root, images, entries, policy_path=policy)

    def test_exact_copied_generation_has_its_own_kernel_image_identities(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = self.fixture(directory)
            root, images, _, _, selected, _ = fixture
            self.assertEqual(stat.S_IMODE(selected.stat().st_mode), 0o755)
            result = self.bind(fixture)
            for name in ('bin/pw', 'bin/painted-wolf-code', 'engine-root/browser/chrome-headless-shell'):
                before, after = images[str(root / name)], result['images'][str(root / name)]
                self.assertNotEqual(before['inode'], after['inode'])
                self.assertEqual(after['inode'], (selected / name).stat().st_ino)
                self.assertEqual(before['sha256'], after['sha256'])
                self.assertEqual(before['mode'], after['mode'])

    def test_substituted_bytes_manifest_selection_or_uncontrolled_generation_is_refused(self):
        for mutation in ('bytes', 'mode', 'manifest', 'policy', 'store', 'writable'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                fixture = self.fixture(directory)
                root, _, entries, policy, selected, _ = fixture
                if mutation == 'bytes':
                    (selected / 'bin/pw').write_bytes(b'substituted')
                elif mutation == 'mode':
                    (selected / 'bin/pw').chmod(0o555)
                elif mutation == 'manifest':
                    (selected / 'manifest.json').write_bytes(b'other manifest')
                elif mutation in {'policy', 'store'}:
                    data = json.loads(policy.read_text())
                    data['manifest_sha256' if mutation == 'policy' else 'generation_root'] = '0' * 64
                    policy.write_text(json.dumps(data))
                else:
                    selected.chmod(0o777)
                with self.assertRaises(ValueError):
                    self.bind(fixture)

    def test_controlled_paths_require_root_regular_objects_and_safe_ancestors(self):
        for mode, uid in ((stat.S_IFREG | 0o644, 1000), (stat.S_IFREG | 0o666, 0),
                          (stat.S_IFLNK | 0o777, 0)):
            path = MagicMock()
            path.lstat.return_value = SimpleNamespace(st_mode=mode, st_uid=uid)
            with self.subTest(mode=mode, uid=uid), self.assertRaises(ValueError):
                generation.controlled(path)
        parent = MagicMock()
        parent.lstat.return_value = SimpleNamespace(st_mode=stat.S_IFDIR | 0o777, st_uid=0)
        path = MagicMock()
        path.parent = parent
        path.lstat.return_value = SimpleNamespace(st_mode=stat.S_IFREG | 0o644, st_uid=0)
        with self.assertRaises(ValueError):
            generation.controlled(path)
