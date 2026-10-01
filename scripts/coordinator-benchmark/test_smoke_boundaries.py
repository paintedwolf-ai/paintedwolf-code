import pathlib
import sqlite3
import tempfile
import unittest
from smoke_boundaries import service_command_completed


class ServiceCompletionTests(unittest.TestCase):
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
