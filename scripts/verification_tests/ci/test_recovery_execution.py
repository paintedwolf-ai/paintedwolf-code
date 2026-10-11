"""Recovery consumes complete source-identified evidence without retrying source failures."""
import unittest
from unittest.mock import patch

from ci_policy import recovery


class RecoveryTests(unittest.TestCase):
    def run_recovery(self, records, failures=(), jobs=(), attempt=1, workflow='ci.yml'):
        run = {'id': 42, 'path': '.github/workflows/' + workflow, 'event': 'merge_group',
               'run_attempt': attempt, 'conclusion': 'failure', 'html_url': 'https://example.test/run'}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(recovery, 'evidence', return_value=(records, list(failures))), \
                patch.object(recovery, 'pages', return_value=iter(jobs)), \
                patch.object(recovery, 'api') as api, \
                patch.object(recovery, 'propose_revert') as revert:
            recovery.recover(run)
        return api, revert

    def test_every_failed_job_needs_matching_oom_evidence(self):
        failed = {'name': 'verification / behavior', 'conclusion': 'failure'}
        aggregate = {'name': 'check', 'conclusion': 'failure'}
        for records, jobs, attempt, retries in [
            ([{'classification': 'runner_oom'}], [failed, aggregate], 1, True),
            ([{'classification': 'runner_oom'}], [failed, aggregate], 2, False),
            ([{'classification': 'runner_oom'}], [failed, failed, aggregate], 1, False),
            ([{'classification': 'test_failure'}], [failed, aggregate], 1, False),
            ([], [failed, aggregate], 1, False),
        ]:
            with self.subTest(records=records, jobs=jobs, attempt=attempt):
                api, revert = self.run_recovery(records, jobs=jobs, attempt=attempt)
                if retries:
                    api.assert_called_once_with('repos/owner/repo/actions/runs/42/rerun-failed-jobs', 'POST')
                else:
                    api.assert_not_called()
                revert.assert_not_called()

    def test_qualification_source_failure_reaches_attribution_without_retry(self):
        failures = [{'status': 'failed', 'tests': ['TestRegression']}]
        api, revert = self.run_recovery([{'classification': 'test_failure'}], failures,
            [{'name': 'verification / behavior', 'conclusion': 'failure'}], workflow='qualification.yml')
        api.assert_not_called()
        self.assertEqual(revert.call_args.args[1], failures)

    def test_qualification_revert_links_evidence_without_creating_issue(self):
        sha = 'a' * 40
        run = dict(head_sha=sha, path='.github/workflows/qualification.yml', event='push',
                   head_branch='main', conclusion='failure', html_url='https://example.test/run')
        pull = dict(number=7, merged_at='date', base={'ref': 'main'}, merge_commit_sha=sha)
        commit = {'parents': [{'sha': 'b' * 40}]}
        responses = [commit, {'object': {'sha': sha}}, None]
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(recovery, 'api', side_effect=responses) as api, \
                patch.object(recovery, 'pages', side_effect=[[pull], []]), \
                patch.object(recovery, 'qualified', return_value=True), \
                patch.object(recovery.subprocess, 'run') as command:
            command.return_value.returncode = 0
            recovery.propose_revert(run, [{'status': 'failed', 'stage': 'go', 'tests': ['TestRegression']}])
        body = api.call_args.args[2]
        self.assertTrue(body['draft'])
        self.assertIn(run['html_url'], body['body'])
        self.assertIn('TestRegression', body['body'])

    def test_ambiguous_qualification_failure_creates_no_issue_or_revert(self):
        sha = 'a' * 40
        run = dict(head_sha=sha, path='.github/workflows/qualification.yml', event='push',
                   head_branch='main', conclusion='failure', html_url='https://example.test/run')
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(recovery, 'api', side_effect=[{'parents': []}, {'object': {'sha': sha}}]), \
                patch.object(recovery, 'pages', return_value=[]), \
                patch.object(recovery.subprocess, 'run') as command:
            recovery.propose_revert(run, [{'status': 'failed'}])
        command.assert_not_called()
