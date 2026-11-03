"""Manual diagnostics run complete lanes without narrowing protected qualification."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]


class DiagnosticLaneTests(unittest.TestCase):
    def test_complete_platform_diagnostic_has_no_queue_or_impact_requirement(self):
        result = self.plan('integration', 'confinement-arm64')
        self.assertEqual(result.returncode, 0, result.stderr)
        rows = json.loads(result.stdout)['include']
        self.assertEqual([(row['lane'], row['runner']) for row in rows],
                         [('confinement-arm64', 'ubuntu-24.04-arm')])
        workflow = yaml.safe_load((ROOT / '.github/workflows/verification.yml').read_text())
        watch = next(step for step in workflow['jobs']['verify']['steps']
                     if step.get('uses') == './.github/actions/merge-group-watch')
        self.assertEqual(watch['if'], "inputs.lane == ''")
        caller = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())
        self.assertIn("inputs.lane == ''", caller['jobs']['integration']['with']['affected'])

    def test_normal_dispatch_binds_the_queue_but_a_diagnostic_does_not(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/verification.yml').read_text())
        command = next(step['run'] for step in workflow['jobs']['plan']['steps']
                       if step.get('name') == 'Resolve verification jobs')
        for lane in ('', 'confinement-arm64'):
            with self.subTest(lane=lane), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                fake = root / 'python3'
                fake.write_text(f'#!{sys.executable}\n' + '''import json, os, sys
with open(os.environ['FIXTURE_CALLS'], 'a') as stream:
 stream.write(json.dumps(sys.argv[1:]) + '\\n')
args = sys.argv[1:]
if args[:2] == ['-m', 'ci_policy.queue_dispatch']: print('queued-source-base')
elif args[1:2] == ['timings']: print('{"lanes": {}, "packages": {}}')
elif args[1:2] == ['matrix']: print('{"include": []}')
elif args[1:2] == ['max-parallel']: print('4')
else: raise SystemExit(2)
''')
                fake.chmod(0o755)
                calls = root / 'calls.jsonl'
                result = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', command], cwd=ROOT,
                    env={**os.environ, 'PATH': str(root) + os.pathsep + os.environ['PATH'],
                         'PROFILE': 'integration', 'GITHUB_EVENT_NAME': 'workflow_dispatch',
                         'LANE': lane, 'AFFECTED': 'false', 'PLATFORM': 'false', 'REUSE': 'false',
                         'SUITE': 'all', 'RUNNER_TEMP': str(root), 'GITHUB_OUTPUT': str(root / 'outputs'),
                         'FIXTURE_CALLS': str(calls)}, text=True, capture_output=True, timeout=30)
                self.assertEqual(result.returncode, 0, result.stderr)
                observed = [json.loads(line) for line in calls.read_text().splitlines()]
                self.assertEqual(sum(call == ['-m', 'ci_policy.queue_dispatch'] for call in observed),
                                 0 if lane else 1)

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
