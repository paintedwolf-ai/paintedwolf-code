"""Full Q01-Q22 Portable Command suite runner and single-call command envelope probe."""
import contextlib
import json
import pathlib
import sqlite3
import uuid
import smoke
import smoke_workers
from capture_storage import checkpoint_closed_store
from portable_command.contracts import ROOT, load_suite
from portable_command.harness import (
    SSECollector,
    create_isolated_session,
    make_app_factory,
    run_case,
    session_posture,
    wait_tool_result,
)
from portable_command.observe import parse_tool_result, source_identity
from portable_command.reporting import (
    aggregate_qualification,
    archive_case_evidence,
    build_discrepancy_breakdown,
    classify_case_disposition,
)
from progress import save


def _refusal_code(tool_result, parsed_payload):
    if isinstance(parsed_payload, dict) and isinstance(parsed_payload.get('refusal'), dict) and parsed_payload['refusal'].get('code'):
        return parsed_payload['refusal']['code']
    fb = (tool_result or {}).get('feedback') or []
    if isinstance(fb, list) and fb and isinstance(fb[0], dict) and fb[0].get('code'):
        return fb[0]['code']
    codes = (tool_result or {}).get('codes') or []
    return codes[0] if codes else None


def _query_committed_revision(db_path, session_id):
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        if db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='session_progress'").fetchone():
            cols = {r[1] for r in db.execute('PRAGMA table_info(session_progress)').fetchall()}
            col = 'document_version' if 'document_version' in cols else ('revision' if 'revision' in cols else None)
            if col:
                row = db.execute(f'SELECT {col} FROM session_progress WHERE session_id=?', (session_id,)).fetchone()
                if row and isinstance(row[0], int) and row[0] > 0:
                    return row[0]
    return None


def probe_command_envelope(
    out_dir,
    *,
    program='printf ok > ./probe.txt',
    progress_content='- [ ] Probe native envelope\n- [x] Setup',
    all_checked_content='- [x] Probe native envelope',
    engine_bin=None,
    engine_manifest=None,
    app_factory=smoke.Application,
    sse_factory=SSECollector,
):
    """Probe the offered command tool schema, valid open-step envelope, and all-checked benign refusal control."""
    out_dir = pathlib.Path(out_dir).resolve()
    app_dir, project_dir, raw_dir = out_dir / 'app', out_dir / 'project', out_dir / 'raw'
    project_dir.mkdir(parents=True, exist_ok=True)
    raw_dir.mkdir(parents=True, exist_ok=True)
    identity = source_identity(ROOT, profile='implement', engine_bin=engine_bin, engine_manifest=engine_manifest)

    application = make_app_factory(engine_bin, app_factory)(app_dir)
    sse, session_id, offered_tools, command_schema = None, None, [], None
    call_id = tool_result = control_call_id = control_result = committed_rev = None
    invocation_args = {'command': program, 'progress_update': {'content': progress_content}}
    control_args = {'command': program, 'progress_update': {'content': all_checked_content}}
    try:
        application.ready()
        sse = sse_factory(application.url, application.token, raw_dir / 'events.jsonl')
        sse.start()
        session_id, _, _, _ = create_isolated_session(application, project_dir, {'id': 'envelope-probe', 'profile': 'implement'})
        application.request(f'/v1/sessions/{session_id}/prompts', {
            'operation_id': str(uuid.uuid4()),
            'text': 'Probe offered command tool schema and single-call progress_update envelope.',
        })
        req = application.wait_for(lambda: smoke_workers.pending(application, session_id), 'envelope probe pending turn', session_id)
        if 'command' not in req.get('tools', []) and 'request_tools' in req.get('tools', []):
            load_id = str(uuid.uuid4())
            application.request('/harness/llm/respond', {
                'id': req['id'],
                'tool_calls': [{'id': load_id, 'name': 'request_tools', 'args': {'need': 'command'}}],
            })
            wait_tool_result(application, session_id, load_id)
            req = application.wait_for(lambda: smoke_workers.pending(application, session_id), 'envelope probe loaded command', session_id)
        offered_tools = list(req.get('tools') or [])
        tool_schemas = list(req.get('tool_schemas') or [])
        command_schema = next((t for t in tool_schemas if isinstance(t, dict) and t.get('name') == 'command'), None)
        save(raw_dir / 'pending-schemas.json', {'tools': offered_tools, 'tool_schemas': tool_schemas})
        if 'command' in offered_tools:
            call_id = str(uuid.uuid4())
            application.request('/harness/llm/respond', {
                'id': req['id'],
                'tool_calls': [{'id': call_id, 'name': 'command', 'args': invocation_args}],
            })
            tool_result = wait_tool_result(application, session_id, call_id)
            committed_rev = _query_committed_revision(app_dir / 'store.db', session_id) or (1 if (tool_result or {}).get('outcome') == 'completed' else None)
            if committed_rev is not None:
                control_args = {'command': program, 'progress_update': {'content': all_checked_content, 'expected_revision': committed_rev}}
            req2 = application.wait_for(lambda: smoke_workers.pending(application, session_id), 'envelope all-checked control turn', session_id)
            control_call_id = str(uuid.uuid4())
            application.request('/harness/llm/respond', {
                'id': req2['id'],
                'tool_calls': [{'id': control_call_id, 'name': 'command', 'args': control_args}],
            })
            control_result = wait_tool_result(application, session_id, control_call_id)
        application.request(f'/v1/sessions/{session_id}/abort', {})
    finally:
        if sse is not None:
            sse.close()
        application.close(cleanup=False)

    checkpoint_closed_store(app_dir / 'store.db', application.process)
    receipts, programs, prog_progress = [], [], []
    if session_id:
        with contextlib.closing(sqlite3.connect(app_dir / 'store.db')) as db:
            db.row_factory = sqlite3.Row
            receipts = [dict(r) for r in db.execute(
                'SELECT id, tool_call_id, tool_name, owner, status FROM invocation_receipts WHERE session_id=? ORDER BY rowid',
                (session_id,),
            ).fetchall()]
            r_ids = [r['id'] for r in receipts]
            if r_ids:
                ph = ','.join('?' for _ in r_ids)
                programs = [dict(p) for p in db.execute(
                    f'SELECT id, parent_receipt_id, state FROM invocation_programs WHERE parent_receipt_id IN ({ph})', r_ids,
                ).fetchall()]
            p_ids = [p['id'] for p in programs]
            if p_ids and db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='invocation_program_progress'").fetchone():
                p_ph = ','.join('?' for _ in p_ids)
                prog_progress = [dict(pp) for pp in db.execute(
                    f'SELECT execution_id, program_id, state, owner_invoked, detail_ref FROM invocation_program_progress WHERE program_id IN ({p_ph})', p_ids,
                ).fetchall()]
    props = ((command_schema or {}).get('schema') or {}).get('properties') or {}
    schema_has_progress = 'progress_update' in props
    top_progress = [r for r in receipts if (r.get('tool_name') == 'update_progress' or r.get('owner') == 'progress') and r.get('status') == 'completed']
    env_progress = [pp for pp in prog_progress if pp.get('state') == 'committed' and pp.get('owner_invoked') == 1]
    cmd_receipts = [r for r in receipts if r.get('tool_name') == 'command' and r.get('tool_call_id') == call_id]
    prog_ids = {p['id'] for p in programs} | {p['parent_receipt_id'] for p in programs} | {r['id'] for r in cmd_receipts}
    progress_ids = [pp['execution_id'] for pp in env_progress] + [r['id'] for r in top_progress]
    independent_receipts = bool(progress_ids and cmd_receipts and all(pid and pid not in prog_ids for pid in progress_ids))
    _, parsed_payload = parse_tool_result({'tool_result_json': json.dumps(tool_result)}) if tool_result else ({}, {})
    _, ctrl_payload = parse_tool_result({'tool_result_json': json.dumps(control_result)}) if control_result else ({}, {})
    ctrl_code = _refusal_code(control_result, ctrl_payload)
    outcome = (tool_result or {}).get('outcome')
    if 'command' not in offered_tools:
        status = 'unavailable'
    elif schema_has_progress and outcome == 'completed' and independent_receipts:
        status = 'supported'
    elif outcome == 'rejected':
        status = 'rejected'
    elif outcome == 'completed' and not progress_ids:
        status = 'accepted_without_progress_receipt'
    else:
        status = 'other'
    report = {
        'version': 1, 'probe': 'command-progress-update-envelope', 'identity': identity,
        'session_id': session_id, 'offered_tools': offered_tools, 'command_schema': command_schema,
        'command_schema_properties': sorted(props.keys()), 'schema_has_progress_update': schema_has_progress,
        'invocation_call_id': call_id, 'invocation_args': invocation_args, 'tool_result': tool_result,
        'parsed_payload': parsed_payload, 'receipts': receipts, 'programs': programs, 'program_progress': prog_progress,
        'committed_progress_revision': committed_rev, 'native_progress_receipt_count': len(progress_ids),
        'independent_receipts': independent_receipts, 'all_checked_refusal_code': ctrl_code,
        'all_checked_control': {
            'invocation_call_id': control_call_id, 'args': control_args,
            'outcome': (control_result or {}).get('outcome'), 'refusal_code': ctrl_code,
            'benign_refusal_verified': bool((control_result or {}).get('outcome') == 'rejected' and ctrl_code == 'PROGRESS_NO_OPEN_STEP'),
        },
        'envelope_status': status, 'native_envelope_qualified': status == 'supported', 'sibling_fallback_used': False,
    }
    save(out_dir / 'envelope-probe.json', report)
    return report


def _case_metadata(case):
    profile = case['profile']
    posture = session_posture(profile)
    has_progress = any('progress_update' in turn for turn in case['turns'])
    return {
        'case_id': case['id'],
        'qualification_cases': list(case['qualification_cases']),
        'profile_binding': {'requested': profile, 'posture': posture, 'exact_match': profile == 'implement'},
        'setup_summary': {
            'files': len(case['setup'].get('files', {})),
            'drafts': sorted(case['setup'].get('drafts', {}).keys()),
            'generators': len(case['setup'].get('generators', [])),
            'host_facts': sorted(case['setup'].get('host_facts', {}).keys()),
            'turns': len(case['turns']),
        },
        'has_progress_update': has_progress,
        'envelope_mode': 'native_command_envelope' if has_progress else 'command_only',
        'native_envelope_qualified': True if has_progress else None,
    }


def run_suite(
    out_dir,
    *,
    case_ids=None,
    export_dir=None,
    engine_bin=None,
    engine_manifest=None,
    app_factory=smoke.Application,
    sse_factory=SSECollector,
    case_runner=run_case,
):
    """Run all 40 Q01-Q22 cases (or a subset), archive durable raw evidence, and emit suite-report.json."""
    out_dir = pathlib.Path(out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    durable_root = (pathlib.Path(export_dir).resolve() if export_dir is not None else out_dir / 'bundles')
    durable_root.mkdir(parents=True, exist_ok=True)
    suite = load_suite()
    by_id = {c['id']: c for c in suite['cases']}
    if case_ids:
        unknown = [cid for cid in case_ids if cid not in by_id]
        if unknown:
            raise ValueError('unknown portable-command case(s): ' + ', '.join(unknown))
        selected = [by_id[cid] for cid in case_ids]
    else:
        selected = list(suite['cases'])

    identity = source_identity(ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest)
    engine_prov = identity.get('engine_provenance')
    records = []
    for case in selected:
        cid = case['id']
        meta = _case_metadata(case)
        case_out = out_dir / 'cases' / cid
        case_export = (pathlib.Path(export_dir).resolve() / cid) if export_dir is not None else None
        run_kwargs = {'export_dir': case_export, 'app_factory': app_factory, 'sse_factory': sse_factory}
        if engine_bin is not None:
            run_kwargs['engine_bin'] = engine_bin
        if engine_manifest is not None:
            run_kwargs['engine_manifest'] = engine_manifest
        try:
            res = case_runner(case, case_out, **run_kwargs)
            grade = res['grade']
            audit = res.get('audit') or {}
            gaps = list(audit.get('observation_gaps', []))
            surface = audit.get('session_surface') or {}
            pb = dict(meta['profile_binding'], effective_posture=surface.get('effective_posture', ''), offered_tools=surface.get('offered_tools', []))
            used_sibling = any(tb.get('envelope_mode') == 'sibling_update_progress' for tb in audit.get('turn_batches') or ())
            env_fields = {'envelope_mode': 'sibling_update_progress', 'native_envelope_qualified': False} if used_sibling else {}
            rec = {
                **meta, **env_fields, 'profile_binding': pb, 'attempt': 'measured', 'session_id': res.get('session_id'),
                'qualification': grade['qualification'], 'passed': grade['passed'],
                'dimensions': grade['dimensions'], 'observation_gaps': gaps, 'error': None,
            }
        except Exception as exc:
            rec = {
                **meta, 'attempt': 'unmeasured', 'session_id': None, 'qualification': 'unqualified',
                'passed': False, 'dimensions': {}, 'observation_gaps': ['execution_error'], 'error': str(exc),
            }
        rec.update(classify_case_disposition(rec, engine_provenance=engine_prov))
        rec['evidence'] = archive_case_evidence(case_out, durable_root, case_id=cid, session_id=rec['session_id'])
        records.append(rec)

    per_q, passed_selected, passed_full = aggregate_qualification(suite['cases'], records)
    breakdown = build_discrepancy_breakdown(records)
    summary = {
        'selection_mode': 'full' if len(records) == len(suite['cases']) else 'partial',
        'planned_total': len(suite['cases']),
        'total': len(records),
        'measured': sum(1 for r in records if r['attempt'] == 'measured'),
        'unmeasured': sum(1 for r in records if r['attempt'] == 'unmeasured'),
        'qualified': sum(1 for r in records if r['disposition'] == 'qualified'),
        'unqualified': sum(1 for r in records if r['disposition'] == 'unqualified'),
        'qualified_passed': sum(1 for r in records if r['qualified_pass']),
        'diagnostic_passed': sum(1 for r in records if r['passed']),
        'passed': sum(1 for r in records if r['qualified_pass']),
        'failed': sum(1 for r in records if not r['qualified_pass']),
        'cases_with_observation_gaps': sum(1 for r in records if r['observation_gaps']),
        'qualification_cases_covered': sorted({q for r in records for q in r['qualification_cases']}),
        'qualification_cases_passed_selected': passed_selected,
        'qualification_cases_passed_full': passed_full,
        'qualification_cases_passed': passed_full,
    }
    report = {
        'version': 1, 'identity': identity, 'durable_bundle_root': str(durable_root),
        'summary': summary, 'qualification_by_id': per_q,
        'discrepancy_breakdown': breakdown, 'cases': records,
    }
    save(out_dir / 'suite-report.json', report)
    return report
