"""Wave, sequencing, decision, and settlement facts for worker dispatches."""
import json
import posixpath
import ledger
from ledger import instant


def dependency_check_id(prerequisite, dependent):
    return 'dispatch-after-integration:' + json.dumps([prerequisite, dependent], separators=(',', ':'))


def waves(dispatches, dependencies):
    by_label = {d['label']: d for d in dispatches}
    if len(by_label) != len(dispatches):
        raise ValueError('dispatch labels must be unique')
    if not set(dependencies) <= set(by_label):
        raise ValueError('a dependent worker is undeclared')
    for label, prerequisites in dependencies.items():
        if (not isinstance(prerequisites, list) or not prerequisites
                or len(set(prerequisites)) != len(prerequisites)
                or not set(prerequisites) <= set(by_label) or label in prerequisites):
            raise ValueError('worker prerequisites must name distinct other assignments')
        if any(by_label[p]['mode'] != 'write' for p in prerequisites):
            raise ValueError('integration prerequisites must be write assignments')
    done = set()
    result = []
    while len(done) < len(by_label):
        ready = [d for d in dispatches if d['label'] not in done
                 and set(dependencies.get(d['label'], [])) <= done]
        if not ready:
            raise ValueError('worker dependencies contain a cycle')
        result.append(ready)
        done.update(d['label'] for d in ready)
    return result


def worker_checks(capture, case, routed_workers, test_paths):
    facts = ledger.read(capture, case['session_id'])
    jobs = ledger.jobs(facts, case['session_id'])
    completed = [j for j in jobs if j['status'] == 'complete' and j['child_session_id'] in routed_workers]
    reviewers = [j for j in completed if j['mode'] == 'read']
    checks = [{'id': 'worker-review', 'passed': bool(reviewers)}]
    if case['id'] == 'delegated-repair':
        return checks
    integrated = {}
    for effect in facts['effects']:
        if effect['branch_id'] == '' and effect['origin'] == 'agent' and effect['entry_kind'] == 'file' and effect['op'] in {'create', 'write', 'rename'}:
            integrated.setdefault(effect['job_id'], set()).add(posixpath.normpath(effect['path']))
    writers = [j for j in completed if j['mode'] == 'write' and j['merge_status'] == 'merged'
               and 'ranking.py' in integrated.get(j['id'], set()) and test_paths & integrated.get(j['id'], set())]
    return checks + [
        {'id': 'independent-workers', 'passed': any(w['child_session_id'] != r['child_session_id'] for w in writers for r in reviewers)},
        {'id': 'worker-contribution', 'passed': bool(writers)},
        {'id': 'resolved-worker-jobs', 'passed': all(j['status'] in {'complete', 'failed', 'cancelled'}
         and j['merge_status'] in {'', 'merged', 'rejected', 'aborted'} for j in jobs)}]


def decision_checks(capture, case, spec, expected):
    """Bind each accepted decision to its assigned job, option and resolver."""
    db = ledger.read(capture, case['session_id'])
    results = ledger.tool_results(db, [case['session_id']])
    jobs = ledger.jobs(db, case['session_id'])
    answers = []
    for result in results:
        if result['tool'] != 'answer_decision' or result['outcome'] != 'completed':
            continue
        try:
            body = json.loads(result['content'])
        except ValueError:
            body = {}
        answers.append({'job': body.get('job_id'), 'option': body.get('option'),
                        'resolved_by': body.get('resolved_by') or result['args'].get('resolved_by') or 'coordinator'})
    dispatches = {d['label']: d for d in spec['setup']['dispatches']}
    checks = []
    for label, requirement in expected.items():
        dispatch = dispatches[label]
        assigned = [j for j in jobs if j['mode'] == dispatch['mode']
                    and {posixpath.normpath(p) for p in j['paths']} == set(dispatch['paths'])]
        bound = [a for a in answers if len(assigned) == 1 and a['job'] == assigned[0]['id']]
        valid = (len(bound) == 1 and bound[0]['option'] == requirement['option']
                 and bound[0]['resolved_by'] == requirement.get('resolved_by', 'coordinator'))
        checks.append({'id': 'decision-answered-' + label, 'passed': valid})
    checks.append({'id': 'decision-answer-count', 'passed': len(answers) == len(expected)})
    return checks


def question_checks(capture, case):
    """Fully specified orchestration tasks require no additional user input."""
    db = ledger.read(capture, case['session_id'])
    calls = ledger.assistant_calls(db, case['session_id'])
    asked = sum(c['name'] == 'ask_user' for m in calls for c in m['calls'])
    return [{'id': 'no-unnecessary-question', 'passed': asked == 0}]


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
