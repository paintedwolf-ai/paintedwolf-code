"""Recovery consumes complete source-identified evidence without retrying source failures."""
import unittest
from unittest.mock import patch

from ci_policy import recovery


class RecoveryTests(unittest.TestCase):
    def run_recovery(self, records, failures=(), jobs=(), attempt=1, workflow='ci.yml'):
        run = {'id': 42, 'path': '.github/workflows/' + workflow, 'event': 'merge_group',
               'run_attempt': attempt, 'conclusion': 'failure', 'html_url': 'https://example.test/run'}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(recovery, 'evidence', return_value=(records, list(failures), [])), \
                patch.object(recovery, 'pages', return_value=iter(jobs)), \
                patch.object(recovery, 'api') as api, \
                patch.object(recovery, 'ensure_issue') as issue, \
                patch.object(recovery, 'propose_revert') as revert:
            recovery.recover(run)
        return api, issue, revert

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
                api, issue, revert = self.run_recovery(records, jobs=jobs, attempt=attempt)
                if retries:
                    api.assert_called_once_with('repos/owner/repo/actions/runs/42/rerun-failed-jobs', 'POST')
                    issue.assert_not_called()
                else:
                    api.assert_not_called()
                    issue.assert_called_once()
                revert.assert_not_called()

    def test_qualification_source_failure_reaches_attribution_without_retry(self):
        failures = [{'status': 'failed', 'tests': ['TestRegression']}]
        api, issue, revert = self.run_recovery([{'classification': 'test_failure'}], failures,
            [{'name': 'verification / behavior', 'conclusion': 'failure'}], workflow='qualification.yml')
        api.assert_not_called()
        issue.assert_not_called()
        self.assertEqual(revert.call_args.args[1], failures)
