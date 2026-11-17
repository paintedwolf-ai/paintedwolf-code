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
            source = Path(__file__).resolve().parents[2]
            for name in ["setup-dev.sh", "warm-workspace-build.sh"]:
                shutil.copyfile(source / name, scripts / name)
            shutil.copytree(source / "ci_policy", scripts / "ci_policy")
            notices = native / "THIRD-PARTY-NOTICES.md"
            (scripts / "licenses-notices.sh").write_text(
                '#!/bin/bash\nset -eu\n'
                'root="$(cd "$(dirname "$0")/.." && pwd)"\n'
                'printf "notices\\n" >> "$root/operations"\n'
                'printf "qualified notices" > "$root/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"\n')
            (scripts / "ensure-tauri-binaries.sh").write_text(
                '#!/bin/bash\nset -eu\n'
                'root="$(cd "$(dirname "$0")/.." && pwd)"\n'
                'test -s "$root/lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"\n'
                'printf "stage\\n" >> "$root/operations"\n')
            (scripts / "test-source-snapshot.sh").write_text(
                '#!/bin/bash\nset -eu\n'
                'if [[ "$1" == holding ]]; then [[ "${WARM_SNAPSHOT:-}" == 1 ]]; exit; fi\n'
                '[[ "$1" == run && "$2" == -- ]]\nshift 2\n'
                'export WARM_SNAPSHOT=1\nexec "$@"\n')
            (scripts / "artifact-paths.sh").write_text(
                'export PW_COMPILER_CACHE_DIR="' + str(root / "compiler") + '"\n')
            (scripts / "decide-features.sh").write_text("#!/bin/bash\necho cpu\n")
            (scripts / "toolchain").mkdir(exist_ok=True)
            (scripts / "toolchain/decide-test.sh").write_text("#!/bin/bash\nexit 0\n")
            (scripts / "build-decide.sh").write_text("#!/bin/bash\nexit 0\n")
            binary = root / "bin"
            binary.mkdir()
            cargo = binary / "cargo"
            cargo.write_text('#!/bin/bash\nset -eu\ncase "$*" in *metadata*) echo \'{"workspace_members":["shell"],"packages":[{"id":"shell","name":"shell"}]}\'; exit 0 ;; *--manifest-path*) exit 0 ;; esac\ntest -s THIRD-PARTY-NOTICES.md\n')
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
                    operations = (root / "operations").read_text().splitlines()
                    self.assertEqual(operations, ["notices"] if flag == "--shell-resources" else ["notices", "stage"])
                    (root / "operations").unlink()
                    notices.unlink()

            (scripts / "licenses-notices.sh").write_text("#!/bin/bash\nexit 42\n")
            for flag in ["--shell-resources", "--workspace-cache"]:
                with self.subTest(failed=flag):
                    self.assertEqual(prepare(flag).returncode, 42)
                    self.assertFalse(notices.exists())

    def test_notices_tools_provision_without_generating_notices(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / "scripts"
            scripts.mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[2] / "setup-dev.sh", scripts / "setup-dev.sh")
            (scripts / "licenses-notices.sh").write_text(
                '#!/bin/bash\nset -eu\n'
                'root="$(cd "$(dirname "$0")/.." && pwd)"\n'
                'printf "%s" "$*" > "$root/arguments"\n')
            result = subprocess.run(["bash", str(scripts / "setup-dev.sh"), "--notices-tools"],
                                    text=True, capture_output=True, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "arguments").read_text(), "--tools")

    def test_tools_mode_installs_both_tools_and_stops_before_generation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts, tools, bin_dir = root / "scripts", root / "tools", root / "bin"
            scripts.mkdir()
            tools.mkdir()
            shutil.copyfile(Path(__file__).resolve().parents[2] / "licenses-notices.sh", scripts / "licenses-notices.sh")
            (scripts / "artifact-paths.sh").write_text(f'PW_BIN_DIR="{bin_dir}"\n')
            (root / "lycaon").mkdir()
            (root / "lycaon/go.mod").write_text("module fixture\n\ngo 1.26.0\n")
            calls = root / "calls"
            for name, body in {
                "go": 'if [[ "$1" == env ]]; then echo /goroot; else touch "$GOBIN/go-licenses"; chmod +x "$GOBIN/go-licenses"; fi',
                "cargo": 'mkdir -p "${@: -1}/bin"; touch "${@: -1}/bin/cargo-about"; chmod +x "${@: -1}/bin/cargo-about"',
                "bun": "exit 97",
            }.items():
                (tools / name).write_text(f'#!/bin/bash\necho "{name} $*" >> "{calls}"\n{body}\n')
                (tools / name).chmod(0o755)
            result = subprocess.run(["bash", str(scripts / "licenses-notices.sh"), "--tools"], text=True,
                                    capture_output=True, env={**os.environ, "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}"})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue(os.access(bin_dir / "go-licenses", os.X_OK))
            self.assertTrue(os.access(bin_dir / "cargo-about", os.X_OK))
            self.assertEqual([line.split()[0] for line in calls.read_text().splitlines()], ["go", "go", "cargo"])

