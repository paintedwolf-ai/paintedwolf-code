"""Session-temp handoff (Q11) and owned background job lifecycle (Q16) oracles."""


def inspect_temp_handoff_and_jobs(case, *, session_id, audit, receipts, sidecar, last_payload):
    """Extract Q11 and Q16 facts strictly from SQLite execution/pin rows and sidecar confinement logs."""
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

    # 2) Q16 owned-background-state: job_handle, captured env, matching wait exit status, and process_control confinement.
    bg_execs = [e for e in execs if e.get('job_handle')]
    wait_execs = [e for e in execs if (e.get('native_command') or e.get('operation_id')) == 'wait' and e.get('state') == 'completed']
    if bg_execs and wait_execs:
        if case.get('id') == 'owned-background-state':
            facts['captured_environment_cwd'] = (last_payload or {}).get('stdout') == 'before\n'
        bg_codes = [e['status_code'] for e in bg_execs if isinstance(e.get('status_code'), int)]
        wait_codes = [w['status_code'] for w in wait_execs if isinstance(w.get('status_code'), int)]
        if bg_codes and wait_codes:
            facts['actual_exit'] = wait_codes[-1] == bg_codes[-1]
        if isinstance(sidecar, dict) and sidecar.get('available') and sidecar.get('complete') and sidecar.get('lines'):
            facts['owned_descendants_only'] = any(
                'component=sandbox' in (m.get('excerpt') or '')
                and 'process_control=' in (m.get('excerpt') or '')
                and 'process_control=none' not in (m.get('excerpt') or '')
                for m in sidecar['lines']
            )
    return facts
