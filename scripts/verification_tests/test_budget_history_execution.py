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
    def test_intake_replays_skipped_merges_against_latest_inventory(self):
        _, latest = validate(report([{**row(), 'touched': False}]), 'a' * 40, 'b' * 40)
        _, earlier = validate(report(), 'a' * 40, 'b' * 40)
        historical = {**run(), 'head_sha': 'e' * 40, 'id': 41}
        prior = dict(source_sha='c' * 40, qualification_created_at='2026-10-09T10:00:00Z')
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(history, 'ancestor', return_value=True), \
                patch.object(history, 'pages', return_value=[historical, {**historical, 'run_attempt': 2}]), \
                patch.object(history, 'merged_pulls', return_value=[{'number': 7}]), \
                patch.object(history, 'api', return_value={'tree': {'sha': 'b' * 40}}), \
                patch.object(history, 'read_snapshot', return_value=({}, earlier)) as read:
            self.assertEqual(history.intake(run(), latest, prior, 'owner/repo'), set(earlier))
        self.assertEqual(read.call_count, 1)
        self.assertEqual(read.call_args.args[0]['run_attempt'], 2)

    def test_initial_checkpoint_does_not_backfill_untouched_baseline(self):
        _, latest = validate(report([{**row(), 'touched': False}]), 'a' * 40, 'b' * 40)
        with patch.object(history, 'pages') as pages:
            self.assertEqual(history.intake(run(), latest, None, 'owner/repo'), set())
            self.assertEqual(history.intake(run(), latest, {'source_sha': 'a' * 40}, 'owner/repo'), set())
        pages.assert_not_called()

    def test_history_gaps_and_rewritten_main_require_explicit_recovery(self):
        _, latest = validate(report(), 'a' * 40, 'b' * 40)
        prior = dict(source_sha='c' * 40, qualification_created_at='2026-10-09T10:00:00Z')
        with patch.object(history, 'ancestor', return_value=False), self.assertRaises(ValueError):
            history.intake(run(), latest, prior, 'owner/repo')
        with patch.dict('os.environ', GITHUB_REPOSITORY='owner/repo'), \
                patch.object(history, 'ancestor', return_value=True), \
                patch.object(history, 'pages', return_value=[{**run(), 'head_sha': 'e' * 40}]), \
                patch.object(history, 'merged_pulls', return_value=[{'number': 7}]), \
                patch.object(history, 'api', return_value={'tree': {'sha': 'b' * 40}}), \
                patch.object(history, 'read_snapshot', side_effect=ValueError('missing report')), self.assertRaises(ValueError):
            history.intake(run(), latest, prior, 'owner/repo')

    def test_checkpoint_ignores_preview_artifacts_and_reads_applied_state(self):
        previous = dict(id=1, run_attempt=1)
        applied = dict(id=2, run_attempt=1)
        state = dict(schema_version=1, source_sha='c' * 40, qualification_created_at='2026-10-09T10:00:00Z')
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
