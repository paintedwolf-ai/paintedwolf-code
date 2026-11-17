"""Publication is downstream of a credential-free desktop upgrade gate."""
from pathlib import Path
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class WorkflowTests(unittest.TestCase):
    def dry_run(self, *arguments):
        with tempfile.TemporaryDirectory() as temporary:
            tools = Path(temporary)
            for name in ("bun", "minisign"):
                executable = tools / name
                executable.write_text("#!/bin/sh\nexit 97\n")
                executable.chmod(0o755)
            environment = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"])
            return subprocess.run(["bash", str(ROOT / "task"), "--dry", "upgrade:rehearse", "--", *arguments],
                                  cwd=ROOT, env=environment, capture_output=True, text=True, timeout=30)

    def test_existing_task_dispatches_bundle_mode_without_building_a_replacement(self):
        result = self.dry_run("--bundle", "--fixtures", "/fixtures", "--evidence", "/evidence", "--case", "1.0.2-preview")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("bundle-rehearse --fixtures /fixtures --evidence /evidence", " ".join((result.stdout + result.stderr).split()))
        self.assertNotIn("build:lycaon-dev", result.stdout + result.stderr)

    def test_preparation_uses_its_own_managed_mode_without_building(self):
        result = self.dry_run("--prepare-bundle", "--manifest", "/manifest", "--archives", "/archives", "--source", "/source", "--output", "/output")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("bundle-prepare --manifest /manifest", " ".join((result.stdout + result.stderr).split()))
        self.assertNotIn("build:lycaon-dev", result.stdout + result.stderr)

    def test_bundle_commands_reach_their_argument_parsers(self):
        for command in ("bundle-prepare", "bundle-rehearse", "bundle-serve"):
            result = subprocess.run([sys.executable, "scripts/release_control.py", command, "--help"], cwd=ROOT,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("usage:", result.stdout)

class ReplayTests(unittest.TestCase):

    def test_fixture_admission_requires_preparation_and_exact_attempt(self):
        from unittest.mock import patch
        from release_upgrade import replay
        run = {"path": ".github/workflows/release.yml", "status": "completed", "event": "push",
               "head_repository": {"full_name": "owner/repo"}, "head_branch": "v1.0.3-rc1", "run_attempt": 2, "head_sha": "a" * 40}
        def rows(path, key):
            if key == "jobs":
                self.assertIn("/attempts/2/jobs", path)
                return [{"name": "prepare-bundle-upgrade", "conclusion": "success"}]
            return [{"name": "bundle-upgrade-fixtures-1", "expired": False, "id": 1},
                    {"name": "bundle-upgrade-fixtures-2", "expired": False, "id": 2}]
        with patch.dict(os.environ, GITHUB_REPOSITORY="owner/repo"), patch.object(replay, "api", return_value=run), patch.object(replay, "pages", side_effect=rows):
            self.assertEqual(replay.select("123", "")["artifact_id"], 2)
            for field, value in (("status", "in_progress"), ("path", "ci.yml"), ("head_branch", "unmerged-feature"), ("head_repository", {"full_name": "foreign/repo"})):
                with patch.dict(run, {field: value}), self.assertRaises(ValueError):
                    replay.select("123", "")
            with patch.object(replay, "pages", return_value=[]), self.assertRaises(ValueError):
                replay.select("123", "")
            with self.assertRaises(ValueError):
                replay.select("123", "v1.0.3-rc1")
            with self.assertRaises(ValueError):
                replay.select("../123", "")
