"""Completed-stage evidence separates admission wait from execution history, which keeps only
passing whole-target runs."""
from contextlib import nullcontext
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import verification_execute as execution
import verification_history as history
from verification_plan import expand

WHOLE = expand(["comments:check"])[0]
SELECTED = expand(["test:runner"], ["-k", "fixture"])[0]


class StageRuntimeTests(unittest.TestCase):
    def publish(self, root, entry, code=0, stage=WHOLE):
        queue = SimpleNamespace(root=root, history=root, locked=nullcontext)
        log = root / 'stage.log'
        log.write_text('')
        executor = SimpleNamespace(queue=queue, directory=root, running={'request': {0}},
                                   cache={}, finished=set(), stages=[])
        job = {'first': stage, 'key': 'fixture', 'packages': [],
               'result': {'stage_id': 'stage-001', 'names': [stage['name']], 'started_at': 100,
                          'consumers': ['request'], 'log': str(log)},
               'result_path': root / 'stage.json', 'consumers': ['request'],
               'future': SimpleNamespace(result=lambda: code),
               'reservation': SimpleNamespace(entry=entry),
               'needs': [({'ticket': 'request'}, 0, {}, [])]}
        with patch.object(execution.time, 'time', return_value=204):
            execution.Executor.publish_stage(executor, job)
        return json.loads((root / 'stages.json').read_text())[0], history.read_history(queue)

    def test_queue_wait_never_inflates_the_completed_runtime_sample(self):
        with tempfile.TemporaryDirectory() as temporary:
            result, measured = self.publish(Path(temporary), {'started_at': 200})
        self.assertEqual((result['queued_at'], result['started_at'], result['finished_at']), (100, 200, 204))
        self.assertEqual(history.samples(measured, WHOLE['name']), [4])

    def test_failed_selected_unadmitted_and_canceled_stages_never_create_runtime_samples(self):
        admitted = {'started_at': 200}
        for entry, code, stage in ((admitted, 1, WHOLE), (admitted, 0, SELECTED), ({}, 0, WHOLE),
                                   ({}, 130, WHOLE), ({}, 2, WHOLE), (admitted, 143, WHOLE)):
            with self.subTest(entry=entry, code=code, stage=stage['name']), tempfile.TemporaryDirectory() as temporary:
                _, measured = self.publish(Path(temporary), entry, code, stage)
                self.assertEqual(measured, {})
