"""Engine staging builds macOS executables at the platform floor without build-host paths."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]
STUBBED_SCRIPTS = ["resolve-opengrep.sh", "stage-bundled-opengrep.sh", "build-document-core.sh", "build-decide.sh",
                   "stage-decide-heads.sh", "generation/run.sh", "stage-macos-host.py"]
GO_STUB = """#!{python}
import json, os, pathlib, sys
args = sys.argv[1:]
if args[0] == "run":
    print("aWRlbnRpdHk=")
    sys.exit(0)
assert args[0] == "build", args
output = pathlib.Path(args[args.index("-o") + 1])
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text("binary")
with open(os.environ["STAGE_TEST_BUILDS"], "a") as log:
    log.write(json.dumps({{"package": args[-1], "args": args,
                          "deployment_target": os.environ.get("MACOSX_DEPLOYMENT_TARGET"),
                          "cgo": os.environ.get("CGO_ENABLED")}}) + "\\n")
"""


class StageEngineFloorTests(unittest.TestCase):
    def test_macos_executables_build_at_the_declared_floor(self):
        with tempfile.TemporaryDirectory(prefix="stage-engine-floor-") as temporary:
            work = Path(temporary)
            fixture, tools, builds = work / "repo", work / "tools", work / "builds.jsonl"
            (fixture / "scripts" / "generation").mkdir(parents=True)
            shutil.copy2(ROOT / "scripts" / "stage-engine.sh", fixture / "scripts" / "stage-engine.sh")
            for name in STUBBED_SCRIPTS:
                (fixture / "scripts" / name).write_text("")
            (fixture / "scripts" / "resolve-opengrep.sh").write_text(f"echo {work / 'opengrep'}\n")
            (fixture / "schemas").mkdir()
            (fixture / "VERSION").write_text("1.2.3\n")
            floor = fixture / "lycaon" / "internal" / "platformfloor" / "macos_floor.txt"
            floor.parent.mkdir(parents=True)
            # A sentinel floor no host SDK defaults to.
            floor.write_text("13.7\n")
            tools.mkdir()
            (tools / "go").write_text(GO_STUB.format(python=sys.executable))
            (tools / "rustc").write_text("#!/bin/sh\necho aarch64-apple-darwin\n")
            for tool in tools.iterdir():
                tool.chmod(0o700)
            env = {key: value for key, value in os.environ.items()
                   if key not in {"MACOSX_DEPLOYMENT_TARGET", "CGO_ENABLED", "LYCAON_OPENGREP_CANDIDATE"}}
            env.update(PATH=str(tools) + os.pathsep + env["PATH"], STAGE_TEST_BUILDS=str(builds))
            result = subprocess.run(["bash", str(fixture / "scripts" / "stage-engine.sh"), "--minimal"],
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            recorded = [json.loads(line) for line in builds.read_text().splitlines()]
            self.assertEqual([build["package"] for build in recorded], ["./cmd/lycaon", "./cmd/pw-logs"])
            for build in recorded:
                with self.subTest(package=build["package"]):
                    self.assertEqual(build["deployment_target"], "13.7")
                    self.assertEqual(build["cgo"], "1")
                    self.assertIn("-trimpath", build["args"])


if __name__ == "__main__":
    unittest.main()
