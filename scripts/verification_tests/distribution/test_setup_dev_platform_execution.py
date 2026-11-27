import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class SetupDevPlatformTests(unittest.TestCase):
    def test_platform_gate_preserves_macos_checks_and_admits_linux(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / 'scripts'
            scripts.mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[2] / 'setup-dev.sh', scripts / 'setup-dev.sh')
            (scripts / 'artifact-paths.sh').write_text('')
            (root / 'lycaon/internal/platformfloor').mkdir(parents=True)
            (root / 'lycaon/internal/platformfloor/macos_floor.txt').write_text('14.0\n')
            (root / 'lycaon/go.mod').write_text('go 1.26.11\n')
            bin_dir = root / 'bin'
            bin_dir.mkdir()
            def tool(name, body):
                path = bin_dir / name
                path.write_text('#!/bin/sh\n' + body + '\n')
                path.chmod(0o755)
            tool('uname', 'case "$1" in -s) echo "$TEST_OS";; -m) echo "$TEST_ARCH";; esac')
            tool('sw_vers', 'echo "$TEST_MACOS"')
            tool('xcrun', 'exit 0')
            tool('go', 'echo go1.1.0')
            def run(os_name, arch, macos='15.0'):
                env = dict(os.environ, PATH=f'{bin_dir}:/usr/bin:/bin', TEST_OS=os_name,
                           TEST_ARCH=arch, TEST_MACOS=macos)
                return subprocess.run(['bash', str(scripts / 'setup-dev.sh')], env=env,
                                      capture_output=True, text=True)
            for os_name, arch in [('Linux', 'x86_64'), ('Linux', 'aarch64'), ('Darwin', 'arm64')]:
                with self.subTest(os=os_name, arch=arch):
                    result = run(os_name, arch)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('go 1.1.0 is too old', result.stderr)
            self.assertIn('macOS 13.0 is unsupported', run('Darwin', 'arm64', '13.0').stderr)
            self.assertIn('development setup supports', run('Darwin', 'x86_64').stderr)
            self.assertIn('development setup supports', run('Linux', 'riscv64').stderr)
