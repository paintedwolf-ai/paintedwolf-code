"""Trusted run eligibility, bounded evidence, and post-merge issue writes."""
import io
import json
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import budget_evidence as evidence, maintainability_issues as tracker
from verification_tests.test_budget_tracking_execution import report
from ci_policy.budget_snapshot import validate
from ci_policy import budget_history as history


def run():
    return dict(id=42, run_attempt=1, repository={'full_name': 'owner/repo'},
                path='.github/workflows/qualification.yml', event='push', head_branch='main',
                status='completed', conclusion='failure', created_at='2026-10-09T12:00:00Z', head_sha='a' * 40)


def zipped(value, duplicate=False):
    raw = io.BytesIO()
    with zipfile.ZipFile(raw, 'w') as archive:
        archive.writestr('budgets/reports/maintainability.json', json.dumps(value))
        if duplicate:
            archive.writestr('reports/maintainability.json', json.dumps(value))
    return raw.getvalue()


class EvidenceTests(unittest.TestCase):
    def test_only_current_qualification_limits_receipt_supplies_snapshot(self):
        raw = zipped(report())
        artifact = dict(id=1, name='receipt-verification-check-limits-1', expired=False, size_in_bytes=len(raw))
        ignored = [{**artifact, 'id': 2, 'name': 'receipt-verification-integration-limits-1'},
                   {**artifact, 'id': 3, 'name': 'receipt-verification-check-limits-2'}]
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(evidence, 'pages', return_value=ignored + [artifact]), \
                patch.object(evidence.subprocess, 'check_output', return_value=raw) as download:
            snapshot, rows = evidence.read_snapshot(run(), 'b' * 40)
        self.assertEqual(len(rows), 1)
        self.assertTrue(snapshot['complete'])
        self.assertEqual(download.call_args.args[0][-1], 'repos/owner/repo/actions/artifacts/1/zip')

    def test_missing_expired_ambiguous_or_oversized_receipts_fail_closed(self):
        artifact = dict(id=1, name='receipt-verification-check-limits-1', expired=False, size_in_bytes=100)
        for artifacts in [[], [{**artifact, 'expired': True}], [artifact, artifact],
                          [{**artifact, 'size_in_bytes': evidence.MAX_ARCHIVE_BYTES + 1}]]:
            with self.subTest(artifacts=artifacts), patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                    patch.object(evidence, 'pages', return_value=artifacts), \
                    patch.object(evidence.subprocess, 'check_output') as download, self.assertRaises(ValueError):
                evidence.read_snapshot(run(), 'b' * 40)
            download.assert_not_called()

    def test_duplicate_members_invalid_identity_and_incomplete_reports_fail_closed(self):
        values = [zipped(report(), duplicate=True), zipped(report([]))]
        invalid = report()
        invalid['tracking']['complete'] = False
        values.append(zipped(invalid))
        for raw in values:
            artifact = dict(id=1, name='receipt-verification-check-limits-1', expired=False, size_in_bytes=len(raw))
            with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                    patch.object(evidence, 'pages', return_value=[artifact]), \
                    patch.object(evidence.subprocess, 'check_output', return_value=raw):
                if raw == values[1]:
                    self.assertEqual(evidence.read_snapshot(run(), 'b' * 40)[1], {})
                else:
                    with self.assertRaises(ValueError):
                        evidence.read_snapshot(run(), 'b' * 40)


class ReconciliationTests(unittest.TestCase):
    def setUp(self):
        self.pull = dict(number=7, merged_at='date', base={'ref': 'main', 'repo': {'full_name': 'owner/repo'}})

    def invoke(self, current=None, pulls=None, existing=None, snapshot=None, dry_run=False):
        value = snapshot if snapshot is not None else report()
        parsed = validate(value, 'a' * 40, 'b' * 40)
        def api(path, method='GET', body=None):
            if path.endswith('/git/ref/heads/main'):
                return {'object': {'sha': 'a' * 40}}
            if '/git/commits/' in path:
                return {'tree': {'sha': 'b' * 40}}
            return {'number': 99}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(tracker, 'api', side_effect=api) as calls, \
                patch.object(history, 'pages', return_value=pulls if pulls is not None else [self.pull]), \
                patch.object(tracker, 'pages', return_value=existing or []), \
                patch.object(tracker, 'read_snapshot', return_value=parsed) as read, \
                patch.object(tracker, 'checkpoint', return_value=None):
            result = tracker.reconcile(current or run(), dry_run)
        return result, calls, read

    def test_failed_qualification_creates_issue_for_merged_warning(self):
        result, calls, read = self.invoke()
        writes = [call for call in calls.call_args_list if len(call.args) > 1]
        self.assertEqual(len(writes), 1)
        self.assertEqual(writes[0].args[:2], ('repos/owner/repo/issues', 'POST'))
        self.assertEqual(result['actions'], {'create': 1})
        read.assert_called_once()

    def test_preview_includes_exact_plan_without_writes(self):
        result, calls, _ = self.invoke(dry_run=True)
        self.assertEqual(result['actions'], {'create': 1})
        self.assertTrue(result['dry_run'])
        self.assertFalse(any(len(call.args) > 1 for call in calls.call_args_list))

    def test_non_merged_commits_never_download_evidence(self):
        for pulls in [[], [{**self.pull, 'merged_at': None}],
                      [{**self.pull, 'base': {'ref': 'other', 'repo': {'full_name': 'owner/repo'}}}],
                      [{**self.pull, 'base': {'ref': 'main', 'repo': {'full_name': 'foreign/repo'}}}]]:
            result, calls, read = self.invoke(pulls=pulls)
            self.assertIn('skipped', result)
            read.assert_not_called()
            self.assertFalse(any(len(call.args) > 1 for call in calls.call_args_list))

    def test_only_completed_repository_qualification_pushes_are_eligible(self):
        mutations = [dict(event='pull_request'), dict(event='merge_group'), dict(event='workflow_dispatch'),
                     dict(head_branch='feature'), dict(repository={'full_name': 'foreign/repo'}),
                     dict(path='.github/workflows/ci.yml'), dict(status='in_progress'), dict(head_sha='invalid')]
        for mutation in mutations:
            with self.subTest(mutation=mutation), patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                    patch.object(tracker, 'api') as api, patch.object(tracker, 'read_snapshot') as read:
                self.assertIn('skipped', tracker.reconcile({**run(), **mutation}))
            api.assert_not_called()
            read.assert_not_called()

    def test_stale_run_and_main_advance_abort_writes(self):
        for states in [[False], [True, False], [True, True, False]]:
            with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                    patch.object(tracker, 'current_main', side_effect=states), \
                    patch.object(tracker, 'api', return_value={'tree': {'sha': 'b' * 40}}) as api, \
                    patch.object(tracker, 'merged_pulls', return_value=[self.pull]), \
                    patch.object(tracker, 'pages', return_value=[]), \
                    patch.object(tracker, 'read_snapshot', return_value=validate(report(), 'a' * 40, 'b' * 40)), \
                    patch.object(tracker, 'checkpoint', return_value=None):
                result = tracker.reconcile(run())
            self.assertTrue('skipped' in result or 'stopped' in result)
            self.assertFalse(any(len(call.args) > 1 for call in api.call_args_list))

    def test_unavailable_snapshot_cannot_resolve_existing_issues(self):
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(tracker, 'current_main', return_value=True), \
                patch.object(tracker, 'api', return_value={'tree': {'sha': 'b' * 40}}) as api, \
                patch.object(tracker, 'merged_pulls', return_value=[self.pull]), \
                patch.object(tracker, 'read_snapshot', side_effect=ValueError('missing receipt')), self.assertRaises(ValueError):
            tracker.reconcile(run())
        self.assertFalse(any(len(call.args) > 1 for call in api.call_args_list))
