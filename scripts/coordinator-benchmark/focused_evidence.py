"""Validate prepared boundaries separately from the candidate's outcome."""
import json
import pathlib
import uuid
from focused_outcomes import required_fields, read_json, same
from episode_evidence import read, member
import ledger


def prepared_case(capture, case, spec):
    if not spec.get('prelude'):
        return case
    session = str(uuid.UUID(case['session_id']))
    receipt = capture / 'conversation-preparation' / (session + '.entry.json')
    if not receipt.exists():
        raise ValueError('durable candidate entry is missing')
    entry = json.loads(receipt.read_text())
    if case.get('preparation') and case['preparation'] != entry:
        raise ValueError('reported candidate entry differs from its durable receipt')
    return {**case, 'preparation': entry}


def preparation_checks(capture, case, spec):
    steps = spec.get('prelude', [])
    if not steps:
        return []
    entry = case.get('preparation')
    if not entry or entry['session_id'] != case['session_id'] or not entry.get('entry_at'):
        raise ValueError('candidate entry was not established by the preparation controller')
    operation = uuid.UUID(entry['operation_id'])
    ids = [str(uuid.uuid5(operation, step['id'])) for step in steps]
    if entry['tool_call_ids'] != ids:
        raise ValueError('preparation receipt differs from the declared steps')
    db=read(capture,case['session_id'])
    if steps:
        for step, call in zip(steps, ids):
            rows=[m['tool_result'] for m in member(db,case['session_id'])['messages']
                  if (m.get('tool_result') or {}).get('tool_call_id')==call]
            if len(rows)!=1:
                raise ValueError('preparation does not have one durable result for its invocation')
            result=rows[0]
            if result['outcome'] != step['outcome'] or result.get('process'):
                raise ValueError('preparation outcome differs from its declared boundary')
            if step.get('checkpoint_status') and result.get('checkpoint_decision',{}).get('status') != step['checkpoint_status']:
                raise ValueError('preparation checkpoint decision differs')
            if step.get('code') and step['code'] not in result.get('codes',[]):
                raise ValueError('preparation result code differs')
            if step.get('json') and not required_fields(json.loads(result['content']),step['json']):
                raise ValueError('preparation result envelope differs')
            if step['tool'] == 'verify':
                proof=next((r for r in ledger.invocations(db,case['session_id']) if r['tool_call_id']==call),None)
                if not proof or proof['invoked'] != 1 or proof['status'] != 'completed' or proof['source_verdict'] != 'passed' or not proof['source_revision']:
                    raise ValueError('prepared verification has no successful source-bound invocation')
    return []


def returned_worker_checks(capture, case, spec, outcomes=None):
    """Validate each declared worker disposition and settled job obligations."""
    expected = spec['setup'].get('overlays') or []
    prepared = (case.get('prepared_overlays') or {}).get('overlays') or []
    labels = [seed.get('label') for seed in expected]
    if any(not isinstance(label, str) or not label for label in labels) or len(set(labels)) != len(labels):
        raise ValueError('prepared workers require unique declared labels')
    if (len(prepared) != len(expected) or len({p['job_id'] for p in prepared}) != len(expected)
        or len({p['child_session_id'] for p in prepared}) != len(expected)):
        raise ValueError('worker fixture cardinality differs')
    if expected and len({p['baseline_sha256'] for p in prepared}) != 1:
        raise ValueError('workers did not branch from the same baseline')
    if any(not isinstance(p.get('label'), str) for p in prepared):
        raise ValueError('prepared worker labels must be strings')
    by_label = {p.get('label'): p for p in prepared}
    if set(by_label) != set(labels):
        raise ValueError('prepared worker labels differ from the fixture')
    if outcomes and (not set(outcomes) <= set(labels) or any(value not in {'merged', 'rejected'} for value in outcomes.values())):
        raise ValueError('worker dispositions must name declared overlays and supported outcomes')
    checks = []
    db=read(capture,case['session_id'])
    jobs={job['id']:job for job in ledger.jobs(db,case['session_id'])}
    for seed in expected:
        actual = by_label[seed['label']]
        job = jobs.get(actual['job_id'])
        verification = actual.get('verification')
        want = seed['verification_verdict']
        if not job or job['child_session_id'] != actual['child_session_id'] or not verification or verification['Verdict'] != want or not verification['CheckID']:
            raise ValueError('worker preparation lacks its required validation')
        if actual.get('result_status') != seed.get('result_status','complete'):
            raise ValueError('worker preparation has a different result status')
        snapshot=db['snapshots'].get(verification['SourceRevision'])
        if not snapshot or snapshot['roots_key']!=verification['SourceRootDigest'] or snapshot['quality']!='exact':
            raise ValueError('worker validation lacks an exact source snapshot')
        label = seed.get('label')
        if actual.get('result_status') == 'partial':
            resumed = any(candidate['id'] != actual['job_id']
                          and candidate['child_session_id'] == actual['child_session_id']
                          and candidate['status'] == 'complete' and candidate['merge_status'] == 'merged'
                          for candidate in jobs.values())
            checks.append({'id':'resumed-existing-worker-'+label,'passed':resumed})
        elif outcomes and label in outcomes:
            checks.append({'id':'overlay-'+outcomes[label]+'-'+label,'passed':job['merge_status'] == outcomes[label]})
        else:
            promoted = any(candidate['child_session_id'] == actual['child_session_id']
                           and candidate['status'] == 'complete' and candidate['merge_status'] == 'merged'
                           for candidate in jobs.values())
            checks.append({'id':'promoted-prepared-worker-'+label,'passed':promoted})
    checks.append({'id':'resolved-worker-obligations','passed':all(job['status'] in {'complete','failed','cancelled'} and job['merge_status'] in {None,'','merged','rejected','aborted'} for job in jobs.values())})
    return checks


def validate_permission(capture, case):
    evidence = case['sandbox']
    call = evidence.get('preparation_call_id')
    if not call:
        raise ValueError('permission fixture has no prepared action identity')
    wanted = 'rejected' if evidence['kind'] in {'deny_read','deny_loopback'} else 'approved'
    db=read(capture,case['session_id'])
    decisions=[card for card in ledger.checkpoints(db,[case['session_id']]) if card['tool_call_id']==call]
    if len(decisions)!=1 or decisions[0]['status']!=wanted:
        raise ValueError('prepared checkpoint did not receive the declared decision')


def sandbox_checks(capture, case):
    evidence = case.get('sandbox')
    if not evidence or evidence['kind'] not in {'approve_read', 'deny_read', 'loopback', 'deny_loopback'}:
        raise ValueError('permission or service evidence is missing')
    service = evidence['kind'] in {'loopback', 'deny_loopback'}
    if evidence['kind'] != 'loopback':
        validate_permission(capture, case)
    project = pathlib.Path(case['project_dir'])
    from fixture_contracts import CONTRACTS, receipt_checks
    if case['id'] in CONTRACTS:
        checks = receipt_checks(project, case['id'], evidence['receipt'])
    else:
        expected = {'receipt': evidence['receipt']}
        if service:
            expected['balances'] = [{'account':'amber','cents':100},{'account':'cedar','cents':100}]
        checks = [{'id': 'current-service-receipt' if service else 'decision-receipt',
                   'passed': same(read_json(project/'receipt.json'), expected)}]
    if service:
        checks.extend([
            {'id':'service-observation-boundary','passed':evidence['observed'] is (evidence['kind'] == 'loopback')},
            {'id':'preserved-service-endpoint','passed':(project/'endpoint.json').is_file() and (project/'endpoint.json').read_bytes() == json.dumps({'url':f"http://127.0.0.1:{evidence['port']}/receipt"}, separators=(',', ':')).encode()},
        ])
    return checks
