"""Exercise installed OCR, document helpers, rendering and visual redacted sends."""
from __future__ import annotations

import base64
import csv
import hashlib
import io
import json
from pathlib import Path
import re
import tempfile
import time
import uuid

from .app import InstalledApp
from .approval import token
from .process import run
from .provider import ProviderFixture
from .products import require_owned_guest
from .records import CaseUnavailable, retain
from .workflow import ToolTurn, accepted

# Public test fixture, deliberately shared with the recognizer's existing contracts.
CANARY = 'AKIAQYJK4TXV6NHRWMFD'
SVG = '<svg xmlns="http://www.w3.org/2000/svg" width="1000" height="160"><rect width="100%" height="100%" fill="white"/><text x="24" y="55" font-family="sans-serif" font-size="32">Painted Wolf Code</text>{}</svg>'
SECRET = '<text x="24" y="115" font-family="sans-serif" font-size="32">aws key ' + CANARY + '</text>'


class VisualProvider(ProviderFixture):
    def __init__(self, calls):
        super().__init__(calls)
        self.images = []
        self.canary_in_text = False

    def completion(self, request):
        if not isinstance(request, dict):
            raise ValueError('visual fixture requires structured provider requests')
        if not isinstance(request.get('messages'), list):
            raise ValueError('visual fixture requires structured provider messages')
        for message in request['messages']:
            content = message.get('content', []) if isinstance(message, dict) else []
            parts = [{'type': 'text', 'text': content}] if isinstance(content, str) else ([] if content is None else content)
            if not isinstance(parts, list):
                raise ValueError('visual fixture received malformed message parts')
            for part in parts:
                if not isinstance(part, dict):
                    raise ValueError('visual fixture received a malformed content part')
                if part.get('type') == 'text':
                    if not isinstance(part.get('text'), str):
                        raise ValueError('visual fixture received nontext content')
                    self.canary_in_text |= CANARY in part['text']
                elif part.get('type') == 'image_url':
                    image = part.get('image_url')
                    url = image.get('url') if isinstance(image, dict) else None
                    if not isinstance(url, str) or not url.startswith('data:image/png;base64,'):
                        raise ValueError('visual fixture refuses remote or non-PNG image transport')
                    raw = base64.b64decode(url.partition(',')[2], validate=True)
                    if not raw.startswith(b'\x89PNG\r\n\x1a\n') or not 0 < len(raw) <= 2 << 20:
                        raise ValueError('visual fixture received an invalid or oversized PNG')
                    if raw not in self.images:
                        if len(self.images) >= 4:
                            raise ValueError('visual fixture image count exceeded its bound')
                        self.images.append(raw)
        return super().completion(request)


def redaction_selection(checkpoint: dict, session: str, calls: list[dict], path: Path) -> tuple[str, str]:
    checkpoint_id = checkpoint.get('id')
    if (not isinstance(checkpoint_id, str) or str(uuid.UUID(checkpoint_id)) != checkpoint_id
            or checkpoint.get('session_id') != session or checkpoint.get('kind') != 'tool_approval'
            or checkpoint.get('status') != 'pending'):
        raise ValueError('visual redaction checkpoint is not pending for this fixture session')
    approval = checkpoint.get('tool_approval', {})
    plan = approval.get('plan', {})
    call_id = approval.get('tool_call_id')
    matching = [row for row in calls if row.get('id') == call_id]
    location = plan.get('presentation', {}).get('location', {})
    if (not token(call_id) or len(matching) != 1 or matching[0].get('name') != 'view_image'
            or matching[0].get('args') != {'path': path.name}
            or matching[0].get('args_malformed') or matching[0].get('args_truncated')
            or approval.get('joined_count', 1) != 1 or approval.get('joined_tool_call_ids')
            or plan.get('stage') != 'pre_send' or plan.get('subject', {}).get('kind') != 'secret'
            or plan.get('held_release') is not None or location.get('destination_kind') != 'model_provider'
            or location.get('origin_kind') != 'file' or location.get('path') not in {path.name, str(path)}):
        raise ValueError('visual redaction is not bound to the exact fixture file and model destination')
    options = plan.get('options')
    if not isinstance(options, list) or any(not isinstance(row, dict) for row in options):
        raise ValueError('visual redaction plan omitted typed options')
    selected = [row for row in options if row.get('kind') == 'redacted' and row.get('rung') == 'redacted'
                and row.get('decision_action') == 'redact' and row.get('disabled', False) is False]
    if (len(selected) != 1 or not token(selected[0].get('id'))
            or sum(row.get('id') == selected[0]['id'] for row in options) != 1):
        raise ValueError('visual fixture has no unique enabled redacted-send option')
    return checkpoint_id, selected[0]['id']


def redact(turn, path: Path):
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        document = accepted(turn.app, 'GET', f'/v1/sessions/{turn.session_id}/checkpoints?status=pending', None, {200})
        checkpoints = document.get('checkpoints')
        if not isinstance(checkpoints, list) or any(not isinstance(row, dict) for row in checkpoints):
            raise ValueError('visual fixture checkpoint list is malformed')
        if checkpoints:
            if len(checkpoints) != 1:
                raise ValueError('visual fixture has ambiguous pending approvals')
            transcript = accepted(turn.app, 'GET', f'/v1/sessions/{turn.session_id}/messages?limit=500', None, {200})
            messages = transcript.get('messages')
            if not isinstance(messages, list) or any(not isinstance(row, dict) for row in messages):
                raise ValueError('visual fixture transcript is malformed')
            calls = []
            for message in messages:
                rows = message.get('tool_calls', [])
                if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
                    raise ValueError('visual fixture transcript calls are malformed')
                calls.extend(rows)
            checkpoint, option = redaction_selection(checkpoints[0], turn.session_id, calls, path)
            result = accepted(turn.app, 'POST', f'/v1/sessions/{turn.session_id}/checkpoints/{checkpoint}',
                              {'kind': 'tool_approval', 'action': 'approve', 'option_id': option}, {200})
            if (result.get('id') != checkpoint or result.get('session_id') != turn.session_id
                    or result.get('kind') != 'tool_approval' or result.get('status') != 'approved'):
                raise ValueError('visual fixture did not resolve its selected redaction checkpoint')
            return {'checkpoint_id': checkpoint, 'option_id': option, 'status': 'approved'}
        time.sleep(0.1)
    raise CaseUnavailable('installed visual fixture did not produce a redacted-send checkpoint')


def visual_result(results: list[dict], redacted: bool) -> dict:
    rows = [row for row in results if row.get('tool') == 'view_image']
    if len(rows) != 1:
        raise ValueError('visual fixture omitted its unique image result')
    row = rows[0]
    invocation, visual = row.get('invocation', {}), row.get('visual', {})
    if (row.get('outcome') != 'completed' or invocation.get('tool') != 'view_image'
            or invocation.get('invoked') is not True or invocation.get('status') != 'completed'
            or visual.get('perceive') is not True or visual.get('mime') != 'image/png'
            or any(type(visual.get(key)) is not int or visual[key] <= 0 for key in ('width', 'height'))):
        raise ValueError('visual fixture did not actually produce a perceived native PNG')
    document = json.loads(row['content'])
    if (not isinstance(document, dict) or document.get('format') != 'svg' or document.get('screening_gap')
            or (document.get('perception') == 'redacted') is not redacted
            or document.get('perception', '') not in {'', 'redacted'} or CANARY in json.dumps(document)):
        raise ValueError('visual fixture returned invalid screening or redaction facts')
    return {'invocation': invocation, 'visual': {key: value for key, value in visual.items() if key != 'bytes'},
            'screening': document}


def read_pixels(binding: dict, raw: bytes) -> tuple[dict, dict]:
    with tempfile.TemporaryDirectory(prefix='paintedwolf-screened-pixels-') as directory:
        image = Path(directory) / 'screened.png'
        image.write_bytes(raw)
        tessdata = str(Path(binding['install_root']) / 'engine-root/ocr/tessdata')
        result = run(binding, 'ocr-observer', [str(image), 'stdout', '--tessdata-dir', tessdata,
                                             '-l', 'eng', '--psm', '1', 'tsv'], timeout=45)
    if result['exit_code'] or result['timed_out'] or result['output_limit']:
        raise ValueError('selected installed OCR observer could not inspect the actual provider pixels')
    rows = csv.DictReader(io.StringIO(result['stdout'].decode('utf-8')), delimiter='\t', quoting=csv.QUOTE_NONE)
    header = ['level', 'page_num', 'block_num', 'par_num', 'line_num', 'word_num', 'left', 'top', 'width', 'height', 'conf', 'text']
    if rows.fieldnames != header:
        raise ValueError('selected installed OCR observer returned malformed geometry')
    words = []
    for row in rows:
        if set(row) != set(header) or any(not isinstance(row[key], str) or not row[key].isdecimal() for key in header[:10]):
            raise ValueError('selected installed OCR observer returned invalid geometry fields')
        if row['level'] == '5':
            confidence = float(row['conf'])
            if not -1 <= confidence <= 100 or not isinstance(row['text'], str):
                raise ValueError('selected installed OCR observer returned invalid word confidence')
            words.append({'text': row['text'], 'box': [int(row[key]) for key in ('left', 'top', 'width', 'height')]})
    text = re.sub('[^A-Z0-9]', '', ' '.join(word['text'] for word in words).upper())
    return {'image_sha256': hashlib.sha256(raw).hexdigest(), 'image_bytes': len(raw), 'words': words,
            'public_heading_read': 'PAINTEDWOLFCODE' in text, 'fixture_canary_read': CANARY in text}, \
           {key: result[key] for key in ('stdout', 'stderr')}


def visual_roundtrip(binding: dict, secret: bool) -> tuple[dict, dict]:
    from .workflow import wait_settled
    app = InstalledApp(binding)
    observations, streams = {}, {}
    with tempfile.TemporaryDirectory(prefix='paintedwolf-installed-visual-') as directory:
        with VisualProvider([{'name': 'view_image', 'arguments': {'path': 'fixture.svg'}}]) as provider:
            try:
                with app, ToolTurn(app, provider, Path(directory), vision=True) as turn:
                    path = turn.workspace / 'fixture.svg'
                    path.write_text(SVG.format(SECRET if secret else ''))
                    turn.submit()
                    if secret:
                        observations['redacted_send'] = redact(turn, path)
                    results = turn.wait(timeout=90)
                    wait_settled(app, turn.session_id)
                    observations['result'] = visual_result(results, secret)
                    if len(provider.images) != 1 or provider.canary_in_text:
                        raise ValueError('visual provider received missing/ambiguous pixels or an unredacted fixture value')
                    pixels, raw_streams = read_pixels(binding, provider.images[0])
                    if not pixels['public_heading_read'] or pixels['fixture_canary_read']:
                        raise ValueError('actual provider pixels lost public content or retained the fixture canary')
                    observations.update({'pixels': pixels, 'provider_requests': provider.observations,
                                         'source_fixture_sha256': hashlib.sha256(path.read_bytes()).hexdigest()})
                    streams.update({'pixels': provider.images[0], 'ocr-stdout': raw_streams['stdout'],
                                    'ocr-stderr': raw_streams['stderr']})
            finally:
                app.stop()
                streams.update(app.streams())
    if not app.observations.get('capture_complete') or app.observations.get('output_limit'):
        raise ValueError('visual fixture lost its bounded installed process capture')
    observations['application'] = app.observations
    return observations, streams


def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    require_owned_guest(binding)
    if capability != 'documents' or binding.get('case_inputs', {}).get('documents', {}) != {}:
        raise ValueError('installed document driver refuses arbitrary fixture or command inputs')
    if case == 'redacted-send':
        observations, streams = visual_roundtrip(binding, True)
        status = 'passed'
    elif case == 'ocr-and-documents':
        observations, streams, failed = {}, {}, False
        for role in ('ocr-engine', 'document-core'):
            result = run(binding, 'pw', ['diagnostics', role], timeout=90)
            streams.update({role + '-' + key: result.pop(key) for key in ('stdout', 'stderr')})
            observations[role] = result
            failed |= result['exit_code'] != 0 or result['timed_out'] or result['output_limit']
        if not failed:
            observations['native_visual'], visual_streams = visual_roundtrip(binding, False)
            streams.update(visual_streams)
        observations['remaining_requirements'] = ['raster-rotation-scale-language-geometry-suite',
            'empty-malformed-oversized-and-exhaustion-fixtures', 'full-document-format-workflow']
        status = 'failed' if failed else 'unverified'
    else:
        raise ValueError('document case belongs to a different fixed probe owner')
    return retain(binding, capability, case, root, status, observations, streams)
