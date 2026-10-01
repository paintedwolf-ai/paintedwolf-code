import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch
import progress as progress_module
from progress import Progress, save


class ProgressTests(unittest.TestCase):
    def test_retry_wait_is_pending_after_restart_and_never_a_measurement(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            item = {'index': 0, 'model': 'candidate'}
            plan = {'episodes': [item], 'execution': {}}
            progress = Progress(root, plan)
            progress.started(item)
            progress.finished(item, {'retry_at': 1234567})
            receipt = json.loads((root / 'attempts/0.json').read_text())
            self.assertEqual(receipt['state'], 'waiting_for_retry')
            recovered = Progress(root, plan)
            self.assertEqual(recovered.pending(), [item])
            self.assertEqual(recovered.records, {})
            self.assertEqual(recovered.blocked, {})

    def test_controller_status_preserves_execution_heartbeat_and_terminal_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            item = {'index': 0, 'model': 'candidate'}
            progress = Progress(root, {'episodes': [item], 'execution': {}})
            progress.started(item)
            attempt = root / 'captures/episode-000/attempt-0000'
            attempt.mkdir(parents=True)
            runtime = {'phase': 'cleanup', 'updated_at': '2026-09-09T00:00:00Z',
                       'supervisor_pid': 123, 'launcher_pid': 124, 'launcher_returncode': 0}
            save(attempt / 'execution.json', runtime)
            progress.write(force=True)
            status = json.loads((root / 'status.json').read_text())
            self.assertEqual(status['active'][0]['runtime'], runtime)
            self.assertNotEqual(status['updated_at'], runtime['updated_at'])
            ended = {'kind': 'exited', 'returncode': 0}
            save(attempt / 'exit.json', ended)
            progress.write(force=True)
            self.assertEqual(json.loads((root / 'status.json').read_text())['active'][0]['runtime'],
                             {'phase': 'execution_finished', 'exit': ended})

    def test_blocked_trial_survives_restart_without_becoming_a_measurement(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            items = [{'index': i, 'model': 'candidate'} for i in range(2)]
            plan = {'episodes': items, 'execution': {}}
            progress = Progress(root, plan)
            progress.started(items[0])
            progress.finished(items[0], {'blocked': 'execution_unresolved', 'capture': 'retained'})
            recovered = Progress(root, plan)
            self.assertEqual(recovered.pending(), [items[1]])
            self.assertEqual(recovered.records, {})
            recovered.write(force=True)
            status = json.loads((root / 'status.json').read_text())
            self.assertEqual(status['blocked_by_model'], {'candidate': 1})
            self.assertEqual(status['completed'], 0)
            self.assertEqual(status['pending_by_model'], {'candidate': 1})

    def test_crash_recovery_never_repeats_admitted_attempts(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            plan = {'episodes': [{'index': i, 'model': 'candidate'} for i in range(3)], 'execution': {}}
            save(root / 'results.json', {})
            progress = Progress(root, plan)
            progress.started(plan['episodes'][0])
            progress.finished(plan['episodes'][0], {'returncode': 0, 'report': 'capture'})
            progress.started(plan['episodes'][1])
            # Simulate losing the aggregate index after its durable attempt receipt.
            save(root / 'results.json', {})
            recovered = Progress(root, plan)
            self.assertEqual(recovered.pending(), plan['episodes'][1:])
            self.assertEqual(recovered.records['0']['returncode'], 0)
            self.assertNotIn('1', recovered.records)
            recovered.write('paused', force=True)
            status = json.loads((root / 'status.json').read_text())
            self.assertEqual(status['completed'], 1)
            self.assertEqual(status['pending_by_model'], {'candidate': 2})
            self.assertEqual(status['active'], [])

    def test_orphan_capture_is_preserved_for_worker_reconciliation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            capture = root / 'captures/episode-000'
            capture.mkdir(parents=True)
            (capture / 'report.json').write_text('{}')
            save(root / 'results.json', {})
            progress = Progress(root, {'episodes': [{'index': 0, 'model': 'candidate'}], 'execution': {}})
            self.assertEqual(progress.pending(), [{'index': 0, 'model': 'candidate'}])
            self.assertEqual((capture / 'report.json').read_text(), '{}')

    def test_terminal_receipt_recovers_when_each_projection_write_is_interrupted(self):
        for blocked in (False, True):
            for destination in ('attempts/0.json', 'results.json', 'blocked.json', 'status.json'):
                for after in (False, True):
                    with self.subTest(blocked=blocked, destination=destination, after=after), tempfile.TemporaryDirectory() as tmp:
                        root = pathlib.Path(tmp)
                        item = {'index': 0, 'model': 'candidate'}
                        plan = {'episodes': [item], 'execution': {}}
                        progress = Progress(root, plan)
                        progress.started(item)
                        record = {'blocked': 'execution_unresolved'} if blocked else {'report': 'capture'}
                        def interrupted_save(path, value):
                            if path == root / destination:
                                if after:
                                    save(path, value)
                                raise OSError('injected checkpoint interruption')
                            save(path, value)
                        with patch.object(progress_module, 'save', side_effect=interrupted_save):
                            with self.assertRaisesRegex(OSError, 'injected checkpoint interruption'):
                                progress.finished(item, record)
                        recovered = Progress(root, plan)
                        committed = destination != 'attempts/0.json' or after
                        self.assertEqual(recovered.pending(), [] if committed else [item])
                        self.assertEqual(recovered.records, {'0': record} if committed and not blocked else {})
                        self.assertEqual(recovered.blocked, {'0': record} if committed and blocked else {})
                        self.assertEqual(json.loads((root / 'results.json').read_text()), recovered.records)
                        self.assertEqual(json.loads((root / 'blocked.json').read_text()), recovered.blocked)


if __name__ == '__main__':
    unittest.main()
