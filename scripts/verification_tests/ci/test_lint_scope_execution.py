import unittest
from unittest.mock import patch

from ci_policy import lint_scope


class LintScopeTests(unittest.TestCase):
    def test_explicit_scope_reaches_analysis_without_appending_the_whole_module(self):
        commands = lint_scope.commands('full', '/tool/golangci-lint', '30m',
                                       ['--scope', './internal/launchguard', '--show-stats'])
        self.assertEqual(commands[0], ['/tool/golangci-lint', 'run', '--timeout', '30m',
                                       '--show-stats', './internal/launchguard'])
        self.assertTrue(commands[1][1].endswith('/scripts/deadcode-check.sh'))
        self.assertFalse(any('./...' in command for command in commands))

    def test_default_and_fast_modes_keep_their_original_obligations(self):
        commands = lint_scope.commands('fast', 'golangci-lint', '30m', [])
        self.assertEqual(commands[0], ['go', 'vet', './...'])
        self.assertIn('--fast-only', commands[1])
        self.assertEqual(commands[1][-1], './...')
        self.assertEqual(len(commands), 2)
        for scope in ('../outside', './internal/no_such_package'):
            with self.assertRaises(ValueError):
                lint_scope.commands('full', 'golangci-lint', '30m', ['--scope', scope])

    def test_first_failure_stops_later_checks_and_preserves_the_exit_code(self):
        with patch.object(lint_scope.subprocess, 'call', side_effect=[0, 1]) as run:
            self.assertEqual(lint_scope.main(['full', 'golangci-lint', '30m', '--scope', './internal/launchguard']), 1)
            self.assertEqual(run.call_count, 2)
