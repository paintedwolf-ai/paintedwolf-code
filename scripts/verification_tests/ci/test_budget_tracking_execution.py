"""Source-bound budget snapshots and issue lifecycle decisions."""
import unittest

from ci_policy import budget_snapshot as snapshot, budget_issues as issues


def row(artifact='pkg/server.go', category='source_files', measured=350):
    return dict(category=category, id=artifact, measured=measured, warn=300, limit=900,
                touched=True, spans=[], effective_cap=900, exception_reason='', sources=['pkg/server.go'])


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


class WorkstreamTests(unittest.TestCase):
    def setUp(self):
        from ci_policy import budget_workstreams
        self.groups = budget_workstreams

    def artifacts(self, *rows):
        return {snapshot.key(r['category'], r['id']): r for r in rows}

    def test_structural_grouping_grows_without_a_module_catalog(self):
        for path, expected in [('lycaon/internal/newmodule/nested/service.go', 'lycaon/internal'),
                               ('new-client/src/newarea/file.ts', 'new-client/src'),
                               ('new-client/src/newarea/internal/file.ts', 'new-client/src'),
                               ('lycaon/test/contract/internal/check/test.go', 'lycaon/test'),
                               ('lycaon/test/contract/new/test.go', 'lycaon/test'),
                               ('scripts/new/path.py', 'scripts'), ('client/src/App.tsx', 'client/src')]:
            self.assertEqual(self.groups.workstream(row(path)), expected)

    def test_migration_adopts_oldest_and_preserves_human_notes_and_evidence(self):
        first = row('app/internal/module/a.go')
        second = row('app/internal/module/module.Service', 'go_receiver_lines')
        old = issue(number=2, body='Human notes\n' + issues.block(first, observation(), True) + '\nAfterword')
        other = issue(number=3, body=issues.block(second, observation(), True))
        operations, _ = self.groups.plan([other, old], self.artifacts(first, second), observation())
        self.assertEqual([o['action'] for o in operations], ['update', 'consolidate'])
        self.assertEqual(operations[0]['number'], 2)
        body = operations[0]['body']['body']
        self.assertTrue(body.startswith('Human notes\n'))
        self.assertTrue(body.endswith('\nAfterword'))
        self.assertEqual(len(self.groups.group_state(issue(body=body))['artifacts']), 2)
        self.assertIn('Consolidated into #2', operations[1]['body']['body'])
        current = {**old, **operations[0]['body']}
        duplicate = {**other, **operations[1]['body']}
        self.assertEqual(self.groups.plan([current, duplicate], self.artifacts(first, second), observation('f' * 40)), ([], 0))

    def test_partial_migration_replay_does_not_recreate_artifact_issues(self):
        first, second = row('pkg/a.go'), row('pkg/b.go')
        old = issue(body=issues.block(first, observation(), True))
        other = issue(number=2, body=issues.block(second, observation(), True))
        ops, _ = self.groups.plan([old, other], self.artifacts(first, second), observation())
        current = {**old, **ops[0]['body']}
        repeated, _ = self.groups.plan([current, other], self.artifacts(first, second), observation())
        self.assertEqual([o['action'] for o in repeated], ['consolidate'])

    def test_resolve_recur_retention_and_new_untouched_debt(self):
        entry = row('pkg/a.go')
        ops, _ = self.groups.plan([], self.artifacts(entry), observation())
        current = issue(body=ops[0]['body']['body'])
        from verification_tests.ci.test_budget_review_execution import reviewed
        retained = {**entry, 'review': reviewed()['review']}
        ops, suppressed = self.groups.plan([current], self.artifacts(retained), observation())
        self.assertEqual(ops[0]['action'], 'resolve')
        self.assertEqual(suppressed, 1)
        closed = {**current, **ops[0]['body']}
        self.assertEqual(self.groups.plan([closed], {}, observation()), ([], 0))
        renewed = {**retained, 'review': {**retained['review'], 'state': 'boundary_changed'}}
        ops, _ = self.groups.plan([closed], self.artifacts(renewed), observation(), intake_keys=set())
        self.assertEqual(ops[0]['action'], 'reopen')
        untouched = {**entry, 'id': 'pkg/b.go', 'touched': False}
        ops, _ = self.groups.plan([current], self.artifacts(entry, untouched), observation())
        self.assertEqual(ops, [])

    def test_human_copies_and_malformed_state_cannot_mutate_issues(self):
        ops, _ = self.groups.plan([], self.artifacts(row()), observation())
        body = ops[0]['body']['body']
        human = issue(body=body)
        human['user']['login'] = 'someone'
        self.assertEqual(self.groups.plan([human], self.artifacts(row()), observation())[0][0]['action'], 'create')
        for broken in [body * 2, self.groups.START, body.replace('"workstream":"pkg"', '"workstream":"../unsafe"')]:
            with self.assertRaises(ValueError):
                self.groups.plan([issue(body=broken)], self.artifacts(row()), observation())

    def test_resolved_oldest_issue_does_not_leave_open_legacy_siblings(self):
        closed = issue(state='closed', state_reason='completed')
        open_issue = issue(number=2)
        ops, _ = self.groups.plan([closed, open_issue], {}, observation())
        self.assertEqual(ops[0]['action'], 'resolve')
        self.assertEqual(ops[0]['number'], 2)

    def test_fingerprint_covers_sources_shape_policy_and_review(self):
        entry = row()
        ops, _ = self.groups.plan([], self.artifacts(entry), observation())
        current = issue(body=ops[0]['body']['body'])
        for change in [{'sources': ['pkg/new.go']}, {'shape': '1' * 64}, {'warn': 250},
                       {'exception_reason': 'bounded decision'}, {'measured': 360}]:
            with self.subTest(change=change):
                ops, _ = self.groups.plan([current], self.artifacts({**entry, **change}), observation())
                self.assertEqual(ops[0]['action'], 'update')

    def test_large_area_rolls_up_without_issue_or_body_sprawl(self):
        entries = [row(f'app/internal/module{i}/long_named_file{i}.go') for i in range(500)]
        ops, _ = self.groups.plan([], self.artifacts(*entries), observation())
        self.assertEqual(len(ops), 1)
        self.assertLess(len(ops[0]['body']['body'].encode()), 60000)
        self.assertIn('linked complete qualification report', ops[0]['body']['body'])
        self.assertEqual(len(self.groups.group_state(issue(body=ops[0]['body']['body']))['artifacts']), 500)

    def test_not_planned_dismissal_is_bounded_by_intake_and_facts(self):
        entry = row()
        closed = issue(state='closed', state_reason='not_planned')
        self.assertEqual(self.groups.plan([closed], self.artifacts(entry), observation(), intake_keys=set()), ([], 1))
        ops, _ = self.groups.plan([closed], self.artifacts({**entry, 'measured': 360}), observation(), intake_keys=set())
        self.assertEqual(ops[0]['action'], 'reopen')

    def test_backfill_and_invalidated_retention_admit_untouched_findings(self):
        entry = {**row(), 'touched': False}
        self.assertEqual(self.groups.plan([], self.artifacts(entry), observation()), ([], 0))
        self.assertEqual(self.groups.plan([], self.artifacts(entry), observation(), backfill=True)[0][0]['action'], 'create')
        from verification_tests.ci.test_budget_review_execution import reviewed
        entry['review'] = reviewed('boundary_changed')['review']
        self.assertEqual(self.groups.plan([], self.artifacts(entry), observation())[0][0]['action'], 'create')

    def test_area_dismissal_retains_fingerprints_until_facts_change(self):
        entry = row()
        ops, _ = self.groups.plan([], self.artifacts(entry), observation())
        closed = issue(state='closed', state_reason='not_planned', body=ops[0]['body']['body'])
        self.assertEqual(self.groups.plan([closed], self.artifacts(entry), observation(), intake_keys=set()), ([], 1))
        for changed in [{**entry, 'measured': 360}, {**entry, 'warn': 250}]:
            ops, _ = self.groups.plan([closed], self.artifacts(changed), observation(), intake_keys=set())
            self.assertEqual(ops[0]['action'], 'reopen')
