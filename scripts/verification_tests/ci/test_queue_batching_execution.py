"""Queue batching preserves human holds, exact source ownership and required gates."""
from copy import deepcopy
from pathlib import Path
import subprocess
import unittest
from unittest.mock import Mock, patch

from ci_policy.queue_batching import coordinator as c, github as gh, selection as s


SHA = 'a' * 40
OTHER = 'b' * 40
POLICY = {'maximum': 4, 'preserve_front': 2, 'held_branches': ['*linux*'], 'held_paths': ['linux/*']}


def row(number):
    return {'number': number, 'id': str(number), 'headRefOid': SHA,
            'headRefName': f'fix/{number}', 'isDraft': False, 'state': 'OPEN', 'labels': []}


def entries():
    return [{'state': 'QUEUED', 'position': n, 'pullRequest': row(n)} for n in range(1, 8)]


class SelectionTests(unittest.TestCase):
    def test_reserves_cumulative_groups_and_preserves_consecutive_queue_order(self):
        self.assertEqual([r['number'] for r in s.select(entries(), POLICY, lambda n: [], set())], [3, 4, 5, 6])
        rows = entries()
        rows[3]['state'] = 'AWAITING_CHECKS'
        self.assertEqual(s.select(rows, POLICY, lambda n: [], set()), [])

    def test_holds_drafts_and_failed_identities_stop_batching_without_leapfrogging(self):
        for change in ({'labels': ['no-combine']}, {'headRefName': 'fix/linux-launch'}, {'isDraft': True}):
            rows = entries()
            rows[3]['pullRequest'].update(change)
            self.assertEqual(s.select(rows, POLICY, lambda n: [], set()), [])
        self.assertEqual(s.select(entries(), POLICY, lambda n: ['linux/a'] if n == 4 else [], set()), [])
        self.assertEqual(s.select(entries(), POLICY, lambda n: [], {(4, SHA)}), [])
        self.assertEqual(len(s.select(entries(), POLICY, lambda n: [], {(4, OTHER)})), 4)

    def test_workflow_changes_stay_individual_and_preserve_queue_order(self):
        for path in ('.github/workflows/release.yml', '.github/workflows/ci.yaml'):
            with self.subTest(path=path):
                self.assertEqual(s.select(entries(), POLICY, lambda n: [path] if n == 4 else [], set()), [])
        self.assertEqual(len(s.select(entries(), POLICY, lambda n: ['.github/actions/setup/action.yml'], set())), 4)

    def test_manifest_round_trip_binds_exact_heads_and_rejects_missing_identity(self):
        record = {'version': 1, 'batch_head': SHA, 'sources': [row(3), row(4)]}
        self.assertEqual(s.manifest(c.describe(record)), record)
        self.assertIsNone(s.manifest('ordinary PR'))
        with self.assertRaises(ValueError):
            s.manifest(c.describe({**record, 'batch_head': ''}))
        self.assertNotEqual(s.identity(SHA, record['sources']), s.identity(OTHER, record['sources']))


class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.record = {'version': 1, 'batch_head': SHA, 'sources': [row(3), row(4)], 'ci.yml_dispatched': True, 'dco.yml_dispatched': True}
        self.pull = {'number': 10, 'node_id': 'ten', 'head': {'sha': SHA, 'ref': 'automation/queue-batch-fixture'}, 'state': 'open',
                     'draft': False, 'merged_at': None, 'labels': [], 'auto_merge': None}

    def ports(self):
        return patch.multiple(c.github, ejected=Mock(return_value=False), withdraw=Mock(), restore=Mock(), mutation=Mock(), checks=Mock(return_value='pending'))

    def test_pass_enables_normal_auto_merge_and_failure_restores_individuals_once(self):
        with self.ports(), patch.object(c, 'source', side_effect=lambda repo, n: row(n)), \
             patch.object(c, 'api', return_value={'labels': []}):
            gh.checks.return_value = 'passed'
            self.assertTrue(c.reconcile('o/r', self.pull, self.record))
            gh.mutation.assert_called_once_with('enablePullRequestAutoMerge', {'pullRequestId': 'ten', 'mergeMethod': 'SQUASH'})
            gh.checks.return_value = 'failed'
            self.assertFalse(c.reconcile('o/r', self.pull, self.record))
            self.assertEqual(gh.restore.call_count, 2)
            self.assertEqual(self.record['outcome'], 'individual_fallback')
            c.reconcile('o/r', self.pull, self.record)
            self.assertEqual(gh.restore.call_count, 2)

    def test_source_change_or_label_invalidates_batch_but_human_draft_is_not_promoted(self):
        for change in ({'headRefOid': OTHER}, {'isDraft': True}):
            with self.subTest(change=change), self.ports(), patch.object(c, 'api', return_value={'labels': []}), \
                 patch.object(c, 'source', return_value={**row(3), **change}):
                record = deepcopy(self.record)
                self.assertFalse(c.reconcile('o/r', self.pull, record))
                gh.withdraw.assert_called_once()
        with self.ports(), patch.object(c, 'api', return_value={'labels': [{'name': s.HOLD}]}), \
             patch.object(c, 'source', side_effect=lambda repo, n: row(n)):
            self.assertFalse(c.reconcile('o/r', self.pull, deepcopy(self.record)))
        with self.ports():
            self.assertTrue(c.reconcile('o/r', {**self.pull, 'draft': True}, self.record))
            gh.mutation.assert_not_called()

    def test_readmitted_source_dissolves_batch_without_stealing_active_work(self):
        with self.ports(), patch.object(c, 'source', return_value={**row(3), 'mergeQueueEntry': {'id': 'active'}}), \
                patch.object(c, 'api', return_value={'labels': []}):
            self.assertFalse(c.reconcile('o/r', self.pull, self.record))
            gh.withdraw.assert_called_once_with('o/r', {'number': 10})
            self.assertEqual(gh.restore.call_count, 2)

    def test_queued_batch_with_null_auto_merge_is_not_enabled_again(self):
        def source(repo, number):
            return {**row(number), 'mergeQueueEntry': {'id': 'active'} if number == 10 else None}
        with self.ports(), patch.object(c, 'source', side_effect=source), patch.object(c, 'api', return_value={'labels': []}):
            gh.checks.return_value = 'passed'
            self.assertTrue(c.reconcile('o/r', self.pull, self.record))
            gh.mutation.assert_not_called()

    def test_merge_closes_only_unchanged_originals_and_keeps_later_heads(self):
        def api(path, method='GET', body=None):
            if method == 'PATCH':
                return {}
            return {'state': 'open', 'draft': False, 'head': {'sha': SHA if path.endswith('/3') else OTHER}}
        with patch.object(c, 'api', side_effect=api) as calls:
            c.reconcile('o/r', {**self.pull, 'merged_at': 'now'}, self.record)
        closed = [call.args[0] for call in calls.call_args_list if len(call.args) > 1 and call.args[2] == {'state': 'closed'}]
        self.assertEqual(closed, ['repos/o/r/pulls/3'])

    def test_changed_or_labeled_original_is_never_reenrolled(self):
        for pull in ({'state': 'open', 'draft': True, 'head': {'sha': SHA}},
                     {'state': 'open', 'draft': False, 'head': {'sha': OTHER}}):
            with patch.object(gh, 'api', return_value=pull), patch.object(gh, 'mutation') as mutation:
                gh.restore('o/r', row(3))
                mutation.assert_not_called()

    def test_checks_require_both_combined_success_and_original_commit_certification(self):
        runs = [{'id': 1, 'name': 'check', 'status': 'completed', 'conclusion': 'failure'},
                {'id': 2, 'name': 'check', 'status': 'completed', 'conclusion': 'success'}]
        with patch.object(gh, 'pages', return_value=runs), patch.object(gh, 'api', return_value={'statuses': [{'context': 'DCO-owned', 'state': 'success'}]}):
            self.assertEqual(gh.checks('o/r', SHA), 'passed')
        with patch.object(gh, 'pages', return_value=runs[:1]):
            self.assertEqual(gh.checks('o/r', SHA), 'failed')

    def test_failed_group_is_excluded_and_an_active_batch_prevents_duplicate_creation(self):
        pull = {**self.pull, 'body': c.describe(self.record)}
        with patch('ci_policy.queue_dispatch.recover', return_value=[]), patch.object(gh, 'withdraw_drafts'), patch.object(gh, 'build_window', return_value=2), patch.object(gh, 'enforce_holds'), patch.object(gh, 'batches', return_value=[pull]), \
             patch.object(c, 'reconcile', return_value=True), patch.object(c, 'build') as build:
            self.assertEqual(c.run('o/r', POLICY)['state'], 'watching')
            build.assert_not_called()
        failed = {**self.record, 'settled': True, 'outcome': 'individual_fallback'}
        with patch('ci_policy.queue_dispatch.recover', return_value=[]), patch.object(gh, 'withdraw_drafts'), patch.object(gh, 'build_window', return_value=2), patch.object(gh, 'enforce_holds'), patch.object(gh, 'batches', return_value=[{**pull, 'body': c.describe(failed)}]), \
             patch.object(gh, 'queue', return_value=entries()), patch.object(c, 'pages', return_value=[]), patch.object(c, 'build') as build:
            self.assertEqual(c.run('o/r', POLICY)['state'], 'no_waiting_batch')
            build.assert_not_called()

    def test_build_conflict_keeps_original_queue_and_removes_owned_scratch_checkout(self):
        calls = []
        def command(args, cwd=None):
            calls.append(args)
            if args[1] == 'merge':
                raise subprocess.CalledProcessError(1, args)
            return ''
        with patch.object(c, 'api', return_value={'object': {'sha': SHA}}), patch.object(c, 'command', side_effect=command), \
             patch.object(gh, 'withdraw') as withdraw:
            with self.assertRaises(subprocess.CalledProcessError):
                c.build('o/r', [row(3), row(4)], 2)
            withdraw.assert_not_called()
        self.assertEqual(calls[-1][:4], ['git', 'worktree', 'remove', '--force'])

    def test_build_dispatches_combined_ci_and_certification_after_withdrawing_sources(self):
        calls, api_calls = [], []
        def command(args, cwd=None):
            calls.append(args)
            return SHA if args[1:3] == ['rev-parse', 'HEAD'] else ''
        def api(path, method='GET', body=None):
            api_calls.append((path, method, body))
            if '/git/ref/' in path:
                return {'object': {'sha': SHA}}
            return {'number': 20, 'node_id': 'twenty', 'html_url': 'https://github.com/o/r/pull/20'}
        with patch.object(c, 'command', side_effect=command), patch.object(c, 'api', side_effect=api), \
             patch.object(gh, 'queue', return_value=entries()), patch.object(c, 'source', side_effect=lambda repo, n: row(n)), \
             patch.object(gh, 'withdraw') as withdraw, patch.object(gh, 'mutation') as mutation:
            self.assertEqual(c.build('o/r', [row(3), row(4)], 2), 'https://github.com/o/r/pull/20')
            self.assertEqual(withdraw.call_count, 2)
            mutation.assert_called_once_with('markPullRequestReadyForReview', {'pullRequestId': 'twenty'})
        dispatches = [path for path, method, body in api_calls if path.endswith('/dispatches')]
        self.assertEqual(len(dispatches), 2)
        self.assertTrue(any('/ci.yml/' in path for path in dispatches))
        self.assertTrue(any('/dco.yml/' in path for path in dispatches))
        self.assertEqual(calls[-1][:3], ['git', 'worktree', 'remove'])

    def test_combined_workflow_change_is_rejected_before_push_or_withdrawal(self):
        calls = []
        def command(args, cwd=None):
            calls.append(args)
            return '.github/workflows/release.yml' if args[1] == 'diff' else ''
        with patch.object(c, 'command', side_effect=command), patch.object(c, 'api', return_value={'object': {'sha': SHA}}), \
             patch.object(gh, 'queue', return_value=entries()), patch.object(c, 'source', side_effect=lambda repo, n: row(n)), \
             patch.object(gh, 'withdraw') as withdraw:
            self.assertIsNone(c.build('o/r', [row(3), row(4)], 2))
            withdraw.assert_not_called()
        self.assertFalse(any(args[1] == 'push' for args in calls))
        self.assertEqual(calls[-1][:3], ['git', 'worktree', 'remove'])

    def test_source_becoming_active_during_build_is_never_withdrawn(self):
        snapshot = entries()
        snapshot[2]['state'] = 'AWAITING_CHECKS'
        with patch.object(c, 'command', return_value=''), patch.object(c, 'api', return_value={'object': {'sha': SHA}}), \
             patch.object(gh, 'queue', return_value=snapshot), patch.object(gh, 'withdraw') as withdraw:
            self.assertIsNone(c.build('o/r', [row(3), row(4)], 2))
            withdraw.assert_not_called()

    def test_no_combine_never_withdraws_an_individual_pull_request(self):
        with patch.object(gh, 'pages', return_value=[{'number': 3, 'labels': [{'name': s.HOLD}]}, {'number': 4, 'labels': []}]), \
             patch.object(gh, 'batches', return_value=[]), patch.object(gh, 'withdraw') as withdraw:
            gh.enforce_holds('o/r')
            withdraw.assert_not_called()

    def test_creation_dispatch_failure_rolls_back_unchanged_original_admission(self):
        pull = {'number': 20, 'node_id': 'twenty', 'html_url': 'url'}
        def api(path, method='GET', body=None):
            if '/git/ref/' in path:
                return {'object': {'sha': SHA}}
            if path.endswith('/dispatches'):
                raise RuntimeError('dispatch unavailable')
            return pull
        with patch.object(c, 'command', side_effect=lambda args, cwd=None: '' if args[1] == 'diff' else SHA), patch.object(c, 'api', side_effect=api), \
             patch.object(gh, 'queue', return_value=entries()), patch.object(c, 'source', side_effect=lambda repo, n: row(n)), \
             patch.object(gh, 'withdraw'), patch.object(gh, 'mutation'), patch.object(c, 'dissolve') as dissolve:
            with self.assertRaisesRegex(RuntimeError, 'dispatch unavailable'):
                c.build('o/r', [row(3), row(4)], 2)
            self.assertEqual(dissolve.call_args.args[1], pull)

    def test_integration_ejection_splits_even_when_fast_checks_still_pass(self):
        with self.ports(), patch.object(c, 'source', side_effect=lambda repo, n: row(n)), \
             patch.object(c, 'api', return_value={'labels': []}):
            gh.checks.return_value = 'passed'
            gh.ejected.return_value = True
            self.assertFalse(c.reconcile('o/r', self.pull, self.record))
            gh.mutation.assert_not_called()
            self.assertEqual(gh.restore.call_count, 2)

    def test_no_combine_splits_its_batch_without_withdrawing_the_individual_pr(self):
        batch = {**self.pull, 'body': c.describe(self.record)}
        with patch.object(gh, 'pages', return_value=[{'number': 3, 'labels': [{'name': s.HOLD}]}]), \
             patch.object(gh, 'batches', return_value=[batch]), patch.object(gh, 'withdraw'), \
             patch.object(c, 'dissolve') as dissolve:
            gh.enforce_holds('o/r')
            dissolve.assert_called_once_with('o/r', batch, self.record, 'no_combine')

    def test_no_combine_original_keeps_its_individual_auto_merge_eligibility(self):
        pull = {'state': 'open', 'draft': False, 'head': {'sha': SHA}, 'node_id': 'three',
                'labels': [{'name': s.HOLD}], 'auto_merge': None}
        with patch.object(gh, 'api', return_value=pull), \
             patch('ci_policy.batch_prs.source', return_value={'mergeQueueEntry': None}), \
             patch.object(gh, 'mutation') as mutation:
            gh.restore('o/r', row(3))
            mutation.assert_called_once_with('enablePullRequestAutoMerge', {'pullRequestId': 'three', 'mergeMethod': 'SQUASH'})

    def test_interrupted_dispatch_resumes_only_the_missing_workflow(self):
        record = {**self.record, 'ci.yml_dispatched': False}
        with patch.object(c, 'api') as api:
            c.dispatch('o/r', self.pull, record)
            c.dispatch('o/r', self.pull, record)
        dispatches = [call for call in api.call_args_list if call.args[0].endswith('/dispatches')]
        self.assertEqual(len(dispatches), 1)
        self.assertIn('/ci.yml/', dispatches[0].args[0])


class GitHubBoundaryTests(unittest.TestCase):
    def test_queue_snapshot_retains_states_and_structured_labels(self):
        result = {'data': {'repository': {'mergeQueue': {'entries': {'nodes': [
            {'state': 'QUEUED', 'position': 3, 'pullRequest': {'number': 3, 'labels': {'nodes': [{'name': s.HOLD}]}}}
        ]}}}}}
        with patch.object(gh, 'api', return_value=result):
            self.assertEqual(gh.queue('o/r')[0]['pullRequest']['labels'], [s.HOLD])
        with patch.object(gh, 'api', return_value={'errors': [{'message': 'denied'}]}):
            with self.assertRaisesRegex(ValueError, 'denied'):
                gh.queue('o/r')
            with self.assertRaisesRegex(ValueError, 'denied'):
                gh.mutation('enablePullRequestAutoMerge', {})

    def test_batch_discovery_ignores_foreign_repositories_and_noncontroller_authors(self):
        pull = {'head': {'ref': s.PREFIX + 'id', 'repo': {'full_name': 'o/r'}},
                'base': {'ref': 'main'}, 'user': {'login': 'github-actions[bot]'}}
        foreign = deepcopy(pull)
        foreign['head']['repo']['full_name'] = 'attacker/r'
        human = deepcopy(pull)
        human['user']['login'] = 'human'
        with patch.object(gh, 'pages', return_value=[pull, foreign, human]), patch.object(gh, 'api', return_value=[]):
            self.assertEqual(gh.batches('o/r'), [pull])

    def test_graphql_mutations_use_structured_inputs_and_capacity_comes_from_the_queue(self):
        with patch.object(gh, 'api', return_value={}) as api:
            gh.mutation('markPullRequestReadyForReview', {'pullRequestId': 'id'})
            self.assertEqual(api.call_args.args[2]['variables']['input'], {'pullRequestId': 'id'})
        with patch.object(gh, 'graphql', return_value={'mergeQueue': {'configuration': {'maximumEntriesToBuild': 4}}}):
            self.assertEqual(gh.build_window('o/r'), 4)
        with patch.object(gh, 'pages', return_value=[{'event': 'removed_from_merge_queue'}]):
            self.assertTrue(gh.ejected('o/r', 3))

    def test_restore_is_idempotent_for_existing_admission_and_withdrawal_checks_the_head(self):
        with patch.object(gh, 'api', return_value={'auto_merge': {'enabledAt': 'now'}}), patch.object(gh, 'mutation') as mutation:
            gh.restore('o/r', row(3))
            mutation.assert_not_called()
        with patch('ci_policy.batch_prs.source', return_value={**row(3), 'headRefOid': OTHER}), \
             patch('ci_policy.batch_prs.withdraw') as withdraw:
            with self.assertRaisesRegex(ValueError, 'source changed'):
                gh.withdraw('o/r', row(3))
            withdraw.assert_not_called()
        with patch('ci_policy.batch_prs.source', return_value=row(3)), patch('ci_policy.batch_prs.withdraw') as withdraw:
            gh.withdraw('o/r', row(3))
            withdraw.assert_called_once_with('o/r', row(3))

    def test_pending_certification_cannot_admit_a_batch(self):
        runs = [{'name': 'check', 'id': 1, 'status': 'completed', 'conclusion': 'success'}]
        with patch.object(gh, 'pages', return_value=runs), patch.object(gh, 'api', return_value={'statuses': []}):
            self.assertEqual(gh.checks('o/r', SHA), 'pending')
        with patch.object(gh, 'pages', return_value=runs), patch.object(gh, 'api', return_value={'statuses': [{'context': 'DCO-owned', 'state': 'failure'}]}):
            self.assertEqual(gh.checks('o/r', SHA), 'failed')


class QueueDispatchTests(unittest.TestCase):
    def test_queue_inventory_refuses_graphql_errors_and_ignores_waiting_entries(self):
        from ci_policy import queue_dispatch as q
        rows = [{'state': 'QUEUED', 'headCommit': None}]
        with patch.object(q, 'api', return_value={'data': {'repository': {'mergeQueue': {'entries': {'nodes': rows}}}}}):
            self.assertEqual(q.entries('o/r'), rows)
        with patch.object(q, 'api', return_value={'errors': ['refused']}), self.assertRaises(ValueError):
            q.entries('o/r')
        with patch.object(q, 'entries', return_value=rows), patch.object(q, 'api') as api:
            self.assertEqual(q.recover('o/r'), [])
            api.assert_not_called()

    def test_missing_run_dispatches_only_the_live_exact_group(self):
        from ci_policy import queue_dispatch as q
        ref = {'ref': q.PREFIX + 'pr-3-base', 'object': {'sha': SHA}}
        calls = []
        def api(path, method='GET', body=None):
            calls.append((path, method, body))
            if 'matching-refs' in path:
                return [ref]
            if path.endswith('/status'):
                return {'statuses': []}
            if '/runs?' in path:
                return {'workflow_runs': []}
            return {}
        with patch.object(q, 'entries', return_value=[{'state': 'AWAITING_CHECKS', 'headCommit': {'oid': SHA}}]), \
                patch.object(q, 'context', return_value=OTHER) as context, patch.object(q, 'api', side_effect=api):
            self.assertEqual(q.recover('o/r'), [{'ref': 'gh-readonly-queue/main/pr-3-base', 'sha': SHA}])
        self.assertEqual(context.call_args_list, [unittest.mock.call('o/r', ref['ref'], SHA)] * 2)
        dispatches = [(path, body) for path, method, body in calls if method == 'POST']
        self.assertEqual(dispatches, [
            ('repos/o/r/actions/workflows/dco.yml/dispatches',
             {'ref': 'main', 'inputs': {'queue_ref': ref['ref'], 'queue_sha': SHA}}),
            ('repos/o/r/actions/workflows/ci.yml/dispatches',
             {'ref': 'gh-readonly-queue/main/pr-3-base', 'inputs': {'profile': 'integration'}}),
        ])

    def test_existing_failed_or_running_run_is_never_replaced(self):
        from ci_policy import queue_dispatch as q
        for status in ['failure', 'cancelled', 'in_progress', 'success']:
            with self.subTest(status=status), \
                    patch.object(q, 'entries', return_value=[{'state': 'AWAITING_CHECKS', 'headCommit': {'oid': SHA}}]), \
                    patch.object(q, 'api', side_effect=[
                        [{'ref': q.PREFIX + 'pr-3-base', 'object': {'sha': SHA}}],
                        {'statuses': [{'context': 'DCO-owned', 'state': 'success'}]},
                        {'workflow_runs': [{'conclusion': status}]}]) as api:
                self.assertEqual(q.recover('o/r'), [])
                self.assertTrue(all(call.args[1:] == () for call in api.call_args_list))

    def test_missing_certification_recovers_with_existing_ci_and_stays_idempotent(self):
        from ci_policy import queue_dispatch as q
        ref = {'ref': q.PREFIX + 'pr-3-base', 'object': {'sha': SHA}}
        calls = []
        def api(path, method='GET', body=None):
            calls.append((path, method, body))
            if 'matching-refs' in path:
                return [ref]
            if path.endswith('/status'):
                return {'statuses': [{'context': 'check', 'state': 'success'}]}
            if 'dco.yml/runs?' in path:
                return {'workflow_runs': [{'display_title': f'DCO queue {OTHER}'}]}
            if 'ci.yml/runs?' in path:
                return {'workflow_runs': [{'conclusion': 'success'}]}
            return {}
        with patch.object(q, 'entries', return_value=[{'state': 'AWAITING_CHECKS', 'headCommit': {'oid': SHA}}]), \
                patch.object(q, 'context', return_value=OTHER) as context, patch.object(q, 'api', side_effect=api):
            self.assertEqual(q.recover('o/r'), [])
        context.assert_called_once_with('o/r', ref['ref'], SHA)
        posts = [(path, body) for path, method, body in calls if method == 'POST']
        self.assertEqual(posts, [('repos/o/r/actions/workflows/dco.yml/dispatches',
                                  {'ref': 'main', 'inputs': {'queue_ref': ref['ref'], 'queue_sha': SHA}})])
        for state in ('pending', 'success'):
            with patch.object(q, 'api', return_value={'statuses': [{'context': 'DCO-owned', 'state': state}]}) as mocked:
                q.certify('o/r', ref['ref'], SHA)
                mocked.assert_called_once_with(f'repos/o/r/commits/{SHA}/status')
        with patch.object(q, 'api', side_effect=[
                {'statuses': []},
                {'workflow_runs': [{'display_title': f'DCO queue {SHA}', 'status': 'in_progress'}]}]) as mocked:
            q.certify('o/r', ref['ref'], SHA)
            self.assertEqual(mocked.call_count, 2)

    def test_dispatch_context_refuses_wrong_ref_changed_sha_and_removed_group(self):
        from ci_policy import queue_dispatch as q
        with self.assertRaises(ValueError):
            q.context('o/r', 'refs/heads/main', SHA)
        with patch.object(q, 'api', return_value={'object': {'sha': OTHER}}), self.assertRaises(ValueError):
            q.context('o/r', q.PREFIX + 'pr-3-base', SHA)
        with patch.object(q, 'api', return_value={'object': {'sha': SHA}}), \
                patch.object(q, 'entries', return_value=[]), self.assertRaises(ValueError):
            q.context('o/r', q.PREFIX + 'pr-3-base', SHA)
        with patch.object(q, 'api', return_value={'object': {'sha': SHA}}), \
                patch.object(q, 'entries', return_value=[{'state': 'AWAITING_CHECKS', 'headCommit': {'oid': SHA}, 'baseCommit': {'oid': OTHER}}]):
            self.assertEqual(q.context('o/r', q.PREFIX + 'pr-3-base', SHA), OTHER)


class QueueOwnershipTests(unittest.TestCase):
    def test_reenqueued_batch_does_not_reuse_an_old_ejection(self):
        events = [{'event': 'added_to_merge_queue'}, {'event': 'removed_from_merge_queue'},
                  {'event': 'added_to_merge_queue'}]
        with patch.object(gh, 'pages', return_value=events):
            self.assertFalse(gh.ejected('o/r', 1))
        with patch.object(gh, 'pages', return_value=events[:-1]):
            self.assertTrue(gh.ejected('o/r', 1))
