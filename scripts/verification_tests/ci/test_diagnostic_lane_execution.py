"""Manual diagnostics run complete lanes without narrowing protected qualification."""
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parents[3]


class DiagnosticLaneTests(unittest.TestCase):
    def plan(self, profile, lane, event='workflow_dispatch'):
        environment = {**os.environ, 'GITHUB_EVENT_NAME': event}
        environment.pop('GITHUB_STEP_SUMMARY', None)
        return subprocess.run([sys.executable, str(ROOT / 'scripts/ci_verification.py'),
                               'matrix', profile, '--lane', lane],
                              cwd=ROOT, env=environment, text=True, capture_output=True, timeout=30)

    def test_manual_selection_keeps_every_shard_of_the_requested_lane(self):
        for lane, shards in [('runner', ['']), ('behavior', ['1/2', '2/2'])]:
            with self.subTest(lane=lane):
                result = self.plan('check', lane)
                self.assertEqual(result.returncode, 0, result.stderr)
                rows = json.loads(result.stdout)['include']
                self.assertEqual({row['lane'] for row in rows}, {lane})
                self.assertEqual([row['shard'] for row in rows], shards)

    def test_protected_events_and_unknown_or_wrong_profile_lanes_are_refused(self):
        for profile, lane, event in [('check', 'runner', 'pull_request'),
                                     ('integration', 'runner', 'merge_group'),
                                     ('check', 'missing', 'workflow_dispatch'),
                                     ('fast', 'runner', 'workflow_dispatch')]:
            with self.subTest(profile=profile, lane=lane, event=event):
                result = self.plan(profile, lane, event)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '')
                self.assertIn('diagnostic lane', result.stderr)
