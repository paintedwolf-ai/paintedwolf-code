"""Trusted run eligibility, bounded evidence, and post-merge issue writes."""
import io
import json
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import budget_evidence as evidence, maintainability_issues as tracker
from verification_tests.ci.test_budget_tracking_execution import report, row, issue
from ci_policy.budget_snapshot import validate
from ci_policy import budget_history as history


def run():
    return dict(id=42, run_attempt=1, repository={'full_name': 'owner/repo'},
                path='.github/workflows/nightly.yml', event='schedule', head_branch='main',
                status='completed', conclusion='failure', created_at='2026-10-09T12:00:00Z', head_sha='a' * 40)


def advanced(main, paths=(), records=()):
    def observe(_root, _source):
        changes = history.MainChanges.__new__(history.MainChanges)
        changes.main, changes.paths, changes.records = main, frozenset(paths), set(records)
        return changes
    return observe


def zipped(value, duplicate=False):
    raw = io.BytesIO()
    with zipfile.ZipFile(raw, 'w') as archive:
        archive.writestr('budgets/reports/maintainability.json', json.dumps(value))
        if duplicate:
            archive.writestr('reports/maintainability.json', json.dumps(value))
    return raw.getvalue()


class EvidenceTests(unittest.TestCase):
    def test_only_current_nightly_limits_receipt_supplies_snapshot(self):
        raw = zipped(report())
        artifact = dict(id=1, name='receipt-verification-nightly-limits-1', expired=False, size_in_bytes=len(raw))
        ignored = [{**artifact, 'id': 2, 'name': 'receipt-verification-integration-limits-1'},
                   {**artifact, 'id': 3, 'name': 'receipt-verification-nightly-limits-2'}]
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(evidence, 'pages', return_value=ignored + [artifact]), \
                patch.object(evidence.subprocess, 'check_output', return_value=raw) as download:
            snapshot, rows = evidence.read_snapshot(run(), 'b' * 40)
        self.assertEqual(len(rows), 1)
        self.assertTrue(snapshot['complete'])
        self.assertEqual(download.call_args.args[0][-1], 'repos/owner/repo/actions/artifacts/1/zip')

    def test_missing_expired_ambiguous_or_oversized_receipts_fail_closed(self):
        artifact = dict(id=1, name='receipt-verification-nightly-limits-1', expired=False, size_in_bytes=100)
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
            artifact = dict(id=1, name='receipt-verification-nightly-limits-1', expired=False, size_in_bytes=len(raw))
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

    def invoke(self, current=None, pulls=None, existing=None, snapshot=None, dry_run=False, main=None):
        value = snapshot if snapshot is not None else report()
        parsed = validate(value, 'a' * 40, 'b' * 40)
        def api(path, method='GET', body=None):
            if '/git/commits/' in path:
                return {'tree': {'sha': 'b' * 40}}
            if method == 'GET' and '/issues/' in path:
                return next(item for item in existing or [] if str(item['number']) == path.rsplit('/', 1)[-1])
            return {'number': 99}
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(tracker, 'api', side_effect=api) as calls, \
                patch.object(history, 'pages', return_value=pulls if pulls is not None else [self.pull]), \
                patch.object(tracker, 'pages', return_value=existing or []), \
                patch.object(tracker, 'read_snapshot', return_value=parsed) as read, \
                patch.object(tracker, 'checkpoint', return_value=None), \
                patch.object(tracker, 'intake', return_value=set(parsed[1])), \
                patch.object(tracker, 'MainChanges', side_effect=main or advanced('a' * 40)), \
                patch.object(tracker.time, 'sleep'):
            result = tracker.reconcile(current or run(), dry_run)
        return result, calls, read

    def writes(self, calls):
        return [call for call in calls.call_args_list if len(call.args) > 1]

    def test_failed_nightly_creates_issue_for_merged_warning(self):
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

    def test_only_completed_repository_nightly_observations_are_eligible(self):
        mutations = [dict(event='pull_request'), dict(event='merge_group'), dict(event='push'),
                     dict(head_branch='feature'), dict(repository={'full_name': 'foreign/repo'}),
                     dict(path='.github/workflows/ci.yml'), dict(status='in_progress'), dict(head_sha='invalid'),
                     dict(conclusion='cancelled'), dict(conclusion='timed_out'), dict(conclusion='neutral'),
                     dict(conclusion='skipped'), dict(conclusion='action_required')]
        for mutation in mutations:
            with self.subTest(mutation=mutation), patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                    patch.object(tracker, 'api') as api, patch.object(tracker, 'read_snapshot') as read:
                self.assertIn('skipped', tracker.reconcile({**run(), **mutation}))
            api.assert_not_called()
            read.assert_not_called()
        self.assertTrue(tracker.eligible({**run(), 'conclusion': 'success'}, 'owner/repo'))
        self.assertTrue(tracker.eligible({**run(), 'event': 'workflow_dispatch'}, 'owner/repo'))
        self.assertTrue(tracker.eligible({**run(), 'conclusion': 'failure'}, 'owner/repo'))

    def test_unavailable_snapshot_cannot_resolve_existing_issues(self):
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(tracker, 'api', return_value={'tree': {'sha': 'b' * 40}}) as api, \
                patch.object(tracker, 'merged_pulls', return_value=[self.pull]), \
                patch.object(tracker, 'read_snapshot', side_effect=ValueError('missing receipt')), self.assertRaises(ValueError):
            tracker.reconcile(run())
        self.assertFalse(any(len(call.args) > 1 for call in api.call_args_list))

    def test_unchanged_artifact_on_advanced_main_writes_its_issue(self):
        result, calls, _ = self.invoke(main=advanced('e' * 40, {'pkg/other.go', 'pkg/nested/server.go'}))
        self.assertEqual([call.args[:2] for call in self.writes(calls)], [('repos/owner/repo/issues', 'POST')])
        self.assertEqual(result['planned_main_sha'], 'e' * 40)
        self.assertEqual(result['held'], [])
        self.assertIn('checkpoint', result)

    def test_changed_artifact_is_held_with_both_commits(self):
        from ci_policy.budget_snapshot import key
        held = [{'category': 'source_files', 'id': 'pkg/server.go', 'report_source': 'a' * 40, 'main': 'e' * 40}]
        for main in [advanced('e' * 40, {'pkg/server.go'}),
                     advanced('e' * 40, {history.POLICY}),
                     advanced('e' * 40, records={key('source_files', 'pkg/server.go')})]:
            with self.subTest(main=main):
                result, calls, _ = self.invoke(main=main)
                self.assertEqual(self.writes(calls), [])
                self.assertEqual(result['operations'], [])
                self.assertEqual(result['held'], held)
                self.assertEqual(result['waiting_intake'], [{'category': 'source_files', 'id': 'pkg/server.go'}])
                self.assertNotIn('checkpoint', result)

    def test_held_artifact_already_listed_does_not_withhold_the_checkpoint(self):
        first, _, _ = self.invoke()
        current = issue(body=first['operations'][0]['body']['body'])
        result, calls, _ = self.invoke(existing=[current], main=advanced('e' * 40, {'pkg/server.go'}))
        self.assertEqual(self.writes(calls), [])
        self.assertEqual(len(result['held']), 1)
        self.assertEqual(result['waiting_intake'], [])
        self.assertIn('checkpoint', result)

    def test_non_ancestor_source_and_untied_artifacts_refuse_reconciliation(self):
        def diverged(_root, _source):
            raise ValueError('the report source is not an ancestor of main')
        for main, snapshot in [(diverged, None), (None, report([{**row(), 'sources': ['pkg/elsewhere.go']}]))]:
            with self.subTest(main=main), self.assertRaises(ValueError):
                self.invoke(main=main, snapshot=snapshot)

    def test_main_advance_during_throttle_holds_newly_changed_areas(self):
        commits = iter([advanced('a' * 40), advanced('e' * 40, {'pkg/server.go'})])
        result, calls, _ = self.invoke(main=lambda root, source: next(commits)(root, source))
        self.assertEqual(self.writes(calls), [])
        self.assertEqual(result['applied_operations'], 0)
        self.assertEqual(result['held_operations'], 1)
        self.assertEqual(result['held'], [{'category': 'source_files', 'id': 'pkg/server.go',
                                           'report_source': 'a' * 40, 'main': 'e' * 40}])
        self.assertNotIn('checkpoint', result)

    def test_applied_create_is_idempotent_on_repeated_run(self):
        first, _, _ = self.invoke()
        current = issue(body=first['operations'][0]['body']['body'])
        repeated, calls, _ = self.invoke(existing=[current])
        self.assertEqual(repeated['operations'], [])
        self.assertEqual(repeated['applied_operations'], 0)
        self.assertIn('checkpoint', repeated)
        self.assertFalse(any(len(call.args) > 1 for call in calls.call_args_list))

    def test_resolve_updates_only_managed_body_and_state(self):
        result, calls, _ = self.invoke(existing=[issue()], snapshot=report([]))
        writes = [call for call in calls.call_args_list if len(call.args) > 1]
        self.assertEqual(result['actions'], {'resolve': 1})
        self.assertEqual(writes[0].args[:2], ('repos/owner/repo/issues/1', 'PATCH'))
        self.assertEqual(set(writes[0].args[2]), {'title', 'body', 'state', 'state_reason'})

    def test_many_metrics_in_one_module_create_one_issue(self):
        result, calls, _ = self.invoke(snapshot=report([row(f'pkg/{index}.go') for index in range(141)]))
        self.assertEqual(result['applied_operations'], 1)
        self.assertEqual(result['deferred_creations'], 0)
        self.assertIn('checkpoint', result)

    def test_new_modules_are_bounded_per_apply_and_defer_checkpoint(self):
        result, _, _ = self.invoke(snapshot=report([row(f'app{index}/internal/module/file.go') for index in range(27)]))
        self.assertEqual(result['applied_operations'], 25)
        self.assertEqual(result['deferred_creations'], 2)
        self.assertNotIn('checkpoint', result)

    def test_concurrent_issue_body_edit_stops_before_patch(self):
        current = issue()
        from ci_policy.budget_snapshot import validate
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(tracker, 'MainChanges', side_effect=advanced('a' * 40)), \
                patch.object(tracker, 'merged_pulls', return_value=[self.pull]), \
                patch.object(tracker, 'read_snapshot', return_value=validate(report([]), 'a' * 40, 'b' * 40)), \
                patch.object(tracker, 'checkpoint', return_value=None), \
                patch.object(tracker, 'intake', return_value=set()), \
                patch.object(tracker, 'pages', return_value=[current]), \
                patch.object(tracker, 'api', side_effect=[{'tree': {'sha': 'b' * 40}}, {**current, 'body': 'Human edit'}]) as api, \
                patch.object(tracker.time, 'sleep'), self.assertRaises(ValueError):
            tracker.reconcile(run())
        self.assertFalse(any(len(call.args) > 1 for call in api.call_args_list))
