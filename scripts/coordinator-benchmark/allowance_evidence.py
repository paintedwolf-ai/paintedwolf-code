"""Validate execution allowance against immutable application outputs."""
from episode_evidence import read


def verify_allowance(capture, case, limit):
    if not limit:
        return
    receipt=read(capture,case['session_id'])['allowance']
    if receipt is None:
        raise ValueError('response allowance is absent')
    if receipt != case.get('task_allowance') or receipt['limit'] != limit:
        raise ValueError('response allowance differs from the frozen execution')
    if not 0 <= receipt['completed'] <= limit:
        raise ValueError('invalid response allowance count')
    if receipt.get('exhausted_attempt_id') and receipt['completed'] != limit:
        raise ValueError('response allowance was not exhausted')
    if case.get('status') == 'review_required' and receipt['completed'] == 0:
        raise ValueError('completed task has no settled model output')


def allowance_checks(case):
    return [{'id': 'task-allowance', 'passed': not (case.get('task_allowance') or {}).get('exhausted_attempt_id')}]
