"""Recovery consumes complete source-identified evidence without retrying source failures."""
import unittest
from unittest.mock import patch

from ci_policy import recovery


class RecoveryTests(unittest.TestCase):
    def test_ineligible_runs_do_not_download_evidence_or_request_recovery(self):
        run = dict(id=42, path='.github/workflows/ci.yml', event='merge_group', conclusion='failure',
                   repository={'full_name': 'owner/repo'}, head_repository={'full_name': 'owner/repo'})
        mutations = [
            {'conclusion': conclusion} for conclusion in ('success', 'cancelled', 'timed_out', None)
        ] + [
            {'event': event} for event in ('push', 'pull_request', 'pull_request_target', 'schedule', None)
        ] + [
            {'path': '.github/workflows/unrelated.yml'},
            {'repository': {'full_name': 'foreign/repo'}},
            {'head_repository': {'full_name': 'foreign/repo'}},
            {'repository': None}, {'repository': {}}, {'head_repository': None}, {'head_repository': {}},
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation), patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                    patch.object(recovery, 'evidence') as evidence, patch.object(recovery, 'pages') as pages, \
                    patch.object(recovery, 'api') as api:
                recovery.recover({**run, **mutation})
                evidence.assert_not_called()
                pages.assert_not_called()
                api.assert_not_called()

    def run_recovery(self, records, failures=(), jobs=(), attempt=1, workflow='ci.yml'):
        run = {'id': 42, 'head_sha': 'a' * 40, 'path': '.github/workflows/' + workflow, 'event': 'workflow_dispatch' if workflow == 'qualification.yml' else 'merge_group',
               'run_attempt': attempt, 'conclusion': 'failure', 'html_url': 'https://example.test/run',
               'repository': {'full_name': 'owner/repo'}, 'head_repository': {'full_name': 'owner/repo'}}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(recovery, 'evidence', return_value=(records, list(failures))), \
                patch.object(recovery, 'current_group', return_value=True), \
                patch.object(recovery, 'pages', return_value=iter(jobs)), \
                patch.object(recovery, 'api') as api:
            recovery.recover(run)
        return api

    def test_every_failed_job_needs_matching_oom_evidence(self):
        failed = {'id': 10, 'name': 'verification / behavior', 'conclusion': 'failure'}
        aggregate = {'name': 'check', 'conclusion': 'failure'}
        for records, jobs, attempt, retries in [
            ([{'classification': 'runner_oom', 'lane': 'behavior', 'shard': '',
               'identity': {'run': '42', 'attempt': '1', 'sha': 'a' * 40}}], [failed, aggregate], 1, True),
            ([{'classification': 'runner_oom'}], [failed, aggregate], 2, False),
            ([{'classification': 'runner_oom'}], [failed, failed, aggregate], 1, False),
            ([{'classification': 'test_failure'}], [failed, aggregate], 1, False),
            ([], [failed, aggregate], 1, False),
        ]:
            with self.subTest(records=records, jobs=jobs, attempt=attempt):
                api = self.run_recovery(records, jobs=jobs, attempt=attempt)
                if retries:
                    api.assert_called_once_with('repos/owner/repo/actions/runs/42/rerun-failed-jobs', 'POST')
                else:
                    api.assert_not_called()

    def test_manual_qualification_source_failure_never_retries(self):
        api = self.run_recovery([{'classification': 'test_failure'}],
            jobs=[{'name': 'verification / behavior', 'conclusion': 'failure'}], workflow='qualification.yml')
        api.assert_not_called()

    def test_manual_qualification_retries_proven_runner_faults(self):
        api = self.run_recovery([{'classification': 'runner_oom', 'lane': 'behavior', 'shard': '',
            'identity': {'run': '42', 'attempt': '1', 'sha': 'a' * 40}}],
            jobs=[{'id': 10, 'name': 'verification / behavior', 'conclusion': 'failure'},
                  {'name': 'qualification', 'conclusion': 'failure'}], workflow='qualification.yml')
        api.assert_called_once_with('repos/owner/repo/actions/runs/42/rerun-failed-jobs', 'POST')
