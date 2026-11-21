import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class NoticeEntrypointsExecutionTest(unittest.TestCase):
    def test_publication_replaces_both_copies_and_preserves_open_readers(self):
        script = Path(__file__).resolve().parents[2] / "stage-notices.py"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "assembled"
            source.write_text("new notices")
            copies = [root / "THIRD-PARTY-NOTICES.md", root / "shell/THIRD-PARTY-NOTICES.md"]
            copies[1].parent.mkdir()
            copies[0].write_text("previous notices")
            with copies[0].open() as reader:
                subprocess.run([sys.executable, str(script), str(source), *map(str, copies)], check=True)
                self.assertEqual(reader.read(), "previous notices")
            self.assertEqual([path.read_text() for path in copies], ["new notices"] * 2)

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
        if "./cmd/pw-service-guest" in sys.argv:
            print("fixture/cmd/private-guest")
else:
    raise SystemExit("unexpected go operation")
''',
                    "go-licenses": '''#!/usr/bin/env python3
import json, os, pathlib, sys
root = pathlib.Path(os.environ["NOTICE_TEST_ROOT"])
if sys.argv[1] == "check":
    if "./cmd/pw-service-guest" in sys.argv:
        (root / "guest_checked.json").write_text(json.dumps([os.environ["GOOS"], os.environ["CGO_ENABLED"]]))
    else:
        (root / "checked.json").write_text(json.dumps(sys.argv[1:]))
        if os.environ["GOOS"] != "darwin":
            raise SystemExit(37)
elif sys.argv[1] == "csv":
    print("shared,https://example.com,MIT")
    if "./cmd/pw-service-guest" in sys.argv:
        print("guest,https://example.com,MIT")
elif sys.argv[1] == "save":
    path = next(arg.split("=", 1)[1] for arg in sys.argv if arg.startswith("--save_path="))
    dest = pathlib.Path(path)
    dest.mkdir()
    names = ("shared", "guest") if "./cmd/pw-service-guest" in sys.argv else ("shared", "host")
    for name in names:
        file = dest / name
        file.write_text(name)
        file.chmod(0o444)
''',
                    "cargo-about": "#!/bin/sh\nexit 0\n",
                    "bun": '''#!/usr/bin/env python3
import pathlib, sys
work = pathlib.Path(sys.argv[sys.argv.index("--out") + 1]).parent
for name in ("host", "guest", "shared"):
    assert (work / "go-save" / name).read_text() == name
assert len((work / "go.csv").read_text().splitlines()) == 2
raise SystemExit(37)
''',
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
                    expected.extend(["fixture/cmd/linux-only", "fixture/cmd/private-guest"])
                if platform == "darwin":
                    self.assertEqual(json.loads((root / "guest_checked.json").read_text()), ["linux", "0"])
                self.assertEqual(packages, expected)
