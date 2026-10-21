"""Admission of native command programs on the command tool surface.

Written ahead of the command rework against the P32 (#1083) and P33 (#1084)
contracts: a command call keeps its real tool-call identity, its child
operations carry persisted receipts with effect certainty (completed, failed,
not started, unknown), and whole-program replay is never a positive after
partial or unknown effects. The child-receipt table and column names below are
the adaptation point once P10/P32 persist them; the rules do not change. On the
legacy surface command receipts are admitted as any other tool's.
"""
import sqlite3

COMMAND_TOOL = "command"
CHILD_TABLE = "program_operations"
EFFECTS = {"none", "completed", "failed", "not_started", "partial", "unknown"}
UNSAFE_EFFECTS = {"partial", "unknown"}


def _children(db, receipt_id):
    try:
        rows = db.execute("SELECT operation, status, effect FROM %s WHERE program_receipt_id=? ORDER BY rowid" % CHILD_TABLE,
                          (receipt_id,)).fetchall()
    except sqlite3.OperationalError as exc:
        raise ValueError("command child receipts unavailable in this store") from exc
    children = [dict(r) for r in rows]
    if any(c["effect"] not in EFFECTS for c in children):
        raise ValueError("command child receipt carries an unknown effect certainty")
    return children


def admit_program(db, receipt, call_args_digest):
    """Admit one completed command invocation; return its child operation ids."""
    if receipt["tool_name"] != COMMAND_TOOL:
        return []
    children = _children(db, receipt["id"])
    if not children:
        raise ValueError("command program has no child operation receipts")
    if any(c["status"] not in ("completed", "rejected", "error", "interrupted") for c in children):
        raise ValueError("command child operation did not settle")
    if any(c["effect"] in UNSAFE_EFFECTS for c in children):
        raise ValueError("command program left partial or unknown effects")
    # A program identical to an earlier one in the session whose effects were partial
    # or unknown is a whole-program replay, which recovery guidance never offers.
    earlier = db.execute("SELECT id FROM invocation_receipts WHERE session_id=? AND tool_name=? AND args_digest=? AND started_at<?",
                         (receipt["session_id"], COMMAND_TOOL, call_args_digest, receipt["started_at"])).fetchall()
    for prior in earlier:
        if any(c["effect"] in UNSAFE_EFFECTS for c in _children(db, prior["id"])):
            raise ValueError("whole-program replay after partial or unknown effects")
    return [c["operation"] for c in children]
