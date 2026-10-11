"""Workflow commands preserve machine-readable plans and required-job verdicts."""
import json
import os
from pathlib import Path
import subprocess
import sys
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
