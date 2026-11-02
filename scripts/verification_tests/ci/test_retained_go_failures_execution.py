"""Compound task summaries retain earlier compiler and runtime failures."""
import json
from pathlib import Path
import tempfile
import unittest

from ci_policy import verification_report


class RetainedGoFailuresTests(unittest.TestCase):
    def fixture(self, root, name, events, source='exact-source', created='1970-01-01T00:02:00+00:00'):
        directory = root / 'last-run' / 'failures' / name
        directory.mkdir(parents=True)
        (directory / 'manifest.json').write_text(json.dumps({
            'version': 1, 'name': 'test:confine:linux-' + name,
            'source_commit': source, 'created_at': created}))
        (directory / 'go-test.json').write_text('\n'.join(json.dumps(event) for event in events))

    def receipt(self, root):
        directory = root / 'verification' / 'batch'
        directory.mkdir(parents=True)
        (directory / '1-request.json').write_text(json.dumps({
            'status': 'failed', 'source_commit': 'exact-source', 'started_at': 100, 'finished_at': 200,
            'evidence': [{'stage': 'test:confine:linux', 'results': {
                'task': {'status': 'failed', 'exit_code': 201}}}]}))

    def test_compile_and_runtime_failures_survive_later_passing_legs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.receipt(root)
            self.fixture(root, 'supervisor', [
                {'Action': 'build-output', 'ImportPath': 'module/exec [module/exec.test]',
                 'Output': 'undefined: missingHelper\n'},
                {'Action': 'build-fail', 'ImportPath': 'module/exec [module/exec.test]'}])
            self.fixture(root, 'launch', [
                {'Action': 'output', 'Package': 'module/launch', 'Test': 'TestOther', 'Output': 'unrelated\n'},
                {'Action': 'output', 'Package': 'module/launch', 'Test': 'TestFuse', 'Output': 'reply failed\n'},
                {'Action': 'fail', 'Package': 'module/launch', 'Test': 'TestFuse'},
                {'Action': 'fail', 'Package': 'module/launch'}])
            (root / 'last-run' / 'go-test.json').write_text(json.dumps({'Action': 'pass', 'Package': 'module/helpers'}))
            found = verification_report.failures(root)
            self.assertEqual([(item['stage'], item['subject']) for item in found], [
                ('test:confine:linux', 'task'), ('test:confine:linux-launch', 'module/launch'),
                ('test:confine:linux-supervisor', 'module/exec')])
            self.assertEqual(found[1]['tests'], ['TestFuse'])
            self.assertEqual(Path(found[1]['output']).read_text(), 'reply failed\n')
            self.assertEqual(Path(found[2]['output']).read_text(), 'undefined: missingHelper\n')
            self.assertEqual(len(verification_report.failures(root)), 3)

    def test_foreign_old_malformed_and_passing_evidence_add_no_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.receipt(root)
            event = {'Action': 'fail', 'Package': 'module/exec'}
            self.fixture(root, 'foreign', [event], source='other-source')
            self.fixture(root, 'old', [event], created='1970-01-01T00:00:01+00:00')
            self.fixture(root, 'passed', [{'Action': 'pass', 'Package': 'module/exec'}])
            self.fixture(root, 'malformed', [])
            (root / 'last-run/failures/malformed/manifest.json').write_text('[]')
            self.assertEqual(len(verification_report.failures(root)), 1)
