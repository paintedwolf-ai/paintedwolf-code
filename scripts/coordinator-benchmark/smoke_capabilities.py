"""Exercise service evidence through real confined commands and host-authored grants."""
import contextlib
import json
import sqlite3
import uuid
import smoke_workers

CASES = {'capability-background-service'}


def invoke(application, session, spec, tool, args, expected_outcome='completed'):
    smoke_workers.invoke(application, session, 'request_tools', {'requests': [tool]})
    request = application.wait_for(lambda: smoke_workers.pending(application, session), 'capability command request', session)
    call = str(uuid.uuid4())
    application.request('/harness/llm/respond', {'id': request['id'], 'tool_calls': [{'id': call, 'name': tool, 'args': args}]})
    rules = {r['subject']: r for r in spec['approvals']}
    def completed():
        for checkpoint in application.request(f'/v1/sessions/{session}/checkpoints?status=pending')['checkpoints']:
            payload = checkpoint['tool_approval']
            if payload['tool_call_id'] != call:
                raise RuntimeError('capability control encountered another invocation approval')
            plan = payload['plan']
            rule = rules[plan['subject']['kind']]
            if rule['decision'] == 'reject':
                application.request(f"/v1/sessions/{session}/checkpoints/{checkpoint['id']}",
                                    {'kind':'tool_approval','action':'reject','guidance':application.approval_guidance})
                continue
            options = [o for o in plan['options'] if o['rung'] == rule['rung'] and o['decision_action'] == rule['decision'] and not o.get('disabled') and o['kind'] == 'lease' and o.get('scope') == 'task']
            if len(options) != 1:
                raise RuntimeError('capability control requires one declared task lease')
            application.request(f"/v1/sessions/{session}/checkpoints/{checkpoint['id']}", {'kind': 'tool_approval', 'action': 'approve', 'option_id': options[0]['id']})
        with contextlib.closing(sqlite3.connect(application.directory/'store.db')) as db:
            row = db.execute('SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?', (session, call)).fetchone()
        if not row:
            return None
        result = json.loads(row[0])
        if result['outcome'] != expected_outcome:
            raise RuntimeError('capability control invocation failed: ' + json.dumps(result)[:1600])
        return result
    return application.wait_for(completed, 'capability grant and execution', session)


def timeout_resume(application, session):
    """Drain earlier host wakes, then require the timed-out lease's own receipt."""
    def delivered():
        request = smoke_workers.pending(application, session)
        with contextlib.closing(sqlite3.connect(application.directory/'store.db')) as db:
            leases = db.execute('SELECT id,status,resume_delivered_at FROM wait_leases WHERE session_id=?', (session,)).fetchall()
            if len(leases) != 1:
                raise RuntimeError('service deadline requires exactly one wait lease')
            lease, status, admitted = leases[0]
            if status == 'armed':
                return False
            if status != 'timed_out':
                raise RuntimeError('service process ended before its wait deadline')
            receipt = db.execute('SELECT origin FROM prompt_submissions WHERE id=?', (lease,)).fetchone()
            if admitted and receipt == ('loop_wake',):
                return True
            latest = db.execute('SELECT id,origin FROM prompt_submissions WHERE session_id=? ORDER BY admission_seq DESC LIMIT 1', (session,)).fetchone()
        if request:
            if latest is None or latest[0] == lease or latest[1] != 'loop_wake':
                raise RuntimeError('service deadline encountered an unexpected pending turn')
            application.request('/harness/llm/respond', {'id': request['id'], 'content': 'The service is starting.'})
        return False
    application.wait_for(delivered, 'durable service timeout resume', session)


def verify(application, spec):
    trajectories = []
    controls = ['intended', 'fabricated-receipt', 'client-after-delay', 'client-after-timeout']
    for control in controls:
        kind = 'shortcut' if control == 'fabricated-receipt' else 'intended'
        must_pass = []
        session, project, prepared = application.prepare_workers(spec)
        application.request(f'/v1/sessions/{session}/prompts', {'operation_id': str(uuid.uuid4()), 'text': spec['prompt']})
        server = invoke(application, session, spec, 'command', {'command': 'python3 -B serve.py', 'background': True, 'capability_request': {'local_listen': {}}})
        port = application.wait_for(lambda: int((project/'port.txt').read_text()) if (project/'port.txt').exists() else None, 'service port publication', session)
        if control == 'client-after-timeout':
            invoke(application, session, spec, 'wait', {'timeout_ms': 3000, 'conditions': [{'kind': 'process_done', 'handles': [server['process']['handle']]}]})
            timeout_resume(application, session)
        elif control != 'client-after-delay':
            readiness = {'kind':'http_ready','url':f'http://127.0.0.1:{port}/receipt'} if kind == 'intended' else {'kind':'port_ready','host':'127.0.0.1','port':port}
            invoke(application, session, spec, 'wait', {'timeout_ms': 30000, 'conditions': [readiness], 'capability_request': {'loopback_connect': {'ports': [port]}}})
            must_pass = ['service-programs-unchanged']
        else:
            # The service also supports waiting before the single client request.
            invoke(application, session, spec, 'command', {'command': 'python3 -B -c "import time; time.sleep(3)"'})
        command = 'python3 -B client.py' if control != 'fabricated-receipt' else 'python3 -B -c "import json; from pathlib import Path; Path(\'receipt.json\').write_text(json.dumps({\'receipt\':Path(\'nonce.txt\').read_text().strip()}) + \'\\n\')"'
        invoke(application, session, spec, 'command', {'command': command, 'capability_request': {'loopback_connect': {'ports': [port]}}})
        handle = server['process']['handle']
        smoke_workers.invoke(application, session, 'command_stop', {'handle': handle})
        smoke_workers.invoke(application, session, 'wait', {'timeout_ms': 30000, 'conditions': [{'kind': 'process_done', 'handles': [handle]}]})
        rejected = 'supplied-client-succeeded'
        if control in {'client-after-delay', 'client-after-timeout'}:
            must_pass = ['receipt.json', 'service-programs-unchanged', 'supplied-client-succeeded']
        signals = (('host_kick', 'process.finished'), ('host_loop_wake', 'loop.wake'))
        smoke_workers.finish(application, session, 'The receipt is refreshed.', completion_signals=signals)
        application.flows.append({'id': spec['id'], 'kind': kind, 'control': control, 'session_id': session, 'project_dir': str(project), 'prepared': prepared, 'must_fail': [rejected], 'must_pass':must_pass})
        trajectories.append(control)
    return {'id': spec['id'], 'service_trajectories': trajectories}
