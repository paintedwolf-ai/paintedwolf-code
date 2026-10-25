"""Native package construction rehearsals refuse foreign hosts, unpinned tools and reused receipts."""
import os
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release import arch_substrate_rehearsal, rpm_metadata_rehearsal

ROOT = Path(__file__).resolve().parents[3]
MODULES = ('scripts.linux_release.rpm_metadata_rehearsal', 'scripts.linux_release.arch_substrate_rehearsal')


def command(module, *arguments, env=None):
    return subprocess.run([sys.executable, '-m', module, *arguments], cwd=ROOT, capture_output=True, text=True,
                          env={**os.environ, 'PYTHONPATH': str(ROOT / 'scripts'), **(env or {})})


class ReceiptAdmissionTests(unittest.TestCase):
    def test_rehearsals_refuse_an_existing_or_aliased_receipt_without_running(self):
        with tempfile.TemporaryDirectory() as temporary:
            existing = Path(temporary) / 'existing.json'
            existing.write_text('prior receipt\n')
            alias = Path(temporary) / 'alias.json'
            alias.symlink_to(Path(temporary) / 'missing.json')
            for module in MODULES:
                for receipt in (existing, alias):
                    with self.subTest(module=module, receipt=receipt.name):
                        result = command(module, '--receipt', str(receipt))
                        self.assertEqual(result.returncode, 2, result.stderr)
                        self.assertIn('receipt', result.stderr)
            self.assertEqual(existing.read_text(), 'prior receipt\n')
            self.assertFalse((Path(temporary) / 'missing.json').exists())


class NativeHostAdmissionTests(unittest.TestCase):
    def test_construction_rehearsals_require_native_linux_amd64_or_arm64(self):
        for system, machine in (('Darwin', 'arm64'), ('Linux', 'riscv64'), ('Linux', 'armv7l')):
            for owner in (rpm_metadata_rehearsal, arch_substrate_rehearsal):
                with self.subTest(owner=owner.__name__, system=system, machine=machine), tempfile.TemporaryDirectory() as work, \
                        patch.object(owner.platform, 'system', return_value=system), \
                        patch.object(owner.platform, 'machine', return_value=machine), \
                        self.assertRaisesRegex(ValueError, 'native Linux'):
                    owner.rehearse(Path(work))

    def test_rpm_metadata_regression_requires_the_pinned_rpmlint(self):
        with tempfile.TemporaryDirectory() as temporary:
            tools = Path(temporary)
            rpmlint = tools / 'rpmlint'
            rpmlint.write_text('#!/bin/sh\necho 2.4.0\n')
            rpmlint.chmod(0o755)
            with patch.object(rpm_metadata_rehearsal.platform, 'system', return_value='Linux'), \
                    patch.object(rpm_metadata_rehearsal.platform, 'machine', return_value='x86_64'), \
                    patch.dict(os.environ, {'PATH': f"{tools}{os.pathsep}{os.environ['PATH']}"}), \
                    self.assertRaisesRegex(ValueError, 'actual rpmlint 2.5.0'):
                rpm_metadata_rehearsal.rehearse(tools / 'work')
            self.assertFalse((tools / 'work').exists())


class SourceIdentityTests(unittest.TestCase):
    def test_container_identity_must_be_a_full_object_name(self):
        for value in ('HEAD', 'a' * 39, 'A' * 40, 'a' * 40 + '\n', 'g' * 40):
            with self.subTest(value=value), patch.dict(os.environ, {'PW_RPM_FIXTURE_COMMIT': value}), \
                    self.assertRaisesRegex(ValueError, 'invalid fixture source identity'):
                rpm_metadata_rehearsal.identity('commit')
        with patch.dict(os.environ, {'PW_RPM_FIXTURE_TREE': 'b' * 40}):
            self.assertEqual(rpm_metadata_rehearsal.identity('tree'), 'b' * 40)


class NativeRehearsalTests(unittest.TestCase):
    def test_rpm_metadata_rehearsal_requires_current_spelling_to_pass(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / 'work'
            assembler = SimpleNamespace(rpm_spec=lambda *_: 'Summary: Painted Wolf Code\nRequires: Git tools\n',
                                        tauri_metadata=lambda *_: {'shortDescription': 'Painted Wolf Code'})
            specification = SimpleNamespace(loader=SimpleNamespace(exec_module=lambda _module: None))

            def run(arguments):
                if arguments[0] == 'rpmbuild':
                    directory = work / ('current' if 'current' in arguments[-1] else 'spelling-regression')
                    (directory / 'fixture.rpm').write_bytes(b'native rpm fixture')
                    return subprocess.CompletedProcess(arguments, 0)
                self.assertEqual(arguments[0], 'rpmlint')
                return subprocess.CompletedProcess(arguments, 0 if 'current' in arguments[-1] else 1)

            with patch.object(rpm_metadata_rehearsal.platform, 'system', return_value='Linux'), \
                    patch.object(rpm_metadata_rehearsal.platform, 'machine', return_value='x86_64'), \
                    patch.object(rpm_metadata_rehearsal.subprocess, 'check_output', return_value='2.5.0\n'), \
                    patch.object(rpm_metadata_rehearsal.importlib.util, 'spec_from_file_location', return_value=specification), \
                    patch.object(rpm_metadata_rehearsal.importlib.util, 'module_from_spec', return_value=assembler), \
                    patch.object(rpm_metadata_rehearsal.shutil, 'copyfile',
                                 side_effect=lambda _source, destination: Path(destination).write_bytes(b'fixture ELF')), \
                    patch.object(rpm_metadata_rehearsal, 'run', side_effect=run), \
                    patch.dict(os.environ, {'PW_RPM_FIXTURE_COMMIT': 'a' * 40, 'PW_RPM_FIXTURE_TREE': 'b' * 40}):
                report = rpm_metadata_rehearsal.rehearse(work)
            self.assertEqual(report['cases']['current']['rpmlint_exit_code'], 0)
            self.assertEqual(report['cases']['spelling-regression']['rpmlint_exit_code'], 1)
            self.assertFalse(report['application_qualified'])

    def test_pacman_rehearsal_binds_native_fixture_bytes_and_final_metadata(self):
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / 'source'
            source.mkdir()
            (source / 'VERSION').write_text('1.2.3\n')
            (source / 'RELEASE_BUILD').write_text('2\n')
            catalog = source / 'packaging/linux/arch/substrates.json'
            catalog.parent.mkdir(parents=True)
            catalog.write_text(json.dumps({'aarch64': {'rootfs_sha256': 'b' * 64}}))
            authenticated = source / 'authenticated-rootfs.tar.gz'
            authenticated.write_bytes(b'authenticated substrate')
            work = Path(temporary) / 'work'
            work.mkdir()

            def copy_executable(_source, destination):
                Path(destination).write_bytes(b'\x7fELF\x02\x01' + b'\0' * 12 + (62).to_bytes(2, 'little'))

            def build_fixture(_root, recipe_dir, _architecture, _epoch):
                (recipe_dir / 'painted-wolf-code-1.2.3-2-x86_64.pkg.tar.zst').write_bytes(b'package')
                return {'distribution': 'reviewed fixture'}

            def extract_fixture(_package, destination, _root, _architecture, _product):
                tree = work / 'fixture-tree'
                for original in tree.rglob('*'):
                    target = destination / original.relative_to(tree)
                    if original.is_dir():
                        target.mkdir(parents=True, exist_ok=True)
                    elif original.is_symlink():
                        target.symlink_to(original.readlink())
                    else:
                        target.write_bytes(original.read_bytes())
                        target.chmod(original.stat().st_mode & 0o777)
                return {'pkgver': ['1.2.3-2']}

            with patch.object(arch_substrate_rehearsal, 'ROOT', source), \
                    patch.object(arch_substrate_rehearsal.platform, 'system', return_value='Linux'), \
                    patch.object(arch_substrate_rehearsal.platform, 'machine', return_value='x86_64'), \
                    patch.object(arch_substrate_rehearsal.shutil, 'copyfile', side_effect=copy_executable), \
                    patch.object(arch_substrate_rehearsal, 'write_payload',
                                 side_effect=lambda _tree, path, _epoch: path.write_bytes(b'payload')), \
                    patch.object(arch_substrate_rehearsal, 'recipe', return_value='pkgname=fixture\n'), \
                    patch.object(arch_substrate_rehearsal, 'rootfs', return_value=authenticated), \
                    patch.object(arch_substrate_rehearsal, 'build', side_effect=build_fixture), \
                    patch.object(arch_substrate_rehearsal, 'extract', side_effect=extract_fixture), \
                    patch.object(arch_substrate_rehearsal.subprocess, 'check_output', return_value='a' * 40 + '\n'):
                report = arch_substrate_rehearsal.rehearse(work)
            self.assertEqual(report['native_version'], '1.2.3-2')
            self.assertEqual(report['substrate']['distribution'], 'reviewed fixture')
            self.assertEqual(report['source_commit'], 'a' * 40)
            self.assertEqual(report['sealed_fixture']['opt/painted-wolf-code/bin/pw'],
                             arch_substrate_rehearsal.stream_digest(work / 'fixture-tree/opt/painted-wolf-code/bin/pw'))


if __name__ == '__main__':
    unittest.main()
