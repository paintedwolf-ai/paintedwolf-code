"""Read typed application facts, execution allowance, and closeout grounding."""
import json
import os
import pathlib
import subprocess
import uuid
from progress import save
from snapshot import file_hash

VERSION = 1
METHODS = {'inspection', 'targeted', 'project', 'blocked'}


def path_for(capture, session):
    return capture / 'episode-evidence' / (str(uuid.UUID(session)) + '.json')


def export(capture, case, source):
    target = path_for(capture, case['session_id'])
    database = capture / 'store.db'
    before = file_hash(database)
    wal = capture / 'store.db-wal'
    command = [str(source / '.bin/lycaon-debug'), 'eval', 'evidence', '--capture', str(capture),
               '--session', case['session_id']]
    project = pathlib.Path(case.get('project_dir') or '')
    if case.get('project_dir') and not case.get('invalid_artifact') and project.is_dir():
        command += ['--project', str(project)]
    environment = {**os.environ, 'LYCAON_DEV': '1', 'LYCAON_CONFIG_ROOT': str(source / 'lycaon')}
    result = subprocess.run(command, capture_output=True, text=True, check=True, cwd=source, env=environment)
    facts = json.loads(result.stdout)
    if (facts['version'] != VERSION or facts['root_session_id'] != case['session_id']
            or file_hash(database) != before or (wal.exists() and wal.stat().st_size)):
        raise ValueError('application evidence changed during extraction')
    target.parent.mkdir(exist_ok=True)
    save(target, facts)
    return facts


def read(capture, session):
    facts = json.loads(path_for(capture, session).read_text())
    if facts['version'] != VERSION or facts['root_session_id'] != session:
        raise ValueError('application evidence does not identify the requested session')
    return facts


def member(facts, session):
    return next(s for s in facts['sessions'] if s['id'] == session)


def verify_execution(capture, case):
    expected = read(capture, case['session_id'])['execution'] if case.get('session_id') else None
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


def verify_allowance(capture, case, limit):
    if not limit:
        return
    receipt = read(capture, case['session_id'])['allowance']
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


def final_grounding(db, session_id):
    """Read the grounding audit bound to the terminal output."""
    execution = db['execution'] or {}
    closeout = execution.get('closeout') or {}
    return (closeout.get('grounding') or {}) if execution.get('status') == 'complete' and closeout.get('model_authored') else {}


def method_checks(capture, case, expected):
    grounding = final_grounding(read(capture, case['session_id']), case['session_id'])
    declared = grounding.get('verification') or {}
    method = declared.get('method') if isinstance(declared, dict) else None
    valid = grounding.get('traced') is True and not grounding.get('host_assembled', False)
    return [{'id': 'declared-verification-' + expected, 'passed': valid and method in METHODS and method == expected}]


def cited_finding_checks(capture, case, findings):
    grounding = final_grounding(read(capture, case['session_id']), case['session_id'])
    cited = {(c.get('path'), c.get('line')) for c in grounding.get('cited_evidence', []) if c.get('verdict') in {'matched', 'traced'}}
    valid = grounding.get('traced') is True and not grounding.get('host_assembled', False)
    return [{'id': 'worker-finding-cited', 'passed': valid and all((f['path'], f['line']) in cited for f in findings)}]


def closeout_checks(capture, case, required_paths):
    grounding = final_grounding(read(capture, case['session_id']), case['session_id'])
    valid = (grounding.get('traced') is True and not grounding.get('host_assembled', False)
             and all(c['status'] != 'failed' for c in grounding.get('checks', [])))
    cited = {c.get('path') for c in grounding.get('cited_evidence', [])
             if c.get('handle') and c.get('verdict') in {'matched', 'traced'}}
    return [{'id': 'model-authored-closeout-citations', 'passed': valid and set(required_paths) <= cited}]


def progress_checks(capture, case, expected):
    entry = case['preparation']['transcript_seq']
    messages = member(read(capture, case['session_id']), case['session_id'])['messages']
    completed = [m['progress_complete'] for m in messages if m['origin'] == 'host' and m.get('kind') == 'progress_complete'
                 and m.get('seq', 0) > entry and m.get('progress_complete')]
    updated = any(m['role'] == 'tool' and m['origin'] == 'tool' and m.get('seq', 0) > entry
                  and (m.get('tool_result') or {}).get('outcome') == 'completed'
                  and ((m.get('tool_result') or {}).get('invocation') or {}).get('tool') == 'update_progress'
                  and ((m.get('tool_result') or {}).get('invocation') or {}).get('invoked') is True for m in messages)
    steps = completed[-1].get('steps', []) if completed else []
    actual = {s['label']: s['state'] for s in steps}
    allowed = {label: set(state) if isinstance(state, list) else {state} for label, state in expected.items()}
    valid = len(actual) == len(steps) and all(actual.get(label) in states for label, states in allowed.items())
    return [{'id': 'reconciled-existing-progress', 'passed': bool(updated) and valid}]
