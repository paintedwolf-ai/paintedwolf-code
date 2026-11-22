"""Analysis preparation excludes runtime engines and native toolchains, and retries a failed pinned
Go fetch without hiding its failure."""
import copy
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import go_retry
from ci_policy import hosted_plan

ROOT = Path(__file__).resolve().parents[3]


class AnalysisSetupTests(unittest.TestCase):
    def test_analysis_prepares_source_tools_and_rejects_unknown_profiles(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts/ci_policy"
            scripts.mkdir(parents=True)
            shutil.copyfile(ROOT / "scripts/ci_policy/provision.sh", scripts / "provision.sh")
            shutil.copyfile(ROOT / "scripts/ci_policy/go_retry.py", scripts / "go_retry.py")
            for name in ("lycaon", "scripts/commentlint", "tools"):
                (root / name).mkdir(parents=True)
            go = root / "tools/go"
            go.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALLS"\n')
            go.chmod(0o755)
            (root / "scripts/workflow_lint").mkdir()
            (root / "scripts/workflow_lint/__init__.py").write_text("")
            (root / "scripts/workflow_lint/__main__.py").write_text(
                'import os,sys\nwith open(os.environ["CALLS"], "a") as calls: calls.write("workflow " + " ".join(sys.argv[1:]) + "\\n")\n')
            calls = root / "calls"
            environment = {**os.environ, "PATH": str(root / "tools") + os.pathsep + os.environ["PATH"], "CALLS": str(calls)}
            for args, code in [(["analysis"], 0), (["unknown"], 2), (["analysis", "extra"], 2)]:
                result = subprocess.run(["bash", str(scripts / "provision.sh"), *args], env=environment,
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, code, result.stderr)
            self.assertEqual(calls.read_text().splitlines(), ["mod download", "mod download", "workflow --provision"])

    def test_catalog_selects_analysis_without_changing_checks_or_capacity(self):
        analysis = [row for row in hosted_plan.matrix("fast")["include"] if row["setup"] == "analysis"]
        self.assertTrue(analysis)
        self.assertNotIn("none", {row["analysis"] for row in analysis})

class GoRetryTests(unittest.TestCase):
    def test_failed_download_retries_then_succeeds(self):
        command = ["go", "mod", "download"]
        failure = subprocess.CalledProcessError(1, command)
        with patch.object(go_retry.subprocess, "run", side_effect=[failure, failure, subprocess.CompletedProcess(command, 0)]) as run, \
                patch.object(go_retry.time, "sleep") as sleep:
            go_retry.run_go(command, cwd="/tmp/module")
        self.assertEqual(run.call_count, 3)
        run.assert_any_call(command, cwd="/tmp/module", env=None, check=True)
        self.assertEqual(sleep.call_count, 2)

    def test_permanent_failure_is_returned_after_bounded_attempts(self):
        command = ["go", "install", "example.com/tool@v1.0.0"]
        failure = subprocess.CalledProcessError(42, command)
        with patch.object(go_retry.subprocess, "run", side_effect=failure) as run, \
                patch.object(go_retry.time, "sleep") as sleep:
            with self.assertRaises(subprocess.CalledProcessError) as raised:
                go_retry.run_go(command, cwd="/tmp/module", attempts=3)
        self.assertEqual(raised.exception.returncode, 42)
        self.assertEqual(run.call_count, 3)
        self.assertEqual(sleep.call_count, 2)
