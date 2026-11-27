"""Read-only SQLite audit loader for isolated Portable Command sessions."""
import contextlib
import pathlib
import sqlite3


def _rows(db, sql, params):
    return [dict(r) for r in db.execute(sql, params).fetchall()]


def _table_exists(db, name):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)).fetchone() is not None


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
        program_progress = _rows(db, f'SELECT execution_id,program_id,session_id,state,owner_invoked,detail_ref FROM invocation_program_progress WHERE program_id IN ({p_ph})', p_ids) if p_ids and _table_exists(db, 'invocation_program_progress') else []
        program_sources = _rows(db, f'SELECT program_id,source_unit_id,source_id,revision,sha256 FROM invocation_program_sources WHERE program_id IN ({p_ph})', p_ids) if p_ids and _table_exists(db, 'invocation_program_sources') else []
        session_progress = _rows(db, 'SELECT * FROM session_progress WHERE session_id=?', (session_id,)) if _table_exists(db, 'session_progress') else []
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
        'invocation_program_progress': program_progress, 'invocation_program_sources': program_sources,
        'session_progress': session_progress, 'invocation_program_deliveries': deliveries,
        'source_command_windows': windows, 'tool_result_messages': messages,
    }
