import shutil
import unittest
from unittest.mock import patch

from candidate_tests import changed_test_paths
from unittest_structure import preserved_definitions
import outcomes
import test_outcomes


class DefinitionTests(unittest.TestCase):
    original = '''import unittest
class Tests(unittest.TestCase):
    def test_original(self):
        self.assertEqual(1, 1)
'''

    def test_additions_and_formatting_preserve_existing_definitions(self):
        for suffix in ('    def test_new(self): self.assertTrue(True)\n',
                       '\nclass Added(unittest.TestCase):\n    def test_new(self): self.assertTrue(True)\n'):
            self.assertTrue(preserved_definitions(self.original, self.original + suffix))
        self.assertTrue(preserved_definitions(self.original, self.original.replace('1, 1', '1,    1')))

    def test_removed_changed_decorated_and_shadowed_definitions_fail(self):
        candidates = [
            self.original.replace('test_original', 'test_replacement'),
            self.original.replace('assertEqual(1, 1)', 'assertTrue(True)'),
            self.original.replace('class Tests', '@unittest.skip("disabled")\nclass Tests'),
            self.original.replace('    def test_original', '    @unittest.skip("disabled")\n    def test_original'),
            self.original + '    def test_original(self): pass\n',
            self.original + '\nclass Tests(unittest.TestCase): pass\n',
            self.original + '\ndef Tests(): pass\n',
        ]
        for candidate in candidates:
            with self.subTest(candidate=candidate):
                self.assertFalse(preserved_definitions(self.original, candidate))


class ExistingFileCoverageTests(unittest.TestCase):
    setUp = test_outcomes.AcceptanceTests.setUp
    fixed = test_outcomes.AcceptanceTests.fixed
    def test_added_methods_in_existing_file_detect_mutations(self):
        self.fixed()
        (self.project / 'test_regression.py').unlink()
        path = self.project / 'test_app.py'
        addition = test_outcomes.REGRESSION.split('class Regression(unittest.TestCase):\n', 1)[1].replace('DEFAULT', '8')
        path.write_text(path.read_text().replace('\n\nif __name__', '\n' + addition + '\n\nif __name__'))
        outcomes.preserved_tests()
        outcomes.regression_coverage(8)
        self.assertTrue(all(c['passed'] for c in outcomes.checks), outcomes.checks)
        self.assertEqual(changed_test_paths(self.project, self.baseline), {'test_app.py'})
        outcomes.checks.clear()
        outcomes.preserved_tests(byte_identical=True)
        self.assertFalse(outcomes.checks[0]['passed'])

    def test_new_class_in_existing_file_counts_but_baseline_tests_do_not(self):
        self.fixed()
        regression = self.project / 'test_regression.py'
        body = regression.read_text()
        regression.unlink()
        path = self.project / 'test_app.py'
        path.write_text(path.read_text() + '\n' + body)
        outcomes.regression_coverage(8)
        self.assertTrue(all(c['passed'] for c in outcomes.checks), outcomes.checks)
        baseline = self.project.parent / 'baseline-with-regression'
        shutil.copytree(self.project, baseline)
        path.write_text(path.read_text() + '\nclass Extra(unittest.TestCase):\n    def test_nothing(self): self.assertTrue(True)\n')
        outcomes.checks.clear()
        with patch.object(outcomes, 'BASELINE', baseline):
            outcomes.regression_coverage(8)
        self.assertTrue(outcomes.checks[0]['passed'])
        self.assertTrue(all(not c['passed'] for c in outcomes.checks[1:]), outcomes.checks)

    def test_execution_exception_detects_mutation_but_discovery_error_does_not(self):
        self.fixed()
        path = self.project / 'test_regression.py'
        setup = "from ranking import rank_profiles\nimport unittest\n"
        expression = "rank_profiles([{'name':'low','score':1}, {'name':'high','score':9,'winner':True}])[0]['winner']"
        path.write_text(setup + 'class Regression(unittest.TestCase):\n    def test_winner(self):\n        self.assertTrue(' + expression + ')\n')
        self.assertTrue(outcomes.successful_tests(outcomes.test_result(new_only=True)))
        result = outcomes.test_result(new_only=True, mutation='ascending')
        self.assertEqual(result['failures'], 0)
        self.assertEqual(result['errors'], 1)
        self.assertTrue(outcomes.detects_mutation(result))
        path.write_text(setup + 'winner = ' + expression + '\nclass Regression(unittest.TestCase):\n    def test_winner(self): self.assertTrue(winner)\n')
        self.assertTrue(outcomes.successful_tests(outcomes.test_result(new_only=True)))
        result = outcomes.test_result(new_only=True, mutation='ascending')
        self.assertEqual(result['discovery_errors'], 1)
        self.assertFalse(outcomes.detects_mutation(result))


if __name__ == '__main__':
    unittest.main()
