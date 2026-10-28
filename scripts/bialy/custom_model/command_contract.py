"""Admission against persisted native program and child execution receipts."""
import sqlite3

EFFECTS = {"none", "completed", "attempted", "unknown"}
UNSAFE_EFFECTS = {"attempted", "unknown"}
SETTLED = {"completed", "refused", "failed", "interrupted"}


def program(db, receipt_id):
    try:
        value = db.execute("SELECT * FROM invocation_programs WHERE parent_receipt_id=?", (receipt_id,)).fetchone()
        if value is None:
            raise ValueError("command program receipt unavailable")
        children = [dict(row) for row in db.execute(
            "SELECT id, state, status_code, effect_certainty, settled_at FROM invocation_program_executions WHERE program_id=? ORDER BY ordinal", (value['id'],))]
    except sqlite3.OperationalError as exc:
        raise ValueError("command child receipts unavailable in this store") from exc
    if any(child['effect_certainty'] not in EFFECTS for child in children):
        raise ValueError("command child has an unknown effect certainty")
    return dict(value), children


def unsafe(value, children):
    return (any(child['effect_certainty'] in UNSAFE_EFFECTS for child in children)
            or value['progress_state'] == 'unknown'
            or (value['state'] != 'completed' and any(child['effect_certainty'] == 'completed' for child in children)))


def admit_program(db, receipt, call_args_digest):
    if receipt['tool_name'] != 'command':
        return []
    value, children = program(db, receipt['id'])
    if not children:
        raise ValueError('command program has no child operation receipts')
    if (not value['source_exact'] or not value['syntax_validated'] or not value['settled_at']
            or any(child['state'] not in SETTLED or not child['settled_at'] for child in children)):
        raise ValueError('command program or child operation did not settle')
    if unsafe(value, children):
        raise ValueError('command program left partial or unknown effects')
    if value['state'] != 'completed' or value['status_code'] != 0:
        raise ValueError('command program did not succeed')
    if any(child['state'] != 'completed' or child['status_code'] != 0 for child in children):
        raise ValueError('command child operation did not succeed')
    earlier = db.execute('SELECT id FROM invocation_receipts WHERE session_id=? AND tool_name=? AND args_digest=? AND started_at<?',
                         (receipt['session_id'], 'command', call_args_digest, receipt['started_at'])).fetchall()
    for prior in earlier:
        old, old_children = program(db, prior['id'])
        if unsafe(old, old_children):
            raise ValueError('whole-program replay after partial or unknown effects')
    return [child['id'] for child in children]
