"""Bounded architectural decisions in the source-bound issue lifecycle."""
import unittest

from ci_policy import budget_issues as issues, budget_snapshot as snapshot
import budgets
from verification_tests.test_change_report_execution import report as display_report, finding
from verification_tests.test_budget_tracking_execution import row, report, issue, observation


def reviewed(state='retained', measured=350):
    return {**row(measured=measured), 'review': {
        'kind': 'runtime_service', 'workstream': 'host-lifecycle', 'disposition': 'retain',
        'reason': 'One ordered shutdown boundary.', 'reviewed_commit': 'e' * 40,
        'reviewed_shape': '', 'ceiling': 400, 'boundaries': {'pkg/contract.go': 'f' * 64}, 'state': state}}


def artifacts(entry):
    return {snapshot.key(entry['category'], entry['id']): entry}


class ArchitecturalReviewTests(unittest.TestCase):
    def test_snapshot_carries_review_without_altering_admission(self):
        _, entries = snapshot.validate(report([reviewed()]), 'a' * 40, 'b' * 40)
        entry = next(iter(entries.values()))
        self.assertEqual(entry['review']['state'], 'retained')
        self.assertEqual(entry['effective_cap'], 900)

    def test_invalid_review_refuses_entire_snapshot(self):
        changes = [{'kind': 'owner'}, {'workstream': 'unsafe\nvalue'}, {'disposition': 'ignore'},
                   {'reason': ''}, {'reviewed_commit': 'main'}, {'ceiling': 1000},
                   {'ceiling': True}, {'ceiling': 300}, {'boundaries': {}},
                   {'boundaries': {'../escape': 'f' * 64}}, {'state': 'unknown'},
                   {'state': 'ceiling_exceeded'}, {'extra': True}]
        for change in changes:
            entry = reviewed()
            entry['review'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)

    def test_retained_warning_closes_as_reviewed_and_growth_reopens(self):
        operations, _ = issues.plan([issue()], artifacts(reviewed()), observation('f' * 40))
        self.assertEqual(operations[0]['action'], 'retain')
        closed = issue(state='closed', state_reason='completed', body=operations[0]['body']['body'])
        self.assertIn('Retained by bounded architectural review', closed['body'])
        self.assertNotIn('Within the cleanup threshold or removed', closed['body'])
        self.assertEqual(issues.plan([closed], artifacts(reviewed()), observation('f' * 40)), ([], 0))
        operations, _ = issues.plan([closed], artifacts(reviewed('ceiling_exceeded', 401)), observation('1' * 40))
        self.assertEqual(operations[0]['action'], 'reopen')
        self.assertEqual(operations[0]['number'], 1)

    def test_changed_boundary_or_removed_decision_renews_review(self):
        closed = issue(state='closed', state_reason='completed', body=issues.block(reviewed(), observation(), True))
        for entry in [reviewed('boundary_changed'), row()]:
            operations, _ = issues.plan([closed], artifacts(entry), observation('f' * 40))
            self.assertEqual(operations[0]['action'], 'reopen')

    def test_retained_baseline_does_not_create_issue_noise(self):
        self.assertEqual(issues.plan([], artifacts(reviewed()), observation(), backfill=True), ([], 1))

    def test_dismissal_is_bounded_by_intake_measurement_and_policy(self):
        closed = issue(state='closed', state_reason='not_planned')
        self.assertEqual(issues.plan([closed], artifacts(row()), observation(), intake_keys=set()), ([], 1))
        for entry, intake in [(row(measured=351), set()), ({**row(), 'warn': 250}, set()),
                              (row(), set(artifacts(row())))]:
            operations, _ = issues.plan([closed], artifacts(entry), observation('f' * 40), intake_keys=intake)
            self.assertEqual(operations[0]['action'], 'reopen')

    def test_history_preserves_prior_observation_without_repeat_writes(self):
        operations, _ = issues.plan([issue()], artifacts(row(measured=380)), observation('e' * 40))
        current = issue(body=operations[0]['body']['body'])
        tracked = issues.tracked(current)
        self.assertEqual(tracked['prior_observation'], {'measured': 350, 'source_sha': 'a' * 40})
        self.assertIn('delta **+30**', current['body'])
        self.assertEqual(issues.plan([current], artifacts(row(measured=380)), observation('e' * 40)), ([], 0))

    def test_refactor_classification_groups_work_without_accepting_warning(self):
        entry = reviewed('refactor')
        entry['review'].update(disposition='refactor', ceiling=0, boundaries={})
        snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)
        operations, _ = issues.plan([], artifacts(entry), observation())
        self.assertEqual(operations[0]['action'], 'create')
        self.assertIn('host-lifecycle', operations[0]['body']['body'])

    def test_renames_do_not_inherit_another_artifacts_review(self):
        old = issue(state='closed', state_reason='completed', body=issues.block(reviewed(), observation(), True))
        entry = row('pkg/renamed.go')
        operations, _ = issues.plan([old], artifacts(entry), observation('f' * 40))
        self.assertEqual([op['action'] for op in operations], ['create'])
        self.assertNotIn('Retained by bounded', operations[0]['body']['body'])

    def test_review_reason_cannot_escape_machine_block(self):
        entry = reviewed()
        entry['review']['reason'] = '--> <script>bad</script>'
        current = issue(body=issues.block(entry, observation(), True))
        self.assertEqual(issues.tracked(current)['review']['reason'], entry['review']['reason'])
        self.assertNotIn('<script>', current['body'])

    def test_retained_issue_records_actual_numeric_resolution(self):
        closed = issue(state='closed', state_reason='completed', body=issues.block(reviewed(), observation(), True))
        operations, _ = issues.plan([closed], {}, observation('f' * 40))
        self.assertEqual(operations[0]['action'], 'update')
        resolved = issue(state='closed', state_reason='completed', body=operations[0]['body']['body'])
        self.assertIn('Last above-threshold measurement', resolved['body'])
        self.assertFalse(issues.tracked(resolved)['active'])
        self.assertEqual(issues.plan([resolved], {}, observation('1' * 40)), ([], 0))

    def test_same_size_type_change_renews_review(self):
        entry = reviewed('shape_changed')
        entry.update(category='go_struct_fields', id='pkg/p.Service', shape='1' * 64,
                     spans=[{'file': 'pkg/server.go', 'first': 1, 'last': 20}])
        entry['review']['reviewed_shape'] = '2' * 64
        snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)
        closed = issue(state='closed', state_reason='completed', body=issues.block(entry, observation(), True))
        operations, _ = issues.plan([closed], artifacts(entry), observation('f' * 40))
        self.assertEqual(operations[0]['action'], 'reopen')
        entry['review']['state'] = 'retained'
        with self.assertRaises(ValueError):
            snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)


class AgentFeedbackTests(unittest.TestCase):
    def test_retained_design_is_a_notice_without_hiding_its_measurement(self):
        value = display_report([finding("over_warn", "warm.go", 15, 10)])
        value['tracking'] = {'artifacts': [{**reviewed(), 'category': 'files', 'id': 'warm.go'}]}
        lines, failed = budgets.suite_findings('maintainability', value)
        self.assertFalse(failed)
        self.assertEqual(lines[0].level, 'notice')
        self.assertIn('15 lines', lines[0].text)
        self.assertIn('design decision retain (retained)', lines[0].text)

    def test_unreviewed_warning_offers_design_choices(self):
        lines, failed = budgets.suite_findings('maintainability', display_report([finding("over_warn", "warm.go", 15, 10)]))
        self.assertFalse(failed)
        self.assertIn('refactoring, consolidation, or bounded retention', lines[0].text)
