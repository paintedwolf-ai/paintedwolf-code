"""Completed-stage evidence separates admission wait from execution history."""
from contextlib import nullcontext
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import verification_execute as execution
import verification_history as history


class StageRuntimeTests(unittest.TestCase):
    def publish(self, root, entry, code=0):
        queue = SimpleNamespace(root=root, locked=nullcontext)
        log = root / 'stage.log'
        log.write_text('')
        executor = SimpleNamespace(queue=queue, directory=root, running={'request': {0}},
                                   cache={}, finished=set(), stages=[])
        job = {'first': {'kind': 'task'}, 'key': 'fixture', 'packages': [],
               'result': {'stage_id': 'stage-001', 'names': ['fixture'], 'started_at': 100,
                          'consumers': ['request'], 'log': str(log)},
               'result_path': root / 'stage.json', 'consumers': ['request'],
               'future': SimpleNamespace(result=lambda: code),
               'reservation': SimpleNamespace(entry=entry),
               'needs': [({'ticket': 'request'}, 0, {}, [])]}
        with patch.object(execution.time, 'time', return_value=204):
            execution.Executor.publish_stage(executor, job)
        return json.loads((root / 'stages.json').read_text())[0], history.read_history(queue)

    def test_queue_wait_never_inflates_the_completed_runtime_sample(self):
        for code in (0, 1):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                result, measured = self.publish(Path(temporary), {'started_at': 200}, code)
                self.assertEqual(result['queued_at'], 100)
                self.assertEqual(result['started_at'], 200)
                self.assertEqual(result['finished_at'], 204)
                self.assertEqual(history.samples(measured, 'fixture'), [4])

    def test_unadmitted_and_canceled_stages_never_create_runtime_samples(self):
        for entry, code in (({}, 130), ({}, 2), ({'started_at': 200}, 143)):
            with self.subTest(entry=entry, code=code), tempfile.TemporaryDirectory() as temporary:
                _, measured = self.publish(Path(temporary), entry, code)
                self.assertEqual(measured, {})
