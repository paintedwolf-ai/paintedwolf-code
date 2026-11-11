from contextlib import redirect_stdout
import io
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import ci_verification

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


class NixBunIdentityTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for directory in ('packaging/nix', 'lycaon-den'):
            (self.root / directory).mkdir(parents=True)
        self.lock = b'locked packages\n'
        self.expression = b'generated packages\n'
        self.revision = 'a' * 40
        (self.root / 'lycaon-den/bun.lock').write_bytes(self.lock)
        (self.root / 'packaging/nix/bun.nix').write_bytes(self.expression)
        (self.root / 'flake.lock').write_text(json.dumps({
            'nodes': {'bun2nix': {'locked': {'rev': self.revision}}}}))
        self.identity = {
            'source_sha256': hashlib.sha256(self.lock).hexdigest(),
            'expression_sha256': hashlib.sha256(self.expression).hexdigest(),
            'generator_revision': self.revision,
        }
        self.path = self.root / 'packaging/nix/bun-lock.json'
        self.publish()

    def publish(self):
        self.path.write_text(json.dumps(self.identity))

    def test_current_identity_needs_no_converter_or_network(self):
        admission_metadata.require_nix_bun(self.root)

    def test_each_stale_pin_refuses_before_admission(self):
        for field in self.identity:
            with self.subTest(field=field):
                saved = self.identity[field]
                self.identity[field] = 'outdated'
                self.publish()
                with self.assertRaisesRegex(ValueError, field):
                    admission_metadata.require_nix_bun(self.root)
                self.identity[field] = saved
                self.publish()

    def test_planner_refuses_stale_inputs_before_matrix_selection(self):
        self.identity['source_sha256'] = 'outdated'
        self.publish()
        output = io.StringIO()
        with patch.object(ci_verification, '__file__', str(self.root / 'scripts/ci_verification.py')), \
                patch('sys.argv', ['ci_verification.py', 'matrix', 'fast']), \
                patch.object(ci_verification.hosted_plan, 'matrix') as matrix, \
                redirect_stdout(output):
            self.assertEqual(ci_verification.main(), 1)
        matrix.assert_not_called()
        self.assertIn('::error file=packaging/nix/bun-lock.json::', output.getvalue())
        self.assertIn('./task codegen:dependency-inventory -- --nix-bun', output.getvalue())

    def test_unknown_shape_does_not_qualify(self):
        self.identity['unexpected'] = 'value'
        self.publish()
        with self.assertRaisesRegex(ValueError, 'unsupported shape'):
            admission_metadata.require_nix_bun(self.root)

    def test_missing_inputs_do_not_qualify(self):
        (self.root / 'packaging/nix/bun.nix').unlink()
        with self.assertRaises(FileNotFoundError):
            admission_metadata.require_nix_bun(self.root)
