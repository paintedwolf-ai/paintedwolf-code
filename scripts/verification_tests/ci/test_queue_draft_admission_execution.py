"""A draft decision withdraws actual admission without claiming another owner."""
from copy import deepcopy
import unittest
from unittest.mock import patch

from ci_policy import batch_prs as batch
from ci_policy.queue_batching import coordinator, github
from ci_policy.queue_batching.selection import PREFIX


SHA = 'a' * 40
OTHER = 'b' * 40


def pull(**changes):
    return {'number': 3, 'id': 'PR_3', 'headRefOid': SHA, 'isDraft': True,
            'state': 'OPEN', 'headRefName': 'fix/fixture', 'labels': [],
            'mergeQueueEntry': {'id': 'QUEUE_3'}, 'autoMergeRequest': None, **changes}


def entry(row, state='AWAITING_CHECKS'):
    return {'state': state, 'position': 1, 'pullRequest': row}


class QueuedDraftTests(unittest.TestCase):
    def test_ordinary_and_automatic_batch_drafts_withdraw_any_queue_state(self):
        for state in ('QUEUED', 'AWAITING_CHECKS'):
            for branch in ('fix/fixture', PREFIX + 'fixture'):
                with self.subTest(state=state, branch=branch):
                    recorded = pull(headRefName=branch)
                    remaining = {**recorded, 'mergeQueueEntry': None}
                    with patch.object(github, 'queue', return_value=[entry(recorded, state)]), \
                            patch.object(batch, 'source', side_effect=[recorded, remaining]), \
                            patch.object(batch, 'withdraw') as remove, patch.object(github, 'mutation') as mutation:
                        self.assertEqual(github.withdraw_drafts('o/r'), [3])
                        remove.assert_called_once_with('o/r', recorded)
                        mutation.assert_not_called()

    def test_empty_queue_and_ready_entries_have_no_effect(self):
        for rows in ([], [entry(pull(isDraft=False))]):
            with patch.object(github, 'queue', return_value=rows), \
                    patch.object(batch, 'source') as source, patch.object(batch, 'withdraw') as remove:
                self.assertEqual(github.withdraw_drafts('o/r'), [])
                source.assert_not_called()
                remove.assert_not_called()

    def test_changed_head_and_new_ready_decision_are_not_withdrawn(self):
        for change in ({'headRefOid': OTHER}, {'isDraft': False}):
            with self.subTest(change=change), patch.object(github, 'queue', return_value=[entry(pull())]), \
                    patch.object(batch, 'source', return_value=pull(**change)), \
                    patch.object(batch, 'withdraw') as remove:
                self.assertEqual(github.withdraw_drafts('o/r'), [])
                remove.assert_not_called()

    def test_missing_admission_is_idempotent_through_real_withdrawal(self):
        absent = pull(mergeQueueEntry=None)
        with patch.object(github, 'queue', return_value=[entry(pull())]), \
                patch.object(batch, 'source', return_value=absent), patch.object(batch, 'api') as api:
            self.assertEqual(github.withdraw_drafts('o/r'), [3])
            api.assert_not_called()

    def test_real_withdrawal_uses_pr_node_and_removes_residual_auto_merge(self):
        recorded = pull()
        waiting = pull(mergeQueueEntry=None, autoMergeRequest={'enabledAt': 'now'})
        absent = pull(mergeQueueEntry=None)
        with patch.object(github, 'queue', return_value=[entry(recorded)]), \
                patch.object(batch, 'source', side_effect=[recorded, recorded, waiting, absent]), \
                patch.object(batch, 'api') as api:
            self.assertEqual(github.withdraw_drafts('o/r'), [3])
        self.assertEqual(len(api.call_args_list), 2)
        dequeue, disable = (call.args[2] for call in api.call_args_list)
        self.assertIn('dequeuePullRequest(input: {id: $id})', dequeue['query'])
        self.assertEqual(dequeue['variables'], {'id': 'PR_3'})
        self.assertIn('disablePullRequestAutoMerge', disable['query'])
        self.assertEqual(disable['variables'], {'id': 'PR_3'})

    def test_change_during_final_guard_never_mutates_admission(self):
        for change in ({'headRefOid': OTHER}, {'isDraft': False}):
            with self.subTest(change=change), patch.object(github, 'queue', return_value=[entry(pull())]), \
                    patch.object(batch, 'source', side_effect=[pull(), pull(**change)]), \
                    patch.object(batch, 'api') as api, self.assertRaisesRegex(ValueError, 'changed before'):
                github.withdraw_drafts('o/r')
            api.assert_not_called()
        with patch.object(github, 'queue', return_value=[entry(pull())]), \
                patch.object(batch, 'api', return_value={'data': {'repository': {'pullRequest': pull(state='CLOSED')}}}), \
                self.assertRaisesRegex(ValueError, 'must be open'):
            github.withdraw_drafts('o/r')

    def test_post_dequeue_head_or_ready_race_does_not_disable_new_auto_merge(self):
        for change in ({'headRefOid': OTHER}, {'isDraft': False}):
            changed = pull(mergeQueueEntry=None, autoMergeRequest={'enabledAt': 'new'}, **change)
            with self.subTest(change=change), \
                    patch.object(batch, 'source', side_effect=[pull(), changed]), \
                    patch.object(batch, 'api') as api, self.assertRaisesRegex(ValueError, 'changed during'):
                batch.withdraw('o/r', pull())
            self.assertEqual(api.call_count, 1)
            self.assertIn('dequeuePullRequest', api.call_args.args[2]['query'])

    def test_queued_draft_not_removed_and_api_errors_are_not_acknowledged(self):
        with patch.object(github, 'queue', return_value=[entry(pull())]), \
                patch.object(batch, 'source', return_value=pull()), \
                patch.object(batch, 'api'), self.assertRaisesRegex(ValueError, 'remains in the merge queue'):
            github.withdraw_drafts('o/r')
        for remaining in (pull(), pull(mergeQueueEntry=None, autoMergeRequest={'enabledAt': 'now'})):
            with patch.object(github, 'queue', return_value=[entry(pull())]), \
                    patch.object(batch, 'source', side_effect=[pull(), remaining]), \
                    patch.object(batch, 'withdraw'), self.assertRaisesRegex(ValueError, 'retains merge admission'):
                github.withdraw_drafts('o/r')
        with patch.object(github, 'queue', return_value=[entry(pull())]), \
                patch.object(batch, 'source', return_value=pull()), \
                patch.object(batch, 'api', side_effect=RuntimeError('GraphQL refused')), self.assertRaises(RuntimeError):
            github.withdraw_drafts('o/r')

    def test_cleanup_precedes_recovery_and_preserves_drafted_batch_handoff(self):
        events = []
        policy = {'maximum': 4, 'preserve_front': 2}
        record = {'version': 1, 'batch_head': SHA, 'sources': [pull(), pull(number=4)]}
        drafted = {'number': 10, 'draft': True, 'state': 'open', 'merged_at': None,
                   'body': coordinator.describe(record)}
        with patch.object(github, 'withdraw_drafts', side_effect=lambda repo: events.append('withdraw')), \
                patch('ci_policy.queue_dispatch.recover', side_effect=lambda repo: events.append('recover')), \
                patch.object(github, 'build_window', return_value=2), patch.object(github, 'enforce_holds'), \
                patch.object(github, 'batches', return_value=[drafted]), \
                patch.object(github, 'restore') as restore, patch.object(coordinator, 'api') as api, \
                patch.object(coordinator, 'build') as build:
            original = deepcopy(record)
            self.assertEqual(coordinator.run('o/r', policy), {'state': 'watching'})
            self.assertEqual(record, original)
            self.assertEqual(events, ['withdraw', 'recover'])
            restore.assert_not_called()
            api.assert_not_called()
            build.assert_not_called()

    def test_cleanup_failure_stops_recovery_and_new_batch_selection(self):
        with patch.object(github, 'withdraw_drafts', side_effect=ValueError('retains admission')), \
                patch('ci_policy.queue_dispatch.recover') as recover, \
                patch.object(coordinator, 'select') as select, self.assertRaises(ValueError):
            coordinator.run('o/r', {})
        recover.assert_not_called()
        select.assert_not_called()
