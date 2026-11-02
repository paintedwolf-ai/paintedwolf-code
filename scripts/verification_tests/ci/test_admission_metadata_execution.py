from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from ci_policy import admission_metadata


class AdmissionMetadataTests(unittest.TestCase):
    def test_plan_entrypoint_reports_known_rejection_and_other_events_do_not_wait(self):
        with tempfile.TemporaryDirectory() as directory:
            event = Path(directory) / 'event.json'
            event.write_text(json.dumps({'pull_request': {'head': {'sha': 'a' * 40}}}))
            with patch.dict(admission_metadata.os.environ,
                            GITHUB_EVENT_NAME='pull_request', GITHUB_EVENT_PATH=str(event)), \
                    patch.object(admission_metadata, 'rejection', return_value={'head': 'a' * 40, 'state': 'failure'}), \
                    redirect_stdout(io.StringIO()) as output:
                self.assertEqual(admission_metadata.main(), 1)
                self.assertIn('::error title=Commit certification', output.getvalue())
        with patch.dict(admission_metadata.os.environ, GITHUB_EVENT_NAME='workflow_run'), \
                patch.object(admission_metadata, 'rejection') as query:
            self.assertEqual(admission_metadata.main(), 0)
            query.assert_not_called()

    def test_known_rejection_is_bound_to_the_actual_pull_head(self):
        head = 'a' * 40
        event = {'pull_request': {'head': {'sha': head}, 'user': {'login': 'person'}}}
        with patch.object(admission_metadata, 'repository', return_value='repos/owner/repo'), \
                patch.object(admission_metadata, 'api', return_value={
                    'sha': head, 'statuses': [{'context': 'other', 'state': 'failure'},
                                 {'context': 'DCO-owned', 'state': 'failure'}]}) as api:
            self.assertEqual(admission_metadata.rejection(event), {'head': head, 'state': 'failure'})
        api.assert_called_once_with(f'repos/owner/repo/commits/{head}/status?per_page=100', timeout=15)

    def test_pending_missing_and_unavailable_certification_never_wait_or_replace_the_gate(self):
        event = {'pull_request': {'head': {'sha': 'a' * 40}}}
        for state in ('pending', 'success', 'unknown'):
            with patch.object(admission_metadata, 'repository', return_value='repos/owner/repo'), \
                    patch.object(admission_metadata, 'api', return_value={
                        'sha': 'a' * 40, 'statuses': [{'context': 'DCO-owned', 'state': state}]}):
                self.assertIsNone(admission_metadata.rejection(event))
        for result in ({}, None, {'sha': 'b' * 40, 'statuses': [{'context': 'DCO-owned', 'state': 'failure'}]}):
            with patch.object(admission_metadata, 'repository', return_value='repos/owner/repo'), \
                    patch.object(admission_metadata, 'api', return_value=result):
                self.assertIsNone(admission_metadata.rejection(event))
        with patch.object(admission_metadata, 'repository', return_value='repos/owner/repo'), \
                patch.object(admission_metadata, 'api', side_effect=subprocess.CalledProcessError(1, 'gh')):
            self.assertIsNone(admission_metadata.rejection(event))
        with patch.object(admission_metadata, 'repository', return_value='repos/owner/repo'), \
                patch.object(admission_metadata, 'api', side_effect=subprocess.TimeoutExpired('gh', 15)):
            self.assertIsNone(admission_metadata.rejection(event))

    def test_bot_route_and_invalid_head_do_not_query_or_create_a_ci_certification_cycle(self):
        for pull in ({'head': {'sha': 'a' * 40}, 'user': {'login': 'dependabot[bot]'}},
                     {'head': {'sha': '../other'}}, {}):
            with patch.object(admission_metadata, 'api') as api:
                self.assertIsNone(admission_metadata.rejection({'pull_request': pull}))
                api.assert_not_called()
