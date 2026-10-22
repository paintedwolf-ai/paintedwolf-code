"""Workflow commands preserve machine-readable plans and required-job verdicts."""
import json
import os
import shlex
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[3]


class WorkflowCommandTests(unittest.TestCase):
    def command(self, *arguments, **environment):
        return subprocess.run([sys.executable, 'scripts/ci_verification.py', *arguments],
                              cwd=ROOT, env={**os.environ, **environment},
                              capture_output=True, text=True, timeout=30)

    def test_ready_plan_and_capacity_are_workflow_outputs(self):
        matrix = self.command('matrix', 'fast')
        self.assertEqual(matrix.returncode, 0, matrix.stderr)
        rows = json.loads(matrix.stdout)['include']
        self.assertEqual([row['lane'] for row in rows], ['ready'])
        capacity = self.command('max-parallel', 'fast')
        self.assertEqual(capacity.returncode, 0, capacity.stderr)
        self.assertEqual(int(capacity.stdout), 1)

    def test_required_job_verdict_reaches_the_process_exit(self):
        results = json.dumps({'verification': {'result': 'success'}, 'platform': {'result': 'skipped'}})
        passed = self.command('gate', '--skipped', 'platform', NEEDS_JSON=results)
        self.assertEqual(passed.returncode, 0, passed.stderr)
        draft = self.command('gate', '--skipped', 'platform', '--draft', 'true', NEEDS_JSON=results)
        self.assertNotEqual(draft.returncode, 0)
        self.assertIn('draft pull requests are not verified', draft.stderr)

    def test_schedule_summary_preserves_matrix_json_output(self):
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            measured = Path(directory) / "timings.json"
            measured.write_text(json.dumps({"runs": [42], "lanes": {"ready": 250}}))
            result = self.command('matrix', 'fast', '--timings', str(measured),
                                  GITHUB_STEP_SUMMARY=str(summary))
            self.assertEqual(result.returncode, 0, result.stderr)
            rows = json.loads(result.stdout)['include']
            self.assertEqual(rows[0]['timing_basis'], 'measured')
            self.assertEqual(rows[0]['expected_seconds'], 250)
            self.assertIn('Estimate basis: `measured`', summary.read_text())
            self.assertIn('Qualification timing sources: 42', summary.read_text())

    def test_nix_workflow_loads_the_shared_shipped_verification_profile(self):
        workflow = (ROOT / ".github/workflows/linux-nix-qualification.yml").read_text()
        step = workflow.split("      - name: Allow native verification engines to create user namespaces\n", 1)[1]
        step = step.split("\n      - name:", 1)[0]
        commands = [shlex.split(line.strip()) for line in step.splitlines() if line.strip().startswith("sudo ")]
        install, reload = commands
        self.assertEqual(install[:4], ["sudo", "install", "-m", "0644"])
        profile = ROOT / install[4]
        self.assertTrue(profile.is_file(), "The frozen source must ship the profile its workflow installs")
        shared = (ROOT / ".github/actions/setup-verification/action.yml").read_text()
        self.assertIn("$GITHUB_ACTION_PATH/" + profile.name, shared)
        self.assertEqual(reload, ["sudo", "apparmor_parser", "-r", install[5]])
        self.assertIn("apparmor_restrict_unprivileged_userns 2>/dev/null)", step)
        self.assertIn("flags=(unconfined)", profile.read_text())
        self.assertIn("userns,", profile.read_text())
        self.assertNotIn("sysctl", step)
