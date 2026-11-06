import copy
from contextlib import ExitStack
import unittest
import verification_plan
from unittest.mock import patch

from ci_policy import ready_scope

CONTRACTS = ['./task', 'test:contract', '--', './test/contract/...']


class ReadyScopeTests(unittest.TestCase):
    def phases(self, selected, changed=(), mutate=None):
        metadata = {'github.com/lycaon/lycaon/internal/' + name:
                    {'dir': 'lycaon/internal/' + name, 'tested': name != 'untested'}
                    for name in ('changed', 'dependent', 'exempt', 'untested')}
        data = copy.deepcopy(verification_plan.catalog())
        if mutate:
            mutate(data)
        with ExitStack() as stack:
            stack.enter_context(patch('ci_policy.impact.go_scope', return_value=selected))
            stack.enter_context(patch('coverage_changes.go_changed_files', return_value=list(changed)))
            stack.enter_context(patch('coverage_changes.go_packages', return_value=metadata))
            stack.enter_context(patch('coverage_changes.exempt_packages', return_value=['internal/exempt']))
            stack.enter_context(patch.object(ready_scope, 'catalog', return_value=data))
            return ready_scope.test_phases({'full': selected is None, 'paths': list(changed)})

    def test_contracts_always_run_before_the_affected_short_tests(self):
        selected, packages = self.phases(['./test/contract/host', './internal/foo', './test/smoke'])
        self.assertEqual(packages, ['./internal/foo'])
        self.assertEqual(selected, [CONTRACTS, ['./task', 'test:short', '--', './internal/foo']])

    def test_unknown_scope_runs_the_complete_short_recipe(self):
        self.assertEqual(self.phases(None), ([CONTRACTS, ['./task', 'test:short']], None))
        self.assertEqual(ready_scope.test_phases(None), ([CONTRACTS, ['./task', 'test:short']], None))

    def test_empty_affected_go_scope_has_no_empty_invocation(self):
        selected, packages = self.phases(['./test/contract/host'])
        self.assertEqual((selected, packages), ([CONTRACTS], []))

    def test_changed_coverage_runs_a_package_once(self):
        selected, packages = self.phases(['./internal/changed', './internal/dependent'], ['lycaon/internal/changed/a.go'])
        self.assertEqual(packages, ['./internal/changed', './internal/dependent'])
        self.assertEqual(selected, [CONTRACTS, ['./task', 'test:short', '--', './internal/dependent']])

    def test_test_only_changes_do_not_remove_short_tests(self):
        selected, _ = self.phases(['./internal/changed'])
        self.assertEqual(selected, [CONTRACTS, ['./task', 'test:short', '--', './internal/changed']])

    def test_exempt_and_untested_changed_packages_remain_in_short_selection(self):
        selected, _ = self.phases(['./internal/exempt', './internal/untested'],
                                  ['lycaon/internal/exempt/a.go', 'lycaon/internal/untested/a.go'])
        self.assertEqual(selected, [CONTRACTS, ['./task', 'test:short', '--', './internal/exempt', './internal/untested']])

    def test_future_short_recipe_options_disable_coverage_partition(self):
        def mutate(data):
            data['go']['test:short']['options'] = ['--race']
        selected, _ = self.phases(['./internal/changed'], ['lycaon/internal/changed/a.go'], mutate)
        self.assertEqual(selected, [CONTRACTS, ['./task', 'test:short', '--', './internal/changed']])

    def test_full_go_scope_uses_current_metadata_and_keeps_every_uncovered_package(self):
        selected, _ = self.phases(None, ['lycaon/internal/changed/a.go'])
        self.assertEqual(selected, [CONTRACTS, ['./task', 'test:short', '--', './internal/dependent', './internal/exempt',
                                                './internal/untested']])

    def test_metadata_failure_refuses_partition_instead_of_dropping_tests(self):
        with patch('ci_policy.impact.go_scope', return_value=['./internal/changed']), \
                patch('coverage_changes.go_changed_files', return_value=['lycaon/internal/changed/a.go']), \
                patch('coverage_changes.go_packages', side_effect=ValueError('metadata')), \
                self.assertRaises(ValueError):
            ready_scope.test_phases({'full': False, 'paths': ['lycaon/internal/changed/a.go']})

    def test_checks_run_native_admission_then_early_targets_then_the_rest(self):
        with patch('ci_policy.native_admission.commands', return_value=[['./task', 'native']]):
            self.assertEqual(ready_scope.check_phases(['build', 'budgets', 'docs:links'], {'full': False}, ['budgets', 'absent']),
                             [['./task', 'native'], ['./task', 'budgets'], ['./task', 'build', 'docs:links']])


if __name__ == '__main__':
    unittest.main()
