import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class NoticeEntrypointsExecutionTest(unittest.TestCase):
    def test_every_buildable_entrypoint_reaches_the_license_check(self):
        for platform in ("darwin", "linux", "windows"):
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                scripts = root / "scripts"
                scripts.mkdir()
                shutil.copyfile(Path(__file__).resolve().parents[2] / "licenses-notices.sh",
                                scripts / "licenses-notices.sh")
                (scripts / "artifact-paths.sh").write_text(
                    'PW_BIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin"\n')
                (root / "lycaon").mkdir()
                (root / "lycaon/go.mod").write_text("module fixture\ngo 1.26.10\n")
                (root / "lycaon-den/node_modules").mkdir(parents=True)
                binaries = root / "bin"
                binaries.mkdir()
                stubs = {
                    "go": '''#!/usr/bin/env python3
import json, os, pathlib, sys
if sys.argv[1:] == ["env", "GOROOT"]:
    print("/fixture/go")
elif sys.argv[1] == "list":
    pathlib.Path(os.environ["NOTICE_TEST_ROOT"], "queried.json").write_text(json.dumps(sys.argv[2:]))
    print("fixture/cmd/engine")
    print("fixture/cmd/logs")
    if os.environ["GOOS"] == "linux":
        print("fixture/cmd/linux-only")
else:
    raise SystemExit("unexpected go operation")
''',
                    "go-licenses": '''#!/usr/bin/env python3
import json, os, pathlib, sys
pathlib.Path(os.environ["NOTICE_TEST_ROOT"], "checked.json").write_text(json.dumps(sys.argv[1:]))
raise SystemExit(37)
''',
                    "cargo-about": "#!/bin/sh\nexit 0\n",
                    "bun": "#!/bin/sh\nexit 0\n",
                }
                for name, body in stubs.items():
                    executable = binaries / name
                    executable.write_text(body)
                    executable.chmod(0o755)
                env = dict(os.environ, GOOS=platform, NOTICE_TEST_ROOT=str(root),
                           PATH=f"{binaries}{os.pathsep}{os.environ['PATH']}")
                result = subprocess.run(["bash", str(scripts / "licenses-notices.sh")],
                                        env=env, text=True, capture_output=True, check=False)
                self.assertEqual(result.returncode, 37, result.stderr)
                query = json.loads((root / "queried.json").read_text())
                self.assertIn("./cmd/pw-launch", query)
                checked = json.loads((root / "checked.json").read_text())
                packages = [arg for arg in checked[1:] if not arg.startswith("--")]
                expected = ["fixture/cmd/engine", "fixture/cmd/logs"]
                if platform == "linux":
                    expected.append("fixture/cmd/linux-only")
                self.assertEqual(packages, expected)
