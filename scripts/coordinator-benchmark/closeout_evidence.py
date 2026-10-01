"""Read closeout declarations from the retained grounding audit."""
from episode_evidence import read

METHODS = {'inspection', 'targeted', 'project', 'blocked'}


def final_grounding(db, session_id):
    """Read the grounding audit bound to the terminal output."""
    execution=db['execution'] or {}
    closeout=execution.get('closeout') or {}
    return (closeout.get('grounding') or {}) if execution.get('status')=='complete' and closeout.get('model_authored') else {}


def method_checks(capture, case, expected):
    grounding = final_grounding(read(capture,case['session_id']),case['session_id'])
    declared = grounding.get('verification') or {}
    method = declared.get('method') if isinstance(declared, dict) else None
    valid = grounding.get('traced') is True and not grounding.get('host_assembled', False)
    return [{'id': 'declared-verification-' + expected, 'passed': valid and method in METHODS and method == expected}]


def cited_finding_checks(capture, case, findings):
    grounding = final_grounding(read(capture,case['session_id']),case['session_id'])
    cited = {(c.get('path'), c.get('line')) for c in grounding.get('cited_evidence', []) if c.get('verdict') in {'matched', 'traced'}}
    valid = grounding.get('traced') is True and not grounding.get('host_assembled', False)
    return [{'id': 'worker-finding-cited', 'passed': valid and all((f['path'], f['line']) in cited for f in findings)}]
