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

from scripts.linux_release import arch_substrate_rehearsal, deb_metadata_rehearsal, metadata_fixture, rpm_metadata_rehearsal

ROOT = Path(__file__).resolve().parents[3]
MODULES = ('scripts.linux_release.rpm_metadata_rehearsal', 'scripts.linux_release.deb_metadata_rehearsal',
           'scripts.linux_release.arch_substrate_rehearsal')


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


class MetadataCommandTests(unittest.TestCase):
    def invoke(self, *arguments, rehearse=lambda work: {'work': work.is_dir()}):
        with patch.object(sys, 'argv', ['rehearsal', *arguments]), \
                patch.object(metadata_fixture.subprocess, 'run') as container, \
                patch('sys.stdout') as stdout, \
                patch.dict(os.environ, {'PW_METADATA_FIXTURE_COMMIT': 'a' * 40, 'PW_METADATA_FIXTURE_TREE': 'b' * 40}):
            metadata_fixture.main('scripts.linux_release.deb_metadata_rehearsal', 'fixture', rehearse)
        return container, ''.join(call.args[0] for call in stdout.write.call_args_list)

    def test_a_report_goes_to_a_fresh_receipt_or_to_standard_output(self):
        container, printed = self.invoke()
        container.assert_not_called()
        self.assertEqual(json.loads(printed), {'work': True})
        with tempfile.TemporaryDirectory() as temporary:
            receipt = Path(temporary) / 'nested' / 'receipt.json'
            _, printed = self.invoke('--receipt', str(receipt))
            self.assertEqual(printed, '')
            self.assertEqual(json.loads(receipt.read_text()), {'work': True})

    def test_the_container_installs_the_declared_tools_and_rehearses_only_inside(self):
        with tempfile.TemporaryDirectory() as temporary:
            receipt = Path(temporary) / 'receipt.json'
            container, printed = self.invoke('--container', '--receipt', str(receipt),
                                             rehearse=lambda _work: self.fail('the host must not rehearse'))
        command = container.call_args.args[0]
        self.assertEqual(printed, '')
        self.assertIn(metadata_fixture.IMAGE, command)
        self.assertIn('PW_METADATA_FIXTURE_COMMIT=' + 'a' * 40, command)
        self.assertIn(f'type=bind,src={Path(temporary).resolve()},dst=/receipt', command)
        self.assertIn(' '.join(metadata_fixture.PACKAGES), command[-1])
        self.assertTrue(command[-1].endswith('-m scripts.linux_release.deb_metadata_rehearsal --receipt /receipt/receipt.json'))
        container, _ = self.invoke('--container', rehearse=lambda _work: self.fail('the host must not rehearse'))
        self.assertFalse(any('/receipt' in part for part in container.call_args.args[0]))


class NativeHostAdmissionTests(unittest.TestCase):
    def test_construction_rehearsals_require_native_linux_amd64_or_arm64(self):
        for system, machine in (('Darwin', 'arm64'), ('Linux', 'riscv64'), ('Linux', 'armv7l')):
            for owner, host in ((rpm_metadata_rehearsal, metadata_fixture), (deb_metadata_rehearsal, metadata_fixture),
                                (arch_substrate_rehearsal, arch_substrate_rehearsal)):
                with self.subTest(owner=owner.__name__, system=system, machine=machine), tempfile.TemporaryDirectory() as work, \
                        patch.object(host.platform, 'system', return_value=system), \
                        patch.object(host.platform, 'machine', return_value=machine), \
                        self.assertRaisesRegex(ValueError, 'native Linux'):
                    owner.rehearse(Path(work))

    def test_rpm_metadata_regression_requires_the_pinned_rpmlint(self):
        with tempfile.TemporaryDirectory() as temporary:
            tools = Path(temporary)
            rpmlint = tools / 'rpmlint'
            rpmlint.write_text('#!/bin/sh\necho 2.4.0\n')
            rpmlint.chmod(0o755)
            with patch.object(metadata_fixture.platform, 'system', return_value='Linux'), \
                    patch.object(metadata_fixture.platform, 'machine', return_value='x86_64'), \
                    patch.dict(os.environ, {'PATH': f"{tools}{os.pathsep}{os.environ['PATH']}"}), \
                    self.assertRaisesRegex(ValueError, 'actual rpmlint 2.5.0'):
                rpm_metadata_rehearsal.rehearse(tools / 'work')
            self.assertFalse((tools / 'work').exists())


class SourceIdentityTests(unittest.TestCase):
    def test_container_identity_must_be_a_full_object_name(self):
        for value in ('HEAD', 'a' * 39, 'A' * 40, 'a' * 40 + '\n', 'g' * 40):
            with self.subTest(value=value), patch.dict(os.environ, {'PW_METADATA_FIXTURE_COMMIT': value}), \
                    self.assertRaisesRegex(ValueError, 'invalid fixture source identity'):
                metadata_fixture.identity('commit')
        with patch.dict(os.environ, {'PW_METADATA_FIXTURE_TREE': 'b' * 40}):
            self.assertEqual(metadata_fixture.identity('tree'), 'b' * 40)


class NativeRehearsalTests(unittest.TestCase):
    def test_rpm_metadata_rehearsal_requires_current_metadata_to_pass_and_a_misspelling_to_fail(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / 'work'
            specs = []
            assembler = SimpleNamespace(rpm_spec=lambda *_: 'Summary: Painted Wolf Code\nRequires: git\n',
                                        tauri_metadata=lambda *_: {'shortDescription': 'Painted Wolf Code'},
                                        BOOTSTRAP={'path': '/usr/libexec/painted-wolf-code/pw-launch'})

            def run(arguments):
                if arguments[0] == 'rpmbuild':
                    directory = work / ('current' if 'current' in arguments[-1] else 'spelling-error')
                    specs.append(Path(arguments[-1]).read_text())
                    (directory / 'fixture.rpm').write_bytes(b'native rpm fixture')
                    return subprocess.CompletedProcess(arguments, 0)
                self.assertEqual(arguments[0], 'rpmlint')
                return subprocess.CompletedProcess(arguments, 0 if 'current' in arguments[-1] else 1)

            with patch.object(metadata_fixture.platform, 'system', return_value='Linux'), \
                    patch.object(metadata_fixture.platform, 'machine', return_value='x86_64'), \
                    patch.object(rpm_metadata_rehearsal.subprocess, 'check_output', return_value='2.5.0\n'), \
                    patch.object(metadata_fixture, 'assembler', return_value=assembler), \
                    patch.object(metadata_fixture.shutil, 'copyfile',
                                 side_effect=lambda _source, destination: Path(destination).write_bytes(b'fixture ELF')), \
                    patch.object(rpm_metadata_rehearsal, 'run', side_effect=run), \
                    patch.dict(os.environ, {'PW_METADATA_FIXTURE_COMMIT': 'a' * 40, 'PW_METADATA_FIXTURE_TREE': 'b' * 40}):
                report = rpm_metadata_rehearsal.rehearse(work)
            self.assertEqual(report['cases']['current']['rpmlint_exit_code'], 0)
            self.assertEqual(report['cases']['spelling-error']['rpmlint_exit_code'], 1)
            self.assertEqual(specs, ['Summary: Painted Wolf Code\nRequires: git\n',
                                     'Summary: Painted Wolf Code with a securty engine\nRequires: git\n'])
            self.assertFalse(report['application_qualified'])

    def test_deb_metadata_rehearsal_requires_declared_conffiles(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary) / 'work'
            work.mkdir()
            fragment = Path(temporary) / 'profile'
            fragment.write_text('profile\n')

            def write_deb(_root, staged, directory, _version, _architecture, _output):
                control = directory / 'deb-root/DEBIAN'
                control.mkdir(parents=True)
                (control / 'control').write_text('Package: painted-wolf-code\n')
                conffiles = sorted('/' + path.relative_to(staged / 'data').as_posix()
                                   for path in (staged / 'data/etc').rglob('*') if path.is_file())
                (control / 'conffiles').write_text('\n'.join(conffiles) + '\n')
                package = directory / 'fixture.deb'
                package.write_bytes(b'deb')
                return package

            linted = []

            def run(arguments):
                if arguments[0] == 'dpkg-deb':
                    self.assertFalse((Path(arguments[-2]) / 'DEBIAN/conffiles').exists())
                    Path(arguments[-1]).write_bytes(b'unmarked deb')
                    return subprocess.CompletedProcess(arguments, 0, '')
                self.assertEqual(arguments[0], 'lintian')
                unmarked = Path(arguments[-1]).read_bytes() == b'unmarked deb'
                linted.append(unmarked)
                tag = deb_metadata_rehearsal.CONFFILE_TAG if unmarked else ''
                return subprocess.CompletedProcess(arguments, 2 if unmarked else 0, tag)

            config = SimpleNamespace(optional_files=lambda *_: {'/etc/apparmor.d/painted-wolf-code': fragment})
            assembler = SimpleNamespace(BOOTSTRAP={'path': '/usr/libexec/painted-wolf-code/pw-launch'},
                                        load_package_config=lambda _root: config, normalise_modes=lambda _data: None,
                                        write_deb=write_deb)
            with patch.object(metadata_fixture.platform, 'system', return_value='Linux'), \
                    patch.object(metadata_fixture.platform, 'machine', return_value='x86_64'), \
                    patch.object(deb_metadata_rehearsal.subprocess, 'check_output', return_value='Lintian v2.117.0\n'), \
                    patch.object(metadata_fixture, 'assembler', return_value=assembler), \
                    patch.object(metadata_fixture.shutil, 'copyfile',
                                 side_effect=lambda source, destination: Path(destination).write_bytes(Path(source).read_bytes()
                                                                                                       if Path(source) == fragment else b'ELF')), \
                    patch.object(deb_metadata_rehearsal.shutil, 'copyfile',
                                 side_effect=lambda source, destination: Path(destination).write_bytes(Path(source).read_bytes())), \
                    patch.object(deb_metadata_rehearsal, 'run', side_effect=run), \
                    patch.dict(os.environ, {'PW_METADATA_FIXTURE_COMMIT': 'a' * 40, 'PW_METADATA_FIXTURE_TREE': 'b' * 40}):
                report = deb_metadata_rehearsal.rehearse(work)
                self.assertEqual(linted, [False, True])
                self.assertEqual(report['cases']['unmarked-conffile']['lintian_exit_code'], 2)
                # An unmarked file that lintian lets pass is a failed rehearsal, not a pass.
                run_passes = lambda arguments: subprocess.CompletedProcess(arguments, 0, '') if arguments[0] == 'lintian' else run(arguments)
                with patch.object(deb_metadata_rehearsal, 'run', side_effect=run_passes), \
                        self.assertRaisesRegex(ValueError, 'unmarked /etc file must fail'):
                    deb_metadata_rehearsal.rehearse(Path(temporary) / 'second')

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
