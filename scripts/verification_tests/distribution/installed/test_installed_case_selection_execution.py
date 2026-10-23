"""A bounded native case selection cannot omit unknown or duplicate identifiers."""
import unittest

from linux_release.installed_case_selection import selected_cases

ROWS = [{'capability': 'conversation', 'cases': ['restart-and-reconnect', 'provider-onboarding']}]


class CaseSelectionTests(unittest.TestCase):
    def test_absent_selection_keeps_full_execution_and_exact_selection_is_scoped(self):
        self.assertIsNone(selected_cases(None, ROWS))
        chosen = selected_cases({'format_version': 1, 'cases': [
            {'capability': 'conversation', 'case': 'restart-and-reconnect'}]}, ROWS)
        self.assertEqual(chosen, {('conversation', 'restart-and-reconnect')})
        self.assertNotIn(('conversation', 'provider-onboarding'), chosen)

    def test_unknown_foreign_duplicate_and_unbounded_selection_refuse(self):
        selected = {'capability': 'conversation', 'case': 'restart-and-reconnect'}
        for cases in ([], [selected] * 65, [selected, selected], [{'capability': 'foreign', 'case': 'restart-and-reconnect'}],
                      [{'capability': 'conversation', 'case': 'foreign'}], [{'capability': [], 'case': 'restart-and-reconnect'}],
                      [{'capability': 'conversation', 'case': 'restart-and-reconnect', 'command': 'arbitrary'}]):
            with self.subTest(cases=cases), self.assertRaises(ValueError):
                selected_cases({'format_version': 1, 'cases': cases}, ROWS)
        for value in ({}, {'format_version': True, 'cases': [selected]},
                      {'format_version': 1, 'cases': [selected], 'skip': True}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                selected_cases(value, ROWS)
