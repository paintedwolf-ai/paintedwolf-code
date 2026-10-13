import pathlib
import json
import sqlite3
import tempfile
import unittest
from smoke_boundaries import fixture, service_command_completed


class ServiceCompletionTests(unittest.TestCase):
    def test_permission_fixture_owns_and_removes_its_protected_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            project=pathlib.Path(tmp)
            for kind in ['approve_read','deny_read']:
                with fixture(project,kind) as owned:
                    path=pathlib.Path(owned['path'])
                    self.assertEqual(path.name,'.env')
                    self.assertFalse(path.is_relative_to(project))
                    self.assertEqual(json.loads(path.read_text()),{'receipt':owned['receipt']})
                self.assertFalse(path.exists())

    def test_file_creation_does_not_imply_terminal_execution(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp)
            database=root/'store.db'
            with sqlite3.connect(database) as db:
                db.executescript('''
                    CREATE TABLE invocation_receipts(session_id,tool_call_id,status,invoked);
                    CREATE TABLE source_command_windows(session_id,tool_call_id,state);
                    INSERT INTO invocation_receipts VALUES ('session','call','running',1);
                    INSERT INTO source_command_windows VALUES ('session','call','running');
                ''')
            (root/'receipt.json').write_text('')
            self.assertFalse(service_command_completed(database,'session','call'))
            with sqlite3.connect(database) as db:
                db.execute("UPDATE invocation_receipts SET status='completed'")
            self.assertFalse(service_command_completed(database,'session','call'))
            (root/'receipt.json').write_text('{"receipt":"complete"}')
            with sqlite3.connect(database) as db:
                db.execute("UPDATE source_command_windows SET state='ended'")
            self.assertTrue(service_command_completed(database,'session','call'))
            self.assertFalse(service_command_completed(database,'session','unrelated'))
            with sqlite3.connect(database) as db:
                db.execute("UPDATE source_command_windows SET state='interrupted'")
            with self.assertRaisesRegex(RuntimeError,'interrupted'):
                service_command_completed(database,'session','call')
