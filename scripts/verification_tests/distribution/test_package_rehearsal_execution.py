"""Native package construction rehearsals refuse foreign hosts, unpinned tools and reused receipts."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
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


if __name__ == '__main__':
    unittest.main()
