import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import pipeline


def completed_run(run, name='public.json', revision='original'):
    run.mkdir(exist_ok=True)
    plan = {'run_id': 'execution', 'mode': 'release', 'models': [{'id': 'candidate', 'configuration': {}}],
            'episodes': [{'index': 0, 'model': 'candidate', 'case': 'fixture'}]}
    report = {'run_id': revision, 'execution_id': 'execution', 'mode': 'release',
              'models': [{'configuration': {}, 'cases': [{'id': 'fixture', 'trials': [{'outcome': 'passed'}]}]}]}
    pipeline.save(run / 'plan.json', plan)
    pipeline.save(run / name, report)
    pipeline.save(run / 'grading-status.json', {'phase': 'completed', 'report_id': revision,
                  'report_path': str((run / name).resolve()), 'report_sha256': pipeline.file_hash(run / name)})



class PipelineTests(unittest.TestCase):
    def test_interrupted_build_resumes_the_frozen_source_runner(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            run = root / 'run'
            source = run / 'source'
            source.mkdir(parents=True)
            (source / 'source.json').write_text('{}')
            (run / 'selection.json').write_text('{}')
            with patch.object(pipeline.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1)) as invoke:
                self.assertFalse(pipeline.pipeline(run, root / 'private'))
            command = invoke.call_args.args[0]
            self.assertEqual(command[1], str(source / 'scripts/coordinator-benchmark/run.py'))
            self.assertIn('--resume', command)

    def test_explicit_target_is_forwarded_once_and_resume_keeps_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=pathlib.Path(temporary); run=root/'run'; calls=[]
            def invoke(command, **kwargs):
                calls.append(command)
                completed_run(run)
                return subprocess.CompletedProcess(command, 0)
            with patch.object(pipeline.subprocess,'run',side_effect=invoke):
                self.assertTrue(pipeline.pipeline(run,root/'private',target_args=['--application-ref','v0.9.0']))
                self.assertTrue(pipeline.pipeline(run,root/'private',target_args=['--application-ref','v0.10.0']))
            self.assertEqual(len(calls),1)
            self.assertIn('v0.9.0',calls[0])
            self.assertNotIn('v0.10.0',calls[0])

    def test_resume_repairs_missing_website_data_without_repeating_evaluation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            run, website = root / 'run', root / 'website'
            website.mkdir()
            destination = website / 'results.json'
            calls = []
            def invoke(command, **kwargs):
                calls.append(command)
                if '--allow-live' in command:
                    completed_run(run)
                elif 'benchmark:import' in command:
                    destination.write_bytes(pathlib.Path(command[-1]).read_bytes())
                else:
                    self.assertEqual(destination.read_bytes(), (run / 'public.json').read_bytes())
                return subprocess.CompletedProcess(command, 0)
            with patch.object(pipeline.subprocess, 'run', side_effect=invoke):
                self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
                destination.unlink()
                self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
            self.assertEqual(sum('--allow-live' in command for command in calls), 1)
            self.assertEqual(sum('benchmark:import' in command for command in calls), 2)
            self.assertEqual(sum('check' in command for command in calls), 2)
            state = json.loads((root / 'run-pipeline/status.json').read_text())
            self.assertEqual(state['completed'], ['evaluation', 'website-import', 'website-check'])

    def test_failed_revalidation_does_not_keep_completed_import_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            run, website = root / 'run', root / 'website'
            imports = 0
            def invoke(command, **kwargs):
                nonlocal imports
                if '--allow-live' in command:
                    completed_run(run)
                if 'benchmark:import' in command:
                    imports += 1
                    if imports == 2:
                        return subprocess.CompletedProcess(command, 1)
                return subprocess.CompletedProcess(command, 0)
            with patch.object(pipeline.subprocess, 'run', side_effect=invoke):
                self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
                self.assertFalse(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
            state = json.loads((root / 'run-pipeline/status.json').read_text())
            self.assertEqual(state['phase'], 'paused')
            self.assertEqual(state['failed_stage'], 'website-import')
            self.assertNotIn('website-import', state['completed'])

    def test_website_failure_does_not_repeat_paid_execution(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            run, website = root / 'run', root / 'website'
            calls = []
            def invoke(command, **kwargs):
                calls.append(command)
                if '--allow-live' in command:
                    completed_run(run)
                    return subprocess.CompletedProcess(command, 0)
                return subprocess.CompletedProcess(command, 1 if len(calls) == 2 else 0)
            with patch.object(pipeline.subprocess, 'run', side_effect=invoke):
                self.assertFalse(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
                self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
            self.assertEqual(sum('--allow-live' in command for command in calls), 1)
            self.assertEqual(sum('benchmark:import' in command for command in calls), 2)
            self.assertEqual(json.loads((root / 'run-pipeline/status.json').read_text())['phase'], 'completed')

    def test_selected_alternate_is_snapshotted_before_website_import(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            run, website = root / 'run', root / 'website'
            calls = []
            def invoke(command, **kwargs):
                calls.append(command)
                if '--allow-live' in command:
                    completed_run(run)
                    completed_run(run, 'corrected.json', 'corrected')
                elif 'benchmark:import' in command:
                    handed_off = pathlib.Path(command[-1])
                    completed_run(run, 'corrected.json', 'later')
                    self.assertEqual(json.loads(handed_off.read_text())['run_id'], 'corrected')
                    self.assertEqual(json.loads((run / 'public.json').read_text())['run_id'], 'original')
                return subprocess.CompletedProcess(command, 0)
            with patch.object(pipeline.subprocess, 'run', side_effect=invoke):
                self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
            self.assertEqual(sum('--allow-live' in command for command in calls), 1)

    def test_invalid_selection_blocks_stale_publication_and_preserves_paid_checkpoint(self):
        for status in ({'phase': 'blocked', 'retryable': False}, {'phase': 'running'}, None):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                run, website = root / 'run', root / 'website'
                calls = []
                def invoke(command, **kwargs):
                    calls.append(command)
                    if '--allow-live' in command:
                        completed_run(run)
                        if status is None:
                            (run / 'grading-status.json').unlink()
                        else:
                            pipeline.save(run / 'grading-status.json', status)
                    return subprocess.CompletedProcess(command, 0)
                with patch.object(pipeline.subprocess, 'run', side_effect=invoke):
                    self.assertFalse(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
                    state = json.loads((root / 'run-pipeline/status.json').read_text())
                    self.assertEqual(state['phase'], 'paused')
                    self.assertEqual(state['failed_stage'], 'publication')
                    self.assertEqual(state['completed'], ['evaluation'])
                    completed_run(run, 'repaired.json', 'repaired')
                    self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
                self.assertEqual(sum('--allow-live' in command for command in calls), 1)
                self.assertEqual(sum('benchmark:import' in command for command in calls), 1)

    def test_import_launch_failure_is_terminal_and_can_resume(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            run, website = root / 'run', root / 'website'
            calls, broken = [], True
            def invoke(command, **kwargs):
                calls.append(command)
                if '--allow-live' in command:
                    completed_run(run)
                elif 'benchmark:import' in command and broken:
                    raise FileNotFoundError('website task unavailable')
                return subprocess.CompletedProcess(command, 0)
            with patch.object(pipeline.subprocess, 'run', side_effect=invoke):
                self.assertFalse(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
                state = json.loads((root / 'run-pipeline/status.json').read_text())
                self.assertEqual(state['phase'], 'paused')
                self.assertEqual(state['failed_stage'], 'website-import')
                self.assertEqual(state['failure']['type'], 'FileNotFoundError')
                self.assertEqual(state['completed'], ['evaluation'])
                broken = False
                self.assertTrue(pipeline.pipeline(run, root / 'private', website, ['--application-ref','v0.9.0']))
            self.assertEqual(sum('--allow-live' in command for command in calls), 1)
            self.assertNotIn('failure', json.loads((root / 'run-pipeline/status.json').read_text()))


if __name__ == '__main__':
    unittest.main()
