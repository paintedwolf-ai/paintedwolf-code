"""Read-only SQLite audit loader and canonical receipt normalizer for Portable Command runs."""
import contextlib
import pathlib
import sqlite3

# Eligible structural orchestration wrappers emitted by the program runtime
# (pipeline/list/loop containers with no direct native command or file effect).
ELIGIBLE_WRAPPER_OPS = {'program |', 'program ;', 'program &&', 'program ||', 'program for', 'program while', 'program if'}


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


def _canonical_op(ex):
    op_id, native_cmd = ex.get('operation_id') or '', ex.get('native_command') or ''
    owner, canonical = ex.get('owner') or '', ex.get('canonical') or ''
    if op_id == 'write':
        return 'write'
    if op_id == 'read' and native_cmd in {'', 'cat', 'head', 'read'}:
        return 'read'
    if op_id == 'command' and native_cmd == 'external' and owner == 'processes':
        return 'external_exec'
    if native_cmd == 'tar' and owner == 'filesystem':
        if '-cjf' in canonical or ' -c' in f' {canonical}':
            return 'tar_create'
        if '-xjf' in canonical or ' -x' in f' {canonical}':
            return 'tar_extract'
    return native_cmd or op_id


def receipt_rows(audit, progress_receipts, draft_bindings=None):
    pubs_by_prog = {p['program_id']: p for p in audit['invocation_program_publications']}
    receipts = [{'execution_id': pr['id'], 'operation': 'update_progress', 'outcome': pr['status']} for pr in progress_receipts]
    effects = [{'operation': 'update_progress', 'outcome': pr['status']} for pr in progress_receipts]
    wrappers = []
    for ex in audit['invocation_program_executions']:
        op_id, native_cmd = ex.get('operation_id') or '', ex.get('native_command') or ''
        if (op_id in ELIGIBLE_WRAPPER_OPS and not native_cmd
                and ex.get('effect_certainty') in {None, '', 'none'}
                and not ex.get('accepted_revision') and not ex.get('published_revision')):
            wrappers.append({
                'execution_id': ex['id'], 'program_id': ex.get('program_id'), 'operation_id': op_id,
                'owner': ex.get('owner'), 'state': ex.get('state'), 'status_code': ex.get('status_code'),
            })
            continue
        op = _canonical_op(ex)
        outcome = 'rejected' if op == 'source' and ex.get('state') == 'refused' else ex['state']
        row = {'execution_id': ex['id'], 'operation': op, 'outcome': outcome}
        effect = {'operation': op, 'outcome': outcome}
        if isinstance(ex.get('status_code'), int):
            row['status'] = ex['status_code']
        if ex.get('publication_eligible') in {0, 1}:
            row['publication_eligible'] = bool(ex['publication_eligible'])
        if ex.get('consumer_closed') == 1:
            row['closure'] = 'expected-consumer'
        if op == 'read' and draft_bindings:
            src_pin = next((p for p in audit['invocation_program_revision_pins']
                            if p['execution_id'] == ex['id'] and p.get('role') == 'source' and p.get('mode') == 'accepted_document'), None)
            if src_pin:
                b = draft_bindings.get(str(src_pin.get('path') or '').lstrip('./'))
                if (isinstance(b, dict) and b.get('injected') is True and b.get('alias')
                        and b.get('revision') == src_pin.get('document_revision')
                        and (not src_pin.get('document_id') or b.get('document_id') == src_pin.get('document_id'))):
                    row['input_revision'] = b['alias']
        if op_id == 'write' or ex.get('accepted_revision') or ex.get('published_revision'):
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
        if ex.get('effect_certainty') in {'completed', 'attempted', 'unknown'} or op_id == 'write':
            effects.append(effect)
    return receipts, effects, wrappers
