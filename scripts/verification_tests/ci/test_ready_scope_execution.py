import copy
from contextlib import ExitStack
import unittest
import verification_plan
from unittest.mock import patch

from ci_policy import ready_scope


class ReadyScopeTests(unittest.TestCase):
    def test_scoping_keeps_failed_work_first_and_every_other_target_once(self):
        phases = [['./task', 'test:short'], ['./task', 'build', 'lint:full', 'docs:links']]
        with patch('ci_policy.impact.go_scope', return_value=['./test/contract/host', './internal/foo', './test/smoke']):
            selected, packages = ready_scope.commands(phases, {'full': False})
        self.assertEqual(packages, ['./internal/foo'])
        self.assertEqual(selected, [['./task', 'test:short', '--', './internal/foo'], phases[1]])

    def test_unknown_scope_runs_the_complete_short_recipe(self):
        phases = [['./task', 'build', 'test:short', 'docs:links']]
        with patch('ci_policy.impact.go_scope', return_value=None):
            self.assertEqual(ready_scope.commands(phases, {'full': True}), (phases, None))
        self.assertEqual(ready_scope.commands(phases, None), (phases, None))

    def test_empty_affected_go_scope_keeps_other_checks_and_no_empty_invocation(self):
        phases = [['./task', 'test:short'], ['./task', 'build', 'test:short', 'docs:links']]
        with patch('ci_policy.impact.go_scope', return_value=['./test/contract/host']):
            selected, packages = ready_scope.commands(phases, {'full': False})
        self.assertEqual(packages, [])
        self.assertEqual(selected, [['./task', 'build'], ['./task', 'docs:links']])

    def test_scoping_partitions_a_combined_phase_without_reordering_checks(self):
        phases = [['./task', 'build', 'test:short', 'lint:full']]
        with patch('ci_policy.impact.go_scope', return_value=['./internal/foo']):
            selected, _ = ready_scope.commands(phases, {'full': False})
        self.assertEqual(selected, [['./task', 'build'], ['./task', 'test:short', '--', './internal/foo'],
                                    ['./task', 'lint:full']])

    def partition(self, selected=None, changed=None, history=None, phases=None, mutate=None):
        metadata = {'github.com/lycaon/lycaon/internal/' + name:
                    {'dir': 'lycaon/internal/' + name, 'tested': name != 'untested'}
                    for name in ('changed', 'dependent', 'exempt', 'untested')}
        selected = selected if selected is not None else ['./internal/changed', './internal/dependent']
        changed = changed if changed is not None else ['lycaon/internal/changed/a.go']
        phases = phases or [['./task', 'build', 'budgets', 'coverage:changes', 'test:short', 'docs:links']]
        data = copy.deepcopy(verification_plan.catalog())
        if mutate:
            mutate(data)
        with ExitStack() as stack:
            stack.enter_context(patch('ci_policy.impact.go_scope', return_value=selected))
            stack.enter_context(patch('coverage_changes.go_changed_files', return_value=changed))
            stack.enter_context(patch('coverage_changes.go_packages', return_value=metadata))
            stack.enter_context(patch('coverage_changes.exempt_packages', return_value=['internal/exempt']))
            stack.enter_context(patch.object(ready_scope, 'catalog', return_value=data))
            return ready_scope.commands(phases, {'full': selected is None, 'paths': changed}, history,
                                        ['budgets', 'coverage:changes'])

    def test_changed_own_tests_run_in_coverage_before_remaining_short_tests(self):
        selected, packages = self.partition()
        self.assertEqual(packages, ['./internal/changed', './internal/dependent'])
        self.assertEqual(selected, [['./task', 'budgets', 'coverage:changes'], ['./task', 'build'],
                                   ['./task', 'test:short', '--', './internal/dependent'], ['./task', 'docs:links']])

    def test_failed_package_runs_first_and_is_not_in_remaining_short_selection(self):
        phases, _ = self.partition(history={'failed_targets': ['test:short'],
                                           'failed_packages': ['./internal/dependent']})
        self.assertEqual(phases[0], ['./task', 'test:short', '--', './internal/dependent'])
        self.assertEqual(phases[1], ['./task', 'budgets', 'coverage:changes'])
        self.assertEqual(sum('test:short' in phase for phase in phases), 1)
        self.assertEqual(sum('build' in phase for phase in phases), 1)
        self.assertEqual(sum('docs:links' in phase for phase in phases), 1)

    def test_unknown_failed_package_preserves_complete_failed_short_selection(self):
        for names in ([], ['./internal/deleted', '../escape', './internal/dependent;touch marker']):
            phases, _ = self.partition(history={'failed_targets': ['test:short'], 'failed_packages': names})
            self.assertEqual(phases[0], ['./task', 'test:short', '--', './internal/changed', './internal/dependent'])
            self.assertEqual(phases[1], ['./task', 'budgets', 'coverage:changes'])

    def test_existing_failed_target_precedes_early_diagnostics_without_omissions(self):
        phases, _ = self.partition(history={'failed_targets': ['docs:links']})
        self.assertEqual(phases[0], ['./task', 'docs:links'])
        self.assertEqual(phases[1], ['./task', 'budgets', 'coverage:changes'])
        self.assertEqual(sum('docs:links' in phase for phase in phases), 1)

    def test_exempt_and_untested_changed_packages_remain_in_short_selection(self):
        phases, _ = self.partition(selected=['./internal/exempt', './internal/untested'],
                                  changed=['lycaon/internal/exempt/a.go', 'lycaon/internal/untested/a.go'])
        self.assertIn(['./task', 'test:short', '--', './internal/exempt', './internal/untested'], phases)

    def test_test_only_changes_do_not_remove_short_tests(self):
        phases, _ = self.partition(changed=[])
        self.assertIn(['./task', 'test:short', '--', './internal/changed', './internal/dependent'], phases)

    def test_future_short_recipe_options_disable_coverage_partition(self):
        phases, _ = self.partition(mutate=lambda data: data['go']['test:short'].update(options=['-race']))
        self.assertIn(['./task', 'test:short', '--', './internal/changed', './internal/dependent'], phases)

    def test_full_go_scope_uses_current_metadata_and_keeps_every_uncovered_package(self):
        metadata = {'github.com/lycaon/lycaon/internal/' + name:
                    {'dir': 'lycaon/internal/' + name, 'tested': True} for name in ('changed', 'dependent')}
        with patch('ci_policy.impact.go_scope', return_value=None), \
                patch('coverage_changes.go_changed_files', return_value=['lycaon/internal/changed/a.go']), \
                patch('coverage_changes.go_packages', return_value=metadata), \
                patch('coverage_changes.exempt_packages', return_value=[]):
            phases, packages = ready_scope.commands([['./task', 'coverage:changes', 'test:short']],
                {'full': True, 'paths': []}, early=['coverage:changes'])
        self.assertEqual(packages, ['./internal/changed', './internal/dependent'])
        self.assertEqual(phases, [['./task', 'coverage:changes'], ['./task', 'test:short', '--', './internal/dependent']])

    def test_metadata_failure_refuses_partition_instead_of_dropping_tests(self):
        with patch('ci_policy.impact.go_scope', return_value=None), \
                patch('coverage_changes.go_changed_files', return_value=['lycaon/internal/changed/a.go']), \
                patch('coverage_changes.go_packages', side_effect=ValueError('invalid metadata')):
            with self.assertRaisesRegex(ValueError, 'invalid metadata'):
                ready_scope.commands([['./task', 'coverage:changes', 'test:short']],
                                     {'full': True, 'paths': []}, early=['coverage:changes'])

    def test_duplicate_early_targets_refuse_to_hide_duplicate_execution(self):
        with self.assertRaisesRegex(ValueError, 'more than once'):
            self.partition(phases=[['./task', 'budgets', 'budgets', 'coverage:changes', 'test:short']])
