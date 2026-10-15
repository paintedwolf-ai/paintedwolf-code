import pathlib
import json
import sqlite3
import tempfile
import unittest
from unittest import mock
from smoke_boundaries import fixture, resolve, service_command_completed


class ServiceCompletionTests(unittest.TestCase):
    def test_permission_fixture_owns_and_removes_its_private_input(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch('pathlib.Path.home',return_value=pathlib.Path(tmp)):
            project=pathlib.Path(tmp)/'project'
            project.mkdir()
            for kind in ['approve_read','deny_read']:
                with fixture(project,kind) as owned:
                    path=pathlib.Path(owned['path'])
                    self.assertEqual(path.name,'input.json')
                    self.assertTrue(path.is_relative_to(pathlib.Path(tmp)/'Documents'))
                    self.assertFalse(path.is_relative_to(project))
                    self.assertEqual(json.loads(path.read_text()),{'receipt':owned['receipt']})
                self.assertFalse(path.exists())
                self.assertFalse((pathlib.Path(tmp)/'Documents').exists())

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


class ApprovalBoundaryTests(unittest.TestCase):
    def test_service_selects_exact_chat_lease(self):
        options = [
            {'id':'once','rung':'once','kind':'current_action','decision_action':'approve'},
            {'id':'day','rung':'day','kind':'lease','scope':'chat','decision_action':'approve'},
            {'id':'chat','rung':'chat','kind':'lease','scope':'chat','decision_action':'approve'},
            {'id':'project','rung':'project','kind':'lease','scope':'project','decision_action':'approve'},
        ]
        checkpoint={'id':'checkpoint','tool_approval':{'tool_call_id':'call','plan':{'options':options}}}
        app=mock.Mock(approval_guidance='Declined.')
        app.request.side_effect=[{'checkpoints':[checkpoint]},{}]
        resolve(app,'session','call','loopback')
        app.request.assert_called_with('/v1/sessions/session/checkpoints/checkpoint',
            {'kind':'tool_approval','action':'approve','option_id':'chat'})
        with tempfile.TemporaryDirectory() as tmp:
            app.directory=pathlib.Path(tmp)
            for altered in [dict(options[2],disabled=True),dict(options[2],scope='project'),dict(options[2],rung='day')]:
                checkpoint['tool_approval']['plan']['options']=[options[0],altered]
                app.request.side_effect=[{'checkpoints':[checkpoint]}]
                with self.assertRaisesRegex(RuntimeError,'no unique host-authored authority option'):
                    resolve(app,'session','call','loopback')
            checkpoint['tool_approval']['plan']={
                'directory_scopes':['/out/leaf','/out','/'],
                'options':[
                    options[0],
                    {'id':'leaf','rung':'chat','kind':'lease','scope':'chat','directory_scope':'/out/leaf','decision_action':'approve'},
                    {'id':'parent','rung':'chat','kind':'lease','scope':'chat','directory_scope':'/out','decision_action':'approve'},
                ],
            }
            app.request.side_effect=[{'checkpoints':[checkpoint]},{}]
            resolve(app,'session','call','write_root')
            app.request.assert_called_with('/v1/sessions/session/checkpoints/checkpoint',
                {'kind':'tool_approval','action':'approve','option_id':'leaf'})
