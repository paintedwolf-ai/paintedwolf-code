from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import threading
import unittest
from scheduler import Admission, dispatch, execution_policy


def fixture():
    providers = [{'id': 'local', 'kind': 'ollama', 'base_url': 'http://private:11434/v1'},
                 {'id': 'cloud', 'kind': 'fixture', 'base_url': 'https://cloud.example/v1'},
                 {'id': 'second', 'kind': 'fixture', 'base_url': 'https://second.example/v1'}]
    models = [{'id': p['id'], 'configuration': {'coordinator': {'provider': p['id']},
               'lite': {'provider': 'cloud'}, 'workers': [{'provider': 'cloud'}]}} for p in providers]
    return models, providers


class SchedulerTests(unittest.TestCase):
    def test_episode_backoff_releases_capacity_and_preserves_the_slot(self):
        import time
        models, providers = fixture()
        items = [{'index': i, 'model': 'cloud'} for i in range(2)]
        calls, results = [], []
        def execute(item):
            calls.append(item['index'])
            if calls == [0]:
                return {'retry_at': time.time() + 0.05}
            return {'report': 'closed'}
        completed = dispatch(items, execution_policy(models, providers, max_active=1), execute,
            threading.Event(), lambda item: None, lambda item, result: results.append((item['index'], result)))
        self.assertTrue(completed)
        self.assertEqual(calls, [0, 1, 0])
        self.assertEqual([index for index, result in results if 'report' in result], [1, 0])

    def test_waiting_retry_can_be_cancelled_without_relaunch(self):
        import time
        models, providers = fixture()
        cancelled = threading.Event()
        calls = []
        def execute(item):
            calls.append(item['index'])
            return {'retry_at': time.time() + 3600}
        completed = dispatch([{'index': 0, 'model': 'cloud'}], execution_policy(models, providers), execute,
            cancelled, lambda item: None, lambda item, result: cancelled.set())
        self.assertFalse(completed)
        self.assertEqual(calls, [0])

    def test_blocked_trial_does_not_stop_independent_trials(self):
        models, providers = fixture()
        items = [{'index': i, 'model': 'cloud'} for i in range(3)]
        results = []
        completed = dispatch(items, execution_policy(models, providers, max_active=1),
            lambda item: {'blocked': 'execution_unresolved'} if item['index'] == 0 else {'report': 'retained'},
            threading.Event(), lambda item: None, lambda item, result: results.append(result))
        self.assertTrue(completed)
        self.assertEqual(len(results), 3)
        self.assertEqual(results[0], {'blocked': 'execution_unresolved'})

    def test_unknown_execution_halts_new_admission(self):
        models, providers = fixture()
        starts, results = [], []
        completed = dispatch([{'index': i, 'model': 'cloud'} for i in range(3)],
            execution_policy(models, providers, max_active=1),
            lambda item: {'blocked': 'execution_unknown'}, threading.Event(), starts.append,
            lambda item, result: results.append(result))
        self.assertFalse(completed)
        self.assertEqual(len(starts), 1)
        self.assertEqual(results, [{'blocked': 'execution_unknown'}])

    def test_local_queue_cannot_block_cloud_progress(self):
        models, providers = fixture()
        policy = execution_policy(models, providers, max_active=2)
        local_started, cloud_finished, release = threading.Event(), threading.Event(), threading.Event()
        cancelled = threading.Event()
        items = [{'index': i, 'model': model} for i, model in enumerate(['local', 'local', 'cloud', 'second'])]
        starts, ends = [], []

        def execute(item):
            if item['model'] == 'local':
                local_started.set()
                if not release.wait(5):
                    raise TimeoutError('test did not release local provider')
            elif item['model'] == 'second':
                cloud_finished.set()
            return {'ok': True}

        with ThreadPoolExecutor(max_workers=1) as pool:
            future = pool.submit(dispatch, items, policy, execute, cancelled, starts.append,
                                 lambda item, result: ends.append(item['index']))
            try:
                self.assertTrue(local_started.wait(5))
                self.assertTrue(cloud_finished.wait(5), 'cloud work parked behind an Ollama waiter')
                self.assertNotIn(1, [item['index'] for item in starts])
            finally:
                release.set()
            future.result(timeout=5)
        self.assertCountEqual(ends, range(4))

    def test_shared_supporting_provider_and_aliases_consume_capacity(self):
        models, providers = fixture()
        providers.append({'id': 'alias', 'kind': 'fixture', 'base_url': 'https://cloud.example:443/other'})
        models[2]['configuration']['lite'] = {'provider': 'alias'}
        policy = execution_policy(models, providers, overrides={'cloud': 1})
        admission = Admission(policy)
        local, second = {'model': 'local'}, {'model': 'second'}
        admission.acquire(local)
        self.assertFalse(admission.available(second))
        admission.release(local)
        self.assertTrue(admission.available(second))
        self.assertNotIn('private', str(policy))
        with self.assertRaisesRegex(ValueError, 'conflicting'):
            execution_policy(models, providers, overrides={'cloud': 1, 'alias': 2})

    def test_local_worker_serializes_cloud_candidates_too(self):
        models, providers = fixture()
        for model in models:
            model['configuration']['workers'] = [{'provider': 'local'}]
        admission = Admission(execution_policy(models, providers))
        admission.acquire({'model': 'cloud'})
        self.assertFalse(admission.available({'model': 'second'}))

    def test_multiple_models_share_one_provider_coordinator_limit(self):
        models, providers = fixture()
        for name in ['cloud-a', 'cloud-b', 'cloud-c']:
            models.append({'id': name, 'configuration': models[1]['configuration']})
        admission = Admission(execution_policy(models, providers, max_active=8, cloud_concurrency=2))
        admission.acquire({'model': 'cloud-a'})
        self.assertTrue(admission.available({'model': 'cloud-b'}))
        admission.acquire({'model': 'cloud-b'})
        self.assertFalse(admission.available({'model': 'cloud-c'}))
        self.assertTrue(admission.available({'model': 'second'}))
        self.assertTrue(admission.available({'model': 'local'}))

    def test_admission_limits_and_completion_hold_for_a_large_matrix(self):
        models, providers = fixture()
        policy = execution_policy(models, providers)
        items = [{'index': i, 'model': models[i % 3]['id']} for i in range(675)]
        active, completed = Counter(), []
        def started(item):
            active[item['model']] += 1
            self.assertLessEqual(sum(active.values()), 5)
            self.assertLessEqual(active['local'], 1)
            self.assertLessEqual(active['cloud'], 2)
            self.assertLessEqual(active['second'], 2)
        def finished(item, result):
            active[item['model']] -= 1
            completed.append(item['index'])
        dispatch(items, policy, lambda item: {'ok': True}, threading.Event(), started, finished)
        self.assertCountEqual(completed, range(675))

    def test_cancellation_leaves_unadmitted_attempts_untouched(self):
        models, providers = fixture()
        policy = execution_policy(models, providers, max_active=1)
        cancelled, starts, results = threading.Event(), [], []
        def execute(item):
            cancelled.set()
            return {'error': 'cancelled'}
        dispatch([{'index': i, 'model': 'cloud'} for i in range(10)], policy, execute, cancelled,
                 starts.append, lambda item, result: results.append(result))
        self.assertEqual(len(starts), 1)
        self.assertEqual(results, [{'error': 'cancelled'}])

    def test_infrastructure_exception_pauses_admission(self):
        models, providers = fixture()
        cancelled, results = threading.Event(), []
        def execute(item):
            raise OSError('private diagnostic')
        dispatch([{'index': i, 'model': 'cloud'} for i in range(3)],
                 execution_policy(models, providers, max_active=1), execute, cancelled,
                 lambda item: None, lambda item, result: results.append(result))
        self.assertFalse(cancelled.is_set())
        self.assertEqual(results, [{'error': 'runner_failed', 'error_type': 'OSError'}])

    def test_invalid_limits_are_rejected(self):
        models, providers = fixture()
        for value in [0, -1, True, 2.5]:
            with self.assertRaises(ValueError):
                execution_policy(models, providers, max_active=value)
        for overrides in [{'missing': 1}, {'local': 2}, {'cloud': 0}]:
            with self.assertRaises(ValueError):
                execution_policy(models, providers, overrides=overrides)

    def test_missing_application_report_pauses_before_launching_more_work(self):
        models, providers = fixture()
        cancelled, starts = threading.Event(), []
        dispatch([{'index': i, 'model': 'cloud'} for i in range(10)],
                 execution_policy(models, providers, max_active=1),
                 lambda item: {'error': 'launch_failed'}, cancelled,
                 starts.append, lambda item, result: None)
        self.assertFalse(cancelled.is_set())
        self.assertEqual(len(starts), 1)


if __name__ == '__main__':
    unittest.main()
