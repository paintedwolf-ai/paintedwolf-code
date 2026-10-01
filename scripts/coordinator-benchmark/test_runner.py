import json
import shutil
import pathlib
import subprocess
import sys
import threading
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace
import yaml
import report
import run
from snapshot import identity, file_hash


class RunnerTests(unittest.TestCase):
    def test_unknown_execution_blocks_resume_before_configuration_or_provider_calls(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'plan.json').write_text('{}')
            attempt = root / 'captures/episode-000/attempt-0000'
            attempt.mkdir(parents=True)
            (attempt / 'exit.json').write_text(json.dumps({'kind': 'execution_unknown', 'returncode': None}))
            args = SimpleNamespace(application_ref=None, application_worktree=False, prepared=None, repair_from=None, harness_ref=None,harness_worktree=False, reason=None, allow_live=True, resume=True, cadence='selected',
                concurrency=None, cloud_concurrency=None, provider_limit=[], out=root,
                models=None, cases=None, repetitions=None, mode=None, tier=None,
                source_config=root / 'unavailable-credentials')
            with patch.object(run, 'model_configurations') as configure, patch.object(run, 'ProviderAdmission') as admit:
                with self.assertRaisesRegex(RuntimeError, 'cleanup is unconfirmed'):
                    run.run(args)
                configure.assert_not_called()
                admit.assert_not_called()
            self.assertFalse((root / 'credentials').exists())

    def test_exec_handoff_releases_the_controller_lock_and_runs_the_frozen_entry(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            entry = root / 'source/scripts/coordinator-benchmark/run.py'
            entry.parent.mkdir(parents=True)
            entry.with_name('python_runtime.py').write_text('import os, sys\nos.execv(sys.executable, [sys.executable, *sys.argv[1:]])\n')
            entry.write_text('''import fcntl, pathlib, sys
root = pathlib.Path(sys.argv[sys.argv.index('--out') + 1])
with (root/'runner.lock').open('a') as lock:
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    (root/'executed-runtime').write_text(__file__)
''')
            script = '''import fcntl, pathlib, run, sys
root = pathlib.Path(sys.argv[1])
lock = (root/'runner.lock').open('a')
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
run.enter_frozen_runtime(root, root/'credentials')
raise RuntimeError('handoff returned to the original runtime')
'''
            subprocess.run([run.sys.executable, '-c', script, str(root)],
                           cwd=pathlib.Path(run.__file__).parent, check=True, timeout=15)
            self.assertEqual((root / 'executed-runtime').read_text(), str(entry))

    def test_controller_handoff_executes_only_the_frozen_runtime_before_spending(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            entry = root / 'source/scripts/coordinator-benchmark/run.py'
            with patch.object(run.os, 'execv') as execute:
                run.enter_frozen_runtime(root, root / 'credentials')
                self.assertEqual(execute.call_args.args[1], [run.sys.executable, str(entry.with_name('python_runtime.py')), str(entry), '--allow-live',
                    '--resume', '--out', str(root), '--source-config', str(root / 'credentials')])
                self.assertEqual(json.loads((root / 'controller.json').read_text())['phase'], 'handoff')
                execute.reset_mock()
                with patch.object(run, '__file__', str(entry)):
                    run.enter_frozen_runtime(root, root / 'credentials')
                execute.assert_not_called()
                self.assertEqual(json.loads((root / 'controller.json').read_text())['phase'], 'running')

    def test_hosted_precision_is_tied_to_exact_endpoint_and_model(self):
        from capture import hosted_runtime
        known=hosted_runtime('https://api.together.xyz/v1','Qwen/Qwen3.5-9B')
        self.assertEqual(known['precision'],'FP8')
        self.assertTrue(known['source'])
        self.assertIsNone(hosted_runtime('https://other.example/v1','Qwen/Qwen3.5-9B')['precision'])
        self.assertIsNone(hosted_runtime('https://api.together.xyz/v1','unknown')['precision'])

    def test_sampling_plan_balances_every_round_and_is_reproducible(self):
        roster = json.loads((report.ROOT / 'lycaon/test/fixtures/eval/coordinator-roster.json').read_text())
        repetitions = report.MANIFEST['release_repetitions']
        self.assertEqual(roster['repetitions'], repetitions)
        self.assertEqual(repetitions, 5)
        models, operations = roster['models'], list(report.CASES)
        episodes = run.episode_plan(models, operations, repetitions, 123)
        self.assertEqual(len(episodes), repetitions * len(operations) * len(models))
        self.assertEqual([item['index'] for item in episodes], list(range(len(episodes))))
        self.assertEqual(episodes, run.episode_plan(models, operations, repetitions, 123))
        self.assertNotEqual(episodes, run.episode_plan(models, operations, repetitions, 124))
        expected = {(m['id'], case) for m in models for case in operations}
        width = len(expected)
        for repeat in range(repetitions):
            block = episodes[repeat*width:(repeat+1)*width]
            self.assertEqual({item['repeat'] for item in block}, {repeat})
            self.assertEqual({(item['model'], item['case']) for item in block}, expected)

    def test_sampling_accepts_full_and_diagnostic_counts_but_rejects_invalid_counts(self):
        for value in [1, 3, 25, 100]:
            run.validate_repetitions(value)
        for value in [0, -1, 25.0, True, None, '25']:
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'positive integer'):
                run.validate_repetitions(value)

    def test_benchmark_uses_application_controls_and_preserves_source_configuration(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, private = pathlib.Path(tmp) / 'source', pathlib.Path(tmp) / 'private'
            source.mkdir(); private.mkdir()
            overrides = {'reasoning_effort': 'high', 'max_tokens': 99, 'temperature': 0.2,
                         'context_length': 4096, 'think_style': 'none', 'thinking_always_on': True}
            original = {'model_thinking': [{'match': ['candidate'], 'style': 'none'}],
                        'providers': [{'id': 'p', 'kind': 'together', 'models': [
                            {'id': name, **overrides} for name in ('candidate', 'worker', 'utility', 'unused')]}]}
            (source / 'providers.local.yaml').write_text(yaml.safe_dump(original))
            (source / 'model-policy.yaml').write_text('coordinator: {}\nlite: {provider_id: p, model: utility}\n')
            roster = {'models': [{'provider': 'p', 'model': 'candidate'}],
                      'worker': {'provider': 'p', 'model': 'worker'}}
            run.configure(source, private, roster)
            self.assertEqual(yaml.safe_load((source / 'providers.local.yaml').read_text()), original)
            resolved = yaml.safe_load((private / 'providers.local.yaml').read_text())
            self.assertNotIn('model_thinking', resolved)
            for model in resolved['providers'][0]['models']:
                if model['id'] == 'unused':
                    self.assertEqual(model, {'id': 'unused', **overrides})
                    continue
                for key in run.MODEL_CONTROLS:
                    self.assertNotIn(key, model)
            self.assertEqual((private / 'providers.local.yaml').stat().st_mode & 0o777, 0o600)

    @patch.object(report, "verify_application")
    def test_unmeasured_execution_is_reported_and_grading_faults_remain_recoverable(self, snapshot):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            implementation = pathlib.Path(report.__file__).parent
            benchmark = {'manifest_sha256': file_hash(implementation / 'benchmark.json'),
                         'implementation_sha256': identity(run.implementation_files(implementation))}
            plan = {'mode':'release', 'benchmark': benchmark, 'run_id': 'fixture', 'started_at': '2026-09-06T00:00:00Z',
                    'comparison_id': identity({'benchmark': benchmark, 'application_configuration': {}}), 'repetitions': 1, 'seed': 1,
                    'models': [{'id': 'candidate', 'label': 'Candidate', 'configuration': {}}],
                    'episodes': [{'index': i, 'case': case, 'model': 'candidate'} for i, case in enumerate(report.CASES)]}
            frozen = root / 'source/scripts/coordinator-benchmark'
            shutil.copytree(implementation, frozen)
            suite = root / 'source' / report.MANIFEST['suite']
            suite.parent.mkdir(parents=True)
            shutil.copy2(report.ROOT / report.MANIFEST['suite'], suite)
            (root / 'plan.json').write_text(json.dumps(plan))
            (root / 'results.json').write_text(json.dumps({str(i): {'error': 'launch_failed'} for i in range(len(report.CASES))}))
            result = report.build(root)
            status = json.loads((root / 'grading-status.json').read_text())
            self.assertEqual(status, {'phase': 'completed_with_unmeasured', 'unmeasured': len(report.CASES)})
            self.assertIsNone(result['models'][0]['score'])
            self.assertTrue(all(case['measured'] == 0 for case in result['models'][0]['cases']))
            self.assertFalse((root / 'public.json').exists())
            with patch.object(report, 'captured_trial', side_effect=subprocess.TimeoutExpired('docker', 180)):
                with self.assertRaisesRegex(RuntimeError, 'grading is incomplete'):
                    report.build(root)
            self.assertEqual(json.loads((root / 'grading-status.json').read_text())['phase'], 'blocked')
            (root / 'results.json').write_text('{}')
            with self.assertRaisesRegex(ValueError, 'execution is incomplete'):
                report.build(root)
            plan['benchmark']['manifest_sha256'] = '0'*64
            (root / 'plan.json').write_text(json.dumps(plan))
            with self.assertRaisesRegex(ValueError, 'frozen report command'):
                report.build(root)

    @patch.object(report, 'verify_application')
    def test_terminal_provider_failure_preserves_other_models_and_withholds_only_incomplete_scores(self, snapshot):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            implementation = pathlib.Path(report.__file__).parent
            benchmark = {'manifest_sha256': file_hash(implementation / 'benchmark.json'),
                         'implementation_sha256': identity(run.implementation_files(implementation))}
            models = [{'id': name, 'label': name, 'configuration': {}} for name in ['interrupted', 'complete']]
            episodes = [{'index': index, 'case': case, 'model': model['id']}
                        for index, (model, case, repeat) in enumerate(
                            (model, case, repeat) for model in models for case in report.SCORED for repeat in range(5))]
            plan = {'mode': 'release', 'benchmark': benchmark, 'run_id': 'fixture', 'started_at': '2026-09-06T00:00:00Z',
                    'comparison_id': identity({'benchmark': benchmark, 'application_configuration': {}}),
                    'repetitions': 5, 'seed': 1, 'models': models, 'episodes': episodes}
            frozen = root / 'source/scripts/coordinator-benchmark'
            shutil.copytree(implementation, frozen)
            suite = root / 'source' / report.MANIFEST['suite']
            suite.parent.mkdir(parents=True)
            shutil.copy2(report.ROOT / report.MANIFEST['suite'], suite)
            (root / 'plan.json').write_text(json.dumps(plan))
            (root / 'results.json').write_text(json.dumps({str(item['index']): {} for item in episodes}))
            failed_case = next(case for case in report.SCORED if report.OPERATIONS[case]['tier'] == 'gate')

            def trial(record, item, *args):
                if item['model'] == 'interrupted' and item['case'] == failed_case:
                    return report.evaluate_case({'id': failed_case, 'session_id': 'root', 'status': 'error',
                        'failure': {'kind': 'provider', 'code': 'provider_request_rejected', 'retryable': False}},
                        {'coordinator': {'provider': 'cloud', 'model': 'candidate'}, 'workers': []},
                        [{'call': 'stream', 'session_id': 'root', 'provider_id': 'cloud', 'model': 'candidate'}],
                        root, {}, root)
                return {'id': item['case'], 'outcome': 'passed'}

            with patch.object(report, 'captured_trial', side_effect=trial):
                result = report.build(root)
            interrupted, complete = result['models']
            self.assertIsNone(interrupted['score'])
            self.assertIsNone(interrupted['tiers']['gate']['score'])
            self.assertEqual(interrupted['tiers']['orchestration']['score'], 100)
            self.assertEqual(complete['score'], 100)
            case = next(case for case in interrupted['cases'] if case['id'] == failed_case)
            self.assertEqual((case['attempts'], case['measured'], case['passed']), (5, 0, 0))
            self.assertTrue(all(trial['outcome'] == 'unmeasured' for trial in case['trials']))
            self.assertEqual(json.loads((root / 'grading-status.json').read_text())['unmeasured'], 5)
            preflight = root / 'provider-preflight/interrupted/result.json'
            preflight.parent.mkdir(parents=True)
            preflight.write_text(json.dumps({'configuration_sha256': identity({}), 'accepted': False}))
            with patch.object(report, 'captured_trial', side_effect=trial):
                with self.assertRaisesRegex(ValueError, 'rejected provider admission'):
                    report.build(root)

            def rejected(record, item, *unused):
                return report.unmeasured(item['case'], 'Provider admission rejected.') if item['model'] == 'interrupted' else {
                    'id': item['case'], 'outcome': 'passed'}

            with patch.object(report, 'captured_trial', side_effect=rejected):
                result = report.build(root)
                self.assertNotIn('provider_observation', result['models'][0])
                self.assertTrue(all(case['measured'] == 0 for case in result['models'][0]['cases']))
                self.assertEqual(result['models'][1]['score'], 100)
                preflight.write_text(json.dumps({'configuration_sha256': 'wrong', 'accepted': False}))
                with self.assertRaisesRegex(ValueError, 'differs from the measured configuration'):
                    report.build(root)

    def test_controller_grades_terminal_blockages_and_removes_private_configuration(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = pathlib.Path(tmp)
            root, config = parent / 'run', parent / 'config'
            config.mkdir()
            (config / 'providers.local.yaml').write_text('private credential configuration')
            implementation = pathlib.Path(report.__file__).parent
            benchmark = {'manifest_sha256': file_hash(implementation / 'benchmark.json'),
                         'implementation_sha256': identity(run.implementation_files(implementation))}
            case_ids = ['current-verification', 'permission-denied']
            items = [{'index': index, 'case': case, 'model': 'candidate', 'repeat': 0}
                     for index, case in enumerate(case_ids)]
            plan = {'mode': 'exploration', 'benchmark': benchmark, 'run_id': 'fixture', 'started_at': '2026-09-06T00:00:00Z',
                    'comparison_id': identity({'benchmark': benchmark, 'application_configuration': {}}),
                    'repetitions': 1, 'seed': 1, 'execution': {'policy': {}},
                    'models': [{'id': 'candidate', 'label': 'Candidate', 'configuration': {}}], 'episodes': items}
            args = SimpleNamespace(application_ref=None, application_worktree=False, prepared=None, repair_from=None, harness_ref=None,harness_worktree=False, reason=None, allow_live=True, resume=False, cadence='selected', concurrency=None,
                                   cloud_concurrency=None, provider_limit=None, out=root, source_config=config, mode='exploration')

            def prepare(*unused):
                frozen = root / 'source/scripts/coordinator-benchmark'
                shutil.copytree(implementation, frozen)
                suite = root / 'source' / report.MANIFEST['suite']
                suite.parent.mkdir(parents=True)
                shutil.copy2(report.ROOT / report.MANIFEST['suite'], suite)
                run.save(root / 'plan.json', plan)
                return plan

            def dispatch(pending, policy, execute, cancelled, started, finished, update):
                for item in pending:
                    started(item)
                    finished(item, {'blocked': 'execution_unresolved'} if item['index'] == 0 else {'report': 'captured'})
                return True

            def captured(record, item, *unused):
                return report.unmeasured(item['case'], 'No execution report.') if record is None else {
                    'id': item['case'], 'outcome': 'passed'}

            def grade(command, destination, progress, cancelled):
                self.assertEqual(pathlib.Path(command[1]).name, 'report.py')
                report.write_report(root)
                return True

            with patch.object(run, 'select', return_value={'version':'1.0.0'}), patch.object(run, 'prepare_target'), \
                 patch.object(run, 'inputs', return_value={'mode':'exploration','tier':'all','cases':None}), \
                 patch.object(run, 'prepare', side_effect=prepare), patch.object(run, 'enter_frozen_runtime'), \
                 patch.object(run, 'ProviderAdmission'), patch.object(run, 'assert_execution_quiescence'), \
                 patch.object(run, 'dispatch', side_effect=dispatch), patch.object(run.subprocess, 'run'), \
                 patch.object(run, 'grading', side_effect=grade), patch.object(report, 'verify_application'), \
                 patch.object(report, 'captured_trial', side_effect=captured):
                run.run(args)
            result = json.loads((root / 'preview.json').read_text())
            self.assertEqual([case['trials'][0]['outcome'] for case in result['models'][0]['cases']], ['unmeasured', 'passed'])
            self.assertIsNone(result['models'][0]['score'])
            self.assertFalse((root / 'credentials').exists())
            self.assertTrue((config / 'providers.local.yaml').exists())
            self.assertEqual(json.loads((root / 'status.json').read_text())['phase'], 'completed_with_unmeasured')
            self.assertEqual(json.loads((root / 'completion.json').read_text())['outcomes'], {'unmeasured': 1, 'passed': 1})


class RetainedReportTests(unittest.TestCase):
    def prepare(self, root):
        implementation = pathlib.Path(report.__file__).parent
        benchmark = {'manifest_sha256': file_hash(implementation / 'benchmark.json'),
                     'implementation_sha256': identity(run.implementation_files(implementation))}
        items = [{'index': index, 'case': case, 'model': 'candidate', 'repeat': 0}
                 for index, case in enumerate(['current-verification', 'permission-denied'])]
        plan = {'mode': 'exploration', 'benchmark': benchmark, 'run_id': 'fixture',
                'started_at': '2026-09-06T00:00:00Z',
                'comparison_id': identity({'benchmark': benchmark, 'application_configuration': {}}),
                'repetitions': 1, 'seed': 1, 'execution': {'policy': {'max_active': 1, 'resources': [], 'models': {}}},
                'models': [{'id': 'candidate', 'label': 'Candidate', 'configuration': {}}], 'episodes': items}
        shutil.copytree(implementation, root / 'source/scripts/coordinator-benchmark')
        suite = root / 'source' / report.MANIFEST['suite']
        suite.parent.mkdir(parents=True)
        shutil.copy2(report.ROOT / report.MANIFEST['suite'], suite)
        run.save(root / 'plan.json', plan)
        run.save(root / 'source/application.json', {})
        progress = run.Progress(root, plan)
        for item in items:
            progress.started(item)
            progress.finished(item, {'report': 'retained'})
        return plan

    def test_regrade_library_call_preserves_original_report(self):
        for explicit in (False, True):
            with self.subTest(explicit=explicit), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                run.save(root / 'plan.json', {'mode': 'exploration'})
                original = root / 'preview.json'
                original.write_text('retained original')
                with self.assertRaisesRegex(ValueError, 'preserve the original'):
                    report.write_report(root, regrade=True, destination=original if explicit else None)
                self.assertEqual(original.read_text(), 'retained original')
                status = json.loads((root / 'grading-status.json').read_text())
                self.assertEqual(status['phase'], 'blocked')
                self.assertFalse(status['retryable'])

    def test_report_cli_records_malformed_plan_as_terminal_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'plan.json').write_text('{')
            status = root / 'private-status.json'
            result = subprocess.run([sys.executable, '-B', report.__file__, '--run', str(root),
                                     '--status-out', str(status)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            receipt = json.loads(status.read_text())
            self.assertEqual(receipt['phase'], 'blocked')
            self.assertEqual(receipt['failure']['type'], 'JSONDecodeError')
            self.assertFalse(receipt['retryable'])
            self.assertFalse((root / 'grading-status.json').exists())

    def test_permanent_grader_defect_retains_other_measurements_and_error_identity(self):
        import completion
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.prepare(root)
            def captured(record, item, *args):
                if item['index'] == 0:
                    raise report.CheckIdentityError(['worker'])
                return {'id': item['case'], 'outcome': 'passed'}
            with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=captured):
                with self.assertRaises(report.GradingIncomplete):
                    report.write_report(root)
            status = json.loads((root/'grading-status.json').read_text())
            self.assertFalse(status['retryable'])
            self.assertEqual(status['errors'][0]['index'], 0)
            self.assertEqual(status['errors'][0]['duplicate_checks'], ['worker'])
            self.assertIn('case', status['errors'][0])
            self.assertIn('model', status['errors'][0])
            self.assertIn('worker', status['errors'][0]['detail'])
            self.assertEqual(completion.result(root)['outcomes'], {'unmeasured': 1, 'passed': 1})
            self.assertIn('report_sha256', status)

    def test_unexpected_trial_exception_does_not_erase_other_grades(self):
        import completion
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.prepare(root)
            def captured(record, item, *args):
                if item['index'] == 0:
                    raise TypeError('invalid grader value')
                return {'id': item['case'], 'outcome': 'passed'}
            with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=captured):
                with self.assertRaises(report.GradingIncomplete):
                    report.write_report(root)
            status = json.loads((root/'grading-status.json').read_text())
            self.assertEqual(status['errors'][0]['type'], 'TypeError')
            self.assertFalse(status['retryable'])
            self.assertEqual(completion.result(root)['outcomes'], {'unmeasured': 1, 'passed': 1})

    def test_early_grading_failure_has_terminal_status_without_old_report_authority(self):
        for private in (False, True):
            with self.subTest(private=private), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                self.prepare(root)
                previous = {'phase': 'completed', 'report_id': 'old', 'report_sha256': 'old'}
                run.save(root / 'grading-status.json', previous)
                status_path = root / 'private-status.json' if private else root / 'grading-status.json'
                with patch.object(report, 'verify_application', side_effect=ValueError('seal changed')):
                    with self.assertRaisesRegex(ValueError, 'seal changed'):
                        report.write_report(root, status_path=status_path if private else None)
                status = json.loads(status_path.read_text())
                self.assertEqual(status['phase'], 'blocked')
                self.assertFalse(status['retryable'])
                self.assertEqual(status['failure']['type'], 'ValueError')
                self.assertNotIn('report_sha256', status)
                if private:
                    self.assertEqual(json.loads((root / 'grading-status.json').read_text()), previous)

    def test_report_persistence_failure_removes_completed_status_authority(self):
        for boundary in ('report', 'receipt'):
            with self.subTest(boundary=boundary), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                self.prepare(root)
                destination = root / 'preview.json'
                original_save = report.save
                def failing_save(path, value):
                    if (boundary == 'report' and path == destination) or (boundary == 'receipt' and 'report_sha256' in value):
                        raise OSError('publication unavailable')
                    original_save(path, value)
                with patch.object(report, 'verify_application'), \
                     patch.object(report, 'captured_trial', side_effect=lambda record, item, *args: {'id': item['case'], 'outcome': 'passed'}), \
                     patch.object(report, 'save', side_effect=failing_save):
                    with self.assertRaisesRegex(OSError, 'publication unavailable'):
                        report.write_report(root)
                status = json.loads((root / 'grading-status.json').read_text())
                self.assertEqual(status['phase'], 'blocked')
                self.assertFalse(status['retryable'])
                self.assertNotIn('report_sha256', status)
                import completion
                self.assertEqual(completion.result(root)['outcomes'], {'retained': 2})

    def test_partial_grading_retains_valid_measurements_and_excludes_corrupt_evidence(self):
        from measurement_cache import measure
        import completion
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.prepare(root)
            capture = root / 'capture'
            capture.mkdir()
            evidence = capture / 'evidence'
            evidence.write_text('closed evidence')
            evaluations = []
            def captured(record, item, *unused):
                if item['index'] == 0:
                    raise report.GradingUnavailable('oracle unavailable')
                def evaluate():
                    evaluations.append(item['index'])
                    return {'id': item['case'], 'outcome': 'passed'}
                return measure(capture, report.GRADING_SHA, [evidence], evaluate)
            with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=captured):
                for attempt in range(3):
                    with self.assertRaises(report.GradingIncomplete):
                        report.write_report(root)
                    data = completion.write(root, run.save)
                    self.assertEqual(data['outcomes'], {'unmeasured': 1, 'passed': 1})
                    self.assertEqual(data['phase'], 'grading_blocked')
                    published = json.loads((root / 'preview.json').read_text())
                    self.assertIsNone(published['models'][0]['tiers']['gate']['score'])
                    self.assertTrue(json.loads((root / 'grading-status.json').read_text())['retryable'])
            self.assertEqual(evaluations, [1])
            run.save(root / 'analysis.json', {'execution_id': 'fixture', 'trials': [
                {'index': 0, 'measurement': {'outcome': 'passed'}}]})
            with patch.object(report, 'verify_application'), \
                 patch.object(report, 'captured_trial', side_effect=ValueError('corrupt evidence')):
                with self.assertRaises(report.GradingIncomplete):
                    report.write_report(root)
            self.assertTrue((root / 'preview.json').exists())
            self.assertFalse(json.loads((root / 'grading-status.json').read_text())['retryable'])
            data = completion.write(root, run.save)
            self.assertEqual(data['outcomes'], {'unmeasured': 2})
            self.assertEqual(data['phase'], 'grading_blocked')

    def test_private_grading_receipt_does_not_replace_selected_standalone_result(self):
        import completion
        from grading_state import report_lease, select_report
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.prepare(root)
            selected = root / 'selected.json'
            def passed(record, item, *unused):
                return {'id': item['case'], 'outcome': 'passed'}
            with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=passed):
                report.write_report(root, regrade=True, destination=selected)
            selected_status = (root / 'grading-status.json').read_bytes()
            private = root / 'attempt-status.json'
            with patch.object(report, 'verify_application'), \
                 patch.object(report, 'captured_trial', side_effect=report.GradingUnavailable('unavailable')):
                with self.assertRaises(report.GradingIncomplete):
                    report.write_report(root, status_path=private)
            self.assertEqual((root / 'grading-status.json').read_bytes(), selected_status)
            self.assertTrue(json.loads(private.read_text())['retryable'])
            self.assertEqual(completion.result(root)['outcomes'], {'passed': 2})
            self.assertFalse((root / 'preview.json').exists())
            immutable = (root / 'report.json').read_bytes()
            for _ in range(2):
                with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=passed):
                    report.write_report(root)
                with report_lease(root):
                    select_report(root, json.loads(private.read_text()), publish=True)
                self.assertEqual((root / 'report.json').read_bytes(), immutable)
                self.assertEqual((root / 'preview.json').read_bytes(), immutable)
                self.assertEqual(completion.result(root)['outcomes'], {'unmeasured': 2})
            with report_lease(root):
                select_report(root, {'phase': 'completed_with_unmeasured', 'retryable': False}, publish=True)
            self.assertFalse((root / 'preview.json').exists())
            self.assertEqual(completion.result(root)['outcomes'], {'retained': 2})
            self.assertTrue(selected.exists())

    def test_alternate_regrade_report_is_selected_without_replacing_original(self):
        import completion
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.prepare(root)
            def passed(record, item, *unused):
                return {'id': item['case'], 'outcome': 'passed'}
            with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=passed):
                report.write_report(root)
            original = (root / 'preview.json').read_bytes()
            def unavailable(record, item, *unused):
                raise report.GradingUnavailable('oracle unavailable')
            alternate = root / 'regraded.json'
            with patch.object(report, 'verify_application'), patch.object(report, 'captured_trial', side_effect=unavailable):
                with self.assertRaises(report.GradingIncomplete):
                    report.write_report(root, regrade=True, destination=alternate)
            self.assertEqual((root / 'preview.json').read_bytes(), original)
            data = completion.write(root, run.save)
            self.assertEqual(data['outcomes'], {'unmeasured': 2})
            self.assertEqual(data['grading']['report_path'], str(alternate.resolve()))

    def test_controller_finishes_exhausted_grading_without_candidate_or_provider_execution(self):
        import recovery
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            self.prepare(root)
            args = SimpleNamespace(application_ref=None, application_worktree=False, prepared=None, repair_from=None, harness_ref=None,harness_worktree=False, reason=None, allow_live=True, resume=True, cadence='selected', concurrency=None,
                cloud_concurrency=None, provider_limit=[], out=root, source_config=root / 'absent-config',
                models=None, cases=None, repetitions=None, mode=None, tier=None)
            calls = []
            def captured(record, item, *unused):
                if item['index'] == 0:
                    raise report.GradingUnavailable('oracle unavailable')
                return {'id': item['case'], 'outcome': 'passed'}
            def grading(command, directory, progress, cancelled):
                status_path = root / 'grading-status.json'
                if status_path.exists():
                    previous = json.loads(status_path.read_text())
                    if (previous.get('recovery') or {}).get('exhausted'):
                        raise recovery.GradingExhausted(previous['recovery'])
                for attempt in range(recovery.MAX_INFRASTRUCTURE_ATTEMPTS):
                    calls.append(attempt)
                    with self.assertRaises(report.GradingIncomplete):
                        report.write_report(root)
                state = json.loads(status_path.read_text())
                exhausted = {'exhausted': True, 'attempt_limit': len(calls), 'attempts': calls.copy()}
                run.save(status_path, {**state, 'phase': 'completed_with_unmeasured',
                                       'retryable': False, 'recovery': exhausted})
                raise recovery.GradingExhausted(exhausted)
            with patch.object(run, 'verify_execution'), patch.object(run, 'enter_frozen_runtime'), \
                 patch.object(run, 'assert_execution_quiescence'), patch.object(run, 'ProviderAdmission') as admit, \
                 patch.object(run, 'execute_attempt') as candidate, patch.object(run.subprocess, 'run') as process, \
                 patch.object(run, 'grading', side_effect=grading), patch.object(report, 'verify_application'), \
                 patch.object(report, 'captured_trial', side_effect=captured):
                run.run(args)
                run.run(args)
                admit.assert_not_called()
                candidate.assert_not_called()
                process.assert_not_called()
            self.assertEqual(len(calls), recovery.MAX_INFRASTRUCTURE_ATTEMPTS)
            self.assertFalse((root / 'credentials').exists())
            self.assertEqual(json.loads((root / 'status.json').read_text())['phase'], 'completed_with_unmeasured')
            result = json.loads((root / 'completion.json').read_text())
            self.assertEqual(result['outcomes'], {'unmeasured': 1, 'passed': 1})
            self.assertTrue(result['grading']['recovery']['exhausted'])


if __name__ == '__main__':
    unittest.main()


class TierSelectionTests(unittest.TestCase):
    def test_tier_selection_filters_scored_operations_and_release_runs_every_tier(self):
        operations = report.MANIFEST['operations']
        from operation_selection import selected_operations
        all_scored = set()
        for tier in report.TIERS:
            selected = selected_operations(report.MANIFEST, tier=tier)
            self.assertTrue(selected)
            self.assertFalse(all_scored & set(selected))
            all_scored.update(selected)
            families = {o['family'] for o in operations if o['id'] in selected}
            self.assertEqual(len(families), report.TIERS[tier]['families'])
        self.assertEqual(set(report.SCORED), all_scored)
        self.assertEqual(set(selected_operations(report.MANIFEST)), all_scored)
