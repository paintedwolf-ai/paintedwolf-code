import pathlib
import tempfile
import unittest
import completion
from progress import save


class CompletionTests(unittest.TestCase):
    def test_report_accounts_for_every_slot_without_turning_blockage_into_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            items = [{'index': i, 'case': 'repair', 'repeat': i, 'model': 'cloud'} for i in range(3)]
            save(root / 'plan.json', {'run_id': 'run', 'episodes': items, 'models': [{'id': 'cloud', 'label': '<model>'}]})
            (root / 'attempts').mkdir()
            save(root / 'attempts/0.json', {'item': items[0], 'state': 'finished', 'result': {'report': 'retained'}})
            save(root / 'attempts/1.json', {'item': items[1], 'state': 'blocked', 'result': {'blocked': 'execution_unresolved'}})
            data = completion.write(root, save)
            self.assertEqual(data['outcomes'], {'retained': 1, 'unmeasured': 1, 'pending': 1})
            self.assertEqual(data['phase'], 'incomplete')
            self.assertNotIn('<model>', (root / 'completion.html').read_text())
            save(root / 'attempts/2.json', {'item': items[2], 'state': 'finished', 'result': {'report': 'retained'}})
            save(root / 'analysis.json', {'execution_id': 'run', 'trials': [
                {'index': 0, 'measurement': {'outcome': 'passed'}},
                {'index': 2, 'measurement': {'outcome': 'failed'}}]})
            data = completion.write(root, save)
            self.assertEqual(data['phase'], 'completed_with_unmeasured')
            self.assertEqual(data['outcomes'], {'passed': 1, 'unmeasured': 1, 'failed': 1})
            self.assertFalse((root / 'public.json').exists())

    def test_grading_receipt_excludes_previous_revision_results_and_detects_changed_report(self):
        from snapshot import file_hash
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            item = {'index': 0, 'case': 'repair', 'repeat': 0, 'model': 'cloud'}
            save(root / 'plan.json', {'run_id': 'run', 'episodes': [item],
                                    'models': [{'id': 'cloud', 'configuration': {}}]})
            (root / 'attempts').mkdir()
            save(root / 'attempts/0.json', {'item': item, 'state': 'finished', 'result': {'report': 'retained'}})
            save(root / 'analysis.json', {'execution_id': 'run', 'trials': [
                {'index': 0, 'measurement': {'outcome': 'passed'}}]})
            previous = {'execution_id': 'run', 'run_id': 'previous-grader', 'models': [
                {'configuration': {}, 'cases': [{'id': 'repair', 'trials': [{'outcome': 'passed'}]}]}]}
            save(root / 'preview.json', previous)
            save(root / 'grading-status.json', {'phase': 'running', 'retryable': False})
            data = completion.result(root)
            self.assertEqual(data['outcomes'], {'retained': 1})
            self.assertEqual(data['phase'], 'grading')
            save(root / 'grading-status.json', {'phase': 'completed', 'report_id': 'previous-grader',
                                               'report_sha256': file_hash(root / 'preview.json'),
                                               'report_path': str(root / 'preview.json')})
            self.assertEqual(completion.result(root)['outcomes'], {'passed': 1})
            previous['models'][0]['cases'][0]['trials'][0]['outcome'] = 'failed'
            save(root / 'preview.json', previous)
            with self.assertRaisesRegex(ValueError, 'selected grading receipt'):
                completion.result(root)

    def test_grading_defects_have_an_explicit_phase_and_visible_diagnostics(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            item = {'index': 0, 'case': 'merge', 'repeat': 0, 'model': 'cloud'}
            save(root/'plan.json', {'run_id': 'run', 'episodes': [item], 'models': [{'id': 'cloud'}]})
            (root/'attempts').mkdir()
            save(root/'attempts/0.json', {'item': item, 'state': 'finished', 'result': {}})
            save(root/'grading-status.json', {'phase': 'blocked', 'retryable': False,
                'errors': [{'index': 0, 'model': 'cloud', 'case': 'merge', 'detail': 'duplicate <worker>'}]})
            data = completion.write(root, save)
            self.assertEqual(data['phase'], 'grading_blocked')
            page = (root/'completion.html').read_text()
            self.assertIn('duplicate &lt;worker&gt;', page)
            self.assertNotIn('duplicate <worker>', page)


if __name__ == '__main__':
    unittest.main()
