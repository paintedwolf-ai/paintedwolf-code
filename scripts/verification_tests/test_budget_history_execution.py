"""Warning intake survives superseded qualification/reconciliation runs."""
import io
import json
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import budget_history as history
from ci_policy.budget_snapshot import validate
from verification_tests.test_budget_tracking_execution import report, row
from verification_tests.test_budget_reconciliation_execution import run


class HistoryTests(unittest.TestCase):
    def test_intake_uses_cumulative_diff_with_exact_type_and_directory_scope(self):
        source = {**row(), 'touched': False}
        directory = {**row('pkg', 'source_directories'), 'touched': False, 'sources': ['pkg']}
        receiver = {**row('pkg/pkg.Server', 'go_receiver_lines'), 'touched': False,
                    'spans': [{'file': 'pkg/server.go', 'first': 20, 'last': 30}]}
        _, latest = validate(report([source, directory, receiver]), 'a' * 40, 'b' * 40)
        prior = dict(source_sha='c' * 40)
        for changed, additions, expected in [
            ({'pkg/server.go': {5}}, [], {'source_files'}),
            ({'pkg/server.go': {25}}, [], {'source_files', 'go_receiver_lines'}),
            ({'pkg/new.go': {1}}, ['pkg/new.go'], {'source_directories'}),
        ]:
            with self.subTest(changed=changed), patch.object(history, 'ancestor', return_value=True), \
                    patch.object(history.change_report, 'git') as git, \
                    patch.object(history.change_report, 'changed_paths', return_value=list(changed)), \
                    patch.object(history.change_report, 'changed_lines', return_value=changed), \
                    patch.object(history.change_report, 'added_and_removed', return_value=(additions, [])):
                git.return_value.stdout = 'a' * 40
                identities = history.intake(run(), latest, prior)
            self.assertEqual({latest[identity]['category'] for identity in identities}, expected)

    def test_initial_intake_uses_installation_baseline_without_backfilling_untouched_debt(self):
        _, latest = validate(report([{**row(), 'touched': False}]), 'a' * 40, 'b' * 40)
        with patch.object(history, 'initial_base', return_value='c' * 40), \
                patch.object(history, 'ancestor', return_value=True), \
                patch.object(history.change_report, 'git') as git, \
                patch.object(history.change_report, 'changed_paths', return_value=[]), \
                patch.object(history.change_report, 'changed_lines', return_value={}), \
                patch.object(history.change_report, 'added_and_removed', return_value=([], [])):
            git.return_value.stdout = 'a' * 40
            self.assertEqual(history.intake(run(), latest, None), set())
        with patch.object(history.change_report, 'git') as git:
            self.assertEqual(history.intake(run(), latest, {'source_sha': 'a' * 40}), set())
        git.assert_not_called()

    def test_installation_baseline_is_the_first_tracking_commit_parent(self):
        from subprocess import CompletedProcess
        with patch.object(history.change_report, 'git', side_effect=[
            CompletedProcess([], 0, 'b' * 40 + '\n' + 'c' * 40 + '\n'), CompletedProcess([], 0, 'd' * 40 + '\n')
        ]) as git:
            self.assertEqual(history.initial_base('root'), 'd' * 40)
        self.assertEqual(git.call_args.args[-1], 'c' * 40 + '^')
        with patch.object(history.change_report, 'git') as git, self.assertRaises(ValueError):
            git.return_value.stdout = ''
            history.initial_base('root')

    def test_missing_history_and_mismatched_checkout_require_explicit_recovery(self):
        _, latest = validate(report(), 'a' * 40, 'b' * 40)
        prior = dict(source_sha='c' * 40)
        with patch.object(history, 'ancestor', return_value=False), self.assertRaises(ValueError):
            history.intake(run(), latest, prior)
        with patch.object(history, 'ancestor', return_value=True), \
                patch.object(history.change_report, 'git') as git, self.assertRaises(ValueError):
            git.return_value.stdout = 'wrong-checkout'
            history.intake(run(), latest, prior)

    def test_checkpoint_ignores_preview_artifacts_and_reads_applied_state(self):
        previous = dict(id=1, run_attempt=1)
        applied = dict(id=2, run_attempt=1)
        state = dict(schema_version=1, source_sha='c' * 40)
        archives = []
        for payload in [None, state]:
            raw = io.BytesIO()
            with zipfile.ZipFile(raw, 'w') as archive:
                archive.writestr('maintainability-issues.json', '{}')
                if payload:
                    archive.writestr('maintainability-checkpoint.json', json.dumps(payload))
            archives.append(raw.getvalue())
        preview = dict(id=10, name='maintainability-issues-1-1', expired=False, size_in_bytes=len(archives[0]))
        artifact = dict(id=20, name='maintainability-issues-2-1', expired=False, size_in_bytes=len(archives[1]))
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(history, 'pages', side_effect=[[previous, applied], [preview], [artifact]]), \
                patch.object(history.subprocess, 'check_output', side_effect=archives):
            self.assertEqual(history.checkpoint(), state)

    def test_expired_checkpoint_cannot_silently_reset_intake(self):
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(history, 'pages', side_effect=[[dict(id=1, run_attempt=1)],
                    [dict(name='maintainability-issues-1-1', expired=True)]]), self.assertRaises(ValueError):
            history.checkpoint()

    def test_ancestry_errors_are_not_treated_as_missing_debt(self):
        for code, result in [(0, True), (1, False)]:
            with patch.object(history.subprocess, 'run') as command:
                command.return_value.returncode = code
                self.assertEqual(history.ancestor('c' * 40, 'a' * 40), result)
        with patch.object(history.subprocess, 'run') as command, self.assertRaises(ValueError):
            command.return_value.returncode = 128
            history.ancestor('c' * 40, 'a' * 40)
