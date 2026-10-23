"""Document builds retain compiler state and stage only successful outputs."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class DocumentCacheTests(unittest.TestCase):
    def test_repeated_sources_share_compiler_state_and_keep_outputs_isolated(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            (root / "scripts/toolchain").mkdir(parents=True)
            for name in ("build-document-core.sh", "artifact-paths.sh", "artifact_paths.py", "digest-run-lock.sh",
                         "toolchain/rust-path-remap.sh"):
                shutil.copyfile(ROOT / "scripts" / name, root / "scripts" / name)
            floor = root / "lycaon/internal/platformfloor/macos_floor.txt"
            floor.parent.mkdir(parents=True)
            floor.write_text("14.0\n")
            tools = root / "tools"
            tools.mkdir()
            cargo = tools / "cargo"
            cargo.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
compiler = pathlib.Path(os.environ['PW_COMPILER_CACHE_DIR'])
assert (compiler / 'document-core-build.lockdir').is_dir()
with open(os.environ['CALLS'], 'a') as output:
    output.write(json.dumps({'args': sys.argv[1:], 'target': os.environ['CARGO_TARGET_DIR']}) + '\\n')
if os.environ.get('FAIL_BUILD'):
    sys.exit(23)
target = pathlib.Path(os.environ['CARGO_TARGET_DIR']) / 'release'
target.mkdir(parents=True, exist_ok=True)
(target / 'dependency').write_text('retained')
(target / 'pw-document-core').write_text(os.environ['SOURCE'])
''')
            cargo.chmod(0o755)
            compiler = root / "compiler"
            calls = root / "calls"
            env = {**os.environ, "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                   "PW_COMPILER_CACHE_DIR": str(compiler), "PW_BUILD_DIR": str(root / "build"),
                   "CARGO_TARGET_DIR": str(root / "unrelated"), "CALLS": str(calls), "SOURCE": "first"}
            script = root / "scripts/build-document-core.sh"
            def run(*args, **extra):
                return subprocess.run(["bash", str(script), *args], env={**env, **extra},
                                      capture_output=True, text=True, timeout=20)
            first = run()
            self.assertEqual(first.returncode, 0, first.stderr)
            self.assertEqual(Path(first.stdout.strip()).read_text(), "first")
            second = root / "staged/second"
            result = run("--output", str(second), SOURCE="second")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(second.read_text(), "second")
            self.assertEqual(Path(first.stdout.strip()).read_text(), "first")
            self.assertEqual((compiler / "document-core/release/dependency").read_text(), "retained")
            failed = run("--output", str(second), FAIL_BUILD="1", SOURCE="third")
            self.assertEqual(failed.returncode, 23, failed.stderr)
            self.assertEqual(second.read_text(), "second")
            self.assertFalse((compiler / "document-core-build.lockdir").exists())
            self.assertFalse((root / "unrelated").exists())
            self.assertEqual(run("--unknown").returncode, 2)
            records = [json.loads(line) for line in calls.read_text().splitlines()]
            self.assertEqual(len(records), 3)
            for record in records:
                self.assertEqual(record['target'], str(compiler / "document-core"))
                self.assertIn('--locked', record['args'])
                self.assertIn('--release', record['args'])
