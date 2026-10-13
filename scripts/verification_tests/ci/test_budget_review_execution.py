"""Bounded architectural decisions in the source-bound issue lifecycle."""
import unittest

from ci_policy import budget_snapshot as snapshot
import budgets
from verification_tests.ci.test_change_report_execution import report as display_report, finding
from verification_tests.ci.test_budget_tracking_execution import row, report


def reviewed(state='retained', measured=350):
    return {**row(measured=measured), 'review': {
        'kind': 'runtime_service', 'workstream': 'host-lifecycle', 'disposition': 'retain',
        'reason': 'One ordered shutdown boundary.', 'reviewed_commit': 'e' * 40,
        'reviewed_shape': '', 'ceiling': 400, 'boundaries': {'pkg/contract.go': 'f' * 64}, 'state': state}}



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
                   {'state': 'ceiling_exceeded'}, {'reviewed_shape': False}, {'reviewed_shape': []}, {'extra': True}]
        for change in changes:
            entry = reviewed()
            entry['review'].update(change)
            with self.subTest(change=change), self.assertRaises(ValueError):
                snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)


    def test_same_size_structural_change_renews_review(self):
        for category, artifact in [('go_struct_fields', 'pkg/p.Service'),
                                   ('ts_local_dependencies', 'pkg/service.ts'),
                                   ('source_directories', 'pkg'), ('test_directories', 'pkg')]:
            with self.subTest(category=category):
                entry = reviewed('shape_changed')
                entry.update(category=category, id=artifact, shape='1' * 64,
                             spans=[{'file': 'pkg/server.go', 'first': 1, 'last': 20}])
                entry['review']['reviewed_shape'] = '2' * 64
                snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)
                entry['review']['state'] = 'retained'
                with self.assertRaises(ValueError):
                    snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)
                entry['shape'] = entry['review']['reviewed_shape']
                snapshot.validate(report([entry]), 'a' * 40, 'b' * 40)
                entry['review']['reviewed_shape'] = ''
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
