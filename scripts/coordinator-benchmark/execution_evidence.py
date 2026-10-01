"""Verify the captured execution references against closed application facts."""
from episode_evidence import read


def verify_execution(capture, case):
    expected=read(capture,case['session_id'])['execution'] if case.get('session_id') else None
    if expected is None:
        failure = case.get('failure') or {}
        if (case.get('status') == 'error' and failure.get('kind') in {'application', 'provider', 'harness'}
                and failure.get('code') and not case.get('execution')
                and not case.get('final') and not case.get('artifact_ids')):
            return False
        raise ValueError('execution has no application turn')
    if case.get('execution') != expected:
        raise ValueError('execution differs from its application facts')
    message = expected.get('closeout') or {}
    final = message.get('content', '') if message.get('visible') and not message.get('has_tool_calls') else ''
    if case.get('final', '') != final:
        raise ValueError('handoff differs from the terminal output')
    return True
