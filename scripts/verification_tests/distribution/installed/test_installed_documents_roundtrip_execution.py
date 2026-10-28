"""Installed document cases run the visual round trip through the real app and fixed observers."""
import base64
import hashlib
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import documents, workflow
from linux_release.installed_cases.records import CaseUnavailable

SESSION = '11111111-1111-4111-8111-111111111111'
CHECKPOINT = '22222222-2222-4222-8222-222222222222'
PATH = Path('/owned/workspace/fixture.svg')
CALLS = [{'id': 'fixture-call', 'name': 'view_image', 'args': {'path': PATH.name}}]
PNG = b'\x89PNG\r\n\x1a\n'


def checkpoint():
    return {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'pending',
            'tool_approval': {'tool_call_id': 'fixture-call', 'plan': {'stage': 'pre_send',
                'subject': {'kind': 'secret'}, 'presentation': {'location': {
                    'origin_kind': 'file', 'destination_kind': 'model_provider', 'path': str(PATH)}},
                'options': [{'id': 'host-opaque-redacted', 'kind': 'redacted', 'rung': 'redacted',
                             'decision_action': 'redact'}]}}}


def approved():
    return {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'approved'}


def request(content):
    return {'model': 'installed-qualification', 'tools': [{'type': 'function', 'function': {'name': 'view_image'}}],
            'messages': [{'role': 'user', 'content': content}]}


def image(raw):
    return {'type': 'image_url', 'image_url': {'url': 'data:image/png;base64,' + base64.b64encode(raw).decode()}}


class RedactTests(unittest.TestCase):
    def redact(self, responses, clock=None):
        turn = SimpleNamespace(app=object(), session_id=SESSION)
        patches = [patch.object(documents, 'accepted', side_effect=responses), patch.object(documents.time, 'sleep')]
        if clock is not None:
            patches.append(patch.object(documents.time, 'monotonic', side_effect=clock))
        mocks = [item.start() for item in patches]
        try:
            return documents.redact(turn, PATH), mocks[0]
        finally:
            for item in patches:
                item.stop()

    def test_the_pending_redaction_checkpoint_is_approved_with_the_redacted_option(self):
        responses = [{'checkpoints': []}, {'checkpoints': [checkpoint()]},
                     {'messages': [{'tool_calls': CALLS}, {'role': 'user'}]}, approved()]
        result, accepted = self.redact(responses)
        self.assertEqual(result, {'checkpoint_id': CHECKPOINT, 'option_id': 'host-opaque-redacted', 'status': 'approved'})
        self.assertEqual(accepted.call_count, 4)
        self.assertEqual(accepted.call_args_list[0].args[1:], ('GET', f'/v1/sessions/{SESSION}/checkpoints?status=pending', None, {200}))
        self.assertEqual(accepted.call_args_list[2].args[1:3], ('GET', f'/v1/sessions/{SESSION}/messages?limit=500'))
        self.assertEqual(accepted.call_args_list[3].args[1:],
                         ('POST', f'/v1/sessions/{SESSION}/checkpoints/{CHECKPOINT}',
                          {'kind': 'tool_approval', 'action': 'approve', 'option_id': 'host-opaque-redacted'}, {200}))

    def test_malformed_or_ambiguous_checkpoints_and_transcripts_are_refused(self):
        for responses in [[{'checkpoints': 'x'}], [{'checkpoints': [1]}], [{'checkpoints': [checkpoint(), checkpoint()]}],
                          [{'checkpoints': [checkpoint()]}, {'messages': None}],
                          [{'checkpoints': [checkpoint()]}, {'messages': [{'tool_calls': 'x'}]}],
                          [{'checkpoints': [checkpoint()]}, {'messages': [{'tool_calls': CALLS}]}, dict(approved(), status='pending')],
                          [{'checkpoints': [checkpoint()]}, {'messages': [{'tool_calls': CALLS}]}, dict(approved(), session_id=CHECKPOINT)]]:
            with self.subTest(responses=responses), self.assertRaises(ValueError):
                self.redact(responses)

    def test_a_turn_without_a_redaction_checkpoint_is_unavailable(self):
        with self.assertRaises(CaseUnavailable):
            self.redact([{'checkpoints': []}, {'checkpoints': []}], clock=[0.0, 1.0, 100.0])


class Roundtrip:
    """Patch the app, provider, turn and observers around one visual round trip."""

    def __init__(self, images=(PNG,), canary=False, heading=True, canary_read=False, complete=True):
        self.images, self.canary, self.heading, self.canary_read, self.complete = list(images), canary, heading, canary_read, complete

    def __enter__(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.app = MagicMock()
        self.app.observations = {'capture_complete': self.complete}
        self.app.streams.return_value = {'app-stdout': b'out'}
        self.app.__enter__.return_value = self.app
        self.provider = MagicMock()
        self.provider.images, self.provider.canary_in_text, self.provider.observations = self.images, self.canary, {'requests': 1}
        provider_manager = MagicMock()
        provider_manager.__enter__.return_value = self.provider
        self.turn = MagicMock()
        self.turn.workspace, self.turn.session_id = Path(self.temporary.name), SESSION
        self.turn.wait.return_value = [{'tool': 'view_image'}]
        turn_manager = MagicMock()
        turn_manager.__enter__.return_value = self.turn
        pixels = ({'public_heading_read': self.heading, 'fixture_canary_read': self.canary_read, 'words': []},
                  {'stdout': b'tsv', 'stderr': b''})
        self.patches = [patch.object(documents, 'InstalledApp', return_value=self.app),
                        patch.object(documents, 'VisualProvider', return_value=provider_manager),
                        patch.object(documents, 'ToolTurn', return_value=turn_manager),
                        patch.object(documents, 'redact', return_value={'status': 'approved'}),
                        patch.object(documents, 'visual_result', return_value={'screening': 'facts'}),
                        patch.object(documents, 'read_pixels', return_value=pixels),
                        patch.object(workflow, 'wait_settled')]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        self.temporary.cleanup()


class RoundtripTests(unittest.TestCase):
    def test_a_secret_fixture_is_redacted_before_the_provider_sees_its_pixels(self):
        with Roundtrip() as trip:
            facts, streams = documents.visual_roundtrip({'install_root': '/selected'}, True)
            written = (trip.turn.workspace / 'fixture.svg').read_text()
        self.assertIn(documents.CANARY, written)
        self.assertEqual(facts['redacted_send'], {'status': 'approved'})
        self.assertEqual(facts['result'], {'screening': 'facts'})
        self.assertEqual(facts['pixels']['words'], [])
        self.assertEqual(facts['provider_requests'], {'requests': 1})
        self.assertEqual(facts['source_fixture_sha256'], hashlib.sha256(written.encode()).hexdigest())
        self.assertEqual(facts['application'], {'capture_complete': True})
        self.assertEqual(streams, {'pixels': PNG, 'ocr-stdout': b'tsv', 'ocr-stderr': b'', 'app-stdout': b'out'})
        self.assertEqual(trip.mocks[1].call_args.args[0], [{'name': 'view_image', 'arguments': {'path': 'fixture.svg'}}])
        self.assertEqual(trip.mocks[2].call_args.args[:2], (trip.app, trip.provider))
        self.assertEqual(trip.mocks[2].call_args.kwargs, {'vision': True})
        trip.mocks[3].assert_called_once_with(trip.turn, trip.turn.workspace / 'fixture.svg')
        trip.mocks[4].assert_called_once_with([{'tool': 'view_image'}], True)
        trip.mocks[5].assert_called_once_with({'install_root': '/selected'}, PNG)
        trip.mocks[6].assert_called_once_with(trip.app, SESSION)
        trip.turn.submit.assert_called_once_with()
        trip.turn.wait.assert_called_once_with(timeout=90)
        trip.app.stop.assert_called_once_with()

    def test_a_public_fixture_needs_no_redaction(self):
        with Roundtrip() as trip:
            facts, _ = documents.visual_roundtrip({}, False)
            written = (trip.turn.workspace / 'fixture.svg').read_text()
        self.assertNotIn(documents.CANARY, written)
        self.assertNotIn('redacted_send', facts)
        trip.mocks[3].assert_not_called()
        trip.mocks[4].assert_called_once_with([{'tool': 'view_image'}], False)

    def test_missing_ambiguous_unredacted_or_uncaptured_pixels_are_refused(self):
        for trip in [Roundtrip(images=()), Roundtrip(images=(PNG + b'1', PNG + b'2')), Roundtrip(canary=True),
                     Roundtrip(heading=False), Roundtrip(canary_read=True), Roundtrip(complete=False)]:
            with trip, self.assertRaises(ValueError):
                documents.visual_roundtrip({}, True)
            trip.app.stop.assert_called_once_with()


class ExecuteTests(unittest.TestCase):
    def execute(self, case, failing=(), capability='documents', binding=None):
        binding = {'artifact': {}} if binding is None else binding

        def diagnostics(selected, program, arguments, timeout):
            role = arguments[1]
            return {'stdout': role.encode(), 'stderr': b'', 'exit_code': int(role in failing), 'timed_out': False, 'output_limit': False}
        with patch.object(documents, 'require_owned_guest') as guest, \
                patch.object(documents, 'visual_roundtrip', return_value=({'visual': 1}, {'pixels': b'p'})) as roundtrip, \
                patch.object(documents, 'run', side_effect=diagnostics) as run, \
                patch.object(documents, 'retain', side_effect=lambda *args: args):
            result = documents.execute(binding, capability, case, Path('/unused'))
        guest.assert_called_once_with(binding)
        return result, roundtrip, run

    def test_the_redacted_send_case_passes_on_a_complete_secret_round_trip(self):
        result, roundtrip, run = self.execute('redacted-send')
        self.assertEqual(result[4:], ('passed', {'visual': 1}, {'pixels': b'p'}))
        roundtrip.assert_called_once_with({'artifact': {}}, True)
        run.assert_not_called()

    def test_the_document_case_runs_both_diagnostics_then_the_public_round_trip(self):
        result, roundtrip, run = self.execute('ocr-and-documents')
        status, facts, streams = result[4:]
        self.assertEqual(status, 'unverified')
        self.assertEqual([call.args[1:3] for call in run.call_args_list],
                         [('pw', ['diagnostics', 'ocr-engine']), ('pw', ['diagnostics', 'document-core'])])
        for role in ('ocr-engine', 'document-core'):
            self.assertEqual(facts[role], {'exit_code': 0, 'timed_out': False, 'output_limit': False})
            self.assertEqual(streams[role + '-stdout'], role.encode())
        self.assertEqual((facts['native_visual'], streams['pixels']), ({'visual': 1}, b'p'))
        self.assertIn('full-document-format-workflow', facts['remaining_requirements'])
        roundtrip.assert_called_once_with({'artifact': {}}, False)

    def test_a_failing_diagnostic_fails_the_case_without_the_round_trip(self):
        result, roundtrip, _ = self.execute('ocr-and-documents', failing=('document-core',))
        self.assertEqual(result[4], 'failed')
        self.assertNotIn('native_visual', result[5])
        roundtrip.assert_not_called()

    def test_foreign_cases_capabilities_and_inputs_are_refused(self):
        with self.assertRaises(ValueError):
            self.execute('other-case')
        with self.assertRaises(ValueError):
            self.execute('redacted-send', capability='browser')
        with self.assertRaises(ValueError):
            self.execute('redacted-send', binding={'case_inputs': {'documents': {'fixture': 'mine'}}})


class VisualProviderTests(unittest.TestCase):
    def fixture(self):
        return documents.VisualProvider([{'name': 'view_image', 'arguments': {'path': PATH.name}}])

    def test_malformed_requests_parts_and_images_are_refused(self):
        fixture = self.fixture()
        for bad in ['not a request', {'messages': 'x'}, request(5), request([1]), request([{'type': 'text', 'text': 7}]),
                    request([image(b'GIF89a')]), request([image(PNG + b'x' * (2 << 20))])]:
            with self.subTest(bad=str(bad)[:40]), self.assertRaises(ValueError):
                fixture.completion(bad)
        self.assertEqual(fixture.images, [])
        self.assertFalse(fixture.canary_in_text)

    def test_at_most_four_distinct_images_are_retained(self):
        fixture = self.fixture()
        for index in range(4):
            fixture.completion(request([image(PNG + bytes([index]))]))
            fixture.completion(request([image(PNG + bytes([index]))]))
        self.assertEqual(len(fixture.images), 4)
        with self.assertRaises(ValueError):
            fixture.completion(request([image(PNG + b'5')]))
        fixture.completion(request('plain text'))
        self.assertFalse(fixture.canary_in_text)


class ReadPixelsTests(unittest.TestCase):
    def test_an_observer_failure_is_refused(self):
        for result in [{'exit_code': 1, 'timed_out': False, 'output_limit': False},
                       {'exit_code': 0, 'timed_out': True, 'output_limit': False},
                       {'exit_code': 0, 'timed_out': False, 'output_limit': True}]:
            result.update(stdout=b'', stderr=b'')
            with self.subTest(result=result), patch.object(documents, 'run', return_value=result), self.assertRaises(ValueError):
                documents.read_pixels({'install_root': '/selected'}, b'PNG')


if __name__ == '__main__':
    unittest.main()
