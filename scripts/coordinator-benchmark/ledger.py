"""Benchmark projections over typed application evidence."""
import json
import pathlib
from episode_evidence import read, member

OUTCOMES = pathlib.Path('worker-scripts') / 'outcomes'


def session_ids(facts):
    return [s['id'] for s in facts['sessions']]


def jobs(facts, session_id):
    return [{**job, 'merge_status':job.get('merge_status',''), 'error':job.get('error',''),
             'mode':job['scope']['mode'], 'paths':sorted(job['scope'].get('paths') or [])}
            for job in facts['workers'] if job.get('parent_session_id') == session_id]


def scripted_outcome(capture, job_id):
    """Read the latest private receipt for a scripted worker."""
    path = capture / OUTCOMES / (job_id + '.json')
    history = json.loads(path.read_text()) if path.is_file() else []
    return history[-1] if history else None


def scripted_execution_errors(capture, case, spec):
    if (spec.get('setup') or {}).get('policy') != 'scripted':
        return []
    seeds = {entry['job_id'] for entry in (case.get('prepared_overlays') or {}).get('overlays', [])}
    executed = jobs(read(capture, case['session_id']), case['session_id'])
    errors = []
    for job in executed:
        if job['id'] in seeds:
            continue
        path = capture / OUTCOMES / (job['id'] + '.json')
        history = json.loads(path.read_text()) if path.is_file() else []
        if (not history and job['status'] != 'cancelled') or any(event.get('kind') == 'error' for event in history):
            errors.append(job['id'])
    return errors


def assistant_calls(facts, session_id):
    return [{'id':m['id'], 'ord':m['ord'], 'calls':m['tool_calls']}
            for m in member(facts,session_id)['messages']
            if m['role']=='assistant' and m['origin']=='model' and m.get('tool_calls')]


def tool_results(facts, session_ids):
    results=[]
    for session in facts['sessions']:
        if session['id'] not in session_ids: continue
        for message in session['messages']:
            result=message.get('tool_result')
            if message['role']!='tool' or not result: continue
            invocation=result.get('invocation') or {}
            results.append({'session_id':session['id'],'ord':message['ord'],
                            'tool':invocation.get('tool') or result.get('tool'),
                            'tool_call_id':result.get('tool_call_id') or invocation.get('tool_call_id'),
                            'args':result.get('tool_args') or {},'outcome':result.get('outcome'),
                            'codes':result.get('codes') or [],'content':result.get('content') or '',
                            'invoked':invocation.get('invoked')})
    return results


def checkpoints(facts, session_ids):
    return [{'id':card['checkpoint_id'],'session_id':session['id'],'status':card['status'],
             'created_at':card['issued_at'],'tool_call_id':card['tool_approval']['tool_call_id'],
             'subject':card['tool_approval']['plan']['subject']['kind']}
            for session in facts['sessions'] if session['id'] in session_ids
            for card in session['checkpoints'] if card['kind']=='tool_approval']


def invocations(facts, session_id):
    return member(facts,session_id)['invocations'] or []


def file_writes(facts, session_id, path):
    return [effect for effect in facts['effects'] if effect['session_id']==session_id
            and effect['path']==path and effect['op'] in {'create','write','rename'}]
