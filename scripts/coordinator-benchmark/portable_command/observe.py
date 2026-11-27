"""Independent observation and audit builder for native Portable Command runs."""
import json
from portable_command.artifacts import project_hashes
from portable_command.contracts import bytes_value
from portable_command.case_oracles import extra_case_facts
from portable_command.db_audit import ELIGIBLE_WRAPPER_OPS, query_db_audit, receipt_rows as _receipt_rows
from portable_command.reporting import (
    FACT_OWNERS,
    extract_recovery as _extract_recovery,
    file_sha256,
    retained_failure_stdout,
    scan_sidecar_launches,
    source_identity,
    structured_rejection as _structured_rejection,
    verified_source_binding as _verified_source_binding,
)

ALLOWED_STOPS = {None, 'rejected', 'conflict', 'cancelled', 'incomplete', 'budget', 'interrupted'}


def parse_tool_result(message_row):
    raw = json.loads(message_row['tool_result_json'])
    body = raw.get('content') if raw.get('content') is not None else raw.get('result')
    if isinstance(body, str):
        text = body.strip()
        while text.startswith('[') and '\n' in text:
            text = text.split('\n', 1)[1].strip()
        if text.startswith('{'):
            try:
                return raw, json.loads(text)
            except json.JSONDecodeError:
                return raw, {}
        return raw, {}
    return raw, body if isinstance(body, dict) else {}


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


def build_observation(case, *, session_id, identity, project_dir, db_path, sidecar_log, turn_records, prelude_records, sidecar_window=None, extra_gaps=(), baseline_hashes=None, session_surface=None, draft_bindings=None, host_scope_binding=None, program_receipt_pages=None, git_setup=None):
    """Construct a version-1 native-runtime observation from the whole isolated session."""
    prelude_calls = {r['call_id'] for r in prelude_records if r.get('call_id')}
    expected_calls = {cid for rec in turn_records for cid in (list(rec['call_ids']) + list(rec.get('await_call_ids') or ()))}
    audit = query_db_audit(db_path, session_id, prelude_calls)
    sidecar = scan_sidecar_launches(sidecar_log, session_id, window=sidecar_window)
    gaps = list(dict.fromkeys(extra_gaps or ()))
    if not sidecar['available']:
        gaps.append('missing_sidecar_log')
    elif not sidecar['complete']:
        gaps.append('incomplete_sidecar_log')

    msgs_by_call = {m['tool_result_call_id']: parse_tool_result(m) for m in audit['tool_result_messages']}
    progs_by_receipt = {p['parent_receipt_id']: p for p in audit['invocation_programs']}
    top_progress = [r for r in audit['invocation_receipts'] if r['tool_name'] == 'update_progress' and r['owner'] == 'progress']
    env_progress = [
        {'id': pp['execution_id'], 'status': 'completed' if pp['state'] == 'committed' else pp['state']}
        for pp in audit.get('invocation_program_progress', [])
        if pp.get('state') in {'committed', 'rejected', 'unknown'}
    ]
    progress_receipts = top_progress + env_progress
    cmd_receipts = [r for r in audit['invocation_receipts'] if r['tool_name'] == 'command']
    receipts_by_call = {r['tool_call_id']: r for r in audit['invocation_receipts'] if r['tool_name'] != 'update_progress'}
    env_prog_ids = {pp['execution_id'] for pp in audit.get('invocation_program_progress', [])}
    unexpected_receipts = [
        r for r in audit['invocation_receipts']
        if r['tool_call_id'] not in expected_calls and r['id'] not in env_prog_ids
    ]
    unexpected_cmd = [r for r in cmd_receipts if r['tool_call_id'] not in expected_calls]

    cmd_invocations, last_payload, last_raw, last_prog, first_rejected = [], {}, {}, None, None
    for rec in turn_records:
        cmd_call = rec['command_call_id']
        raw_res, payload = msgs_by_call.get(cmd_call, ({}, {}))
        inv_row = receipts_by_call.get(cmd_call, {})
        prog_row = progs_by_receipt.get(inv_row.get('id'))
        await_meta = rec.get('handle_await') or {}
        if payload.get('state') == 'running' and await_meta.get('terminal') and isinstance(await_meta.get('final_output'), dict):
            fo = await_meta['final_output']
            payload = {**payload, 'stdout': fo.get('stdout', ''), 'stderr': fo.get('stderr', '')}
            if not (prog_row and prog_row.get('state')) and fo.get('state'):
                payload['state'] = fo['state']
            if not (prog_row and isinstance(prog_row.get('status_code'), int)):
                if isinstance(fo.get('status'), int):
                    payload['status'] = fo['status']
                elif isinstance(fo.get('exit_code'), int):
                    payload['status'] = fo['exit_code']
        outcome, _, code = normalize_outcome(raw_res, prog_row, payload)
        cmd_invocations.append({'id': (prog_row or {}).get('id') or inv_row.get('id') or cmd_call,
                                'program': rec['program'], 'outcome': outcome, 'rejection_code': code})
        if outcome == 'rejected' and first_rejected is None:
            first_rejected = (payload, raw_res, (prog_row or {}).get('id'))
        last_payload, last_raw, last_prog = payload, raw_res, prog_row
    for extra in unexpected_cmd:
        cmd_invocations.append({'id': extra['id'], 'program': '<unexpected>', 'outcome': extra['status'], 'rejection_code': None})

    outcome, host_stop, last_code = normalize_outcome(last_raw, last_prog, last_payload)
    _, last_details = _structured_rejection(last_raw, last_payload)
    last_structured_ok = bool(turn_records and turn_records[-1].get('envelope_mode') == 'structured_tool' and outcome == 'completed' and host_stop is None)
    status_code = last_payload.get('status') if isinstance(last_payload.get('status'), int) else (
        last_prog['status_code'] if last_prog and isinstance(last_prog.get('status_code'), int) else (
            last_details.get('status') if isinstance(last_details.get('status'), int) else (
                0 if last_structured_ok else (
                    2 if (outcome == 'rejected' and last_code == 'COMMAND_SYNTAX_INVALID'
                          and last_details.get('diagnostic_pre_evaluation') is True
                          and last_details.get('command_execution') == 'not_started'
                          and (last_raw.get('invocation') or {}).get('invoked') is False) else None
                )
            )
        )
    )
    if status_code is None:
        gaps.append('missing_program_status_code')
    complete = bool(outcome == 'completed' and host_stop is None and ((last_prog is not None and last_prog.get('state') == 'completed') or last_structured_ok))
    receipts, effects, wrappers = _receipt_rows(audit, progress_receipts, draft_bindings=draft_bindings)
    prog_ids = {p['id'] for p in audit['invocation_programs']}
    wrappers_valid = all(
        w.get('program_id') in prog_ids and w.get('owner') in {None, '', 'processes'}
        and w.get('state') in {'completed', 'refused', 'rejected', 'failed', 'interrupted'}
        for w in wrappers
    )
    for extra in unexpected_receipts:
        effects.append({'operation': extra['tool_name'], 'outcome': extra['status'], 'unexpected_call_id': extra['tool_call_id']})
    recovery = _extract_recovery(case['id'], last_payload, last_raw, audit, unexpected_cmd)
    if recovery is None and first_rejected is not None:
        rej_payload, rej_raw, rej_pid = first_rejected
        rej_audit = {**audit, 'invocation_program_executions': [e for e in audit['invocation_program_executions'] if rej_pid and e.get('program_id') == rej_pid]}
        recovery = _extract_recovery(case['id'], rej_payload, rej_raw, rej_audit, unexpected_cmd)
    explicit_correction = len(cmd_invocations) >= 2 and cmd_invocations[0]['outcome'] == 'rejected' and cmd_invocations[-1]['outcome'] == 'completed'
    fresh_identity = len(cmd_invocations) >= 2 and len({i['id'] for i in cmd_invocations}) == len(cmd_invocations)
    had_focused_help = bool(recovery and recovery['remedy'] in {'focused_command_help', 'focused_syntax_help'})
    if recovery and recovery['cause'] == 'option' and explicit_correction and fresh_identity:
        recovery = {**recovery, 'remedy': 'explicit_fresh_command'}

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
    scoped_producers = [e for e in execs if e.get('operation_id') != 'write' and not (e.get('operation_id') or '').startswith('program ') and (not last_pid or e.get('program_id') == last_pid)]
    digested_outputs = [o for o in outputs if o.get('stream') in {'stdout', 'stderr'} and o.get('complete') == 1 and (len(o.get('blob_id') or '') == 64 or len(o.get('inline_digest') or '') == 64)]
    write_rev_ok = all(bool(e.get('published_revision') or e.get('accepted_revision')) for e in write_execs)
    committed_session_progress = [r for r in (audit.get('session_progress') or ()) if (r.get('content') or '').strip()]
    eff_posture = (session_surface or {}).get('effective_posture', '')
    facts = {
        'progress_and_program_separate': bool(progress_receipts and audit['invocation_programs'] and all(pr['id'] not in {p['parent_receipt_id'], p['id']} for pr in progress_receipts for p in audit['invocation_programs'])),
        'absence_pin': any(p['mode'] == 'absent' and p['role'] == 'destination' and p['execution_id'] in write_exec_ids for p in pins),
        'child_identities': bool(child_ids) and len(set(child_ids)) == len(child_ids),
        'unique_dynamic_ids': bool(execs) and len({e['id'] for e in execs}) == len(execs),
        'no_external_launch': no_external_launch,
        'option_source_span': bool(recovery and recovery['cause'] == 'option' and recovery['source_span_required']),
        'syntax_span': bool(recovery and recovery['cause'] == 'syntax' and recovery['source_span_required']),
        'offending_source_span': bool(recovery and recovery['source_span_required']),
        'focused_help': bool(had_focused_help),
        'zero_program_effects': bool(recovery and recovery['zero_program_effects'] and (baseline_hashes is None or final_hashes == baseline_hashes)),
        'zero_progress_effects': not progress_receipts and not audit.get('invocation_program_progress') and not committed_session_progress,
        'progress_committed_separately': bool(
            env_progress and any(pr['status'] == 'completed' for pr in env_progress)
            and audit['invocation_programs']
            and all(pr['id'] not in {p['parent_receipt_id'], p['id']} for pr in env_progress for p in audit['invocation_programs'])
        ),
        'no_gated_effect': bool(recovery and recovery['zero_program_effects'] and (baseline_hashes is None or final_hashes == baseline_hashes)),
        'fresh_resubmission_only': bool(recovery and recovery['whole_program_resubmit_allowed'] is False and not unexpected_cmd and len(cmd_receipts) == len(turn_records)),
        'exact_source_binding': source_bound,
        'execution_revision': bool(source_bound and write_rev_ok and all(len(p.get('content_sha256') or '') == 64 or (p.get('document_revision') or 0) > 0 for p in source_pins)),
        'accepted_then_published': bool(write_execs and all(e.get('accepted_revision') and e.get('published_revision') for e in write_execs) and any(p.get('state') == 'committed' and p.get('materialized') == 1 for p in audit['invocation_program_publications'])),
        'no_conversational_read_base': source_bound and not audit.get('invocation_program_deliveries'),
        'preview_only_first_line': any(o.get('stream') == 'stdout' and o.get('preview_truncated') == 1 and '\n' not in (o.get('preview') or '').rstrip('\n') for o in outputs),
        'unseen_ranges_not_read_bases': any(o.get('stream') == 'stdout' and o.get('preview_truncated') == 1 for o in outputs) and not audit.get('invocation_program_deliveries'),
        'shared_static_node': bool(node_ids) and len(set(node_ids)) < len(node_ids),
        'session_job_handle': any(e.get('job_handle') for e in execs),
        'no_device_file_mutation': not write_exec_ids,
        'first_action_rejected': bool(cmd_invocations and cmd_invocations[0]['outcome'] == 'rejected'),
        'explicit_correction': explicit_correction,
        'fresh_identity': fresh_identity,
        'prior_parent_receipt': any(e.get('state') == 'completed' for e in execs),
        'no_whole_program_retry': len(cmd_receipts) == len(turn_records) and not unexpected_cmd,
        'missing_progress_owner_code': bool(recovery and 'progress' in (recovery.get('owner_code') or '').lower()),
        'reachable_frozen_remedy': bool(had_focused_help or (recovery and recovery['remedy'] == 'explicit_fresh_command')),
        'readonly_profile': eff_posture in {'vet', 'spec'},
        'reachable_readonly_remedy': bool(recovery and recovery['remedy']),
    }
    if digested_outputs:
        facts['full_stream_digest'] = True
        facts['retained_full_output'] = any(bool(o.get('reference')) for o in digested_outputs)
        facts['preview_independent'] = any(o.get('preview_truncated') == 1 or bool(o.get('blob_id')) for o in digested_outputs)
    if scoped_producers:
        any_published_write = any(r.get('operation') == 'write' and r.get('published') is True for r in receipts)
        facts['every_producer_eligible'] = all(e.get('publication_eligible') == 1 for e in scoped_producers) or not any_published_write
    if baseline_hashes:
        dest_paths = {k.lstrip('./') for k in case.get('expected', {}).get('files', {})} & set(baseline_hashes)
        pub_paths = {r.get('path', '').lstrip('./') for r in receipts if r.get('operation') == 'write' and r.get('published') is True}
        if dest_paths:
            facts['destination_unchanged'] = all(final_hashes.get(p) == baseline_hashes.get(p) for p in dest_paths) and not (dest_paths & pub_paths)
    if any(s.get('source_unit_id') and len(s.get('sha256') or '') == 64 for s in audit.get('invocation_program_sources') or ()):
        facts['captured_source_identity'] = True
    src_execs = [e for e in execs if e.get('operation_id') == 'source' and e.get('state') in {'refused', 'rejected'}]
    if src_execs:
        src_ids = {e['id'] for e in src_execs}
        src_max_ord = max((e.get('ordinal') or 0) for e in src_execs)
        non_wrapper_execs = [e for e in execs if not ((e.get('operation_id') or '') in ELIGIBLE_WRAPPER_OPS and not e.get('native_command'))]
        facts['no_source_statement_started'] = (
            not any(e.get('parent_execution_id') in src_ids for e in non_wrapper_execs)
            and not any((e.get('ordinal') or 0) > src_max_ord for e in non_wrapper_execs)
        )
    if any(r.get('operation') == 'external_exec' and r.get('outcome') == 'completed' for r in receipts):
        raw_out = (last_payload.get('stdout') or '').strip()
        if raw_out.startswith('['):
            try:
                parsed_argv = json.loads(raw_out)
                if isinstance(parsed_argv, list) and parsed_argv and all(isinstance(a, str) for a in parsed_argv) and 'never' not in final_hashes:
                    facts['exact_argv'] = True
            except ValueError:
                pass
    if not write_exec_ids and (baseline_hashes is None or final_hashes == baseline_hashes) and any('/dev/null' in (rec.get('program') or '') for rec in turn_records):
        facts['typed_null_sink'] = True
    if bool(node_ids) and len(set(node_ids)) < len(node_ids) and outcome == 'completed':
        facts['bounded_control_flow'] = True
    if any(e.get('native_command') == 'find' and e.get('state') == 'completed' and e.get('complete') == 1 for e in execs):
        facts['complete_tree'] = True
    if recovery and recovery.get('owner_code'):
        facts['owner_refusal'] = True
        facts['no_outside_effect'] = bool(recovery['zero_program_effects'] and (baseline_hashes is None or final_hashes == baseline_hashes))
        if any(tok in recovery['owner_code'].lower() for tok in ('zip', 'traversal', 'escape', 'archive')):
            facts['zip_slip_refused'] = True
    failure_output = None
    if case['id'] == 'failed-producer':
        expected = bytes_value(case['setup']['helpers']['fixture-producer']['stdout'])
        failure_output = retained_failure_stdout(last_pid, execs, outputs,
                                                 (program_receipt_pages or {}).get(last_pid), expected)
        facts['screened_retained_failure_output'] = bool(failure_output)
    if case['id'] == 'progress-then-denial':
        facts['denied_path_bound'] = (
            bool(host_scope_binding) and last_code == 'WRITE_SCOPE_DENIED'
            and last_details.get('path') == host_scope_binding['denied_path']
            and last_details.get('profile') == 'coordinator'
        )
        progress = audit.get('invocation_program_progress') or []
        facts['progress_committed_separately'] = (
            len(progress) == 1 and progress[0]['program_id'] == last_pid
            and progress[0]['state'] == 'committed' and progress[0]['owner_invoked'] == 1
            and progress[0]['execution_id'] == last_details.get('program_progress_execution_id')
            and bool(progress[0]['detail_ref'])
            and progress[0]['detail_ref'] == last_details.get('program_progress_detail_ref')
            and progress[0]['execution_id'] != last_details.get('execution_id')
        )
    extra_facts, recovery, deliveries = extra_case_facts(case, session_id=session_id, identity=identity, project_dir=project_dir, git_setup=git_setup, receipts=receipts, effects=effects, audit=audit, recovery=recovery, last_payload=last_payload, final_hashes=final_hashes, baseline_hashes=baseline_hashes, sidecar=sidecar, session_surface=session_surface, last_raw=last_raw, no_external_launch=no_external_launch)
    facts.update(extra_facts)
    facts['structural_wrappers_valid'] = wrappers_valid
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
        'receipts': receipts, 'effects': effects, 'facts': facts, 'deliveries': deliveries, 'obligations': [],
        'closeout': 'complete' if outcome == 'completed' else 'blocked', 'forbidden_replay': forbidden_replay, 'recovery': recovery, 'invocations': cmd_invocations,
        **({'workflow_init_artifacts': (session_surface or {}).get('workflow_init_artifacts')} if (session_surface or {}).get('declared_profile_bound') else {}),
    }
    audit_bundle = {
        'session_id': session_id, 'session_surface': session_surface or {},
        'structural_wrappers': {'count': len(wrappers), 'valid': wrappers_valid, 'records': wrappers},
        'prelude': prelude_records, 'turn_batches': turn_records,
        'unexpected_receipt_ids': [r['id'] for r in unexpected_receipts], 'observation_gaps': gaps,
        'refusal_stderr': raw_stderr if pre_exec_refusal else '',
        'retained_failure_output': failure_output,
        'external_launch_audit': {
            'sidecar': sidecar, 'source_command_windows': len(audit['source_command_windows']),
            'external_executions': len(external_execs), 'source_window_field': last_payload.get('source_window'),
            'native_gaps': last_payload.get('native_gaps', 0),
        },
        'filesystem_hashes': final_hashes, 'db': audit,
    }
    return observation, audit_bundle
