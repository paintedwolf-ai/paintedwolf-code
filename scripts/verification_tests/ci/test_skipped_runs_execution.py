"""Skipped CI runs on a head are deleted without ever risking the required check."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import yaml

from ci_policy import skipped_runs as s

ROOT = Path(__file__).resolve().parents[3]
HEAD = 'a' * 40
CURRENT = 500
FAKE_GH = '''#!/usr/bin/env python3
import json, os, sys
method, path = sys.argv[3], sys.argv[4]
with open(os.environ['GH_CALLS'], 'a') as calls:
    calls.write(f'{method} {path}\\n')
if os.environ.get('GH_FAIL') in (method, 'ALL'):
    print('HTTP 403: Resource not accessible', file=sys.stderr)
    sys.exit(1)
print(os.environ['GH_RUNS'] if method == 'GET' else '')
'''


def run(identifier=1, **change):
    return {'id': identifier, 'head_sha': HEAD, 'path': s.WORKFLOW, 'event': 'pull_request',
            'status': 'completed', 'conclusion': 'skipped', **change}


class SelectionTest(unittest.TestCase):
    def test_selects_only_a_completed_skipped_ci_run_on_this_head(self):
        runs = [run(1), run(2),
                run(CURRENT),
                run(3, head_sha='b' * 40),
                run(4, path='.github/workflows/dco.yml'),
                run(5, event='workflow_dispatch'),
                run(6, status='in_progress', conclusion=None),
                run(7, status='queued', conclusion=None),
                *[run(10 + i, conclusion=conclusion) for i, conclusion in enumerate(
                    ['success', 'failure', 'cancelled', 'timed_out', 'neutral', 'action_required', 'stale'])]]
        self.assertEqual([r['id'] for r in s.selected(runs, HEAD, CURRENT)], [1, 2])

    def test_deletes_each_selected_run_from_a_query_bound_to_the_head(self):
        with patch.object(s, 'pages', return_value=[run(1), run(2), run(3, conclusion='success')]) as pages, \
                patch.object(s, 'api') as api:
            self.assertEqual(s.delete_skipped_runs('o/r', HEAD, CURRENT), [])
        self.assertIn(f'head_sha={HEAD}', pages.call_args.args[0])
        self.assertEqual([call.args for call in api.call_args_list],
                         [('repos/o/r/actions/runs/1', 'DELETE'), ('repos/o/r/actions/runs/2', 'DELETE')])

    def test_one_refused_delete_still_attempts_the_rest(self):
        refused = subprocess.CalledProcessError(1, ['gh'], stderr='HTTP 403\nforbidden')
        with patch.object(s, 'pages', return_value=[run(1), run(2)]), \
                patch.object(s, 'api', side_effect=[refused, None]) as api:
            failures = s.delete_skipped_runs('o/r', HEAD, CURRENT)
        self.assertEqual(api.call_count, 2)
        self.assertEqual(failures, ['could not delete skipped CI run 1: HTTP 403 forbidden'])

    def test_an_unexpected_listing_is_reported(self):
        for failure in (subprocess.TimeoutExpired(['gh'], 1), FileNotFoundError('gh'), ValueError('not json'), KeyError('id')):
            with patch.object(s, 'pages', side_effect=failure), patch.object(s, 'api') as api:
                failures = s.delete_skipped_runs('o/r', HEAD, CURRENT)
            self.assertEqual(len(failures), 1)
            self.assertIn('could not list CI runs', failures[0])
            api.assert_not_called()


class CommandTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        fake = self.directory / 'gh'
        fake.write_text(FAKE_GH)
        fake.chmod(fake.stat().st_mode | stat.S_IEXEC)
        self.calls = self.directory / 'calls'
        self.calls.touch()

    def command(self, **environment):
        return subprocess.run(
            [sys.executable, 'scripts/ci_verification.py', 'delete-skipped-runs', HEAD], cwd=ROOT, capture_output=True, text=True,
            timeout=30, env={**os.environ, 'PATH': f'{self.directory}{os.pathsep}{os.environ["PATH"]}', 'GH_CALLS': str(self.calls),
                             'GH_RUNS': json.dumps({'workflow_runs': [run(1), run(CURRENT), run(2, conclusion='success')]}),
                             'GITHUB_REPOSITORY': 'o/r', 'GITHUB_RUN_ID': str(CURRENT), **environment})

    def deletes(self):
        return [line for line in self.calls.read_text().splitlines() if line.startswith('DELETE')]

    def test_deletes_only_the_other_skipped_run(self):
        result = self.command()
        self.assertEqual((result.returncode, result.stdout), (0, ''), result.stderr)
        self.assertEqual(self.deletes(), ['DELETE repos/o/r/actions/runs/1'])

    def test_failed_cleanup_reports_a_warning_and_exits_cleanly(self):
        for failing, expected in (('DELETE', 'could not delete skipped CI run 1'), ('ALL', f'could not list CI runs for {HEAD}')):
            result = self.command(GH_FAIL=failing)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f'::warning::{expected}: HTTP 403', result.stdout)


class WorkflowTest(unittest.TestCase):
    def test_required_check_cannot_fail_on_cleanup_and_alone_holds_the_token(self):
        job = yaml.safe_load((ROOT / '.github/workflows/ci.yml').read_text())['jobs']['check']
        self.assertEqual(job['permissions']['actions'], 'write')
        checkout, gate, cleanup = job['steps']
        self.assertIs(checkout['with']['persist-credentials'], False)
        self.assertNotIn('GH_TOKEN', gate['env'])
        self.assertIn('delete-skipped-runs', cleanup['run'])
        self.assertIs(cleanup['continue-on-error'], True)
        self.assertLess(cleanup['timeout-minutes'], job['timeout-minutes'])
        self.assertEqual(cleanup['env']['HEAD_SHA'], '${{ github.event.pull_request.head.sha }}')


if __name__ == '__main__':
    unittest.main()
