import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class ShellResourcesTests(unittest.TestCase):
    def test_preparation_publishes_resources_to_the_build_checkout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / "scripts"
            scripts.mkdir()
            native = root / "lycaon-den/src-tauri"
            native.mkdir(parents=True)
            source = Path(__file__).resolve().parents[1]
            for name in ["setup-dev.sh", "warm-workspace-build.sh"]:
                shutil.copyfile(source / name, scripts / name)
            notices = native / "THIRD-PARTY-NOTICES.md"
            (scripts / "licenses-notices.sh").write_text(
                '#!/bin/bash\nset -eu\n'
                'root="$(cd "$(dirname "$0")/.." && pwd)"\n'
                'printf "qualified notices" > "$root/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"\n')
            (scripts / "ensure-tauri-binaries.sh").write_text(
                '#!/bin/bash\nset -eu\n'
                'root="$(cd "$(dirname "$0")/.." && pwd)"\n'
                'test -s "$root/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"\n')
            binary = root / "bin"
            binary.mkdir()
            cargo = binary / "cargo"
            cargo.write_text('#!/bin/bash\nset -eu\ntest -s THIRD-PARTY-NOTICES.md\n')
            cargo.chmod(0o755)
            env = dict(os.environ, PATH=f"{binary}{os.pathsep}{os.environ['PATH']}")

            def prepare(flag):
                return subprocess.run(["bash", str(scripts / "setup-dev.sh"), flag],
                                      env=env, text=True, capture_output=True, check=False)

            for flag in ["--shell-resources", "--workspace-cache"]:
                with self.subTest(flag=flag):
                    result = prepare(flag)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(notices.read_text(), "qualified notices")
                    notices.unlink()

            (scripts / "licenses-notices.sh").write_text("#!/bin/bash\nexit 42\n")
            for flag in ["--shell-resources", "--workspace-cache"]:
                with self.subTest(failed=flag):
                    self.assertEqual(prepare(flag).returncode, 42)
                    self.assertFalse(notices.exists())
