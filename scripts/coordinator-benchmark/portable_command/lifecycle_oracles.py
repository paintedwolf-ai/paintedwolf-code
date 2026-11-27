"""Session-temp handoff (Q11) and owned background job lifecycle (Q16) oracles."""
import contextlib
import json
import pathlib
import sqlite3
import uuid


def _parse_body(raw_result):
    body = raw_result.get('content') if raw_result.get('content') is not None else raw_result.get('result')
    if isinstance(body, str):
        text = body.strip()
        while text.startswith('[') and '\n' in text:
            text = text.split('\n', 1)[1].strip()
        if text.startswith('{'):
            try:
                return json.loads(text)
            except json.JSONDecodeError:
                return {}
        return {}
    return body if isinstance(body, dict) else {}


def _read_handles(db_path, session_id):
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        rows = db.execute(
            "SELECT DISTINCT e.job_handle FROM invocation_program_executions e "
            "JOIN invocation_programs p ON p.id=e.program_id "
            "JOIN invocation_receipts r ON r.id=p.parent_receipt_id "
            "WHERE r.session_id=? AND e.job_handle IS NOT NULL AND e.job_handle != ''",
            (session_id,),
        ).fetchall()
    return [r[0] for r in rows if r and r[0]]


def _read_handle_output(application, session_id, handle, send_calls):
    [(cid, res)] = send_calls(application, session_id, [{'name': 'command_output', 'args': {'handle': handle, 'cursor': 0}}])
    payload = _parse_body(res)
    chunks = payload.get('chunks') if isinstance(payload.get('chunks'), list) else []
    stdout = ''.join(str(c.get('text') or '') for c in chunks if isinstance(c, dict) and c.get('stream') == 'stdout')
    return cid, {
        'outcome': res.get('outcome'), 'returned_handle': payload.get('handle'),
        'stdout': stdout, 'running': payload.get('running'), 'exit_code': payload.get('exit_code'),
    }


def probe_background_job_handles(application, session_id, db_path, *, project_dir, create_base_session, send_calls):
    """Verify background job handle output, unknown-handle refusal, and cross-session handle isolation."""
    handles = _read_handles(db_path, session_id)
    if not handles:
        return None
    call_ids, handle_outputs = [], {}
    for handle in handles:
        cid, info = _read_handle_output(application, session_id, handle, send_calls)
        call_ids.append(cid)
        handle_outputs[handle] = info
    bad_cid, bad_info = _read_handle_output(application, session_id, 'nonexistent-job-handle', send_calls)
    call_ids.append(bad_cid)
    cross_refused, other_usable, other_handle = False, False, ''
    sid2, _, _, _ = create_base_session(application, project_dir, {'profile': 'implement', 'turns': []})
    try:
        application.request(f'/v1/sessions/{sid2}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Cross-session Q16 handle witness.'})
        send_calls(application, sid2, [{'name': 'update_progress', 'args': {'content': '- [ ] Cross-session witness'}}])
        send_calls(application, sid2, [{'name': 'command', 'args': {'command': 'export FIXTURE=other; fixture-job & job=$!; wait "$job"'}}])
        h2 = _read_handles(db_path, sid2)
        if h2:
            other_handle = h2[-1]
            c_cid, c_info = _read_handle_output(application, session_id, other_handle, send_calls)
            call_ids.append(c_cid)
            cross_refused = c_info['outcome'] == 'rejected'
            _, own_info = _read_handle_output(application, sid2, other_handle, send_calls)
            other_usable = bool(own_info['outcome'] == 'completed' and own_info['returned_handle'] == other_handle and own_info['stdout'].startswith('other\n'))
    finally:
        with contextlib.suppress(Exception):
            application.request(f'/v1/sessions/{sid2}/abort', {})
    return {
        'handles': handles, 'call_ids': call_ids, 'handle_outputs': handle_outputs,
        'wrong_handle_rejected': bad_info['outcome'] == 'rejected',
        'other_session_handle': other_handle, 'cross_session_refused': cross_refused, 'other_session_usable': other_usable,
    }


def inspect_temp_handoff_and_jobs(case, *, session_id, project_dir, audit, receipts, sidecar, last_payload, turn_records=None):
    """Extract Q11 and Q16 facts strictly from SQLite rows, retained job handle probes, and sidecar logs."""
    execs = audit['invocation_program_executions']
    pins = audit['invocation_program_revision_pins']
    facts = {}

    temp_pins = [
        str(p.get('path') or '') for p in pins
        if p.get('role') == 'destination'
        and (f'/scratch/{session_id}/' in str(p.get('path') or '') or str(p.get('path') or '').startswith('@scratch/'))
    ]
    if temp_pins:
        facts['temp_path_owned'] = True
        facts['same_physical_identity'] = len(set(temp_pins)) == 1
    ext_ids = {r['execution_id'] for r in receipts if r.get('operation') == 'external_exec'}
    if ext_ids and any(
        p.get('basis') == 'declared_at_launch' and p.get('execution_id') in ext_ids
        and len(p.get('disk_sha256') or '') == 64
        for p in pins
    ):
        facts['external_disk_provenance'] = True

    bg_execs = [e for e in execs if e.get('job_handle')]
    wait_execs = [e for e in execs if (e.get('native_command') or e.get('operation_id')) == 'wait' and e.get('state') == 'completed']
    if case.get('id') == 'owned-background-state' and bg_execs and wait_execs:
        bg = bg_execs[-1]
        same_prog_waits = [w for w in wait_execs if w.get('program_id') == bg.get('program_id')]
        probe = next((r.get('job_probe') for r in (turn_records or ()) if isinstance(r.get('job_probe'), dict)), None)
        ho = (probe.get('handle_outputs') or {}).get(bg['job_handle']) if probe else None
        expected_cwd = str(pathlib.Path(project_dir).resolve())
        lines = (ho.get('stdout') or '').splitlines() if isinstance(ho, dict) else []
        facts['captured_environment_cwd'] = bool(
            isinstance(ho, dict) and ho.get('outcome') == 'completed'
            and ho.get('returned_handle') == bg['job_handle'] and ho.get('running') is False
            and len(lines) >= 2 and lines[0] == 'before' and lines[1] == expected_cwd
        )
        bg_code = bg.get('status_code')
        wait_code = same_prog_waits[-1].get('status_code') if same_prog_waits else None
        facts['actual_exit'] = bool(
            isinstance(ho, dict) and same_prog_waits and isinstance(bg_code, int)
            and wait_code == bg_code and ho.get('exit_code') == bg_code
        )
        boundary_ok = bool(
            isinstance(sidecar, dict) and sidecar.get('available') and sidecar.get('complete') and sidecar.get('lines')
            and any(
                'component=sandbox' in (m.get('excerpt') or '')
                and 'host_execution=false' in (m.get('excerpt') or '')
                and 'process_control=false' in (m.get('excerpt') or '')
                for m in sidecar['lines']
            )
        )
        facts['owned_descendants_only'] = bool(
            probe and same_prog_waits and boundary_ok
            and probe.get('wrong_handle_rejected') is True
            and probe.get('cross_session_refused') is True
            and probe.get('other_session_usable') is True
        )
    return facts
