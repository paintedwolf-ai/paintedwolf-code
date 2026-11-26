"""Independent observation and audit builder for native Portable Command runs."""
import contextlib
import json
import pathlib
import sqlite3
from portable_command.artifacts import project_hashes
from portable_command.reporting import (
    FACT_OWNERS,
    file_sha256,
    scan_sidecar_launches,
    source_identity,
    verified_source_binding as _verified_source_binding,
)

ALLOWED_STOPS = {None, 'rejected', 'conflict', 'cancelled', 'incomplete', 'budget', 'interrupted'}


def _rows(db, sql, params):
    return [dict(r) for r in db.execute(sql, params).fetchall()]


def query_db_audit(db_path, session_id, prelude_call_ids=()):
    """Read ALL authoritative SQLite rows for the isolated session, separating prelude from tested work."""
    uri = pathlib.Path(db_path).resolve().as_uri() + '?mode=ro'
    prelude_set = set(prelude_call_ids or ())
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as db:
        db.row_factory = sqlite3.Row
        all_receipts = _rows(db, 'SELECT * FROM invocation_receipts WHERE session_id=? ORDER BY rowid', (session_id,))
        prelude_receipts = [r for r in all_receipts if r['tool_call_id'] in prelude_set]
        receipts = [r for r in all_receipts if r['tool_call_id'] not in prelude_set]
        r_ids = [r['id'] for r in receipts]
        r_ph = ','.join('?' for _ in r_ids) or "''"
        programs = _rows(db, f'SELECT * FROM invocation_programs WHERE parent_receipt_id IN ({r_ph}) ORDER BY rowid', r_ids) if r_ids else []
        p_ids = [p['id'] for p in programs]
        p_ph = ','.join('?' for _ in p_ids) or "''"
        executions = _rows(db, f'SELECT * FROM invocation_program_executions WHERE program_id IN ({p_ph}) ORDER BY program_id, ordinal', p_ids) if p_ids else []
        pins = _rows(db, f'SELECT * FROM invocation_program_revision_pins WHERE program_id IN ({p_ph}) ORDER BY program_id, execution_id, ordinal', p_ids) if p_ids else []
        outputs = _rows(db, f'SELECT id,program_id,execution_id,stream,reference,complete,redacted,preview,preview_truncated,line_count,inline_digest,blob_id FROM invocation_program_outputs WHERE program_id IN ({p_ph}) ORDER BY rowid', p_ids) if p_ids else []
        publications = _rows(db, f'SELECT * FROM invocation_program_publications WHERE program_id IN ({p_ph})', p_ids) if p_ids else []
        o_ids = [o['id'] for o in outputs]
        o_ph = ','.join('?' for _ in o_ids) or "''"
        deliveries = _rows(db, f'SELECT id,output_id,start_byte,end_byte,source_owner_ref FROM invocation_program_deliveries WHERE output_id IN ({o_ph})', o_ids) if o_ids and _table_exists(db, 'invocation_program_deliveries') else []
        windows = [w for w in _rows(db, 'SELECT id,session_id,tool_call_id,state FROM source_command_windows WHERE session_id=?', (session_id,))
                   if w['tool_call_id'] not in prelude_set]
        messages = [m for m in _rows(db, 'SELECT id,ord,role,tool_result_call_id,tool_result_json FROM messages WHERE session_id=? AND tool_result_call_id IS NOT NULL AND tool_result_call_id != "" ORDER BY ord', (session_id,))
                    if m['tool_result_call_id'] not in prelude_set]
    return {
        'prelude_receipts': prelude_receipts, 'invocation_receipts': receipts, 'invocation_programs': programs,
        'invocation_program_executions': executions, 'invocation_program_revision_pins': pins,
        'invocation_program_outputs': outputs, 'invocation_program_publications': publications,
        'invocation_program_deliveries': deliveries,
        'source_command_windows': windows, 'tool_result_messages': messages,
    }


def _table_exists(db, name):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)).fetchone() is not None


def parse_tool_result(message_row):
    raw = json.loads(message_row['tool_result_json'])
    body = raw.get('content') if raw.get('content') is not None else raw.get('result')
    if isinstance(body, str):
        text = body.strip()
        if text.startswith('[') and '\n' in text:
            text = text.split('\n', 1)[1].strip()
        if text.startswith('{'):
            try:
                return raw, json.loads(text)
            except json.JSONDecodeError:
                return raw, {}
        return raw, {}
    return raw, body if isinstance(body, dict) else {}


def _structured_rejection(raw_result, payload):
    fb_list = raw_result.get('feedback') or []
    fb0 = fb_list[0] if isinstance(fb_list, list) and fb_list and isinstance(fb_list[0], dict) else {}
    refusal = payload.get('refusal') if isinstance(payload, dict) and isinstance(payload.get('refusal'), dict) else {}
    inv_failure = (raw_result.get('invocation') or {}).get('failure') or {}
    rejection = raw_result.get('rejection') or {}
    codes = raw_result.get('codes') or []
    code = refusal.get('code') or fb0.get('code') or inv_failure.get('code') or rejection.get('code') or (codes[0] if codes else None)
    details = refusal.get('details') or fb0.get('details') or rejection.get('details') or {}
    return code, details if isinstance(details, dict) else {}


def normalize_outcome(raw_result, program_row, payload):
    raw_outcome = raw_result.get('outcome')
    code, _ = _structured_rejection(raw_result, payload)
    if not code and program_row:
        code = program_row.get('stop_code') or None
    if raw_outcome == 'rejected' or (program_row and program_row.get('state') == 'rejected') or (isinstance(payload.get('refusal'), dict) and payload['refusal'].get('code')):
        return 'rejected', 'rejected', code
    state = (program_row or {}).get('state') or raw_outcome or None
    stop = None if state == 'completed' else (state if state in ALLOWED_STOPS else 'incomplete')
    return state, stop, code


def _receipt_rows(audit, progress_receipts):
    pubs_by_prog = {p['program_id']: p for p in audit['invocation_program_publications']}
    receipts = [{'execution_id': pr['id'], 'operation': 'update_progress', 'outcome': pr['status']} for pr in progress_receipts]
    effects = [{'operation': 'update_progress', 'outcome': pr['status']} for pr in progress_receipts]
    for ex in audit['invocation_program_executions']:
        op = ex['operation_id'] if ex.get('operation_id') in {'read', 'write'} else (ex.get('native_command') or ex.get('operation_id'))
        row = {'execution_id': ex['id'], 'operation': op, 'outcome': ex['state']}
        effect = {'operation': op, 'outcome': ex['state']}
        if isinstance(ex.get('status_code'), int):
            row['status'] = ex['status_code']
        if ex.get('publication_eligible') in {0, 1}:
            row['publication_eligible'] = bool(ex['publication_eligible'])
        if ex.get('consumer_closed') == 1:
            row['closure'] = 'expected-consumer'
        if ex.get('operation_id') == 'write' or ex.get('accepted_revision') or ex.get('published_revision'):
            pin = next((p for p in audit['invocation_program_revision_pins']
                        if p['execution_id'] == ex['id'] and p['role'] == 'destination'), None)
            if pin:
                row['path'] = effect['path'] = pin['path']
            pub = pubs_by_prog.get(ex['program_id'])
            pub_committed = bool(pub and pub.get('state') == 'committed' and pub.get('materialized') == 1)
            published = bool(ex.get('published_revision')) and pub_committed
            accepted = bool(ex.get('accepted_revision')) or (published and ex.get('effect_certainty') == 'completed')
            row['accepted'] = effect['accepted'] = accepted
            row['published'] = effect['published'] = published
        receipts.append(row)
        if ex.get('effect_certainty') in {'completed', 'attempted', 'unknown'} or ex.get('operation_id') == 'write':
            effects.append(effect)
    return receipts, effects


def _has_structured_span(details):
    if isinstance(details.get('span'), dict) and isinstance(details['span'].get('start'), int) and isinstance(details['span'].get('end'), int):
        return True
    return isinstance(details.get('source_start_byte'), int) and isinstance(details.get('source_end_byte'), int)


def _extract_recovery(case_id, payload, raw_result, audit, unexpected_cmd):
    code, details = _structured_rejection(raw_result, payload)
    if not code:
        return None
    has_span = _has_structured_span(details)
    has_help = bool(details.get('expected_form') or details.get('usage') or details.get('help'))
    cause = 'option' if code == 'COMMAND_OPTION_UNSUPPORTED' else ('syntax' if code == 'COMMAND_SYNTAX_INVALID' else code.lower())
    remedy = ('focused_syntax_help' if cause == 'syntax' else 'focused_command_help') if has_help else ''
    zero_effects = (
        payload.get('completed_effects', 0) == 0 and payload.get('uncertain_effects', 0) == 0
        and not audit['invocation_program_executions'] and details.get('command_execution') in {None, 'not_started'}
    )
    replay_fact = details.get('program_replay_available')
    if isinstance(replay_fact, bool):
        replay_allowed = replay_fact and zero_effects
    elif isinstance(payload.get('replay_available'), bool):
        replay_allowed = payload['replay_available'] and zero_effects
    else:
        replay_allowed = None
    return {
        'case_id': case_id, 'cause': cause, 'zero_program_effects': zero_effects,
        'whole_program_resubmit_allowed': replay_allowed, 'automatic_execution': bool(unexpected_cmd),
        'remedy': remedy, 'effect_certainty_required': zero_effects,
        'source_span_required': has_span, 'owner_code': code,
    }


def build_observation(case, *, session_id, identity, project_dir, db_path, sidecar_log, turn_records, prelude_records, sidecar_window=None, extra_gaps=(), baseline_hashes=None, session_surface=None):
    """Construct a version-1 native-runtime observation from the whole isolated session."""
    prelude_calls = {r['call_id'] for r in prelude_records if r.get('call_id')}
    expected_calls = {cid for rec in turn_records for cid in rec['call_ids']}
    audit = query_db_audit(db_path, session_id, prelude_calls)
    sidecar = scan_sidecar_launches(sidecar_log, session_id, window=sidecar_window)
    gaps = list(dict.fromkeys(extra_gaps or ()))
    if not sidecar['available']:
        gaps.append('missing_sidecar_log')
    elif not sidecar['complete']:
        gaps.append('incomplete_sidecar_log')

    msgs_by_call = {m['tool_result_call_id']: parse_tool_result(m) for m in audit['tool_result_messages']}
    progs_by_receipt = {p['parent_receipt_id']: p for p in audit['invocation_programs']}
    receipts_by_call = {r['tool_call_id']: r for r in audit['invocation_receipts']}
    progress_receipts = [r for r in audit['invocation_receipts'] if r['tool_name'] == 'update_progress' and r['owner'] == 'progress']
    cmd_receipts = [r for r in audit['invocation_receipts'] if r['tool_name'] == 'command']
    unexpected_receipts = [r for r in audit['invocation_receipts'] if r['tool_call_id'] not in expected_calls]
    unexpected_cmd = [r for r in cmd_receipts if r['tool_call_id'] not in expected_calls]

    cmd_invocations, last_payload, last_raw, last_prog = [], {}, {}, None
    for rec in turn_records:
        cmd_call = rec['command_call_id']
        raw_res, payload = msgs_by_call.get(cmd_call, ({}, {}))
        inv_row = receipts_by_call.get(cmd_call, {})
        prog_row = progs_by_receipt.get(inv_row.get('id'))
        outcome, _, code = normalize_outcome(raw_res, prog_row, payload)
        cmd_invocations.append({'id': (prog_row or {}).get('id') or inv_row.get('id') or cmd_call,
                                'program': rec['program'], 'outcome': outcome, 'rejection_code': code})
        last_payload, last_raw, last_prog = payload, raw_res, prog_row
    for extra in unexpected_cmd:
        cmd_invocations.append({'id': extra['id'], 'program': '<unexpected>', 'outcome': extra['status'], 'rejection_code': None})

    outcome, host_stop, _ = normalize_outcome(last_raw, last_prog, last_payload)
    status_code = last_payload.get('status') if isinstance(last_payload.get('status'), int) else (
        last_prog['status_code'] if last_prog and isinstance(last_prog.get('status_code'), int) else None
    )
    if status_code is None:
        gaps.append('missing_program_status_code')
    complete = bool(outcome == 'completed' and host_stop is None and last_prog is not None and last_prog.get('state') == 'completed')
    receipts, effects = _receipt_rows(audit, progress_receipts)
    for extra in unexpected_receipts:
        effects.append({'operation': extra['tool_name'], 'outcome': extra['status'], 'unexpected_call_id': extra['tool_call_id']})
    recovery = _extract_recovery(case['id'], last_payload, last_raw, audit, unexpected_cmd)

    external_execs = [e for e in audit['invocation_program_executions']
                      if e.get('owner') == 'external' or e.get('native_command') == 'external' or e.get('native_gap_json')]
    no_external_launch = (
        sidecar['available'] and sidecar['complete'] and sidecar['command_launches'] == 0
        and not audit['source_command_windows'] and not external_execs
        and last_payload.get('source_window') is None and last_payload.get('native_gaps', 0) == 0
    )
    execs = audit['invocation_program_executions']
    pins = audit['invocation_program_revision_pins']
    outputs = audit['invocation_program_outputs']
    source_bound, source_pins = _verified_source_binding(case, pins, execs, baseline_hashes)
    write_execs = [e for e in execs if e.get('operation_id') == 'write']
    write_exec_ids = {e['id'] for e in write_execs}
    child_ids = [r['execution_id'] for r in receipts]
    node_ids = [e['node_id'] for e in execs if e.get('node_id')]
    final_hashes = project_hashes(project_dir)
    last_pid = (last_prog or {}).get('id')
    scoped_producers = [e for e in execs if e.get('operation_id') != 'write' and (not last_pid or e.get('program_id') == last_pid)]
    digested_outputs = [o for o in outputs if o.get('complete') == 1 and len(o.get('inline_digest') or '') == 64]
    write_rev_ok = all(bool(e.get('published_revision') or e.get('accepted_revision')) for e in write_execs)
    facts = {
        'progress_and_program_separate': bool(progress_receipts and audit['invocation_programs']
                                              and all(pr['id'] != p['parent_receipt_id'] for pr in progress_receipts for p in audit['invocation_programs'])),
        'absence_pin': any(p['mode'] == 'absent' and p['role'] == 'destination' and p['execution_id'] in write_exec_ids for p in pins),
        'child_identities': bool(child_ids) and len(set(child_ids)) == len(child_ids),
        'unique_dynamic_ids': bool(execs) and len({e['id'] for e in execs}) == len(execs),
        'no_external_launch': no_external_launch,
        'option_source_span': bool(recovery and recovery['cause'] == 'option' and recovery['source_span_required']),
        'syntax_span': bool(recovery and recovery['cause'] == 'syntax' and recovery['source_span_required']),
        'focused_help': bool(recovery and recovery['remedy'] in {'focused_command_help', 'focused_syntax_help'}),
        'zero_program_effects': bool(recovery and recovery['zero_program_effects']),
        'zero_progress_effects': not progress_receipts,
        'exact_source_binding': source_bound,
        'execution_revision': bool(source_bound and write_rev_ok and all(len(p.get('content_sha256') or '') == 64 or (p.get('document_revision') or 0) > 0 for p in source_pins)),
        'no_conversational_read_base': source_bound and not audit.get('invocation_program_deliveries'),
        'preview_only_first_line': any(o.get('preview_truncated') == 1 and '\n' not in (o.get('preview') or '').rstrip('\n') for o in outputs),
        'unseen_ranges_not_read_bases': any(o.get('preview_truncated') == 1 for o in outputs) and not audit.get('invocation_program_deliveries'),
        'shared_static_node': bool(node_ids) and len(set(node_ids)) < len(node_ids),
        'session_job_handle': any(e.get('job_handle') for e in execs),
        'no_device_file_mutation': not write_exec_ids,
        'first_action_rejected': bool(cmd_invocations and cmd_invocations[0]['outcome'] == 'rejected'),
        'explicit_correction': len(cmd_invocations) >= 2 and cmd_invocations[0]['outcome'] == 'rejected' and cmd_invocations[-1]['outcome'] == 'completed',
        'fresh_identity': len(cmd_invocations) >= 2 and len({i['id'] for i in cmd_invocations}) == len(cmd_invocations),
        'prior_parent_receipt': any(e.get('state') == 'completed' for e in execs),
        'no_whole_program_retry': len(cmd_receipts) == len(turn_records) and not unexpected_cmd,
        'missing_progress_owner_code': bool(recovery and 'progress' in (recovery.get('owner_code') or '').lower()),
        'reachable_frozen_remedy': bool(recovery and recovery['remedy'] in {'focused_command_help', 'focused_syntax_help'}),
    }
    if digested_outputs:
        facts['full_stream_digest'] = True
        facts['retained_full_output'] = any(bool(o.get('reference')) for o in digested_outputs)
        facts['preview_independent'] = any(o.get('preview_truncated') == 1 or bool(o.get('blob_id')) for o in digested_outputs)
    if scoped_producers:
        facts['every_producer_eligible'] = all(e.get('publication_eligible') == 1 for e in scoped_producers)
    for req_fact in case.get('required_evidence') or ():
        if req_fact not in facts:
            gaps.append(f"missing_observation:{req_fact}:{FACT_OWNERS.get(req_fact, 'runtime_owner')}")
    pre_exec_refusal = bool(outcome == 'rejected' and not execs and recovery is not None)
    raw_stderr = last_payload.get('stderr', '')
    program_stderr = '' if pre_exec_refusal else raw_stderr
    forbidden_replay = bool(unexpected_cmd) or len(cmd_receipts) > len(turn_records) or len({r['id'] for r in cmd_receipts}) != len(cmd_receipts)
    observation = {
        'version': 1, 'case_id': case['id'], 'capture_kind': 'native-runtime', 'identity': identity,
        'program': {'stdout': last_payload.get('stdout', ''), 'stderr': program_stderr,
                    'status': status_code, 'host_stop': host_stop, 'complete': complete},
        'receipts': receipts, 'effects': effects, 'facts': facts, 'deliveries': [], 'obligations': [],
        'closeout': 'complete' if outcome == 'completed' else 'blocked', 'forbidden_replay': forbidden_replay,
        'recovery': recovery, 'invocations': cmd_invocations,
    }
    audit_bundle = {
        'session_id': session_id, 'session_surface': session_surface or {},
        'prelude': prelude_records, 'turn_batches': turn_records,
        'unexpected_receipt_ids': [r['id'] for r in unexpected_receipts], 'observation_gaps': gaps,
        'refusal_stderr': raw_stderr if pre_exec_refusal else '',
        'external_launch_audit': {
            'sidecar': sidecar, 'source_command_windows': len(audit['source_command_windows']),
            'external_executions': len(external_execs), 'source_window_field': last_payload.get('source_window'),
            'native_gaps': last_payload.get('native_gaps', 0),
        },
        'filesystem_hashes': final_hashes, 'db': audit,
    }
    return observation, audit_bundle
