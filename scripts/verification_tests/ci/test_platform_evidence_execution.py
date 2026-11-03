"""The standalone upgrade gate retains its error without masking its verdict."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]


class PlatformEvidenceTests(unittest.TestCase):
    def test_linux_confinement_legs_execute_once_before_and_after_the_build(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/platform-verification.yml").read_text())
        steps = {step.get("id"): step for step in workflow["jobs"]["upgrade-corpus"]["steps"]}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            task = root / "task"
            task.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
            task.chmod(0o755)
            legs = []
            for name in ("launch", "confinement"):
                result = subprocess.run(["bash", "-e", "-c", steps[name]["run"]], cwd=root,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                arguments = result.stdout.splitlines()
                self.assertEqual(arguments[:2], ["test:confine:linux", "--"])
                legs.extend(arguments[2:])
            self.assertEqual(legs, ["launch", "sandbox", "supervisor", "browser", "helpers"])
            self.assertEqual(steps["build"]["if"], "${{ !cancelled() }}")
            self.assertEqual(steps["corpus"]["if"], "${{ !cancelled() && steps.build.outcome == 'success' }}")

    def test_upgrade_output_survives_success_and_failure(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/platform-verification.yml").read_text())
        step = next(item for item in workflow["jobs"]["upgrade-corpus"]["steps"]
                    if item.get("name") == "Boot upgrade corpus fixtures")
        for code in (0, 23):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                (root / "scripts").mkdir()
                shutil.copyfile(ROOT / "scripts/artifact_paths.py", root / "scripts/artifact_paths.py")
                task = root / "task"
                task.write_text("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = upgrade:corpus:boot ] || exit 98\n"
                                f"echo fixture-start\necho fixture-diagnostic >&2\nexit {code}\n")
                task.chmod(0o755)
                artifacts = root / "evidence"
                environment = {**os.environ, "GITHUB_WORKSPACE": str(root),
                               "PW_TEST_ARTIFACT_ROOT": str(artifacts)}
                result = subprocess.run(["bash", "-e", "-c", step["run"]], cwd=root, env=environment,
                                        capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, code, result.stderr)
                expected = "fixture-start\nfixture-diagnostic\n"
                self.assertEqual(result.stdout, expected)
                self.assertEqual((artifacts / "ci/upgrade-corpus.log").read_text(), expected)
