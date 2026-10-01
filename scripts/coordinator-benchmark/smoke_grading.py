"""Grade the smoke's manual trajectories with the production graders once the store is closed."""
import pathlib
import report
from ledger import scripted_execution_errors
from artifacts import retain_project


def retain_flows(capture, flows):
    """Keep each delivered tree so grader failures do not erase their evidence."""
    retained = []
    for flow in flows:
        destination = capture/'smoke-projects'/flow['session_id']
        destination.mkdir(parents=True,exist_ok=True)
        project = retain_project(destination, flow)
        retained.append({**flow,'source_project_dir':flow['project_dir'],'project_dir':str(project)})
    return retained


def grade_flow(capture, flow, spec):
    """The same ledger checks report.evaluate_case applies to a paid trial."""
    case = {'id': spec['id'], 'session_id': flow['session_id'], 'project_dir': flow['project_dir'],
            'workflow_run_id':flow.get('workflow_run_id'), 'prepared_overlays': flow.get('prepared'), 'automatic_responses': flow.get('automatic_responses') or [], 'sandbox': flow.get('sandbox')}
    if flow.get('entry'):
        case['preparation'] = flow['entry']
    from episode_evidence import export
    facts=export(capture,case,pathlib.Path(__file__).resolve().parents[2])
    execution=facts['execution'] or {}
    message=execution.get('closeout') or {}
    final=message.get('content','') if message.get('visible') and not message.get('has_tool_calls') else ''
    complete=execution.get('status')=='complete'
    case.update(status='review_required' if complete and (final or spec.get('workflow_id')) else 'failed', final=final)
    if scripted_execution_errors(capture, case, spec):
        raise RuntimeError(f"{flow['id']}: scripted worker execution could not be verified")
    checks = report.outcome_checks(capture, case, spec, pathlib.Path(__file__).resolve().parents[2], outside=pathlib.Path(flow['outside']) if flow.get('outside') else None)
    return {c['id']: c['passed'] for c in checks}


def verify_flows(capture, flows, suite):
    """Intended trajectories pass every check; shortcuts fail the checks they bypass."""
    specs = {c['id']: c for c in suite['cases']}
    verdicts = []
    for flow in flows:
        spec = specs[flow['id']]
        checks = grade_flow(capture, flow, spec)
        failed = sorted(name for name, passed in checks.items() if not passed)
        missing = [name for name in flow.get('must_pass',[]) if not checks.get(name)]
        if missing:
            raise RuntimeError(f"{flow['id']}: the control omitted required successful checks {missing}")
        if flow['kind'] == 'intended' and failed:
            raise RuntimeError(f"{flow['id']}: the intended trajectory failed {failed}")
        if flow['kind'] == 'shortcut':
            expected = set(flow['must_fail'])
            if flow.get('exact_failures') and expected != set(failed):
                raise RuntimeError(f"{flow['id']}: the control failed {failed}, expected exactly {sorted(expected)}")
            if not expected <= set(failed):
                raise RuntimeError(f"{flow['id']}: the shortcut passed {sorted(expected - set(failed))}; graders accept the bypass")
        verdicts.append({'id': flow['id'], 'kind': flow['kind'], 'control': flow.get('control', flow['kind']),
                         'worker': flow.get('worker'),
                         'failed': failed, 'checks': len(checks)})
    return verdicts
