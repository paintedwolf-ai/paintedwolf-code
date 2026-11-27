"""Session-temp handoff (Q11) and owned background job lifecycle (Q16) oracles."""
import contextlib
import json
import sqlite3


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


def probe_background_job_handles(application, session_id, db_path, *, send_calls):
    """Read background job output via command_output(handle=...) plus a wrong-handle negative control."""
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        rows = db.execute(
            "SELECT DISTINCT e.job_handle FROM invocation_program_executions e "
            "JOIN invocation_programs p ON p.id=e.program_id "
            "JOIN invocation_receipts r ON r.id=p.parent_receipt_id "
            "WHERE r.session_id=? AND e.job_handle IS NOT NULL AND e.job_handle != ''",
            (session_id,),
        ).fetchall()
    handles = [r[0] for r in rows if r and r[0]]
    if not handles:
        return None
    call_ids, handle_outputs = [], {}
    for handle in handles:
        [(cid, res)] = send_calls(application, session_id, [{'name': 'command_output', 'args': {'handle': handle, 'cursor': 0}}])
        call_ids.append(cid)
        payload = _parse_body(res)
        chunks = payload.get('chunks') if isinstance(payload.get('chunks'), list) else []
        stdout = ''.join(str(c.get('text') or '') for c in chunks if isinstance(c, dict) and c.get('stream') == 'stdout')
        handle_outputs[handle] = {
            'outcome': res.get('outcome'), 'returned_handle': payload.get('handle'),
            'stdout': stdout, 'running': payload.get('running'), 'exit_code': payload.get('exit_code'),
        }
    [(bad_cid, bad_res)] = send_calls(application, session_id, [{'name': 'command_output', 'args': {'handle': 'nonexistent-job-handle', 'cursor': 0}}])
    call_ids.append(bad_cid)
    return {
        'handles': handles, 'call_ids': call_ids, 'handle_outputs': handle_outputs,
        'wrong_handle_rejected': bad_res.get('outcome') == 'rejected',
    }


def inspect_temp_handoff_and_jobs(case, *, session_id, audit, receipts, sidecar, last_payload, turn_records=None):
    """Extract Q11 and Q16 facts strictly from SQLite execution/pin rows, job handle probes, and sidecar logs."""
    execs = audit['invocation_program_executions']
    pins = audit['invocation_program_revision_pins']
    facts = {}

    # 1) Q11 temp-native-process-handoff: session-temp pins + external declared_at_launch disk provenance.
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

    # 2) Q16 owned-background-state: route output through job_handle, match terminal exit, and correlate process_control=false.
    bg_execs = [e for e in execs if e.get('job_handle')]
    wait_execs = [e for e in execs if (e.get('native_command') or e.get('operation_id')) == 'wait' and e.get('state') == 'completed']
    if bg_execs and wait_execs:
        bg = bg_execs[-1]
        same_prog_waits = [w for w in wait_execs if w.get('program_id') == bg.get('program_id')]
        probe = next((r.get('job_probe') for r in (turn_records or ()) if isinstance(r.get('job_probe'), dict)), None)
        if case.get('id') == 'owned-background-state':
            if probe and bg['job_handle'] in (probe.get('handle_outputs') or {}):
                ho = probe['handle_outputs'][bg['job_handle']]
                facts['captured_environment_cwd'] = bool(
                    ho.get('outcome') == 'completed' and ho.get('returned_handle') == bg['job_handle']
                    and ho.get('running') is False and ho.get('stdout') == 'before\n'
                )
            else:
                facts['captured_environment_cwd'] = (last_payload or {}).get('stdout') == 'before\n'
        bg_code = bg.get('status_code')
        wait_code = same_prog_waits[-1].get('status_code') if same_prog_waits else None
        if isinstance(bg_code, int) and isinstance(wait_code, int):
            probe_exit_ok = True
            if probe and bg['job_handle'] in (probe.get('handle_outputs') or {}):
                ho_exit = probe['handle_outputs'][bg['job_handle']].get('exit_code')
                probe_exit_ok = (ho_exit is None or ho_exit == bg_code)
            facts['actual_exit'] = bool(same_prog_waits and wait_code == bg_code and probe_exit_ok)
        if isinstance(sidecar, dict) and sidecar.get('available') and sidecar.get('complete') and sidecar.get('lines'):
            wrong_ok = True if probe is None else bool(probe.get('wrong_handle_rejected') is True)
            facts['owned_descendants_only'] = bool(wrong_ok and same_prog_waits and any(
                'component=sandbox' in (m.get('excerpt') or '')
                and 'host_execution=false' in (m.get('excerpt') or '')
                and 'process_control=false' in (m.get('excerpt') or '')
                for m in sidecar['lines']
            ))
    return facts
