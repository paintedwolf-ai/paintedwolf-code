"""Exercise worker flows and rejected shortcuts through coordinator tools."""
import contextlib
import json
import pathlib
import re
import shlex
import sqlite3
import uuid
from orchestration_evidence import waves


def pending(application, session):
    """Require pending model requests to belong to the active flow."""
    value = application.request('/harness/llm/pending')
    if not value.get('pending'):
        return None
    if value.get('session_id') != session:
        raise RuntimeError('a model request is waiting for another session: ' + json.dumps({k: value.get(k) for k in ('id', 'session_id')}))
    return value


def respond(application, session, calls):
    """Answer the next model request with one assistant message carrying every call."""
    request = application.wait_for(lambda: pending(application, session), 'coordinator tool request', session)
    tools = [c['name'] for c in calls]
    missing = sorted({name for name in tools if name not in request['tools']})
    if missing:
        if 'request_tools' not in request['tools']:
            raise RuntimeError('coordinator surface does not offer: ' + ','.join(missing))
        load = str(uuid.uuid4())
        application.request('/harness/llm/respond', {'id': request['id'], 'tool_calls': [{'id': load, 'name': 'request_tools', 'args': {'need': ', '.join(missing)}}]})
        terminal(application, session, load)
        request = application.wait_for(lambda: pending(application, session), 'loaded coordinator tools', session)
        if any(name not in request['tools'] for name in tools):
            raise RuntimeError('coordinator tools were not loaded: ' + ','.join(missing))
    ids = [str(uuid.uuid4()) for _ in calls]
    resolved_calls = [
        {**c, 'args': progress_args(application, session, c.get('args') or {}, request=request)}
        if c.get('name') == 'update_progress' else c
        for c in calls
    ]
    application.request('/harness/llm/respond', {'id': request['id'], 'tool_calls': [{'id': i, **c} for i, c in zip(ids, resolved_calls)]})
    return [terminal(application, session, i) for i in ids]


def _checklist_labels(content):
    labels = []
    for line in (content or '').splitlines():
        match = re.match(r'^\s*-\s*\[[^\]]\]\s*(.+?)\s*$', line)
        if match:
            labels.append(match.group(1))
    return labels


def _request_texts(request):
    if not isinstance(request, dict):
        return []
    texts = []
    for key in ('system', 'system_prompt', 'prompt'):
        val = request.get(key)
        if isinstance(val, str):
            texts.append(val)
    for msg in request.get('messages') or ():
        if not isinstance(msg, dict):
            continue
        content = msg.get('content')
        if isinstance(content, str):
            texts.append(content)
        elif isinstance(content, list):
            for part in content:
                if isinstance(part, dict) and isinstance(part.get('text'), str):
                    texts.append(part['text'])
    return texts


def _progress_snapshot_from_request(request):
    decoder = json.JSONDecoder()
    for text in reversed(_request_texts(request)):
        for marker in ('"scope_session_id"', '"initialization_required"', '"revision"'):
            pos = text.rfind(marker)
            while pos != -1:
                start = text.rfind('{', 0, pos)
                while start != -1:
                    try:
                        obj, _ = decoder.raw_decode(text, start)
                    except Exception:
                        start = text.rfind('{', 0, start)
                        continue
                    if isinstance(obj, dict) and obj.get('revision') and ('content' in obj or 'item_ids' in obj):
                        if obj.get('initialization_required') or not (obj.get('content') or '').strip():
                            return None
                        return obj
                    break
                pos = text.rfind(marker, 0, pos)
    return None


def _latest_progress_snapshot(application, session):
    directory = getattr(application, 'directory', None)
    if not isinstance(directory, (str, pathlib.Path)):
        return None
    store = pathlib.Path(directory) / 'store.db'
    if not store.is_file():
        return None
    with contextlib.closing(sqlite3.connect(store)) as db:
        rows = db.execute(
            'SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_json IS NOT NULL ORDER BY rowid DESC',
            (session,),
        ).fetchall()
    for (raw,) in rows:
        try:
            result = json.loads(raw)
        except Exception:
            continue
        if result.get('outcome') != 'completed':
            continue
        if ((result.get('invocation') or {}).get('tool') or '') != 'update_progress':
            continue
        try:
            payload = json.loads(result.get('content') or '{}')
        except Exception:
            continue
        snapshot = payload.get('snapshot') if isinstance(payload, dict) else None
        if isinstance(snapshot, dict) and snapshot.get('revision'):
            return snapshot
    return None


def _reconciliation_token_from_request(request):
    patterns = (
        re.compile(r'"reconciliation"\s*:\s*\{[^{}]*"token"\s*:\s*"([0-9a-f]{64})"'),
        re.compile(r'"reconciliation_token"\s*:\s*"([0-9a-f]{64})"'),
    )
    for text in reversed(_request_texts(request)):
        for pattern in patterns:
            match = pattern.search(text)
            if match:
                return match.group(1)
    return ''


def progress_args(application, session, args, request=None):
    updated = dict(args)
    live_snapshot = _progress_snapshot_from_request(request)
    if 'expected_revision' not in updated:
        snapshot = live_snapshot or _latest_progress_snapshot(application, session)
        if snapshot:
            updated['expected_revision'] = snapshot['revision']
            if 'item_ids' not in updated:
                prev_labels = _checklist_labels(snapshot.get('content', ''))
                prev_ids = list(snapshot.get('item_ids') or ())
                by_label = {}
                for label, item_id in zip(prev_labels, prev_ids):
                    by_label.setdefault(label, []).append(item_id)
                new_labels = _checklist_labels(updated.get('content', ''))
                aligned = []
                for idx, label in enumerate(new_labels):
                    bucket = by_label.get(label)
                    if bucket:
                        aligned.append(bucket.pop(0))
                    elif idx < len(prev_ids) and len(new_labels) == len(prev_labels):
                        aligned.append(prev_ids[idx])
                    else:
                        aligned.append('')
                updated['item_ids'] = aligned
    if 'reconciliation_token' not in updated:
        rec = (live_snapshot or {}).get('reconciliation')
        token = rec.get('token', '') if isinstance(rec, dict) else ''
        if not token:
            token = _reconciliation_token_from_request(request)
        if token:
            updated['reconciliation_token'] = token
    return updated


def invoke(application, session, tool, args):
    return respond(application, session, [{'name': tool, 'args': args}])[0]


def finish(application, session, text, *, completion_signals=()):
    """End the turn with a plain user-facing answer, then require the session to go quiet."""
    request = application.wait_for(lambda: pending(application, session), 'coordinator closeout', session)
    application.request('/harness/llm/respond', {'id': request['id'], 'content': text})
    settle(application, session, text=text, completion_signals=completion_signals)


def closeout(operation, text, findings=()):
    """Write the closeout as Markdown, citing exact worker findings inline."""
    nodes = []
    if operation.get('cited_findings'):
        observed = {(f['path'], f['line']): f for f in findings}
        for f in operation['cited_findings']:
            excerpt = observed.get((f['path'], f['line']), {}).get('excerpt')
            quoted = (' ' + json.dumps(excerpt, ensure_ascii=False)) if excerpt else ''
            nodes.append('[' + citation_path(f['path']) + ':' + str(f['line']) + quoted + ']')
    if operation.get('closeout_method'):
        nodes.append('[verify: ' + operation['closeout_method'] + ' - Declared by the intended trajectory.]')
    elif operation.get('verification'):
        nodes.append('[verify: project - The supplied project checks passed.]')
    elif nodes:
        # Surveys cite inspection evidence without modifying files.
        nodes.append('[verify: inspection - The worker survey changed nothing; its finding is cited.]')
    return text + (' ' + ' '.join(nodes) if nodes else '')


def citation_path(path):
    """Quote a path the bracket grammar would otherwise split."""
    return json.dumps(path, ensure_ascii=False) if ' ' in path or ',' in path else path


def completion_wake(db, session, allowed):
    """Match host notifications delivered after closeout."""
    final = db.execute("SELECT max(ord) FROM messages WHERE session_id=? AND role='assistant' AND origin='model' AND kind='completion_report'", (session,)).fetchone()[0]
    if final is None:
        return False
    rows = db.execute('SELECT origin,kind,host_signal_id FROM messages WHERE session_id=? AND ord>?', (session, final)).fetchall()
    return bool(rows) and all(origin == 'host' and (kind, signal) in allowed for origin, kind, signal in rows)


def settle(application, session, *, text='', completion_signals=()):
    """Wait for durable closeout and handle declared completion wakes."""
    def complete():
        request = application.request('/harness/llm/pending')
        if request.get('pending') and request.get('session_id') == session:
            with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
                expected = completion_signals and completion_wake(db, session, completion_signals)
            if expected:
                application.request('/harness/llm/respond', {'id': request['id'], 'content': text})
                return False
            tail = [m for m in request['messages'] if m['role'] != 'assistant'][-2:]
            raise RuntimeError('the session was woken after its closeout: ' + json.dumps([{'role': m['role'], 'content': m['content'][:300]} for m in tail]))
        with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
            latest = db.execute("SELECT id FROM prompt_submissions WHERE session_id=? ORDER BY admission_seq DESC LIMIT 1", (session,)).fetchone()
            return latest is not None and application.submission_settled(session, latest[0])
    application.wait_for(complete, 'coordinator closeout settlement', session)


def terminal(application, session, call):
    def observe():
        with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
            row = db.execute('SELECT tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id=?', (session, call)).fetchone()
        if not row:
            return None
        result = json.loads(row[0])
        if result['outcome'] != 'completed':
            raise RuntimeError('scripted coordinator tool failed: ' + json.dumps(result)[:1200])
        return result
    return application.wait_for(observe, 'coordinator tool completion', session)


def job_row(application, job_id):
    with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
        db.row_factory = sqlite3.Row
        row = db.execute('SELECT id,child_session_id,status,merge_status,error FROM worker_jobs WHERE id=?', (job_id,)).fetchone()
    return dict(row) if row else None


def outcome(application, job_id):
    path = application.directory / 'worker-scripts' / 'outcomes' / (job_id + '.json')
    history = json.loads(path.read_text()) if path.is_file() else []
    return history[-1] if history else None


def settled(application, session, job_id, statuses):
    def observe():
        row = job_row(application, job_id)
        if not row or row['status'] in {'pending', 'running', 'waiting'}:
            return None
        if row['status'] not in statuses:
            raise RuntimeError('scripted worker settled unexpectedly: ' + json.dumps(row))
        return row
    return application.wait_for(observe, 'scripted worker settlement', session)



def dispatch_call(script):
    return {'name': 'task', 'args': {'agent_type': 'implementer' if script['mode'] == 'write' else 'repo-researcher',
            'brief': {'goal': script['label'], 'done_when': ['The declared scope is delivered.']},
            'scope': {'mode': script['mode'], 'paths': script['paths']}}}


def worker_name(result):
    content = str(result.get('content') or '')
    match = re.search(r'"worker"\s*:\s*"([^"]+)"', content)
    return match.group(1) if match else ''


def worker_name_for_job(application, job, fallback=''):
    if fallback:
        return fallback
    with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
        row = db.execute(
            'SELECT w.name FROM workers w JOIN worker_jobs j ON w.child_session_id = j.child_session_id WHERE j.id = ? LIMIT 1',
            (job,),
        ).fetchone()
    return row[0] if row and row[0] else job


def result_handle_for_job(application, job):
    with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
        row = db.execute(
            'SELECT id FROM worker_results WHERE worker_job_id = ? ORDER BY rowid DESC LIMIT 1',
            (job,),
        ).fetchone()
    return row[0] if row and row[0] else job


def job_id(result, application=None, seen=None, session=None):
    job = (result.get('dispatch') or {}).get('job_id')
    if not job and application is not None:
        with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
            if session:
                rows = [r[0] for r in db.execute('SELECT id FROM worker_jobs WHERE parent_session_id=? ORDER BY rowid ASC', (session,)).fetchall()]
            else:
                rows = [r[0] for r in db.execute('SELECT id FROM worker_jobs ORDER BY rowid ASC').fetchall()]
        used = seen if seen is not None else set()
        candidates = rows if seen is not None else list(reversed(rows))
        for jid in candidates:
            if jid not in used:
                job = jid
                break
    if not job:
        raise RuntimeError('task did not return a job id: ' + json.dumps(result)[:300])
    if seen is not None:
        seen.add(job)
    return job


def continue_worker(application, session, project, seed, script):
    """A partial return resumes on the same child and lands the script's completion."""
    stage = script['stages'][0]
    invoke(application, session, 'task', {'agent_type': 'implementer', 'child_session_id': seed['child_session_id'],
        'brief': {'goal': 'Finish the requested status labels.', 'done_when': ['The supplied tests pass.']},
        'scope': {'mode': 'write', 'paths': sorted(stage['files'])}})
    def completed():
        with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
            rows = db.execute('SELECT id,status,merge_status FROM worker_jobs WHERE child_session_id=? AND id<>?', (seed['child_session_id'], seed['job_id'])).fetchall()
        if len(rows) > 1:
            raise RuntimeError('continuation created multiple jobs')
        if not rows or rows[0][1] in {'pending', 'running'}:
            return None
        if rows[0][1:] != ('complete', 'pending'):
            raise RuntimeError('worker continuation did not return a complete overlay: ' + str(rows))
        return rows[0][0]
    job = application.wait_for(completed, 'deterministic worker continuation', session)
    invoke(application, session, 'wait', {'timeout_ms': 5000, 'conditions': [{'kind': 'next_worker_done'}]})
    # The partial return's own overlay is superseded by the continuation on the same child.
    invoke(application, session, 'reject_overlay', {'overlay_id': seed['job_id']})
    invoke(application, session, 'promote_overlay', {'overlay_id': job})
    for path, body in stage['files'].items():
        if (project / path).read_text() != body:
            raise RuntimeError('promoted continuation differs: ' + path)
    return job


def intended_dispatches(application, session, project, dispatches, existing=None, operation=None):
    """Dispatch and integrate the declared waves, reconciling existing progress rows."""
    states = {d['label']: ' ' for d in dispatches}
    responses = []
    def checklist():
        if existing:
            invoke(application, session, 'update_progress', {'content': existing})
            return
        invoke(application, session, 'update_progress', {'content': '## Progress\n' + ''.join(f"- [{states[d['label']]}] {d['label']}\n" for d in dispatches)})
    checklist()
    observed = []
    seen_jobs = set()
    for wave in waves(dispatches, (operation or {}).get('dispatch', {}).get('dependencies', {})):
        results = respond(application, session, [dispatch_call(d) for d in wave])
        jobs = {d['label']: job_id(r, application, seen_jobs, session=session) for d, r in zip(wave, results)}
        workers = {d['label']: worker_name(r) for d, r in zip(wave, results)}
        invoke(application, session, 'wait', {'timeout_ms': 30000, 'conditions': [{'kind': 'next_worker_done'}]})
        for script in wave:
            stage = script['stages'][0]
            job = jobs[script['label']]
            if stage['kind'] == 'failed':
                row = settled(application, session, job, {'failed'})
                receipt = outcome(application, job) or {}
                if receipt.get('code') != stage['code'] or 'HARNESS' in (row['error'] or '') or stage['code'] in (row['error'] or ''):
                    raise RuntimeError('failed stage did not retain its code without exposing it: ' + json.dumps([row, receipt]))
                states[script['label']] = '~'
                observed.append({'label': script['label'], 'job_id': job, 'outcome': 'failed'})
                continue
            if stage['kind'] == 'needs_decision':
                settled(application, session, job, {'held'})
                option = operation['decisions'][script['label']]['option']
                wname = worker_name_for_job(application, job, workers.get(script['label'], ''))
                invoke(application, session, 'answer_decision', {'worker': wname, 'answer': option})
                invoke(application, session, 'wait', {'timeout_ms': 30000, 'conditions': [{'kind': 'next_worker_done'}]})
                row = settled(application, session, job, {'complete'})
                expected = stage['outcomes'][option]['files']
            else:
                row = settled(application, session, job, {'complete'})
                expected = stage.get('files') or {}
            if script['mode'] == 'write':
                if row['merge_status'] != 'pending':
                    raise RuntimeError('write leg did not leave a pending overlay: ' + json.dumps(row))
                invoke(application, session, 'promote_overlay', {'overlay_id': result_handle_for_job(application, job)})
                for path, body in expected.items():
                    if (project / path).read_text() != body:
                        raise RuntimeError('promoted dispatch differs: ' + path)
            else:
                with contextlib.closing(sqlite3.connect(application.directory / 'store.db')) as db:
                    result = json.loads(db.execute('SELECT result_json FROM worker_jobs WHERE id=?', (job,)).fetchone()[0] or '{}')
                findings = (result.get('completion_report') or {}).get('findings') or []
                if [f['path'] for f in findings] != [f['path'] for f in stage['findings']]:
                    raise RuntimeError('read leg findings differ from the script: ' + json.dumps(findings))
            states[script['label']] = 'x'
            observed.append({'label': script['label'], 'job_id': job, 'child_session_id': row['child_session_id'], 'outcome': row['merge_status'] or row['status'],
                             'findings': findings if script['mode'] == 'read' else []})
        checklist()
    return observed, responses


def shortcut_dispatches(application, session, project, dispatches, operation):
    """Produce expected bytes without worker integration and issue an off-plan dispatch."""
    invoke(application, session, 'update_progress', {'content': '## Progress\n' + ''.join(f"- [ ] {d['label']}\n" for d in dispatches)})
    for script in dispatches:
        stage = script['stages'][0]
        if stage['kind'] == 'needs_decision':
            option = operation['decisions'][script['label']]['option']
            files = stage['outcomes'][option]['files']
        else:
            files = stage.get('files') or {}
        for path, body in files.items():
            quoted = shlex.quote(path)
            if (project / path).is_file():
                invoke(application, session, 'command', {'command': f'cat {quoted}'})
            parent = str(pathlib.PurePosixPath(path).parent)
            prefix = f'mkdir -p {shlex.quote(parent)} && ' if parent not in ('', '.') else ''
            invoke(application, session, 'command', {'command': f"{prefix}printf '%s' {shlex.quote(body)} > {quoted}"})
    off_plan = job_id(invoke(application, session, 'task', dispatch_call({'label': 'off-plan', 'mode': 'write', 'paths': ['unplanned-shortcut.py']})['args']), application, session=session)
    row = settled(application, session, off_plan, {'failed'})
    receipt = outcome(application, off_plan) or {}
    if receipt.get('code') != 'HARNESS_DISPATCH_UNPLANNED' or 'HARNESS' in (row['error'] or ''):
        raise RuntimeError('an off-plan dispatch must settle through the retained receipt: ' + json.dumps([row, receipt]))
    invoke(application, session, 'update_progress', {'content': '## Progress\n' + ''.join(f"- [x] {d['label']}\n" for d in dispatches)})
    return {'off_plan_job': off_plan}


def promote_prepared(application, session, seed, overlay, spec, operation):
    """Integrate a complete return using the fixture's independent positive control."""
    if (operation.get('overlay_outcomes') or {}).get(overlay['label']) == 'rejected':
        invoke(application, session, 'reject_overlay', {'overlay_id': seed['job_id']})
        return
    controls = json.loads(pathlib.Path(__file__).with_name('fixture-controls.json').read_text())
    delivered = (controls.get(spec['id']) or {}).get('positive') or overlay['files']
    changed_primary = spec['setup'].get('integration_files') or {}
    resolutions = [{'path': path, 'content': delivered[path]} for path in overlay['files']
                   if path in changed_primary and path in delivered]
    invoke(application, session, 'promote_overlay', {'overlay_id': seed['job_id'], 'resolutions': resolutions})
    row = job_row(application, seed['job_id'])
    if row['merge_status'] != 'merged':
        raise RuntimeError('prepared return did not finish promotion: ' + json.dumps(row))


def reconcile_progress(application, session, expected):
    states = {'done': 'x', 'pending': ' ', 'na': '~'}
    content = '## Progress\n' + ''.join(f"- [{states[state[0] if isinstance(state, list) else state]}] {label}\n" for label, state in expected.items())
    invoke(application, session, 'update_progress', {'content': content})


def unresolved_decision(application, spec, dispatch):
    """Leave a worker decision unanswered for the negative control."""
    session, project, prepared = application.prepare_workers(spec)
    application.request(f'/v1/sessions/{session}/prompts', {'operation_id':str(uuid.uuid4()), 'text':spec['prompt']})
    label = dispatch['label']
    invoke(application, session, 'update_progress', {'content':f'## Progress\n- [ ] {label}\n'})
    job = job_id(invoke(application, session, 'task', dispatch_call(dispatch)['args']), application, session=session)
    invoke(application, session, 'wait', {'timeout_ms':30000, 'conditions':[{'kind':'next_worker_done'}]})
    settled(application, session, job, {'held'})
    invoke(application, session, 'update_progress', {'content':f'## Progress\n- [~] {label}\n'})
    finish(application, session, 'The requested change is unfinished.')
    application.flows.append({'id':spec['id'], 'kind':'shortcut', 'control':'unanswered-worker-decision',
                             'worker':label,
                             'session_id':session, 'project_dir':str(project), 'prepared':prepared,
                             'must_fail':['planned-leg-settled-'+label,'decision-answered-'+label]})


def swapped_decisions(application, spec, operation):
    """Swap worker answers to exercise routing failures."""
    dispatches = [d for d in spec['setup']['dispatches'] if d['label'] in operation['decisions']]
    choices = [operation['decisions'][d['label']]['option'] for d in dispatches]
    if len(set(choices)) < 2:
        raise ValueError('decision routing requires distinct correct options')
    session, project, prepared = application.prepare_workers(spec)
    application.request(f'/v1/sessions/{session}/prompts', {'operation_id':str(uuid.uuid4()),'text':spec['prompt']})
    invoke(application,session,'update_progress',{'content':'## Progress\n'+''.join('- [ ] '+d['label']+'\n' for d in dispatches)})
    results = respond(application,session,[dispatch_call(d) for d in dispatches])
    seen_jobs = set()
    jobs = [job_id(r, application, seen_jobs, session=session) for r in results]
    for index, (result, job) in enumerate(zip(results, jobs)):
        settled(application,session,job,{'held'})
        choice = choices[(index+1) % len(choices)]
        wname = worker_name_for_job(application, job, worker_name(result))
        invoke(application,session,'answer_decision',{'worker':wname,'answer':choice})
    invoke(application,session,'wait',{'timeout_ms':30000,'conditions':[{'kind':'all_workers_idle'}]})
    for job in jobs:
        row = settled(application,session,job,{'complete','failed'})
        if row['merge_status'] == 'pending':
            invoke(application,session,'reject_overlay',{'overlay_id':result_handle_for_job(application, job)})
    invoke(application,session,'update_progress',{'content':'## Progress\n'+''.join('- [~] '+d['label']+'\n' for d in dispatches)})
    finish(application,session,'The requested reports are unfinished.')
    application.flows.append({'id':spec['id'],'kind':'shortcut','control':'swapped-worker-decisions',
                             'session_id':session,'project_dir':str(project),'prepared':prepared,
                             'must_pass':['decision-answer-count'],
                             'must_fail':['decision-answered-'+d['label'] for d in dispatches]})
