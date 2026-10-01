import pathlib
import json
import shutil
import tempfile
import unittest
from unittest.mock import patch
import outcomes


class WorkspaceTests(unittest.TestCase):
    def test_record_preservation_includes_nested_json_types(self):
        self.assertTrue(outcomes.same_json({'records': [{'score': 1}]}, {'records': [{'score': 1}]}))
        self.assertFalse(outcomes.same_json({'records': [{'score': True}]}, {'records': [{'score': 1}]}))
        self.assertFalse(outcomes.same_json({'records': [{'score': 1.0}]}, {'records': [{'score': 1}]}))

    def test_timeout_or_signal_is_not_successful_input_validation(self):
        for result in [(-1, '', 'candidate command did not finish'), (-9, '', 'killed')]:
            with patch.object(outcomes, 'document', return_value=result):
                self.assertFalse(outcomes.rejects({}))
        with patch.object(outcomes, 'document', return_value=(2, '', 'Expected list')):
            self.assertTrue(outcomes.rejects({}))

    def test_candidate_can_write_but_every_invocation_starts_fresh(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = pathlib.Path(temporary)
            (project / 'app.py').write_text(
                'from pathlib import Path\np = Path("counter")\n'
                'n = int(p.read_text()) if p.exists() else 0\n'
                'p.write_text(str(n+1))\nprint(n+1)\n')
            with patch.object(outcomes, 'PROJECT', project):
                for _ in range(2):
                    code, out, err = outcomes.invoke([])
                    self.assertEqual((code, out, err), (0, '1\n', ''))
            self.assertFalse((project / 'counter').exists())

    def test_error_words_do_not_decide_whether_rejection_was_handled(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = pathlib.Path(temporary)
            app = project / 'app.py'
            with patch.object(outcomes, 'PROJECT', project):
                app.write_text('import os, sys\nos.write(2, b"Traceback is a literal input value\\n")\nsys.exit(1)\n')
                self.assertTrue(outcomes.rejects({}))
                app.write_text('raise ValueError("invalid input")\n')
                self.assertFalse(outcomes.rejects({}))
                app.write_text('import os\nos.write(1, b"direct stdout\\n")\n')
                self.assertEqual(outcomes.invoke([]), (0, 'direct stdout\n', ''))


FIXTURES = pathlib.Path(__file__).resolve().parents[2] / 'lycaon/test/fixtures/eval'
REGRESSION = '''import unittest
from ranking import rank_profiles

class Regression(unittest.TestCase):
    def test_order(self):
        rows = [{'name':'z','score':9}, {'name':'b','score':1}, {'name':'a','score':9}]
        self.assertEqual([r['name'] for r in rank_profiles(rows)], ['a','z','b'])

    def test_default(self):
        rows = [{'name':str(i),'score':i} for i in range(12)]
        self.assertEqual(len(rank_profiles(rows)), DEFAULT)
'''


class AcceptanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.project = pathlib.Path(temporary.name) / 'candidate'
        self.baseline = FIXTURES / 'agent-project'
        shutil.copytree(self.baseline, self.project)
        for name, value in [('PROJECT', self.project), ('BASELINE', self.baseline), ('checks', [])]:
            patcher = patch.object(outcomes, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def fixed(self, limit=8):
        (self.project / 'ranking.py').write_text(
            f'DEFAULT_LIMIT = {limit}\n'
            'def rank_profiles(records, limit=DEFAULT_LIMIT):\n'
            '    return sorted(records, key=lambda r: (-r["score"], r["name"]))[:limit]\n')
        (self.project / 'test_regression.py').write_text(REGRESSION.replace('DEFAULT', str(limit)))

    def test_correct_repair_and_changed_default_have_a_passing_witness(self):
        for limit in [8, 3]:
            with self.subTest(limit=limit):
                self.fixed(limit)
                outcomes.checks.clear()
                outcomes.preserved_tests()
                outcomes.regression_coverage(limit)
                outcomes.ranking(limit)
                outcomes.service(False)
                self.assertTrue(all(c['passed'] for c in outcomes.checks),
                                [c for c in outcomes.checks if not c['passed']])

    def test_passing_but_irrelevant_tests_do_not_count_as_regression_coverage(self):
        self.fixed()
        (self.project / 'test_regression.py').write_text(
            'import unittest\nclass Regression(unittest.TestCase):\n'
            '    def test_nothing(self): self.assertTrue(True)\n')
        outcomes.regression_coverage(8)
        self.assertTrue(outcomes.checks[0]['passed'])
        self.assertTrue(all(not c['passed'] for c in outcomes.checks[1:]))

    def test_alternative_correct_implementation_is_accepted(self):
        self.fixed()
        (self.project / 'ranking.py').write_text(
            'def rank_profiles(records, *args, **options):\n'
            '    limit = args[0] if args else options.get("limit", 8)\n'
            '    by_name = sorted(records, key=lambda r: r["name"])\n'
            '    return sorted(by_name, key=lambda r: r["score"], reverse=True)[:limit]\n')
        outcomes.regression_coverage(8)
        outcomes.ranking()
        self.assertTrue(all(c['passed'] for c in outcomes.checks),
                        [c for c in outcomes.checks if not c['passed']])

    def test_regression_names_and_subprocess_tests_are_not_prescribed(self):
        self.fixed(3)
        (self.project / 'test_regression.py').unlink()
        package = self.project / 'tests'
        package.mkdir()
        (package / '__init__.py').write_text('')
        (package / 'test_ordering.py').write_text('''import json, subprocess, sys, unittest
class Ordering(unittest.TestCase):
    def test_cli(self):
        from pathlib import Path
        rows = [{'name':'z','score':9}, {'name':'b','score':1}, {'name':'a','score':9}, {'name':'x','score':0}]
        Path('input.json').write_text(json.dumps(rows))
        result = subprocess.run([sys.executable, 'app.py', 'input.json'], capture_output=True, text=True, check=True)
        self.assertEqual(result.stdout.splitlines()[2:], ['a                    9', 'z                    9', 'b                    1', 'Total: 3'])
''')
        outcomes.regression_coverage(3)
        self.assertTrue(all(c['passed'] for c in outcomes.checks), outcomes.checks)

    def test_existing_regressions_do_not_make_new_irrelevant_tests_pass(self):
        self.fixed()
        baseline = self.project.parent / 'baseline'
        shutil.copytree(self.baseline, baseline)
        body = (self.project / 'test_regression.py').read_text()
        (baseline / 'test_existing.py').write_text(body)
        (self.project / 'test_existing.py').write_text(body)
        (self.project / 'test_regression.py').write_text(
            'import unittest\nclass Extra(unittest.TestCase):\n def test_ok(self): self.assertTrue(True)\n')
        with patch.object(outcomes, 'BASELINE', baseline):
            outcomes.regression_coverage(8)
        self.assertTrue(outcomes.checks[0]['passed'])
        self.assertTrue(all(not c['passed'] for c in outcomes.checks[1:]))

    def test_skipped_tests_and_changed_baseline_files_fail(self):
        self.fixed()
        original = self.project / 'test_app.py'
        original.write_text(original.read_text().replace('class ReportTests', '@unittest.skip("disabled")\nclass ReportTests'))
        outcomes.preserved_tests()
        self.assertTrue(all(not c['passed'] for c in outcomes.checks))

    def test_ordering_oracle_rejects_tie_reversal_input_mutation_and_limit_loss(self):
        bodies = [
            'return sorted(records, key=lambda r: (r["score"], r["name"]), reverse=True)[:limit]',
            'records.sort(key=lambda r: (-r["score"], r["name"])); return records[:limit]',
            'return sorted(records, key=lambda r: (-r["score"], r["name"]))[:8]',
        ]
        for body in bodies:
            with self.subTest(body=body):
                (self.project / 'ranking.py').write_text('def rank_profiles(records, limit=8):\n    ' + body + '\n')
                outcomes.checks.clear()
                outcomes.ranking()
                self.assertFalse(all(c['passed'] for c in outcomes.checks))

    def test_orientation_types_and_preservation_are_exact(self):
        expected = {'entry_file': 'app.py', 'pipeline': ['source', 'ranking', 'assembly'],
                    'score_order': 'ascending', 'tie_order': 'ascending', 'default_limit': 8,
                    'mutates_input': False, 'unused_module': 'collector'}
        artifact = self.project / 'analysis.json'
        artifact.write_text(json.dumps(expected))
        outcomes.orientation()
        self.assertTrue(all(c['passed'] for c in outcomes.checks))
        for altered in [{**expected, 'mutates_input': 0}, {**expected, 'default_limit': 8.0},
                        {**expected, 'score_order': 'descending'}, {**expected, 'extra': True}]:
            artifact.write_text(json.dumps(altered))
            outcomes.checks.clear()
            outcomes.orientation()
            self.assertFalse(outcomes.checks[0]['passed'])


if __name__ == '__main__':
    unittest.main()
