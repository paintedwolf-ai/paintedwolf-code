"""Warning intake survives superseded nightly/reconciliation runs."""
import io
import json
import unittest
import zipfile
from unittest.mock import patch

from ci_policy import budget_history as history
from ci_policy.budget_snapshot import validate
from verification_tests.ci.test_budget_tracking_execution import report, row
from verification_tests.ci.test_budget_reconciliation_execution import run


class HistoryTests(unittest.TestCase):
    def test_intake_uses_cumulative_diff_with_exact_type_and_directory_scope(self):
        source = {**row(), 'touched': False}
        directory = {**row('pkg', 'source_directories'), 'touched': False, 'sources': ['pkg']}
        receiver = {**row('pkg/pkg.Server', 'go_receiver_lines'), 'touched': False,
                    'spans': [{'file': 'pkg/server.go', 'first': 20, 'last': 30}]}
        _, latest = validate(report([source, directory, receiver]), 'a' * 40, 'b' * 40)
        prior = dict(source_sha='c' * 40)
        for paths, changed, additions, expected in [
            (['pkg/server.go'], {'pkg/server.go': {5}}, [], {'source_files'}),
            (['pkg/server.go'], {'pkg/server.go': {25}}, [], {'source_files', 'go_receiver_lines'}),
            (['pkg/new.go'], {'pkg/new.go': {1}}, ['pkg/new.go'], {'source_directories'}),
            (['pkg/server.go'], {}, [], {'source_files'}),
        ]:
            with self.subTest(changed=changed), patch.object(history, 'ancestor', return_value=True), \
                    patch.object(history.change_report, 'changed_paths', return_value=paths) as diff, \
                    patch.object(history.change_report, 'changed_lines', return_value=changed) as hunks, \
                    patch.object(history.change_report, 'added_and_removed', return_value=(additions, [])) as entries:
                identities = history.intake(run(), latest, prior)
            self.assertEqual({latest[identity]['category'] for identity in identities}, expected)
            # Intake measures the report source, not whatever main the checkout holds.
            for call in [diff, hunks, entries]:
                self.assertEqual(call.call_args.args[-1], 'a' * 40)

    def test_removed_or_retargeted_review_enters_intake_without_source_change(self):
        import subprocess
        import tempfile
        from pathlib import Path
        from ci_policy.budget_snapshot import key
        first, second = key('source_files', 'pkg/one.go'), key('source_files', 'pkg/two.go')
        records = [dict(category='source_files', artifact='pkg/one.go'),
                   dict(category='source_files', artifact='pkg/two.go')]
        path = 'lycaon/test/contract/maintainability-reviews/design.json'
        with tempfile.TemporaryDirectory() as directory:
            def git(*arguments):
                return subprocess.check_output(['git', '-C', directory, *arguments], text=True).strip()
            git('init', '-q')
            git('config', 'user.name', 'Fixture')
            git('config', 'user.email', 'fixture@example.invalid')
            file = Path(directory) / path
            file.parent.mkdir(parents=True)
            file.write_text(json.dumps(records[0]))
            git('add', '--', path)
            git('commit', '-qm', 'Initial decision')
            base = git('rev-parse', 'HEAD')
            file.write_text(json.dumps(records[1]))
            git('add', '--', path)
            git('commit', '-qm', 'Retarget decision')
            changed = git('rev-parse', 'HEAD')
            self.assertEqual(history.changed_records(directory, base, changed, [path], (history.REVIEWS,)), {first, second})
            file.unlink()
            git('add', '--', path)
            git('commit', '-qm', 'Remove decision')
            self.assertEqual(history.changed_records(directory, base, git('rev-parse', 'HEAD'), [path], (history.REVIEWS,)), {first})
            self.assertEqual(history.changed_records(directory, base, changed, [path], (history.EXCEPTIONS,)), set())

    def test_initial_intake_uses_installation_baseline_without_backfilling_untouched_debt(self):
        _, latest = validate(report([{**row(), 'touched': False}]), 'a' * 40, 'b' * 40)
        with patch.object(history, 'initial_base', return_value='c' * 40), \
                patch.object(history, 'ancestor', return_value=True), \
                patch.object(history.change_report, 'changed_paths', return_value=[]), \
                patch.object(history.change_report, 'changed_lines', return_value={}), \
                patch.object(history.change_report, 'added_and_removed', return_value=([], [])):
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

    def test_missing_history_requires_explicit_recovery(self):
        _, latest = validate(report(), 'a' * 40, 'b' * 40)
        with patch.object(history, 'ancestor', return_value=False), self.assertRaises(ValueError):
            history.intake(run(), latest, dict(source_sha='c' * 40))

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
                self.assertEqual(history.ancestor('root', 'c' * 40, 'a' * 40), result)
        with patch.object(history.subprocess, 'run') as command, self.assertRaises(ValueError):
            command.return_value.returncode = 128
            history.ancestor('root', 'c' * 40, 'a' * 40)


class MeasuredContentTests(unittest.TestCase):
    def test_measured_scope_ties_each_category_to_its_paths(self):
        for entry, scope in [(row(), ('file', 'pkg/server.go')),
                             ({**row('pkg', 'source_directories'), 'sources': ['pkg']}, ('directory', 'pkg')),
                             (row('pkg/pkg.Server', 'go_receiver_methods'), ('directory', 'pkg')),
                             ({'category': 'go_struct_fields', 'id': 'pkg/pkg.Server'}, ('directory', 'pkg')),
                             ({'category': 'test_files', 'id': 'pkg/a_test.go'}, ('file', 'pkg/a_test.go'))]:
            with self.subTest(entry=entry):
                self.assertEqual(history.measured_scope(entry), scope)
        for entry in [{**row(), 'sources': ['pkg/other.go']}, {**row(), 'sources': ['pkg/server.go', 'pkg/b.go']},
                      {**row('pkg/pkg.Server', 'go_receiver_lines'), 'sources': ['other/server.go']},
                      {'category': 'go_struct_fields', 'id': 'pkg.Server'}]:
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                history.measured_scope(entry)

    def changes(self, paths, records=()):
        changes = history.MainChanges.__new__(history.MainChanges)
        changes.main, changes.paths, changes.records = 'e' * 40, frozenset(paths), set(records)
        return changes

    def test_directory_scopes_cover_only_immediate_entries(self):
        from ci_policy.budget_snapshot import key
        directory = {'category': 'source_directories', 'id': 'pkg'}
        nested = {'category': 'go_struct_fields', 'id': 'pkg/nested/nested.Server'}
        paths = self.changes({'pkg/nested/file.go', 'pkg/server.go'})
        self.assertTrue(paths.changed(row()))
        self.assertFalse(paths.changed({'category': 'source_files', 'id': 'pkg/other.go'}))
        self.assertTrue(paths.changed(nested))
        self.assertFalse(paths.changed({'category': 'go_struct_fields', 'id': 'pkg/nested/deeper/deeper.Server'}))
        self.assertTrue(self.changes({'pkg/server.go'}).changed(directory))
        self.assertFalse(self.changes({'pkg/nested/file.go'}).changed(directory))
        self.assertTrue(self.changes({history.POLICY}).changed(directory))
        self.assertTrue(self.changes((), {key('source_directories', 'pkg')}).changed(directory))
        self.assertFalse(self.changes((), {key('source_files', 'pkg')}).changed(directory))

    def test_main_comparison_uses_the_report_source_and_fetched_main(self):
        from ci_policy.budget_snapshot import key
        import subprocess
        import tempfile
        from pathlib import Path
        with tempfile.TemporaryDirectory() as directory:
            def git(*arguments):
                return subprocess.check_output(['git', '-C', directory, *arguments], text=True).strip()
            def commit(path, text, message):
                file = Path(directory) / path
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_text(text)
                git('add', '--', path)
                git('commit', '-qm', message)
                return git('rev-parse', 'HEAD')
            git('init', '-q', '-b', 'main')
            git('config', 'user.name', 'Fixture')
            git('config', 'user.email', 'fixture@example.invalid')
            git('remote', 'add', 'origin', directory)
            source = commit('pkg/server.go', 'one\n', 'Report source')
            git('checkout', '-qb', 'side')
            side = commit('pkg/side.go', 'side\n', 'Unmerged work')
            git('checkout', '-q', 'main')
            commit('other/file.go', 'two\n', 'Advance main')
            review = 'lycaon/test/contract/maintainability-reviews/design.json'
            main = commit(review, json.dumps({'category': 'source_files', 'artifact': 'pkg/server.go'}), 'Review')
            changes = history.MainChanges(directory, source)
            self.assertEqual((changes.main, changes.paths), (main, frozenset({'other/file.go', review})))
            self.assertEqual(changes.records, {key('source_files', 'pkg/server.go')})
            self.assertTrue(changes.changed({'category': 'source_files', 'id': 'pkg/server.go'}))
            self.assertFalse(changes.changed({'category': 'source_files', 'id': 'pkg/other.go'}))
            self.assertFalse(history.MainChanges(directory, main).paths)
            with self.assertRaisesRegex(ValueError, 'not an ancestor'):
                history.MainChanges(directory, side)
