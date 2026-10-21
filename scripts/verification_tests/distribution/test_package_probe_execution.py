"""Package diagnostics admit only the selected extracted package and always remove their exact profile."""
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

from scripts.linux_release import package_probe as owner

HOST = (os.geteuid(), 'x86_64')
HOSTED = {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted'}
SUDO = """#!/bin/sh
echo "$*" >> '{shim}/calls'
[ "$1" = -n ] || exit 1
case "$2" in
  cat) cat '{shim}/profiles' ;;
  apparmor_parser)
    name=$(sed -n 's/^profile \\([^ ]*\\) .*/\\1/p')
    case "$3" in
      --add)
        echo "$name (unconfined)" >> '{shim}/profiles'
        [ ! -e '{shim}/policy-flips' ] || echo 0 > '{shim}/policy'
        [ ! -e '{shim}/add-fails' ] || exit 1 ;;
      --remove)
        [ -e '{shim}/remove-ignored' ] || {{ grep -v "^$name " '{shim}/profiles' > '{shim}/kept'; mv '{shim}/kept' '{shim}/profiles'; }} ;;
    esac ;;
esac
"""


def elf(machine):
    return b'\x7fELF\x02\x01' + bytes(12) + machine.to_bytes(2, 'little')


def package_tree(root, machine=62):
    """An extracted package whose two probe executables are described by its manifest."""
    tree = root / 'package'
    binaries = tree / owner.INSTALL / 'bin'
    binaries.mkdir(parents=True)
    rows = []
    for name in ('pw', 'pw-document-core'):
        path = binaries / name
        path.write_bytes(elf(machine))
        path.chmod(0o755)
        rows.append({'path': '/' + owner.INSTALL + '/bin/' + name,
                     'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'size': path.stat().st_size})
    manifest = {'format_version': 1, 'install_root': '/' + owner.INSTALL, 'executables': rows}
    write_manifest(tree, manifest)
    return tree, manifest


def write_manifest(tree, manifest):
    path = tree / owner.INSTALL / 'manifest.json'
    path.write_text(json.dumps(manifest))
    path.chmod(0o644)


class ProfileAndInputTests(unittest.TestCase):
    def test_profile_has_one_literal_engine_attachment(self):
        executable = '/tmp/owned extraction/deb/opt/painted-wolf-code/bin/pw'
        name, source = owner.profile_source(executable)
        self.assertIn(('"' + executable + '"').encode(), source)
        self.assertIn(b'userns,', source)
        self.assertNotIn(b'mount,', source)
        self.assertTrue(name.startswith('paintedwolf-package-probe-'))
        for token in ('*', '?', '[', ']', '{', '}', '"', '\\', '\n'):
            with self.subTest(token=token), self.assertRaises(ValueError):
                owner.profile_source(executable.replace('owned', 'owned' + token))

    def test_physical_identity_refuses_group_write_symlinks_and_oversize(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve() / 'engine'
            path.write_bytes(b'fixture'); path.chmod(0o700)
            measured = owner.physical(path, owner=os.geteuid(), executable=True)
            self.assertEqual(measured['sha256'], hashlib.sha256(b'fixture').hexdigest())
            link = path.parent / 'link'; link.symlink_to(path)
            with self.assertRaises(ValueError):
                owner.physical(link, owner=os.geteuid())
            with self.assertRaises(ValueError):
                owner.physical(path, owner=os.geteuid(), limit=3)
            with self.assertRaises(ValueError):
                owner.physical(path, owner=os.geteuid() + 1)
            path.chmod(0o720)
            with self.assertRaises(ValueError):
                owner.physical(path, owner=os.geteuid())


class SelectedPackageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name).resolve()
        self.tree, self.manifest = package_tree(self.root)

    def tearDown(self):
        self.temporary.cleanup()

    def test_manifest_bound_native_executables_are_selected(self):
        binding = owner.selected(self.tree, HOST)
        self.assertEqual(binding['tree'], str(self.tree))
        self.assertEqual(binding['pw']['sha256'], self.manifest['executables'][0]['sha256'])
        self.assertEqual(binding['pw-document-core']['path'], str(self.tree / owner.INSTALL / 'bin/pw-document-core'))

    def test_executables_must_match_one_manifest_row(self):
        for rows in ([dict(self.manifest['executables'][0], sha256='0' * 64), self.manifest['executables'][1]],
                     [dict(self.manifest['executables'][0], size=1), self.manifest['executables'][1]],
                     self.manifest['executables'] + [self.manifest['executables'][0]],
                     self.manifest['executables'][1:]):
            write_manifest(self.tree, dict(self.manifest, executables=rows))
            with self.subTest(rows=rows), self.assertRaisesRegex(ValueError, 'differs from its selected package manifest'):
                owner.selected(self.tree, HOST)

    def test_manifest_must_describe_this_install_root(self):
        for change in ({'install_root': '/opt/other'}, {'format_version': 2}):
            write_manifest(self.tree, dict(self.manifest, **change))
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, 'unsupported identity'):
                owner.selected(self.tree, HOST)

    def test_executables_must_be_native_executable_elf64(self):
        with self.assertRaisesRegex(ValueError, 'not native ELF64'):
            owner.selected(self.tree, (os.geteuid(), 'aarch64'))
        (self.tree / owner.INSTALL / 'bin/pw').chmod(0o644)
        with self.assertRaisesRegex(ValueError, 'ownership, mode or size'):
            owner.selected(self.tree, HOST)

    def test_package_root_must_be_physical_private_and_literal(self):
        alias = self.root / 'alias'
        alias.symlink_to(self.tree)
        with self.assertRaisesRegex(ValueError, 'absolute physical'):
            owner.selected(alias, HOST)
        self.tree.chmod(0o775)
        with self.assertRaisesRegex(ValueError, 'private to its probe owner'):
            owner.selected(self.tree, HOST)
        self.tree.chmod(0o755)
        glob = self.root / 'glob*'
        glob.mkdir()
        tree, _ = package_tree(glob)
        with self.assertRaisesRegex(ValueError, 'literal extracted engine path'):
            owner.selected(tree, HOST)


class OperationAuthorityTests(unittest.TestCase):
    def setUp(self):
        self.binding = {'tree': '/srv/package', 'pw': {'path': '/srv/package/opt/painted-wolf-code/bin/pw', 'sha256': 'a' * 64}}
        self.marker = {'format_version': 1, 'desktop': 'gnome', 'package': '/' + owner.INSTALL}
        self.lease = {'format_version': 1, 'uid': 1000, 'tree': '/srv/package', 'engine_path': self.binding['pw']['path'],
                      'engine_sha256': 'a' * 64, 'marker_sha256': 'b' * 64}

    def test_exact_lease_for_this_caller_package_and_marker_is_admitted(self):
        owner.operation_lease(self.marker, 'b' * 64, self.lease, self.binding, 1000)

    def test_marker_must_name_a_supported_disposable_desktop_guest_for_this_package(self):
        for marker in ({**self.marker, 'desktop': 'kiosk'}, {**self.marker, 'package': '/usr'},
                       {**self.marker, 'format_version': True}, {**self.marker, 'extra': 1}, ['marker']):
            with self.subTest(marker=marker), self.assertRaisesRegex(ValueError, 'marker does not authorize'):
                owner.operation_lease(marker, 'b' * 64, self.lease, self.binding, 1000)

    def test_lease_must_bind_the_caller_package_engine_and_marker(self):
        cases = [({**self.lease, 'uid': '1000'}, 1000), (self.lease, 1001), ({**self.lease, 'engine_sha256': 'c' * 64}, 1000),
                 ({**self.lease, 'tree': '/srv/other'}, 1000), ({**self.lease, 'marker_sha256': 'c' * 64}, 1000),
                 ({**self.lease, 'extra': True}, 1000), ({k: v for k, v in self.lease.items() if k != 'uid'}, 1000)]
        for lease, uid in cases:
            with self.subTest(lease=lease, uid=uid), self.assertRaisesRegex(ValueError, 'lease differs'):
                owner.operation_lease(self.marker, 'b' * 64, lease, self.binding, uid)

    def test_unhosted_probe_requires_the_exact_root_operation_lease_path(self):
        environment = {k: v for k, v in os.environ.items() if k not in HOSTED}
        with patch.dict(os.environ, environment, clear=True):
            for lease in (None, Path('/tmp/paintedwolf-package-stage-' + 'a' * 32 + '.json'),
                          Path('/run/paintedwolf-package-stage-' + 'A' * 32 + '.json')):
                with self.subTest(lease=lease), self.assertRaisesRegex(ValueError, 'exact root-owned operation lease'):
                    owner.authority(self.binding, lease)
        with patch.dict(os.environ, HOSTED):
            self.assertEqual(owner.authority(self.binding, None), {'kind': 'disposable-github-hosted-runner'})


class TransientProfileTests(unittest.TestCase):
    """Run the probe against a sudo shim that keeps a profile table, with a real fixture package."""

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        root = Path(self.temporary.name).resolve()
        self.tree, _ = package_tree(root)
        self.shim = root / 'shim'
        self.shim.mkdir()
        sudo = self.shim / 'sudo'
        sudo.write_text(SUDO.format(shim=self.shim))
        sudo.chmod(0o755)
        (self.shim / 'profiles').write_text('unrelated-profile (enforce)\n')
        (self.shim / 'policy').write_text('1\n')
        self.name, _ = owner.profile_source(str(self.tree / owner.INSTALL / 'bin/pw'))
        environment = {**HOSTED, 'PATH': f"{self.shim}{os.pathsep}{os.environ['PATH']}"}
        for context in (patch.dict(os.environ, environment), patch.object(owner, 'POLICY', self.shim / 'policy')):
            context.start()
            self.addCleanup(context.stop)
        self.addCleanup(self.temporary.cleanup)

    def calls(self):
        path = self.shim / 'calls'
        return path.read_text().splitlines() if path.exists() else []

    def profiles(self):
        return (self.shim / 'profiles').read_text().splitlines()

    def test_failed_diagnostic_removes_only_its_exact_profile(self):
        report = owner.run(self.tree, host=HOST)
        self.assertEqual(report['status'], 'failed')
        self.assertEqual(report['error']['type'], 'OSError')
        self.assertTrue(report['profile_loaded'])
        self.assertTrue(report['profile_removed'])
        self.assertFalse(report['global_policy_changed'])
        self.assertIn('-n apparmor_parser --add', self.calls())
        self.assertIn('-n apparmor_parser --remove', self.calls())
        self.assertEqual(self.profiles(), ['unrelated-profile (enforce)'])

    def test_profile_already_loaded_by_another_operation_is_left_untouched(self):
        (self.shim / 'profiles').write_text(self.name + ' (unconfined)\n')
        report = owner.run(self.tree, host=HOST)
        self.assertIn('already belongs to another operation', report['error']['message'])
        self.assertFalse(report['profile_loaded'])
        self.assertNotIn('profile_add_attempted', report)
        self.assertFalse(any('apparmor_parser' in call for call in self.calls()))
        self.assertEqual(self.profiles(), [self.name + ' (unconfined)'])

    def test_parser_failure_after_load_still_removes_the_attempted_profile(self):
        (self.shim / 'add-fails').touch()
        report = owner.run(self.tree, host=HOST)
        self.assertEqual(report['error']['type'], 'CalledProcessError')
        self.assertTrue(report['profile_add_attempted'])
        self.assertTrue(report['profile_loaded'])
        self.assertTrue(report['profile_removed'])
        self.assertEqual(self.profiles(), ['unrelated-profile (enforce)'])

    def test_profile_that_survives_removal_fails_the_operation(self):
        (self.shim / 'remove-ignored').touch()
        report = owner.run(self.tree, host=HOST)
        self.assertEqual(report['status'], 'failed')
        self.assertFalse(report['profile_removed'])
        self.assertIn('remained loaded', report['cleanup_error'])

    def test_changed_global_userns_policy_fails_the_operation(self):
        (self.shim / 'policy-flips').touch()
        report = owner.run(self.tree, host=HOST)
        self.assertTrue(report['global_policy_changed'])
        self.assertEqual(report['policy_error'], 'global userns restriction changed')

    def test_unrestricted_host_loads_no_profile_and_cannot_claim_a_baseline_differential(self):
        (self.shim / 'policy').write_text('0\n')
        report = owner.run(self.tree, host=HOST)
        self.assertEqual(report['error']['type'], 'OSError')
        self.assertFalse(report['profile_loaded'])
        baseline = owner.run(self.tree, host=HOST, require_baseline_refusal=True)
        self.assertIn('requires the active host restriction', baseline['error']['message'])
        self.assertEqual(self.calls(), [])

    def test_journal_must_be_a_fresh_file_outside_the_package_and_records_the_outcome(self):
        inside = self.tree / 'journal.json'
        existing = self.shim / 'profiles'
        for output in (inside, existing, Path('relative.json')):
            with self.subTest(output=output), self.assertRaisesRegex(ValueError, 'fresh independent absolute file'):
                owner.run(self.tree, host=HOST, output=output)
        self.assertEqual(self.calls(), [])
        journal = self.shim / 'journal.json'
        owner.run(self.tree, host=HOST, output=journal)
        self.assertEqual(json.loads(journal.read_text())['status'], 'failed')
        self.assertTrue(json.loads(journal.read_text())['profile_removed'])
        self.assertEqual(journal.stat().st_mode & 0o777, 0o600)


@unittest.skipUnless(sys.platform.startswith('linux') and hasattr(os, 'waitid') and hasattr(os, 'WNOWAIT'),
                     'the fixed diagnostic retains its leader with Linux waitid(WNOWAIT)')
class DiagnosticProcessTests(unittest.TestCase):
    def test_diagnostic_returns_output_and_kills_its_process_group(self):
        with tempfile.TemporaryDirectory() as temporary:
            marker = Path(temporary) / 'grandchild'
            script = ('import subprocess, sys; child = subprocess.Popen(["sleep", "30"]); '
                      f'open({str(marker)!r}, "w").write(str(child.pid)); print("owned child")')
            result = owner.diagnostic([sys.executable, '-c', script], os.environ.copy())
            grandchild = int(marker.read_text())
        self.assertEqual((result.returncode, result.stdout), (0, b'owned child\n'))
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            status = Path(f'/proc/{grandchild}/stat')
            if not status.exists() or status.read_text().rsplit(')', 1)[1].split()[0] == 'Z':
                return
            time.sleep(0.05)
        self.fail('diagnostic left a process from its own group running')

    def test_diagnostic_output_is_bounded(self):
        with self.assertRaisesRegex(ValueError, 'output exceeded its bound'):
            owner.diagnostic([sys.executable, '-c', 'import sys; sys.stdout.write("x" * (2 << 20))'], os.environ.copy())


if __name__ == '__main__':
    unittest.main()
