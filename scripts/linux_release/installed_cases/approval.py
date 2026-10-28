"""Approve only the fixed qualification command's host-authored one-shot plan."""
from __future__ import annotations

import json
import re
import time
import uuid

from .records import CaseUnavailable
from .workflow import accepted


def token(value):
    return isinstance(value, str) and re.fullmatch('[A-Za-z0-9_.:-]+', value) is not None


def selection(checkpoint: dict, session: str, calls: list[dict], arguments: dict) -> tuple[str, str]:
    checkpoint_id = checkpoint.get('id')
    if (not isinstance(checkpoint_id, str) or str(uuid.UUID(checkpoint_id)) != checkpoint_id
            or checkpoint.get('session_id') != session or checkpoint.get('kind') != 'tool_approval'
            or checkpoint.get('status') != 'pending'):
        raise ValueError('host execution checkpoint is not pending for this fixture session')
    approval = checkpoint.get('tool_approval', {})
    call_id, plan = approval.get('tool_call_id'), approval.get('plan', {})
    matches = [call for call in calls if call.get('id') == call_id]
    if (not token(call_id) or len(matches) != 1 or matches[0].get('name') != 'command'
            or json.dumps(matches[0].get('args'), sort_keys=True) != json.dumps(arguments, sort_keys=True)
            or matches[0].get('args_malformed') or matches[0].get('args_truncated')
            or approval.get('joined_count', 1) != 1 or approval.get('joined_tool_call_ids')
            or plan.get('stage') != 'pre_spawn' or plan.get('subject', {}).get('kind') != 'host_execution'
            or plan.get('held_release') is not None):
        raise ValueError('host execution approval is not bound to the exact fixed command')
    options = plan.get('options')
    if not isinstance(options, list) or any(not isinstance(row, dict) for row in options):
        raise ValueError('host execution plan omitted typed approval options')
    candidates = [row for row in options if row.get('kind') == 'current_action'
                  and row.get('rung') == 'once' and row.get('decision_action') == 'approve'
                  and row.get('disabled', False) is False]
    if len(candidates) != 1 or not token(candidates[0].get('id')):
        raise ValueError('host execution plan has no unique enabled one-shot option')
    option = candidates[0]['id']
    if sum(row.get('id') == option for row in options) != 1:
        raise ValueError('host execution plan duplicated its option identity')
    return checkpoint_id, option


def approve_host_execution(turn, arguments: dict):
    app, session = turn.app, turn.session_id
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        document = accepted(app, 'GET', f'/v1/sessions/{session}/checkpoints?status=pending', None, {200})
        checkpoints = document.get('checkpoints')
        if not isinstance(checkpoints, list) or any(not isinstance(row, dict) for row in checkpoints):
            raise ValueError('installed checkpoint list has an invalid shape')
        if checkpoints:
            if len(checkpoints) != 1:
                raise ValueError('fixture has more than one pending approval')
            transcript = accepted(app, 'GET', f'/v1/sessions/{session}/messages?limit=500', None, {200})
            messages = transcript.get('messages')
            if not isinstance(messages, list) or any(not isinstance(row, dict) for row in messages):
                raise ValueError('installed approval transcript has an invalid shape')
            calls = []
            for message in messages:
                entries = message.get('tool_calls', [])
                if not isinstance(entries, list) or any(not isinstance(row, dict) for row in entries):
                    raise ValueError('installed approval calls have an invalid shape')
                calls.extend(entries)
            checkpoint, option = selection(checkpoints[0], session, calls, arguments)
            result = accepted(app, 'POST', f'/v1/sessions/{session}/checkpoints/{checkpoint}',
                              {'kind': 'tool_approval', 'action': 'approve', 'option_id': option}, {200})
            if (result.get('id') != checkpoint or result.get('session_id') != session
                    or result.get('kind') != 'tool_approval' or result.get('status') != 'approved'):
                raise ValueError('installed approval did not resolve the selected one-shot checkpoint')
            turn.observations['host_execution_approval'] = {
                'checkpoint_id': checkpoint, 'option_id': option, 'status': 'approved'}
            return
        time.sleep(0.1)
    raise CaseUnavailable('installed host execution did not produce an approval checkpoint')
