"""Grade the smoke's manual trajectories with the production graders once the store is closed."""
import copy
import json
import pathlib
import uuid
import report
import smoke_capabilities
import smoke_workers
from smoke_workers import invoke, pending
from ledger import scripted_execution_errors
from artifacts import retain_project

CASES = {'closeout-blocked'}


def plan(contract, controls, negative=None):
    receipts = copy.deepcopy(contract['receipts'])
    steps = copy.deepcopy(controls['steps'])
    failed = []
    if negative:
        if 'steps' in negative:
            steps = copy.deepcopy(negative['steps'])
        if 'receipt' in negative:
            index = negative['receipt']
            receipt = receipts[index]
            receipt['verdict'].update(negative.get('values', {}))
            receipt['phase'] = negative.get('phase', receipt['phase'])
            failed.append('workflow-receipt-'+contract['receipts'][index]['id'])
        if 'transition' in negative:
            steps[negative['transition']] = {'transition': negative['choice']}
        failed.extend(negative.get('must_fail', []))
        if not failed:
            raise ValueError('workflow negative control requires a named failed check')
    for step in steps:
        if type(step) is int:
            if not 0 <= step < len(receipts):
                raise ValueError('workflow control names an absent receipt')
        elif not isinstance(step, dict) or set(step) != {'transition'} or not isinstance(step['transition'], str):
            raise ValueError('workflow control requires a receipt index or named transition')
    return receipts, steps, failed


def record(application, session, receipt):
    citations = []
    for required in receipt['citations']:
        invoke(application, session, 'read', {'path': required['path']})
        citations.append({'line': 1, **required})
    return invoke(application, session, 'submit_verdict', {'verdict': receipt['verdict'], 'cited_evidence': citations})


def trajectory(application, spec, operation, controls, negative=None, alternative=None):
    session, project = application.session(spec)
    admitted = application.request(f'/v1/sessions/{session}/workflow-runs',
                                   {'operation_id':str(uuid.uuid4()),'workflow_id':spec['workflow_id'],'workflow_version':spec['workflow_version'],'request':spec['prompt']})
    receipts, steps, failed = plan(operation['workflow'], controls['workflow'], negative)
    if alternative:
        steps = alternative['steps']
    invoke(application, session, 'read', {'path':'README.md'})
    for step in steps:
        if type(step) is int:
            record(application, session, receipts[step])
        else:
            invoke(application, session, 'workflow_transition', {'transition_id':step['transition']})
    def settled():
        observation = application.request(f'/harness/workflow-execution/{session}/{admitted["id"]}')
        if observation['run']['id'] != admitted['id'] or observation['execution']['session_id'] != session:
            raise RuntimeError('workflow execution does not match its admission')
        if observation['execution']['settled']:
            return True
        request = pending(application, session)
        if request:
            application.request('/harness/llm/respond', {'id':request['id'],'content':'Release records are complete.'})
        return False
    application.wait_for(settled, 'workflow completion',session)
    application.flows.append({'id':spec['id'],'kind':'shortcut' if negative else 'intended',
        'control':negative['id'] if negative else alternative['id'] if alternative else 'intended',
        'session_id':session,'workflow_run_id':admitted['id'],'project_dir':str(project),
        'must_fail':failed, 'exact_failures':True, 'must_pass':['preserved-'+name for name in controls['positive']]})


def verify_workflow(application, spec, operation):
    controls = json.loads(pathlib.Path(__file__).with_name('fixture-controls.json').read_text())[spec['id']]
    trajectory(application, spec, operation, controls)
    for alternative in controls['workflow']['alternatives']:
        trajectory(application, spec, operation, controls, alternative=alternative)
    for negative in controls['workflow']['negative']:
        trajectory(application, spec, operation, controls, negative)
    return {'id':spec['id'],'workflow_id':spec['workflow_id']}


def verify_closeout(application, spec, operation):
    session, project, prepared = application.prepare_workers(spec)
    application.request(f'/v1/sessions/{session}/prompts', {'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
    application.wait_for(lambda:application.request('/harness/preparation/'+session,missing=True),'closeout entry',session)
    smoke_workers.invoke(application,session,'read',{'path':'report.py'})
    smoke_workers.invoke(application,session,'update_progress',{'content':'- [ ] Change the default limit\n- [ ] Validate the change'})
    smoke_workers.invoke(application,session,'edit',{'path':'report.py','old_string':'def select(records, limit=8):','new_string':'def select(records, limit=3):'})
    smoke_workers.invoke(application,session,'command',{'command':'python3 -B -m unittest discover','verification':True})
    result = smoke_capabilities.invoke(application,session,spec,'verify',{
        'command':'python3 -B check.py','capability_request':{'direct_ip':{'declared_destinations':['udp://127.0.0.1:9']}}},
        expected_outcome='rejected')
    if 'SANDBOX_DIRECT_IP_DENIED' not in (result.get('codes') or []):
        raise RuntimeError('verification refusal lost its direct-IP boundary identity')
    smoke_workers.reconcile_progress(application,session,operation['progress'])
    evidence = {'path':'report.py','line':1,'excerpt':'def select(records, limit=3):'}
    declaration = {**operation,'cited_findings':[evidence]}
    smoke_workers.finish(application,session,smoke_workers.closeout(declaration,'The default limit is updated.',[evidence]))
    application.flows.append({'id':spec['id'],'kind':'intended','session_id':session,'project_dir':str(project),'prepared':prepared,'entry':application.request('/harness/preparation/'+session)})
    return {'id':spec['id'],'verification_closeout':operation['closeout_method']}


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
