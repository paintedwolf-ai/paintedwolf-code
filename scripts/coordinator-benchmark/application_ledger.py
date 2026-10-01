"""Prepare an owned application store for immutable grading after shutdown."""
from contextlib import closing
import sqlite3


def checkpoint_closed_store(database, process, timeout=30):
    if process.poll() is None:
        raise ValueError('application must exit before checkpointing its ledger')
    if database.is_symlink() or not database.is_file():
        raise ValueError('application ledger is not a regular file')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=rw', uri=True, timeout=timeout)) as db:
        busy, frames, copied = db.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone()
        if busy or frames != copied:
            raise ValueError('application ledger checkpoint remains blocked')
    wal = database.with_name(database.name + '-wal')
    if wal.exists() and wal.stat().st_size:
        raise ValueError('application ledger changed after checkpointing')
    return {'busy': busy, 'log_frames': frames, 'checkpointed_frames': copied}
