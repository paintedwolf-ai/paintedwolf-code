"""Decision builds retain compiler inputs while isolating staged executables."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from artifact_paths import compiler_cache_dir


class NativeCacheTests(unittest.TestCase):
    def test_compiler_directory_is_scoped_to_a_checkout_and_an_explicit_override(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(os.environ, {"PW_ARTIFACT_ROOT": directory}, clear=True):
            self.assertEqual(compiler_cache_dir("/source"), Path(directory).resolve() / "compiler-cache")
            with patch.dict(os.environ, {"PW_COMPILER_CACHE_DIR": directory + "/custom"}):
                self.assertEqual(compiler_cache_dir("/snapshot"), Path(directory).resolve() / "custom")

    def test_decision_build_ignores_tauri_target_and_stages_each_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = Path(__file__).resolve().parents[2]
            scripts = root / "scripts"
            (scripts / "toolchain").mkdir(parents=True)
            for name in ("build-decide.sh", "artifact-paths.sh", "artifact_paths.py", "digest-run-lock.sh"):
                shutil.copyfile(source / name, scripts / name)
            shutil.copytree(source / "toolchain", scripts / "toolchain", dirs_exist_ok=True)
            floor = root / "lycaon/internal/platformfloor/macos_floor.txt"
            floor.parent.mkdir(parents=True)
            floor.write_text("14.0\n")
            (scripts / "decide-features.sh").write_text("#!/bin/bash\necho cpu\n")
            tools = root / "tools"
            tools.mkdir()
            cargo = tools / "cargo"
            cargo.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
with open(os.environ['NATIVE_CALLS'], 'a') as output:
    output.write(json.dumps({'arguments': sys.argv[1:], 'target': os.environ['CARGO_TARGET_DIR'], 'flags': os.environ['CARGO_ENCODED_RUSTFLAGS'], 'metal': os.environ['MLX_RS_METAL_PATH'], 'floor': os.environ.get('MACOSX_DEPLOYMENT_TARGET')}) + '\\n')
if sys.argv[1] == 'build':
    assert (pathlib.Path(os.environ['PW_COMPILER_CACHE_DIR']) / 'decide-build.lockdir').is_dir()
if os.environ.get('NATIVE_FAIL'):
    sys.exit(19)
target = pathlib.Path(os.environ['CARGO_TARGET_DIR']) / 'release'
target.mkdir(parents=True, exist_ok=True)
(target / 'bialy').write_text('qualified fixture')
''')
            cargo.chmod(0o755)
            calls = root / "calls"
            compiler = root / "compiler"
            env = {**os.environ, "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                   "PW_COMPILER_CACHE_DIR": str(compiler), "PW_BIN_DIR": str(root / "pinned"),
                   "PW_ARTIFACT_ROOT": str(root / "artifacts"), "PW_LOCK_ROOT": str(root / "locks"),
                   "CARGO_TARGET_DIR": str(root / "tauri"), "NATIVE_CALLS": str(calls)}
            for name in ("first", "second"):
                output = root / name
                result = subprocess.run(["bash", str(scripts / "build-decide.sh")],
                                        env={**env, "PW_BUILD_DIR": str(output)}, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((output / "bialy").read_text(), "qualified fixture")
                self.assertFalse((compiler / "decide-build.lockdir").exists())
            records = [json.loads(line) for line in calls.read_text().splitlines()]
            self.assertEqual([record["target"] for record in records], [str(compiler / "decide")] * 2)
            self.assertTrue(all("--locked" in record["arguments"] and "--release" in record["arguments"]
                                for record in records))
            self.assertFalse((root / "tauri").exists())
            for arguments in (["--no-run"], ["--quiet"]):
                result = subprocess.run(["bash", str(scripts / "toolchain/decide-test.sh"), *arguments],
                                        env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
            prepared = [json.loads(line) for line in calls.read_text().splitlines()]
            for record in prepared:
                self.assertEqual({k: v for k, v in record.items() if k != "arguments"},
                                 {k: v for k, v in prepared[0].items() if k != "arguments"})
            self.assertIn("--no-run", prepared[-2]["arguments"])
            self.assertNotIn("--no-run", prepared[-1]["arguments"])

            failed = subprocess.run(["bash", str(scripts / "build-decide.sh")],
                                    env={**env, "PW_BUILD_DIR": str(root / "failed"), "NATIVE_FAIL": "1"},
                                    capture_output=True, text=True)
            self.assertEqual(failed.returncode, 19, failed.stderr)
            self.assertFalse((compiler / "decide-build.lockdir").exists())
            self.assertFalse((root / "failed/bialy").exists())
