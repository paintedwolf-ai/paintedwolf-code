"""Exercise workflow records and transitions through manual coordinator completions."""
import json
import pathlib
import uuid
from smoke_workers import invoke, pending
from workflow_controls import plan


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


def verify(application, spec, operation):
    controls = json.loads(pathlib.Path(__file__).with_name('fixture-controls.json').read_text())[spec['id']]
    trajectory(application, spec, operation, controls)
    for alternative in controls['workflow']['alternatives']:
        trajectory(application, spec, operation, controls, alternative=alternative)
    for negative in controls['workflow']['negative']:
        trajectory(application, spec, operation, controls, negative)
    return {'id':spec['id'],'workflow_id':spec['workflow_id']}
