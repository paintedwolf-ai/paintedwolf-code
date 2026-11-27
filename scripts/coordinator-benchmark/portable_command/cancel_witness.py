"""Live command abort/cancellation witness driver (Q15/Q16; explicit continued-program Stop tracked under ST01-ST03)."""
import contextlib
import sqlite3
import time
import uuid
import smoke_workers


def _wait_settled(app, sid):
    app.wait_for(
        lambda: app.request(f'/v1/sessions/{sid}').get('status') in {'idle', 'interrupted', 'completed'},
        'cancellation session settled',
    )


def run_cancellation_turn(app, session_id, project_id, db_path, surface, send_calls, program):
    """Submit a live loop command, trigger POST /v1/sessions/{id}/abort once 3 children admit, and verify quiet teardown + fresh recovery."""
    req = app.wait_for(lambda: smoke_workers.pending(app, session_id), 'cancellation turn', session_id)
    call_id = str(uuid.uuid4())
    app.request('/harness/llm/respond', {'id': req['id'], 'tool_calls': [{'id': call_id, 'name': 'command', 'args': {'command': program}}]})

    def _admitted():
        with contextlib.closing(sqlite3.connect(db_path)) as db:
            row = db.execute(
                "SELECT p.id, (SELECT count(*) FROM invocation_program_executions e "
                "WHERE e.program_id=p.id AND (e.operation_id IS NULL OR e.operation_id NOT LIKE 'program %') "
                "AND e.owner_started=1 AND e.owner_invoked=1) "
                "FROM invocation_programs p JOIN invocation_receipts r ON r.id=p.parent_receipt_id WHERE r.tool_call_id=?",
                (call_id,),
            ).fetchone()
        return row if (row and row[1] >= 3) else None

    cancel_pid, _ = app.wait_for(_admitted, 'cancellation active child started', session_id, timeout=30)
    app.request(f'/v1/sessions/{session_id}/abort', {'reason': 'q15_command_abort_cancellation'})
    _wait_settled(app, session_id)

    with contextlib.closing(sqlite3.connect(db_path)) as db:
        p_row = db.execute('SELECT state, status_code, stop_code FROM invocation_programs WHERE id=?', (cancel_pid,)).fetchone()
        count_at_idle = db.execute(
            "SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND (operation_id IS NULL OR operation_id NOT LIKE 'program %')",
            (cancel_pid,),
        ).fetchone()[0]
        unsettled = db.execute(
            "SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND state IN ('prepared','waiting','running')",
            (cancel_pid,),
        ).fetchone()[0]
        last_child = db.execute(
            "SELECT id, state, status_code, owner_started, owner_invoked, effect_certainty "
            "FROM invocation_program_executions WHERE program_id=? AND (operation_id IS NULL OR operation_id NOT LIKE 'program %') "
            "ORDER BY ordinal DESC LIMIT 1",
            (cancel_pid,),
        ).fetchone()
    time.sleep(0.5)
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        count_after_wait = db.execute(
            "SELECT count(*) FROM invocation_program_executions WHERE program_id=? AND (operation_id IS NULL OR operation_id NOT LIKE 'program %')",
            (cancel_pid,),
        ).fetchone()[0]

    rec_sid = app.request('/v1/sessions', {'project_id': project_id, 'posture': 'build'})['id']
    recovery_ok = False
    try:
        app.request('/harness/model-limit', {'session_id': rec_sid, 'limit': 12})
        _wait_settled(app, rec_sid)
        app.request(f'/v1/sessions/{rec_sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': 'Post-cancellation recovery check.'})
        send_calls(app, rec_sid, [{'name': 'update_progress', 'args': {'content': '- [ ] Post-cancel recovery'}}], surface=surface)
        [(_, rec_res)] = send_calls(app, rec_sid, [{'name': 'command', 'args': {'command': "printf 'post-cancel-ok\\n'"}}], surface=surface)
        recovery_ok = rec_res.get('outcome') == 'completed' and 'post-cancel-ok' in str(rec_res.get('content') or '')
    finally:
        with contextlib.suppress(Exception):
            app.request(f'/v1/sessions/{rec_sid}/abort', {})
            _wait_settled(app, rec_sid)

    meta = {
        'control_surface': 'POST /v1/sessions/{id}/abort (command abort/cancellation; explicit continued-program Stop tracked under ST01-ST03)',
        'st01_st03_stop_fence_separate': True,
        'abort_barrier': 'active_child_started',
        'aborted_child_id': last_child[0] if last_child else '',
        'aborted_child_state': last_child[1] if last_child else '',
        'aborted_child_status': last_child[2] if last_child else None,
        'aborted_child_started': bool(last_child and last_child[3] == 1),
        'aborted_child_invoked': bool(last_child and last_child[4] == 1),
        'aborted_child_certainty': last_child[5] if last_child else '',
        'program_id': cancel_pid,
        'db_program': {'state': p_row[0], 'status_code': p_row[1], 'stop_code': p_row[2]} if p_row else None,
        'exec_count_at_idle': count_at_idle,
        'exec_count_after_wait': count_after_wait,
        'unsettled_children': unsettled,
        'recovery_ok': recovery_ok,
    }
    return [(call_id, {'outcome': 'interrupted'})], meta
