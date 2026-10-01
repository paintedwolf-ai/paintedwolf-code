"""Wave, sequencing, and settlement facts for fixture-planned worker dispatches."""
import ledger
import posixpath
from timestamps import instant
from dispatch_graph import dependency_check_id

def planned_outcome(dispatch):
    """The settlement a dispatch script ends in, derived from its final stage."""
    final = dispatch['stages'][-1]
    if final['kind'] == 'failed':
        return {'status': 'failed', 'code': final['code']}
    return {'status': 'complete', 'merge': 'merged' if dispatch['mode'] == 'write' else None}


def settled_job(capture, jobs, dispatch):
    key = (dispatch['mode'], tuple(sorted(dispatch['paths'])))
    wanted = planned_outcome(dispatch)
    for job in jobs:
        if (job['mode'], tuple(sorted({posixpath.normpath(p) for p in job['paths']}))) != key or job['status'] != wanted['status']:
            continue
        if wanted['status'] == 'failed':
            outcome = ledger.scripted_outcome(capture, job['id']) or {}
            if outcome.get('code') == wanted['code']:
                return job
        elif wanted['merge'] in (None, job['merge_status']):
            return job
    return None


def planned_dispatch_checks(capture, case, spec, expected):
    """Every scripted dispatch reached its declared settlement, bound to the coordinator's calls."""
    dispatches = (spec.get('setup') or {}).get('dispatches') or []
    db = ledger.read(capture, case['session_id'])
    jobs = ledger.jobs(db, case['session_id'])
    landed = db['promotions']
    calls = ledger.assistant_calls(db, case['session_id'])
    checks = []
    matched = {dispatch['label']: settled_job(capture, jobs, dispatch) for dispatch in dispatches}
    for dispatch in dispatches:
        checks.append({'id': 'planned-leg-settled-' + dispatch['label'], 'passed': matched[dispatch['label']] is not None})
    off_plan = [j for j in jobs if (ledger.scripted_outcome(capture, j['id']) or {}).get('code')
                in {'HARNESS_DISPATCH_UNPLANNED', 'HARNESS_SCRIPT_EXHAUSTED'}]
    checks.append({'id': 'no-off-plan-dispatch', 'passed': not off_plan})
    call_ids = {c['id'] for m in calls for c in m['calls'] if c['name'] == 'task'}
    for dependent, prerequisites in expected.get('dependencies', {}).items():
        after = matched[dependent]
        for label in prerequisites:
            before = matched[label]
            ordered = bool(before and after and before['id'] in landed
                           and after['source_tool_call_id'] in call_ids
                           and before['child_session_id'] != after['child_session_id']
                           and instant(after['created_at']) > instant(landed[before['id']]))
            checks.append({'id': dependency_check_id(label, dependent), 'passed': ordered})
    return checks
