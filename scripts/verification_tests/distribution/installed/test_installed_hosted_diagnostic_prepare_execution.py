"""Partial hosted preparation takes administrator custody of one measured candidate."""
import hashlib
import json
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.hosted import diagnostic
from linux_release.hosted import installed as runtime

MARKER = '/etc/paintedwolf-qualification.json'
SELECTED = {'partial': True, 'controller_commit': 'a' * 40, 'controller_tree': 'b' * 40,
            'source_commit': 'c' * 40, 'source_tree': 'd' * 40}


class Marker:
    def __init__(self, uid=0, mode=stat.S_IFREG | 0o644):
        self.info = SimpleNamespace(st_uid=uid, st_mode=mode)

    def lstat(self):
        return self.info


def identity_of(path: Path) -> dict:
    data = path.read_bytes()
    return {'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}


class Custody:
    """Patch the hosted runtime so prepare runs against a scratch root and home."""

    def __init__(self, base: Path, marker=None, declared=None, identity=None):
        self.root, self.install, self.home = base / 'root', base / 'install', base / 'home'
        self.home.mkdir()
        self.candidate = base / 'candidate.deb'
        self.candidate.write_bytes(b'candidate package bytes')
        self.marker = marker or Marker()
        self.declared = declared or {'format_version': 1, 'desktop': 'gnome', 'package': str(self.install)}
        self.identity = identity or identity_of(self.candidate)
        self.final = {'format_version': 1, 'artifacts': [{'id': 'pw.deb'}]}
        self.producer = {'provenance': {'run_id': 12, 'artifact_id': 34}}

    def path(self, value):
        return self.marker if value == MARKER else Path(value)

    def __enter__(self):
        package = (self.final, self.producer, {'id': 'pw.deb'}, self.candidate, self.identity)
        self.patches = [patch.object(runtime, 'context', return_value=SimpleNamespace(pw_dir=str(self.home))),
                        patch.object(runtime, 'ROOT', self.root), patch.object(runtime, 'INSTALL', self.install),
                        patch.object(runtime, 'USER', 'qualification'),
                        patch.object(runtime, 'candidate_package', return_value=package),
                        patch.object(diagnostic, 'Path', side_effect=self.path),
                        patch.object(diagnostic, 'read', return_value=self.declared),
                        patch.object(diagnostic, 'file_identity', side_effect=identity_of)]
        for item in self.patches:
            item.start()
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()


class HostedDiagnosticPrepareTests(unittest.TestCase):
    def test_prepare_copies_the_measured_candidate_and_writes_a_partial_contract(self):
        with tempfile.TemporaryDirectory() as temporary, Custody(Path(temporary)) as custody:
            contract = diagnostic.prepare(SELECTED, Path(temporary) / 'controller', custody.candidate, {})
            package = custody.root / 'packages' / 'pw.deb'
            self.assertEqual(package.read_bytes(), b'candidate package bytes')
            self.assertEqual(stat.S_IMODE(package.stat().st_mode), 0o444)
            self.assertEqual(stat.S_IMODE(custody.root.stat().st_mode), 0o755)
            for name, value in [('final-artifacts.json', custody.final), ('candidate-selection.json', custody.producer),
                                ('contract.json', contract)]:
                retained = custody.root / name
                self.assertEqual(json.loads(retained.read_text()), value)
                self.assertEqual(stat.S_IMODE(retained.stat().st_mode), 0o444)
            self.assertEqual(contract['scope'], 'partial-native-first-launch-diagnostic')
            self.assertEqual((contract['controller_commit'], contract['driver_tree'], contract['source_commit'],
                              contract['source_tree']), ('a' * 40, 'b' * 40, 'c' * 40, 'd' * 40))
            self.assertEqual(contract['artifact_id'], 'pw.deb')
            self.assertEqual(contract['output'], str(custody.home / 'installed-observations'))
            self.assertEqual(contract['user'], 'qualification')
            self.assertEqual(contract['candidate_provenance'], custody.producer['provenance'])
            self.assertEqual(len(contract['execution_nonce']), 32)
            int(contract['execution_nonce'], 16)

    def test_unmeasured_markers_and_used_observation_paths_refuse_before_custody(self):
        refusals = [dict(marker=Marker(uid=1000)), dict(marker=Marker(mode=stat.S_IFREG | 0o666)),
                    dict(marker=Marker(mode=stat.S_IFLNK | 0o644)),
                    dict(declared={'format_version': 1, 'desktop': 'kde', 'package': 'elsewhere'}),
                    dict(declared={'format_version': '1', 'desktop': 'gnome', 'package': 'elsewhere'})]
        for index, changed in enumerate(refusals):
            with tempfile.TemporaryDirectory() as temporary, self.subTest(index=index), \
                    Custody(Path(temporary), **changed) as custody:
                with self.assertRaises(ValueError):
                    diagnostic.prepare(SELECTED, Path(temporary) / 'controller', custody.candidate, {})
                self.assertFalse(custody.root.exists())
        with tempfile.TemporaryDirectory() as temporary, Custody(Path(temporary)) as custody:
            (custody.home / 'installed-observations').mkdir()
            with self.assertRaises(ValueError):
                diagnostic.prepare(SELECTED, Path(temporary) / 'controller', custody.candidate, {})
            self.assertFalse(custody.root.exists())

    def test_candidate_changing_while_entering_custody_is_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            identity = {'sha256': '0' * 64, 'bytes': 1}
            with Custody(Path(temporary), identity=identity) as custody:
                with self.assertRaises(ValueError):
                    diagnostic.prepare(SELECTED, Path(temporary) / 'controller', custody.candidate, {})
                self.assertFalse((custody.root / 'contract.json').exists())

    def test_retention_requires_a_fresh_path_outside_the_controller_checkout(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            home, checkout = base / 'home', base / 'controller'
            leaf = home / 'installed-observations'
            leaf.mkdir(parents=True)
            checkout.mkdir()
            provenance = {'run_id': 12, 'artifact_id': 34}
            contract = {'controller_commit': SELECTED['controller_commit'], 'candidate_provenance': provenance,
                        'output': str(leaf), 'execution_nonce': 'f' * 32}
            report = {'format_version': 1, 'scope': 'partial-native-first-launch-diagnostic',
                      'source_commit': SELECTED['source_commit'], 'source_tree': SELECTED['source_tree'],
                      'driver_tree': SELECTED['controller_tree'], 'candidate_provenance': provenance,
                      'complete_candidate': False, 'application_qualified': False, 'qualification_authority': False,
                      'publication_authority': False, 'status': 'failed', 'first_launch': None}
            used = base / 'used'
            used.mkdir()
            for output in (used, checkout / 'nested'):
                with self.subTest(output=output.name), patch.object(runtime, 'ROOT', base / 'root'), \
                        patch.object(runtime, 'context', return_value=SimpleNamespace(pw_dir=str(home))), \
                        patch.object(diagnostic, 'read', side_effect=[contract, report]), \
                        patch.object(diagnostic, 'retain_tree') as retained:
                    with self.assertRaises(ValueError):
                        diagnostic.retain(SELECTED, checkout, {}, output)
                    retained.assert_not_called()
            self.assertFalse((checkout / 'nested').exists())


if __name__ == '__main__':
    unittest.main()
