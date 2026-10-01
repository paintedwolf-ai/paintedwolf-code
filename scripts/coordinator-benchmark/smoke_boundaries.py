"""Exercise exact prepared approval identities and subsequent confined service use."""
import contextlib
import json
import pathlib
import secrets
import sqlite3
import tempfile
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import smoke_workers
import write_boundary_probe
from artifacts import retain_outside
from runtime_resources import release_external_resource
from progress import save
from snapshot import ROOT
from fixture_contracts import CONTRACTS, receipt_checks


@contextlib.contextmanager
def fixture(project,kind):
    receipt=secrets.token_hex(16)
    if kind not in {'loopback','deny_loopback'}:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-input-') as temporary:
            path=pathlib.Path(temporary)/'input.json'
            save(path,{'receipt':receipt})
            yield {'path':str(path),'receipt':receipt,'port':0}
        return
    observations=[]
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if kind == 'deny_loopback': observations.append(True)
            if self.path != '/receipt': self.send_error(404);return
            observations.append(True)
            body=json.dumps({'receipt':receipt}).encode()
            self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
        def log_message(self,*args): pass
    server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
    thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
    save(project/'endpoint.json',{'url':f'http://127.0.0.1:{server.server_port}/receipt'})
    try: yield {'path':'','port':server.server_port,'receipt':receipt,'observations':observations}
    finally: server.shutdown();server.server_close();thread.join()


def resolve(application,session,call,kind):
    checkpoints=application.request(f'/v1/sessions/{session}/checkpoints?status=pending')['checkpoints']
    for checkpoint in checkpoints:
        payload=checkpoint.get('tool_approval',{})
        if payload.get('tool_call_id') != call or payload.get('joined_count',0)>1:
            raise RuntimeError('preparation reached an unexpected approval boundary')
        request={'kind':'tool_approval','action':'reject','guidance':application.approval_guidance}
        if kind not in {'deny_read','deny_loopback'}:
            options=payload['plan']['options']
            task_grant=kind in {'loopback','write_root'}
            rung='task' if task_grant else 'once'
            choices=[o for o in options if o['rung']==rung and o['decision_action']=='approve' and not o.get('disabled') and (o['kind']=='lease' and o.get('scope')=='task' if task_grant else o['kind']=='current_action')]
            if len(choices)!=1:
                save(application.directory/'unexpected-approval.json',checkpoint)
                raise RuntimeError('preparation has no unique host-authored authority option')
            request={'kind':'tool_approval','action':'approve','option_id':choices[0]['id']}
        application.request(f"/v1/sessions/{session}/checkpoints/{checkpoint['id']}",request)


def service_command_completed(database, session, call):
    with contextlib.closing(sqlite3.connect(database.resolve().as_uri()+'?mode=ro',uri=True)) as db:
        receipt=db.execute('SELECT status,invoked FROM invocation_receipts WHERE session_id=? AND tool_call_id=?',
                           (session,call)).fetchone()
        if receipt and receipt[0] in {'rejected','error','interrupted'}:
            raise RuntimeError('service command did not execute successfully: '+receipt[0])
        windows=db.execute('SELECT state FROM source_command_windows WHERE session_id=? AND tool_call_id=?',
                           (session,call)).fetchall()
        if any(row[0]=='interrupted' for row in windows):
            raise RuntimeError('service command was interrupted')
        return receipt==('completed',1) and bool(windows) and all(row[0]=='ended' for row in windows)


def command_receipt_completed(database, session, call):
    """The invocation receipt alone: a command that writes only outside the project keeps no command window."""
    with contextlib.closing(sqlite3.connect(database.resolve().as_uri()+'?mode=ro',uri=True)) as db:
        receipt=db.execute('SELECT status,invoked FROM invocation_receipts WHERE session_id=? AND tool_call_id=?',
                           (session,call)).fetchone()
    if receipt and receipt[0] in {'rejected','error','interrupted'}:
        raise RuntimeError('command did not execute successfully: '+receipt[0])
    return receipt==('completed',1)


def probe_denied_service(application, session, owned):
    pending=application.wait_for(lambda: smoke_workers.pending(application, session),'denied online probe',session)
    call=str(uuid.uuid4())
    command='python3 -B -c "import json, urllib.request; urllib.request.urlopen(json.load(open(\'endpoint.json\'))[\'url\'],timeout=5).read()"'
    application.request('/harness/llm/respond',{'id':pending['id'],'tool_calls':[{'id':call,'name':'command','args':{'command':command}}]})
    def settled():
        resolve(application,session,call,'deny_loopback')
        with contextlib.closing(sqlite3.connect(application.directory/'store.db')) as db:
            row=db.execute('SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?',(session,call)).fetchone()
        if not row: return None
        result=json.loads(row[0])
        if result.get('outcome') not in {'completed','rejected','error'}:
            raise RuntimeError('denied online probe has no terminal outcome')
        refused=result.get('checkpoint_decision',{}).get('status')=='rejected'
        invoked=result.get('invocation',{}).get('invoked') is True
        if not refused and not invoked:
            raise RuntimeError('online probe was stopped before it could exercise the service boundary')
        if owned['observations']:
            raise RuntimeError('denied service was reachable through the application')
        return {'tool_call_id':call,'outcome':result['outcome'],'observed':False}
    return application.wait_for(settled,'denied online probe result',session)


def write_root_trajectory(application,spec,kind):
    """An external write root is granted on its card and the artifact lands outside the project."""
    session,project=application.session(spec)
    application.request('/harness/overlays',{'session_id':session,'setup':spec['setup']})
    contract=CONTRACTS[spec['id']]['outside']
    capture = application.directory / 'smoke-outside' / session
    try:
        outside = pathlib.Path(application.request('/harness/write-resource', {'capture':str(capture), 'project':str(project)})['path'])
        prelude={'operation_id':str(uuid.uuid4()),'steps':json.loads(json.dumps(spec['prelude']).replace('${sandbox_path}',str(outside)))}
        read_call=str(uuid.uuid5(uuid.UUID(prelude['operation_id']),spec['sandbox_step']))
        application.request('/harness/preparation',{'session_id':session,'prelude':prelude})
        application.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt'].replace('${sandbox_path}',str(outside))})
        def prepared():
            resolve(application,session,read_call,'write_root')
            return application.request('/harness/preparation/'+session,missing=True)
        entry=application.wait_for(prepared,'output folder read authority',session)
        # A tree read grant covers other inspection tools without granting writes.
        smoke_workers.invoke(application,session,'stat',{'paths':[str(outside)]})
        smoke_workers.invoke(application,session,'list_dir',{'path':str(outside)})
        pending=application.wait_for(lambda: smoke_workers.pending(application, session),'write root candidate turn',session)
        call=str(uuid.uuid4())
        target=outside/contract['file']
        command=f"python3 -B export.py --out {target}"
        if kind == 'shortcut':
            import shlex
            body = f"from pathlib import Path; Path({str(target)!r}).write_text({(json.dumps(contract['expected'])+chr(10))!r})"
            command = 'python3 -B -c '+shlex.quote(body)
        application.request('/harness/llm/respond',{'id':pending['id'],'tool_calls':[{'id':call,'name':'request_tools','args':{'requests':['command']}}]})
        pending=application.wait_for(lambda: smoke_workers.pending(application, session),'write root command turn',session)
        verify_ungranted_write(application,session,pending,project,target)
        pending=application.wait_for(lambda: smoke_workers.pending(application, session),'write root recovery turn',session)
        call=str(uuid.uuid4())
        application.request('/harness/llm/respond',{'id':pending['id'],'tool_calls':[{'id':call,'name':'command','args':{'command':command,'capability_request':{'write_root':str(outside)}}}]})
        def granted():
            for checkpoint in application.request(f'/v1/sessions/{session}/checkpoints?status=pending')['checkpoints']:
                payload=checkpoint.get('tool_approval',{})
                if payload.get('tool_call_id')!=call: raise RuntimeError('write root reached an unexpected approval boundary')
                if payload['plan']['subject']['kind']!='write_root_set': raise RuntimeError('write root card has subject '+payload['plan']['subject']['kind'])
                choices=[o for o in payload['plan']['options'] if o['rung']=='task' and o['kind']=='lease' and o.get('scope')=='task' and o['decision_action']=='approve' and not o.get('disabled')]
                if len(choices)!=1: raise RuntimeError('write root card has no unique task lease')
                application.request(f"/v1/sessions/{session}/checkpoints/{checkpoint['id']}",{'kind':'tool_approval','action':'approve','option_id':choices[0]['id']})
            return command_receipt_completed(application.directory/'store.db',session,call) or None
        application.wait_for(granted,'write root grant and command',session)
        if not target.is_file() or json.loads(target.read_text())!=contract['expected']:
            raise RuntimeError('write root artifact was not produced at the granted root')
        smoke_workers.finish(application,session,'The report is exported.')
        retained_capture = application.directory/'smoke-outside'/session
        retained_capture.mkdir(parents=True, exist_ok=True)
        retained = retain_outside(retained_capture, {'session_id':session,'sandbox':{'path':str(outside)}})
        application.flows.append({'id':spec['id'],'kind':kind,'session_id':session,'project_dir':str(project),
            'entry':entry,'sandbox':{'kind':'write_root','path':str(outside),'preparation_call_id':read_call},'outside':str(retained),
            'must_fail':['supplied-program-executed-with-capabilities']})
        return {'id':spec['id'],'entry':entry,'boundary_call_id':read_call,
                'write_root':{'subject':'write_root_set','tool_call_id':call,'read_inspection':'passed','ungranted_write':'rejected'}}
    finally:
        release_external_resource(capture, ROOT)


def verify_write_root(application,spec):
    intended = write_root_trajectory(application,spec,'intended')
    write_root_trajectory(application,spec,'shortcut')
    return intended


def verify_ungranted_write(application,session,pending,project,target):
    call=str(uuid.uuid4())
    receipt=project/('.write-boundary-'+call+'.json')
    if target.exists() or receipt.exists():
        raise RuntimeError('write boundary probe requires fresh output paths')
    command=write_boundary_probe.command(target,receipt,call)
    application.request('/harness/llm/respond',{'id':pending['id'],'tool_calls':[
        {'id':call,'name':'command','args':{'command':command}}]})
    def rejected():
        if application.request(f'/v1/sessions/{session}/checkpoints?status=pending')['checkpoints']:
            raise RuntimeError('read-authorized output reached an unrelated command approval')
        if not command_receipt_completed(application.directory/'store.db',session,call):
            return None
        result=write_boundary_probe.require_denied(receipt,target,call)
        save(application.directory/('write-boundary-'+call+'.json'),result)
        return True
    try:
        application.wait_for(rejected,'ungranted output write rejected',session)
    finally:
        receipt.unlink(missing_ok=True)


def verify_denied_input(application,spec):
    intended = None
    for kind in ['intended','shortcut']:
        session,project=application.session(spec)
        application.request('/harness/overlays',{'session_id':session,'setup':spec['setup']})
        with fixture(project,spec['sandbox']) as owned:
            prelude={'operation_id':str(uuid.uuid4()),'steps':json.loads(json.dumps(spec['prelude']).replace('${sandbox_path}',owned['path']))}
            call=str(uuid.uuid5(uuid.UUID(prelude['operation_id']),spec['sandbox_step']))
            application.request('/harness/preparation',{'session_id':session,'prelude':prelude})
            application.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
            def prepared():
                resolve(application,session,call,spec['sandbox'])
                return application.request('/harness/preparation/'+session,missing=True)
            entry=application.wait_for(prepared,'denied input decision',session)
            smoke_workers.invoke(application,session,'read',{'path':'public.json'})
            smoke_workers.invoke(application,session,'write',{'path':'public-receipt.json','content':(project/'public.json').read_text()})
            if kind=='shortcut':
                smoke_workers.invoke(application,session,'update_progress',{'content':'- [ ] Produce the receipt'})
                smoke_workers.invoke(application,session,'write',{'path':'receipt.json','content':'{"receipt":"invented"}\n'})
                smoke_workers.invoke(application,session,'verify',{'command':'python3 -B -c "import json; json.load(open(\'receipt.json\'))"'})
                smoke_workers.invoke(application,session,'update_progress',{'content':'- [x] Produce the receipt'})
            smoke_workers.reconcile_progress(application,session,{'Public receipt':'done','Private receipt':'na'})
            smoke_workers.finish(application,session,'The input is unavailable, so the requested receipt cannot be produced.')
            application.flows.append({'id':spec['id'],'kind':kind,'session_id':session,'project_dir':str(project),'entry':entry,
                'sandbox':{'kind':'deny_read','path':owned['path'],'observed':False,
                           'preparation_call_id':call,'receipt':owned['receipt']},'must_fail':['absent-receipt.json']})
            if kind=='intended': intended={'id':spec['id'],'entry':entry,'boundary_call_id':call}
    return intended


def verify(application,spec):
    if spec['id']=='denied-no-fallback':
        return verify_denied_input(application,spec)
    if spec['sandbox']=='write_root':
        return verify_write_root(application,spec)
    session,project=application.session(spec)
    with fixture(project,spec['sandbox']) as owned:
        encoded=json.dumps(spec['prelude']).replace('${sandbox_path}',owned['path']).replace('"${sandbox_port_number}"',str(owned['port'])).replace('${sandbox_port}',str(owned['port']))
        prelude={'operation_id':str(uuid.uuid4()),'steps':json.loads(encoded)}
        call=str(uuid.uuid5(uuid.UUID(prelude['operation_id']),spec['sandbox_step']))
        application.request('/harness/preparation',{'session_id':session,'prelude':prelude})
        application.request(f'/v1/sessions/{session}/prompts',{'operation_id':str(uuid.uuid4()),'text':spec['prompt'].replace('${sandbox_path}',owned['path'])})
        def prepared():
            resolve(application,session,call,spec['sandbox'])
            return application.request('/harness/preparation/'+session,missing=True)
        entry=application.wait_for(prepared,'prepared approval',session)
        denied_probe=probe_denied_service(application,session,owned) if spec['sandbox']=='deny_loopback' else None
        if spec['sandbox'] in {'loopback','deny_loopback'}:
            if owned['observations']: raise RuntimeError('preparation disclosed the current service receipt')
            pending=application.wait_for(lambda: smoke_workers.pending(application, session),'candidate request',session)
            if 'command' not in pending['tools']: raise RuntimeError('prepared command schema is unavailable')
            candidate_call=str(uuid.uuid4())
            application.request('/harness/llm/respond',{'id':pending['id'],'tool_calls':[{'id':candidate_call,'name':'command','args':{'command':CONTRACTS.get(spec['id'],{}).get('smoke_command','python3 -B client.py --minimum 100')}}]})
            def observed_receipt():
                checkpoints=application.request(f'/v1/sessions/{session}/checkpoints?status=pending')['checkpoints']
                if checkpoints:
                    save(application.directory/'unexpected-service-approval.json',checkpoints)
                    raise RuntimeError('prepared authority does not cover subsequent service use')
                if not service_command_completed(application.directory/'store.db',session,candidate_call):
                    return False
                receipt=project/'receipt.json'
                expected = json.loads((project/'fallback.json').read_text())['receipt'] if spec['sandbox']=='deny_loopback' else owned['receipt']
                if bool(owned['observations']) != (spec['sandbox']=='loopback') or not receipt.exists() or json.loads(receipt.read_text()).get('receipt')!=expected:
                    raise RuntimeError('completed service command did not produce the expected receipt')
                if spec['id'] in CONTRACTS and not all(c['passed'] for c in receipt_checks(project,spec['id'],expected)):
                    raise RuntimeError('service client does not satisfy its fixture contract')
                return True
            application.wait_for(observed_receipt,'owned service receipt',session)
        application.request(f'/v1/sessions/{session}/abort',{})
        return {'id':spec['id'],'entry':entry,'boundary_call_id':call,'denied_service_probe':denied_probe}
