import contextlib
import pathlib
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock

from application_ledger import checkpoint_closed_store
from closed_store import closed_ledger


class ApplicationLedgerTests(unittest.TestCase):
    def test_exit_with_committed_wal_preserves_rows_for_immutable_grading(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            database = root / 'store.db'
            process = subprocess.Popen([sys.executable, '-c', '''
import os,sqlite3,sys
connection=sqlite3.connect(sys.argv[1])
connection.execute('PRAGMA journal_mode=WAL')
connection.execute('PRAGMA wal_autocheckpoint=0')
connection.execute('CREATE TABLE outcomes (value TEXT)')
connection.execute("INSERT INTO outcomes VALUES ('committed')")
connection.commit()
connection.execute("INSERT INTO outcomes VALUES ('uncommitted')")
os._exit(0)
''', str(database)])
            self.assertEqual(process.wait(timeout=30), 0)
            self.assertGreater((root / 'store.db-wal').stat().st_size, 0)
            with self.assertRaisesRegex(ValueError, 'uncheckpointed'):
                with closed_ledger(root):
                    self.fail('unprepared ledger was accepted')
            receipt = checkpoint_closed_store(database, process)
            self.assertEqual(receipt['busy'], 0)
            with closed_ledger(root) as db:
                self.assertEqual([r['value'] for r in db.execute('SELECT value FROM outcomes')], ['committed'])
            checkpoint_closed_store(database, process)

    def test_empty_journal_header_is_checkpointed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            database = root / 'store.db'
            with contextlib.closing(sqlite3.connect(database)) as db:
                db.execute('PRAGMA journal_mode=WAL')
                db.execute('CREATE TABLE outcomes (value TEXT)')
                db.commit()
                header = (root / 'store.db-wal').read_bytes()[:32]
                self.assertEqual(db.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone(), (0, 0, 0))
            (root / 'store.db-wal').write_bytes(header)
            checkpoint_closed_store(database, Mock(poll=Mock(return_value=0)))
            with closed_ledger(root) as db:
                self.assertEqual(db.execute('SELECT COUNT(*) FROM outcomes').fetchone()[0], 0)

    def test_active_application_cannot_be_checkpointed(self):
        with tempfile.TemporaryDirectory() as temporary:
            database = pathlib.Path(temporary) / 'store.db'
            with self.assertRaisesRegex(ValueError, 'must exit'):
                checkpoint_closed_store(database, Mock(poll=Mock(return_value=None)))
            self.assertFalse(database.exists())

    def test_other_writer_prevents_immutable_handoff(self):
        with tempfile.TemporaryDirectory() as temporary:
            database = pathlib.Path(temporary) / 'store.db'
            with contextlib.closing(sqlite3.connect(database)) as db:
                db.execute('PRAGMA journal_mode=WAL')
                db.execute('CREATE TABLE outcomes (value TEXT)')
                db.commit()
                db.execute('BEGIN IMMEDIATE')
                with self.assertRaisesRegex(ValueError, 'checkpoint remains blocked'):
                    checkpoint_closed_store(database, Mock(poll=Mock(return_value=0)), timeout=0)

    def test_missing_ledger_is_not_created(self):
        with tempfile.TemporaryDirectory() as temporary:
            database = pathlib.Path(temporary) / 'store.db'
            with self.assertRaisesRegex(ValueError, 'regular file'):
                checkpoint_closed_store(database, Mock(poll=Mock(return_value=0)))
            self.assertFalse(database.exists())
