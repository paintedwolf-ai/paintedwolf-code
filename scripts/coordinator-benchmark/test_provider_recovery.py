import pathlib
import json
import threading
import tempfile
import unittest
from unittest.mock import Mock, patch
import episode
from progress import save
from recovery import infrastructure_recovery
from episode_runtime import RetainedProcessingExhausted
from progress import Progress
from scheduler import dispatch, execution_policy


class ProviderRecoveryTests(unittest.TestCase):
    def test_no_report_cleanup_requires_confirmed_shutdown(self):
        for state in ['exited', 'interrupted', 'worker_failed', 'execution_unknown']:
            with self.subTest(state=state), tempfile.TemporaryDirectory() as temporary:
                run = pathlib.Path(temporary)
                item = {'index': 0, 'model': 'candidate', 'case': 'operation'}
                plan = {'models': [{'id': 'candidate', 'configuration': {}}]}
                config = run / 'captures/episode-000/attempt-0000/capture/config'

                def execute(attempt, *unused):
                    config.mkdir(parents=True)
                    (config / 'credential-vault.age').write_text('copied vault')
                    (config / 'retained-settings.json').write_text('evidence')
                    return {'kind': state, 'returncode': -9}

                with patch.object(episode, 'execute', side_effect=execute), \
                     patch.object(episode, 'command_for', return_value=['fixture']), \
                     patch.object(episode, 'verify_runtime'), patch.object(episode, 'finalize') as finalize:
                    result = episode.episode(run, plan, run, item, Mock(is_set=Mock(return_value=False)))
                self.assertIn('blocked', result)
                finalize.assert_not_called()
                self.assertEqual((config / 'credential-vault.age').exists(), state == 'execution_unknown')
                self.assertEqual((config / 'retained-settings.json').read_text(), 'evidence')

    def test_finalization_exhaustion_blocks_one_slot_without_replaying_candidates(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            reference = {'provider':'cloud', 'model':'candidate'}
            model = {'id':'candidate', 'configuration':{'coordinator':reference, 'lite':reference, 'workers':[reference]}}
            items = [{'index':index, 'model':'candidate', 'case':'operation', 'repeat':index} for index in range(2)]
            policy = execution_policy([model], [{'id':'cloud','kind':'fixture','base_url':'https://example.com'}], max_active=1)
            plan = {'models':[model], 'episodes':items, 'execution':{'policy':policy}}
            progress = Progress(root, plan)
            recovery = {'attempt_limit':8, 'attempts':[{'attempt':'attempt-0007', 'failure':{
                'kind':'harness','code':'retained_processing_failed','retryable':True}}], 'exhausted':True}
            def execute(attempt, *unused):
                capture = attempt / 'capture'
                capture.mkdir(parents=True)
                save(capture / 'report.json', {'cases':[{'status':'review_required'}]})
                save(attempt / 'exit.json', {'kind':'exited','returncode':0})
                return {'kind':'exited','returncode':0}
            with patch.object(episode, 'execute', side_effect=execute) as candidates, \
                 patch.object(episode, 'command_for', return_value=['candidate-command']), \
                 patch.object(episode, 'verify_runtime'), \
                 patch.object(episode, 'finalize', side_effect=[RetainedProcessingExhausted(recovery), None]):
                completed = dispatch(items, policy,
                    lambda item: episode.episode(root, plan, root, item, threading.Event()), threading.Event(),
                    progress.started, progress.finished)
            self.assertTrue(completed)
            self.assertEqual(candidates.call_count, 2)
            self.assertEqual(progress.blocked['0']['blocked'], 'finalization_recovery_exhausted')
            self.assertEqual(progress.blocked['0']['finalization_recovery'], recovery)
            self.assertEqual(set(progress.records), {'1'})
            resumed = Progress(root, plan)
            self.assertEqual(resumed.pending(), [])
            self.assertEqual(json.loads((root / 'captures/episode-000/attempt-0000/exit.json').read_text()),
                             {'kind':'exited','returncode':0})

    def test_provider_outage_waits_beyond_attempt_limit_and_resumes_without_early_spending(self):
        with tempfile.TemporaryDirectory() as temporary:
            run = pathlib.Path(temporary)
            item = {'index': 0, 'model': 'candidate', 'case': 'operation'}
            plan = {'models': [{'id': 'candidate', 'configuration': {}}]}
            fault = {'kind': 'provider', 'code': 'provider_rate_limited', 'retryable': True}
            def execute(attempt, *args):
                (attempt / 'capture').mkdir(parents=True, exist_ok=True)
                save(attempt / 'capture/report.json', {'cases': [{'status': 'error', 'failure': fault, 'task_allowance': {'completed': 0}}]})
                return {'kind': 'exited', 'returncode': 1}
            with patch('recovery.MAX_INFRASTRUCTURE_ATTEMPTS', 3), \
                    patch.object(episode, 'execute', side_effect=execute) as launch, \
                    patch.object(episode, 'command_for', return_value=['fixture']), \
                    patch.object(episode, 'verify_runtime'), patch.object(episode, 'finalize'), \
                    patch.object(episode.time, 'time', return_value=1000) as clock:
                for index in range(12):
                    result = episode.episode(run, plan, run, item, threading.Event())
                    self.assertIn('retry_at', result)
                    self.assertEqual(episode.episode(run, plan, run, item, threading.Event()), result)
                    self.assertEqual(launch.call_count, index + 1)
                    clock.return_value = result['retry_at']
                self.assertFalse(infrastructure_recovery(run/'captures/episode-000')['exhausted'])

    def test_only_recorded_infrastructure_replacements_count_once(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            receipts = [
                {'action': 'retry', 'failure': {'kind': 'provider', 'code': 'provider_rate_limited'}},
                {'action': 'retry', 'failure': {'kind': 'provider', 'code': 'provider_rate_limited'}},
                {'action': 'retry', 'cause': 'execution_interrupted', 'failure': None},
                {'action': 'retry', 'failure': {'kind': 'harness', 'code': 'preparation_transport'}},
                {'action': 'measured', 'failure': None},
            ]
            for index, receipt in enumerate(receipts):
                path = root / f'attempt-{index:04}'
                path.mkdir()
                save(path / 'disposition.json', receipt)
            result = infrastructure_recovery(root)
            self.assertEqual([r['attempt'] for r in result['attempts']], ['attempt-0000', 'attempt-0001', 'attempt-0003'])
            self.assertFalse(result['exhausted'])
            self.assertEqual(infrastructure_recovery(root), result)


    def test_provider_fault_cannot_replay_completed_candidate_work(self):
        fault={'kind':'provider','code':'provider_overloaded','retryable':True}
        for count in (1,10):
            report={'cases':[{'status':'error','failure':fault,'task_allowance':{'completed':count}}]}
            self.assertEqual(episode.disposition({'kind':'exited'},report),('blocked','provider_overloaded'))

    def test_empty_completion_is_not_resampled_or_assigned_model_blame(self):
        fault={'kind':'provider','code':'provider_empty_completion','retryable':True}
        report={'cases':[{'status':'error','failure':fault,'task_allowance':{'completed':0}}]}
        self.assertEqual(episode.disposition({'kind':'exited'},report),('blocked','provider_empty_completion'))
