"""Read an owned closed store for capture maintenance and cost analysis."""
import sqlite3
from contextlib import closing, contextmanager


@contextmanager
def closed_ledger(capture):
    database = capture / 'store.db'
    wal = database.with_name(database.name + '-wal')
    if wal.exists() and wal.stat().st_size:
        raise ValueError('worker ledger has uncheckpointed writes; wait for application shutdown')
    with closing(sqlite3.connect(database.resolve().as_uri() + '?mode=ro&immutable=1', uri=True)) as db:
        db.row_factory = sqlite3.Row
        yield db


