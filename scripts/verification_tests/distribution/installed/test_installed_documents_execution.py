"""Visual qualification binds actual perceived pixels to exact redacted-send authority."""
import base64
import copy
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from linux_release.installed_cases import documents, process, workflow
from linux_release.installed_cases.records import CaseUnavailable

SESSION = '11111111-1111-4111-8111-111111111111'
CHECKPOINT = '22222222-2222-4222-8222-222222222222'
PATH = Path('/owned/workspace/fixture.svg')
CALLS = [{'id': 'fixture-call', 'name': 'view_image', 'args': {'path': PATH.name}}]


def checkpoint():
    return {'id': CHECKPOINT, 'session_id': SESSION, 'kind': 'tool_approval', 'status': 'pending',
            'tool_approval': {'tool_call_id': 'fixture-call', 'plan': {'stage': 'pre_send',
                'subject': {'kind': 'secret'}, 'presentation': {'location': {
                    'origin_kind': 'file', 'destination_kind': 'model_provider', 'path': str(PATH)}},
                'options': [{'id': 'host-opaque-redacted', 'kind': 'redacted', 'rung': 'redacted',
                             'decision_action': 'redact'}]}}}


def visual(redacted=True):
    return {'tool': 'view_image', 'outcome': 'completed', 'invocation': {
        'tool': 'view_image', 'invoked': True, 'status': 'completed'},
        'visual': {'id': CHECKPOINT, 'width': 1000, 'height': 160, 'mime': 'image/png', 'perceive': True},
        'content': json.dumps({'format': 'svg', 'perception': 'redacted' if redacted else ''})}


class InstalledDocumentsTests(unittest.TestCase):
    def test_only_exact_pending_visual_file_can_select_host_redaction_option(self):
        self.assertEqual(documents.redaction_selection(checkpoint(), SESSION, CALLS, PATH),
                         (CHECKPOINT, 'host-opaque-redacted'))
        changes = [lambda row: row.update(session_id=CHECKPOINT),
                   lambda row: row.update(status='approved'),
                   lambda row: row['tool_approval'].update(joined_count=2),
                   lambda row: row['tool_approval']['plan'].update(stage='pre_spawn'),
                   lambda row: row['tool_approval']['plan']['subject'].update(kind='host_execution'),
                   lambda row: row['tool_approval']['plan'].update(held_release={'secrets': []}),
                   lambda row: row['tool_approval']['plan']['presentation']['location'].update(path='/foreign'),
                   lambda row: row['tool_approval']['plan']['presentation']['location'].update(destination_kind='service'),
                   lambda row: row['tool_approval']['plan']['options'][0].update(disabled=True),
                   lambda row: row['tool_approval']['plan']['options'].append(copy.deepcopy(row['tool_approval']['plan']['options'][0]))]
        for change in changes:
            row = checkpoint()
            change(row)
            with self.subTest(row=row), self.assertRaises(ValueError):
                documents.redaction_selection(row, SESSION, CALLS, PATH)
        for calls in [[], CALLS * 2, [dict(CALLS[0], args={'path': 'foreign.svg'})],
                      [dict(CALLS[0], args_truncated=True)]]:
            with self.assertRaises(ValueError):
                documents.redaction_selection(checkpoint(), SESSION, calls, PATH)

    def test_visual_receipt_requires_actual_invocation_and_perception(self):
        report = documents.visual_result([visual()], True)
        self.assertEqual(report['screening']['perception'], 'redacted')
        changes = [lambda row: row.update(outcome='rejected'),
                   lambda row: row['invocation'].update(invoked=False),
                   lambda row: row['visual'].update(perceive=False),
                   lambda row: row['visual'].update(width=True),
                   lambda row: row.update(content=json.dumps({'format': 'svg', 'screening_gap': 'ocr_failed', 'perception': 'redacted'})),
                   lambda row: row.update(content=json.dumps({'format': 'svg', 'perception': 'redacted', 'text': documents.CANARY}))]
        for change in changes:
            row = visual()
            change(row)
            with self.subTest(row=row), self.assertRaises(ValueError):
                documents.visual_result([row], True)
        with self.assertRaises(ValueError):
            documents.visual_result([visual(False)], True)
        with self.assertRaises(ValueError):
            documents.visual_result([visual(), visual()], True)

    def test_fixture_provider_observes_actual_png_and_canary_without_retaining_requests(self):
        raw = b'\x89PNG\r\n\x1a\npublic-pixel-fixture'
        request = {'model': 'installed-qualification', 'tools': [{'type': 'function', 'function': {'name': 'view_image'}}],
                   'messages': [{'role': 'user', 'content': [
                       {'type': 'image_url', 'image_url': {'url': 'data:image/png;base64,' + base64.b64encode(raw).decode()}},
                       {'type': 'text', 'text': documents.CANARY}]}]}
        fixture = documents.VisualProvider([{'name': 'view_image', 'arguments': {'path': PATH.name}}])
        fixture.completion(request)
        fixture.completion(request)
        self.assertEqual(fixture.images, [raw])
        self.assertTrue(fixture.canary_in_text)
        self.assertNotIn(documents.CANARY, repr(fixture.observations))
        for url in ['https://foreign.invalid/image.png', 'data:image/png;base64,%%%']:
            changed = copy.deepcopy(request)
            changed['messages'][0]['content'][0]['image_url']['url'] = url
            with self.assertRaises(ValueError):
                fixture.completion(changed)

    def test_independent_ocr_reads_positive_heading_and_detects_fixture_canary(self):
        header = 'level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n'
        for text, canary in [('Painted Wolf Code', False), ('Painted Wolf Code ' + documents.CANARY, True)]:
            result = {'stdout': (header + '5\t1\t1\t1\t1\t1\t24\t24\t900\t40\t99\t' + text + '\n').encode(),
                      'stderr': b'', 'exit_code': 0, 'timed_out': False, 'output_limit': False}
            with patch.object(documents, 'run', return_value=result) as run:
                facts, streams = documents.read_pixels({'install_root': '/selected'}, b'PNG-fixture')
                self.assertTrue(facts['public_heading_read'])
                self.assertEqual(facts['fixture_canary_read'], canary)
                self.assertEqual(streams['stdout'], result['stdout'])
                self.assertEqual(run.call_args.args[1], 'ocr-observer')
                self.assertIn('/selected/engine-root/ocr/tessdata', run.call_args.args[2])
        for raw in [b'', header.encode() + b'5\t1\n', (header + '5\t1\t1\t1\t1\t1\t0\t0\t1\t1\tnan\ttext\n').encode()]:
            result['stdout'] = raw
            with patch.object(documents, 'run', return_value=result), self.assertRaises(ValueError):
                documents.read_pixels({'install_root': '/selected'}, b'PNG-fixture')

    def test_ocr_observer_can_only_run_the_fixed_final_manifest_image(self):
        path = Path('/selected/engine-root/ocr/bin/tesseract')
        identity = {'sha256': 'a' * 64, 'bytes': 42, 'mode': 0o755, 'device': 1, 'inode': 2}
        binding = {'install_root': '/selected', 'images': {str(path): identity}, 'ocr_path': '/foreign'}
        with patch.object(process, 'file_identity', return_value=dict(identity, header=b'ELF')):
            self.assertEqual(process.probe_path(binding, 'ocr-observer'), path)
        with patch.object(process, 'file_identity', return_value=dict(identity, inode=3, header=b'ELF')):
            with self.assertRaises(ValueError):
                process.probe_path(binding, 'ocr-observer')
        with self.assertRaises(CaseUnavailable):
            process.probe_path(dict(binding, images={}), 'ocr-observer')
        with patch.object(process, 'unchanged', return_value=Path('/ordinary')) as unchanged:
            self.assertEqual(process.probe_path(binding, 'pw'), Path('/ordinary'))
            unchanged.assert_called_once_with(binding, 'pw')

    def test_vision_capability_is_explicit_and_never_changes_normal_fixtures(self):
        arguments = (SimpleNamespace(), SimpleNamespace(), Path('/unused'))
        self.assertNotIn('vision', workflow.ToolTurn(*arguments).capabilities)
        self.assertIn('vision', workflow.ToolTurn(*arguments, vision=True).capabilities)
        for value in [1, 'true', None]:
            with self.assertRaises(ValueError):
                workflow.ToolTurn(*arguments, vision=value)
