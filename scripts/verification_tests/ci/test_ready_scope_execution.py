import unittest
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
