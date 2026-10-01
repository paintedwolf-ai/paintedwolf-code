"""Drive verification closeout through real accepted and rejected verification calls."""
import uuid
import smoke_capabilities
import smoke_workers

CASES = {'closeout-blocked'}


def verify(application, spec, operation):
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
