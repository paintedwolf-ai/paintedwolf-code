"""Source-bound budget snapshots and issue lifecycle decisions."""
import unittest

from ci_policy import budget_snapshot as snapshot, budget_issues as issues


def row(artifact='pkg/server.go', category='source_files', measured=350):
    return dict(category=category, id=artifact, measured=measured, warn=300, limit=900,
                touched=True, effective_cap=900, exception_reason='', sources=['pkg/server.go'])


def report(rows=None):
    value = {'suite': 'maintainability', 'tracking': {'schema_version': 1, 'complete': True,
                                                   'artifacts': rows if rows is not None else [row()]}}
    snapshot.seal(value, 'a' * 40, 'b' * 40, 'c' * 40)
    return value


def observation(sha='a' * 40):
    return dict(source_sha=sha, source_tree='b' * 40, base_sha='c' * 40, digest='d' * 64,
                run_id=42, run_attempt=1, run_url='https://github.com/owner/repo/actions/runs/42',
                repository='owner/repo', merged_prs=[7])


def issue(number=1, state='open', state_reason=None, body=None):
    return dict(number=number, title='A human-edited title', state=state, state_reason=state_reason,
                body=body if body is not None else issues.block(row(), observation(), True),
                user={'login': 'github-actions[bot]'})


class SnapshotTests(unittest.TestCase):
    def test_snapshot_roundtrip_and_empty_complete_inventory(self):
        for rows in [[row()], []]:
            value, artifacts = snapshot.validate(report(rows), 'a' * 40, 'b' * 40)
            self.assertEqual(len(artifacts), len(rows))
            self.assertTrue(value['complete'])

    def test_invalid_inventory_refuses_reconciliation(self):
        mutations = [lambda v: v.pop('tracking'), lambda v: v.update(suite='prompts'),
                     lambda v: v['tracking'].update(complete=False),
                     lambda v: v['tracking'].update(schema_version=2),
                     lambda v: v['tracking'].update(source_sha='f' * 40),
                     lambda v: v['tracking'].update(source_tree='f' * 40),
                     lambda v: v['tracking'].update(base_sha='invalid'),
                     lambda v: v['tracking'].update(artifacts=[row(), row()]),
                     lambda v: v['tracking'].update(artifacts=[row(category='unknown')]),
                     lambda v: v['tracking'].update(artifacts=[row(artifact='../unsafe')]),
                     lambda v: v['tracking'].update(artifacts=[{**row(), 'sources': []}]),
                     lambda v: v['tracking'].update(artifacts=[{**row(), 'warn': True}]),
                     lambda v: v['tracking'].update(artifacts=[row(measured=300)]),
                     lambda v: v['tracking'].update(artifacts=[{**row(), 'effective_cap': 1000}])]
        for mutation in mutations:
            value = report()
            mutation(value)
            if 'tracking' in value:
                value['tracking']['digest'] = snapshot.digest({k: v for k, v in value['tracking'].items() if k != 'digest'})
            with self.subTest(value=value), self.assertRaises(ValueError):
                snapshot.validate(value, 'a' * 40, 'b' * 40)
        value = report()
        value['tracking']['artifacts'][0]['measured'] += 1
        with self.assertRaises(ValueError):
            snapshot.validate(value, 'a' * 40, 'b' * 40)

    def test_stable_identity_excludes_measurement_and_pr(self):
        self.assertEqual(snapshot.key('source_files', 'pkg/server.go'), snapshot.key(row()['category'], row()['id']))
        self.assertNotEqual(snapshot.key('source_files', 'pkg/server.go'), snapshot.key('test_files', 'pkg/server.go'))


class IssueLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.artifacts = {snapshot.key(row()['category'], row()['id']): row()}

    def test_create_then_repeat_has_no_writes(self):
        operations, suppressed = issues.plan([], self.artifacts, observation())
        self.assertEqual([op['action'] for op in operations], ['create'])
        current = issue(body=operations[0]['body']['body'])
        self.assertEqual(issues.plan([current], self.artifacts, observation()), ([], 0))
        self.assertEqual(suppressed, 0)

    def test_later_merge_updates_same_issue_preserving_human_prose(self):
        current = issue(body='Human notes\n\n' + issues.block(row(), observation(), True) + '\nAfterword')
        changed = {next(iter(self.artifacts)): row(measured=400)}
        operations, _ = issues.plan([current], changed, observation('e' * 40))
        self.assertEqual(operations[0]['action'], 'update')
        self.assertEqual(operations[0]['number'], 1)
        body = operations[0]['body']['body']
        self.assertTrue(body.startswith('Human notes\n\n'))
        self.assertTrue(body.endswith('\nAfterword'))
        self.assertNotIn('title', operations[0]['body'])
        self.assertEqual(issues.tracked(issue(body=body))['measured'], 400)

    def test_resolution_and_recurrence_reuse_original_issue(self):
        operations, _ = issues.plan([issue()], {}, observation('e' * 40))
        self.assertEqual(operations[0]['action'], 'resolve')
        self.assertEqual(operations[0]['body']['state_reason'], 'completed')
        resolved = issue(state='closed', state_reason='completed', body=operations[0]['body']['body'])
        self.assertEqual(issues.plan([resolved], {}, observation('f' * 40)), ([], 0))
        operations, _ = issues.plan([resolved], self.artifacts, observation('f' * 40))
        self.assertEqual(operations[0]['action'], 'reopen')
        self.assertEqual(operations[0]['number'], 1)

    def test_dismissal_suppresses_reopening_and_duplicate_creation(self):
        current = issue(state='closed', state_reason='not_planned')
        self.assertEqual(issues.plan([current], self.artifacts, observation('e' * 40)), ([], 1))

    def test_adopt_legacy_bot_issue_and_deduplicate(self):
        old = issue(body='Original evidence')
        old['title'] = 'Maintainability debt: source_files pkg/server.go'
        operations, _ = issues.plan([issue(number=2), old], self.artifacts, observation())
        self.assertEqual([op['action'] for op in operations], ['deduplicate', 'update'])
        self.assertEqual(operations[0]['number'], 2)
        self.assertIn('Tracked by #1', operations[0]['body']['body'])
        self.assertTrue(operations[1]['body']['body'].startswith('Original evidence'))

    def test_human_copy_of_marker_cannot_be_mutated(self):
        current = issue()
        current['user']['login'] = 'human'
        operations, _ = issues.plan([current], self.artifacts, observation())
        self.assertEqual([op['action'] for op in operations], ['create'])

    def test_malformed_tracking_blocks_abort_the_whole_plan(self):
        for body in [issues.START, issues.START + '\n' + issues.END,
                     issues.block(row(), observation(), True) * 2,
                     issues.block(row(), observation(), True).replace('pkg/server.go', 'changed.go', 1)]:
            with self.subTest(body=body), self.assertRaises(ValueError):
                issues.plan([issue(body=body)], self.artifacts, observation())

    def test_exception_copy_cannot_escape_the_state_comment(self):
        special = {**row(), 'exception_reason': '--> <script>danger</script>'}
        current = issue(body=issues.block(special, observation(), True))
        self.assertEqual(issues.tracked(current)['exception_reason'], special['exception_reason'])
        self.assertNotIn('<script>', current['body'])

    def test_gradual_intake_tracks_existing_issues_without_baseline_flood(self):
        untouched = {identity: {**entry, 'touched': False} for identity, entry in self.artifacts.items()}
        self.assertEqual(issues.plan([], untouched, observation()), ([], 0))
        operations, _ = issues.plan([], untouched, observation(), backfill=True)
        self.assertEqual(operations[0]['action'], 'create')
        operations, _ = issues.plan([issue()], untouched, observation('e' * 40))
        self.assertEqual(operations[0]['action'], 'update')
        operations, _ = issues.plan([], untouched, observation(), intake_keys=set(untouched))
        self.assertEqual(operations[0]['action'], 'create')
