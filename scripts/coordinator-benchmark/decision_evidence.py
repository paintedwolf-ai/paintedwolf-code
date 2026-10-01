"""Worker decisions and coordinator questions from the closed ledger."""
import json
import ledger
import posixpath


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
