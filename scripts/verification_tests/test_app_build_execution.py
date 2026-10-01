import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class AppBuildExecutionTest(unittest.TestCase):
    def test_profile_controls_build_and_opened_artifact(self):
        for profile in ("release", "debug"):
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / "scripts"
                scripts.mkdir()
                shutil.copyfile(Path(__file__).resolve().parents[1].joinpath("den-build-app.sh"), scripts / "den-build-app.sh")
                shutil.copytree(Path(__file__).resolve().parents[1].joinpath("macos-build-tools"), scripts / "macos-build-tools")
                for name in ("sync-den-versions.sh",):
                    (scripts / name).write_text("#!/bin/bash\nexit 0\n")
                stage_log = root / "staged.txt"
                (scripts / "stage-engine.sh").write_text(
                    '#!/bin/bash\nprintf "%s" "$*" > "$APP_TEST_STAGE_LOG"\n'
                )
                (scripts / "licenses-notices.sh").write_text(
                    '#!/bin/bash\nmkdir -p "$(dirname "$0")/../lycaon-den/src-tauri"\ntouch "$(dirname "$0")/../lycaon-den/src-tauri/THIRD-PARTY-NOTICES.md"\n'
                )
                den = root / "lycaon-den"
                den.mkdir()
                binaries = root / "bin"
                binaries.mkdir()
                build_log = root / "build.json"
                open_log = root / "opened.txt"
                stubs = {
                    "go": "#!/bin/sh\nexit 0\n",
                    "cargo": "#!/bin/sh\nexit 0\n",
                    "rustc": "#!/bin/sh\nprintf 'test-host\\n'\n",
                    "uname": "#!/bin/sh\nprintf 'Darwin\\n'\n",
                    "open": '#!/bin/sh\nprintf "%s" "$1" > "$APP_TEST_OPEN_LOG"\n',
                    "bun": '''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys
pathlib.Path(os.environ["APP_TEST_BUILD_LOG"]).write_text(json.dumps(sys.argv[1:]))
assert (pathlib.Path.cwd() / "src-tauri" / "THIRD-PARTY-NOTICES.md").is_file()
assert os.environ.get("IBToolNeverDeque") == "1", "asset compiler must bypass pooled ibtoold"
assert pathlib.Path(shutil.which("actool")).samefile(pathlib.Path.cwd().parent / "scripts" / "macos-build-tools" / "actool")
profile = "debug" if "--debug" in sys.argv else "release"
(pathlib.Path.cwd() / "src-tauri" / "target" / "test-host" / profile / "bundle" / "macos" / "Fixture.app").mkdir(parents=True)
''',
                }
                for name, content in stubs.items():
                    executable = binaries / name
                    executable.write_text(content)
                    executable.chmod(0o755)
                env = dict(os.environ, PATH=f"{binaries}{os.pathsep}{os.environ['PATH']}",
                           APP_TEST_BUILD_LOG=str(build_log), APP_TEST_OPEN_LOG=str(open_log),
                           APP_TEST_STAGE_LOG=str(stage_log))
                env.pop("LYCAON_OPENGREP_CANDIDATE", None)
                env.pop("IBToolNeverDeque", None)
                args = ["bash", str(scripts / "den-build-app.sh"), "--open", "--no-devtools"]
                if profile == "debug":
                    args.append("--debug")
                result = subprocess.run(args, env=env, text=True, capture_output=True, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(stage_log.read_text(), "" if profile == "debug" else "--release")
                command = json.loads(build_log.read_text())
                self.assertEqual("--debug" in command, profile == "debug")
                self.assertNotIn("--features", command)
                config = json.loads(command[command.index("--config") + 1])
                self.assertFalse(config["bundle"]["createUpdaterArtifacts"])
                self.assertNotIn("icon", config["bundle"])
                expected = den / "src-tauri" / "target" / "test-host" / profile / "bundle" / "macos" / "Fixture.app"
                self.assertEqual(open_log.read_text(), str(expected))

    @unittest.skipUnless(os.name == "posix", "macOS tool requires POSIX descriptors")
    def test_asset_compiler_reopens_closed_stdin_and_preserves_result(self):
        wrapper = Path(__file__).resolve().parents[1].joinpath("macos-build-tools") / "actool"
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            log = root / "arguments.json"
            xcrun = root / "xcrun"
            xcrun.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
os.fstat(0)
assert os.read(0, 1) == b"", "asset compiler stdin must be open at EOF"
pathlib.Path(os.environ["APP_TEST_TOOL_LOG"]).write_text(json.dumps(sys.argv[1:]))
print("asset compiler diagnostic", file=sys.stderr)
sys.exit(int(os.environ["APP_TEST_TOOL_EXIT"]))
''')
            xcrun.chmod(0o755)
            for code in (0, 37):
                with self.subTest(exit_code=code):
                    env = dict(os.environ, PATH=f"{root}{os.pathsep}{os.environ['PATH']}",
                               APP_TEST_TOOL_LOG=str(log), APP_TEST_TOOL_EXIT=str(code))
                    args = ["Icon with spaces.icon", "--compile", "output with spaces"]
                    result = subprocess.run(
                        ["bash", "-c", 'exec 0<&-; exec "$@"', "bash", str(wrapper), *args],
                        env=env, text=True, capture_output=True, check=False,
                    )
                    self.assertEqual(result.returncode, code, result.stderr)
                    self.assertEqual(json.loads(log.read_text()), ["actool", *args])
                    self.assertIn("asset compiler diagnostic", result.stderr)
