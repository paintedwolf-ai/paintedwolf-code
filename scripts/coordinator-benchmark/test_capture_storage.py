import contextlib
import pathlib
import signal
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock
from capture_storage import PRIVATE_CONFIG_FILES, checkpoint_closed_store, cleanup_private_config, clone_file, closed_ledger, share_file, stage_runtime


class CaptureStorageTests(unittest.TestCase):
    def test_forced_launcher_exit_does_not_retain_copied_credentials(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for name in ('pw-logs', 'pw-document-core', 'bialy'):
                (root / name).write_bytes(name.encode())
                (root / name).chmod(0o755)
            source, captured = root / 'source', root / 'capture/config'
            source.mkdir()
            captured.mkdir(parents=True)
            for name in PRIVATE_CONFIG_FILES:
                (source / name).write_text('source value')
                (captured / name).write_text('copied value')
            evidence = captured / 'retained-settings.json'
            evidence.write_text('evidence')
            result = subprocess.run(['bash', '-c',
                'trap \'rm -f "$1/config/providers.local.yaml"\' EXIT; kill -KILL $$',
                'launcher', str(captured.parent)])
            self.assertEqual(result.returncode, -signal.SIGKILL)
            self.assertTrue((captured / 'providers.local.yaml').exists())
            cleanup_private_config(captured, {'kind': 'exited', 'returncode': result.returncode})
            cleanup_private_config(captured, {'kind': 'exited', 'returncode': result.returncode})
            self.assertTrue(all(not (captured / name).exists() for name in PRIVATE_CONFIG_FILES))
            self.assertTrue(all((source / name).read_text() == 'source value' for name in PRIVATE_CONFIG_FILES))
            self.assertEqual(evidence.read_text(), 'evidence')

    def test_unknown_execution_preserves_configuration(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = pathlib.Path(tmp)
            path = config / 'providers.local.yaml'
            path.write_text('active configuration')
            for state in ['execution_unknown', 'running', None]:
                cleanup_private_config(config, {'kind': state})
                self.assertEqual(path.read_text(), 'active configuration')

    def test_private_cleanup_never_follows_configuration_links(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for name in ('pw-logs', 'pw-document-core', 'bialy'):
                (root / name).write_bytes(name.encode())
                (root / name).chmod(0o755)
            source = root / 'source'
            source.mkdir()
            key = source / 'providers.local.yaml'
            key.write_text('source value')
            linked = root / 'linked-config'
            linked.symlink_to(source, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, 'directory is a symlink'):
                cleanup_private_config(linked, {'kind': 'exited'})
            config = root / 'config'
            config.mkdir()
            (config / key.name).symlink_to(key)
            cleanup_private_config(config, {'kind': 'exited'})
            self.assertFalse((config / key.name).is_symlink())
            self.assertEqual(key.read_text(), 'source value')

    def test_active_runtime_copies_cannot_mutate_the_frozen_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for name in ('pw-logs', 'pw-document-core', 'bialy'):
                (root / name).write_bytes(name.encode())
                (root / name).chmod(0o755)
            engine, catalog, capture = root / 'binary', root / 'catalog', root / 'capture'
            engine.write_bytes(b'engine')
            engine.chmod(0o755)
            catalog.mkdir()
            (catalog / 'policy').write_text('original')
            (capture / 'module').mkdir(parents=True)
            stage_runtime(engine, catalog, capture)
            for name in ('pw-logs', 'pw-document-core', 'bialy'):
                self.assertFalse((root / name).samefile(capture / name))
                self.assertEqual((capture / name).stat().st_mode & 0o777, 0o755)
                (capture / name).write_bytes(b'changed')
                self.assertEqual((root / name).read_bytes(), name.encode())
            self.assertFalse(engine.samefile(capture / 'engine'))
            self.assertEqual((capture / 'engine').stat().st_mode & 0o755, 0o755)
            (capture / 'engine').write_bytes(b'changed')
            (capture / 'module/config/policy').write_text('changed')
            self.assertEqual(engine.read_bytes(), b'engine')
            self.assertEqual((catalog / 'policy').read_text(), 'original')
            with self.assertRaises(FileExistsError):
                clone_file(engine, capture / 'engine')

    def test_incomplete_runtime_is_rejected_before_capture(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            engine = root / 'engine'
            engine.write_bytes(b'engine')
            engine.chmod(0o755)
            with self.assertRaisesRegex(ValueError, 'runtime executable is unavailable'):
                stage_runtime(engine, root / 'catalog', root / 'capture')
            self.assertFalse((root / 'capture').exists())

    def test_only_identical_completed_files_share_storage(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for name in ('pw-logs', 'pw-document-core', 'bialy'):
                (root / name).write_bytes(name.encode())
                (root / name).chmod(0o755)
            original, captured = root / 'original', root / 'captured'
            original.write_bytes(b'frozen runtime')
            captured.write_bytes(b'frozen runtime')
            share_file(original, captured)
            self.assertTrue(original.samefile(captured))
            share_file(original, captured)
            self.assertEqual(captured.read_bytes(), b'frozen runtime')
            changed = root / 'changed'
            changed.write_bytes(b'changed source')
            share_file(original, changed)
            self.assertEqual(changed.read_bytes(), b'changed source')
            self.assertFalse(original.samefile(changed))
            link = root / 'link'
            link.symlink_to(changed)
            share_file(original, link)
            self.assertTrue(link.is_symlink())


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


if __name__ == '__main__':
    unittest.main()
