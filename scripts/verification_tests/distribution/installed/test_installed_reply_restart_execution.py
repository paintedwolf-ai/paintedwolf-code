"""A tool result alone cannot certify durable fixed reply and restart identity."""
from contextlib import ExitStack, nullcontext
import hashlib
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.installed_cases import reply_observer as observer, reply_restart as owner
from linux_release import installed_execution
from linux_release.installed_input.protocol import steps

PROJECT = '10000000-0000-4000-8000-000000000001'
SESSION = '10000000-0000-4000-8000-000000000002'
OUTPUT = '10000000-0000-4000-8000-000000000003'
MESSAGE = '10000000-0000-4000-8000-000000000004'


class ReplyObserverTests(unittest.TestCase):
    def fixture(self, root, *, model=observer.MODEL, digest=observer.DIGEST, status='idle'):
        database = sqlite3.connect(':memory:')
        self.addCleanup(database.close)
        database.executescript('CREATE TABLE sessions(id,project_id,status); '
            'CREATE TABLE content_blob_objects(project_id,sha256,byte_size,stored_size); '
            'CREATE TABLE model_outputs(id,message_id,content_blob_sha256,project_id,session_id,provider_id,model,finish_reason,settled_at,scripted);')
        database.execute('INSERT INTO sessions VALUES(?,?,?)', (SESSION, PROJECT, status))
        database.execute('INSERT INTO content_blob_objects VALUES(?,?,?,?)', (PROJECT, digest, len(observer.REPLY), 4))
        database.execute('INSERT INTO model_outputs VALUES(?,?,?,?,?,?,?,?,?,?)',
            (OUTPUT, MESSAGE, digest, PROJECT, SESSION, 'fixture', model, 'stop', 'settled', 0))
        path = root / 'projects' / PROJECT / 'model-content' / digest[:2] / digest[2:]
        path.parent.mkdir(parents=True)
        path.write_bytes(b'zstd')
        return database, path

    def observe(self, root, database, *, body=observer.REPLY, code=0):
        child = MagicMock()
        child.stdout.read.return_value = body
        child.wait.return_value = code
        child.poll.return_value = code
        with patch.object(observer, '_database', return_value=nullcontext(database)), patch.object(
                observer, 'profile', return_value=nullcontext(root)), patch.object(observer, 'physical'), patch.object(
                observer.subprocess, 'Popen', return_value=child) as spawned:
            result = observer.committed_reply(None, PROJECT, SESSION, 'fixture')
        return result, spawned, child

    def test_sql_settlement_and_physical_fixed_body_both_required(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database, path = self.fixture(root)
            result, spawned, child = self.observe(root, database)
            self.assertEqual(result['content_blob_sha256'], hashlib.sha256(observer.REPLY).hexdigest())
            self.assertTrue(result['fixed_reply_verified'])
            self.assertEqual(spawned.call_args.args[0], ['zstd', '--decompress', '--stdout', '--quiet'])
            child.stdout.read.assert_called_once_with(len(observer.REPLY) + 1)
            child.stdout.close.assert_called_once()
            for body, code in ((b'wrong', 0), (observer.REPLY + b'x', 0), (observer.REPLY, 1)):
                with self.subTest(body=body, code=code), self.assertRaises(ValueError):
                    self.observe(root, database, body=body, code=code)

    def test_busy_or_foreign_model_is_never_a_committed_reply(self):
        for changes in ({'status': 'busy'}, {'model': 'foreign'}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                database, _ = self.fixture(root, **changes)
                result, spawned, _ = self.observe(root, database)
                self.assertIsNone(result)
                spawned.assert_not_called()

    def test_wrong_content_reference_and_missing_or_changed_object_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database, path = self.fixture(root, digest='b' * 64)
            with self.assertRaisesRegex(ValueError, 'fixed content'):
                self.observe(root, database)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database, path = self.fixture(root)
            path.write_bytes(b'changed-size')
            with self.assertRaisesRegex(ValueError, 'size differs'):
                self.observe(root, database)
            path.unlink()
            with self.assertRaises(FileNotFoundError):
                self.observe(root, database)


class ReplyRestartTests(unittest.TestCase):
    def test_registry_and_physical_operation_are_fixed(self):
        self.assertEqual(installed_execution.case_family('conversation', 'restart-and-reconnect', 'deb'), 'reply_restart')
        self.assertEqual(steps('reply-fixture-prompt-v1')[0], ('ctrl', 'a'))
        self.assertNotIn(('ret',), steps('reply-fixture-prompt-v1'))
        with self.assertRaises(ValueError):
            steps('reply-fixture-prompt-v1-arbitrary')

    def test_gui_setup_selects_model_then_finishes_optional_setup_before_turn(self):
        app, ui = MagicMock(), MagicMock()
        fixture = SimpleNamespace(server=SimpleNamespace(server_port=12345), base_url='http://127.0.0.1:12345/v1')
        ui.__enter__.return_value = ui
        with patch.object(owner, 'DesktopUI', return_value=ui), patch.object(owner, 'physical_input') as physical, patch.object(
                owner, 'find_gui_provider', return_value={'provider_id': 'fixture', 'models': [owner.MODEL]}):
            self.assertEqual(owner.configure_provider(app, fixture)['provider_id'], 'fixture')
        self.assertEqual([call.args[0] for call in ui.click.call_args_list],
                         ['Add AI provider', 'LiteLLM', 'Add AI provider', 'Default model', owner.MODEL, 'Skip'])
        self.assertEqual([call.args[3] for call in physical.call_args_list],
                         ['provider-name-v1', 'provider-endpoint-v1-12345', 'provider-fixture-credential-v1'])

    def test_input_without_independently_bound_physical_actor_is_unverified(self):
        with self.assertRaises(owner.CaseUnavailable):
            owner.physical_input(SimpleNamespace(binding={}), None, 'Describe your idea', 'reply-fixture-prompt-v1')

    def execute(self, root, *, after=None, completed=True, same_engine=False):
        before = {'session_id': SESSION, 'content_blob_sha256': observer.DIGEST}
        apps = [MagicMock(), MagicMock()]
        for i, app in enumerate(apps):
            app.__enter__.return_value = app
            app.observations = {'engine_identity': {'pid': 1 if same_engine else i + 1}}
            app.streams.return_value = {}
        fixture, ui = MagicMock(), MagicMock()
        fixture.__enter__.return_value = fixture
        fixture.observations = [{'completed': completed}]
        ui.__enter__.return_value = ui
        with ExitStack() as stack:
            for name, value in [('applicability', ({'bootstrap': {}}, None)), ('interactive_window', {}),
                    ('configure_provider', {'provider_id': 'fixture'}), ('physical_input', {}),
                    ('run', {'exit_code': 0, 'timed_out': False, 'output_limit': False})]:
                stack.enter_context(patch.object(owner, name, return_value=value))
            stack.enter_context(patch.object(owner, 'InstalledApp', side_effect=apps))
            stack.enter_context(patch.object(owner.ProviderFixture, 'gui', return_value=fixture))
            stack.enter_context(patch.object(owner, 'DesktopUI', return_value=ui))
            stack.enter_context(patch.object(owner, 'await_reply', side_effect=[before, after or before]))
            retained = stack.enter_context(patch.object(owner, 'retain', side_effect=lambda *args: args))
            owner.execute({}, 'conversation', 'restart-and-reconnect', root)
        return retained.call_args.args, apps, ui

    def test_restart_requires_same_reply_and_a_new_captured_engine(self):
        with tempfile.TemporaryDirectory() as directory:
            result, apps, ui = self.execute(Path(directory))
            self.assertEqual(result[4], 'passed')
            self.assertEqual(result[5]['reply_before'], result[5]['reply_after'])
            for app in apps:
                app.__exit__.assert_called_once()
            ui.click.assert_called_once_with('Start')
        for changes in ({'after': {'session_id': SESSION, 'content_blob_sha256': 'b' * 64}},
                        {'completed': False}, {'same_engine': True}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(ValueError):
                    self.execute(Path(directory), **changes)
