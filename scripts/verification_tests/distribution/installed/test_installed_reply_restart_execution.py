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
from linux_release.campaign import execution as installed_execution
from linux_release.installed_cases import desktop_launch, provider_setup
from linux_release.installed_input.protocol import steps

PROJECT = '10000000-0000-4000-8000-000000000001'
SESSION = '10000000-0000-4000-8000-000000000002'
OUTPUT = '10000000-0000-4000-8000-000000000003'
MESSAGE = '10000000-0000-4000-8000-000000000004'
TURN = '10000000-0000-4000-8000-000000000005'
ATTEMPT = '10000000-0000-4000-8000-000000000006'


class ReplyObserverTests(unittest.TestCase):
    def fixture(self, root, *, model=observer.MODEL, digest=observer.DIGEST, status='idle'):
        database = sqlite3.connect(':memory:')
        self.addCleanup(database.close)
        database.executescript('CREATE TABLE sessions(id,project_id,status,provider_id,model,parent_session_id); '
            'CREATE TABLE content_blob_objects(project_id,sha256,byte_size,stored_size); '
            'CREATE TABLE model_outputs(id,message_id,content_blob_sha256,project_id,session_id,provider_id,model,finish_reason,settled_at,scripted,turn_attempt_id); '
            'CREATE TABLE turns(id,project_id,session_id,active_attempt_id,final_output_id,origin,status,completed_at); '
            'CREATE TABLE turn_attempts(id,turn_id,status,phase,completed_at);')
        database.execute('INSERT INTO sessions VALUES(?,?,?,?,?,?)', (SESSION, PROJECT, status, None, None, None))
        database.execute('INSERT INTO content_blob_objects VALUES(?,?,?,?)', (PROJECT, digest, len(observer.REPLY), 4))
        database.execute('INSERT INTO model_outputs VALUES(?,?,?,?,?,?,?,?,?,?,?)',
            (OUTPUT, MESSAGE, digest, PROJECT, SESSION, 'fixture', model, '', 'settled', 0, ATTEMPT))
        database.execute('INSERT INTO turns VALUES(?,?,?,?,?,?,?,?)',
            (TURN, PROJECT, SESSION, ATTEMPT, OUTPUT, 'user', 'complete', 'completed'))
        database.execute('INSERT INTO turn_attempts VALUES(?,?,?,?,?)',
            (ATTEMPT, TURN, 'complete', 'complete', 'completed'))
        path = root / 'projects' / PROJECT / 'model-content' / digest[:2] / digest[2:]
        path.parent.mkdir(parents=True)
        path.write_bytes(b'zstd')
        return database, path

    def observe(self, root, database, *, body=observer.REPLY):
        with patch.object(observer, '_database', return_value=nullcontext(database)), patch.object(
                observer, 'read_object', return_value=body) as read:
            result = observer.committed_reply(None, PROJECT, SESSION, 'fixture')
        return result, read

    def test_sql_settlement_and_physical_fixed_body_both_required(self):
        with tempfile.TemporaryDirectory() as directory:
            database, _ = self.fixture(Path(directory))
            result, read = self.observe(Path(directory), database)
            self.assertEqual(result['content_blob_sha256'], hashlib.sha256(observer.REPLY).hexdigest())
            self.assertTrue(result['fixed_reply_verified'])
            self.assertEqual(observer.REPLY, b'{"content":"Fixture complete.","tool_calls_json":[],"reasoning_json":null}')
            read.assert_called_once_with(None, PROJECT, observer.DIGEST, len(observer.REPLY), 4, maximum=len(observer.REPLY))
            for body in (b'wrong', observer.REPLY + b'x'):
                with self.subTest(body=body), self.assertRaises(ValueError):
                    self.observe(Path(directory), database, body=body)

    def test_busy_or_foreign_model_is_never_a_committed_reply(self):
        for changes in ({'status': 'busy'}, {'model': 'foreign'}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                database, _ = self.fixture(root, **changes)
                result, spawned = self.observe(root, database)
                self.assertIsNone(result)
                spawned.assert_not_called()

    def test_terminal_links_and_exact_root_selection_are_required(self):
        changes = (
            ("UPDATE turns SET status='running'", ()),
            ("UPDATE turns SET completed_at=''", ()),
            ("UPDATE turns SET origin='worker'", ()),
            ("UPDATE turns SET final_output_id=?", (MESSAGE,)),
            ("UPDATE turns SET active_attempt_id=?", (MESSAGE,)),
            ("UPDATE turns SET project_id=?", (MESSAGE,)),
            ("UPDATE turn_attempts SET turn_id=?", (MESSAGE,)),
            ("UPDATE turn_attempts SET phase='tools'", ()),
            ("UPDATE turn_attempts SET status='failed'", ()),
            ("UPDATE turn_attempts SET completed_at=''", ()),
            ("UPDATE model_outputs SET turn_attempt_id=?", (MESSAGE,)),
            ("UPDATE model_outputs SET settled_at=''", ()),
            ("UPDATE model_outputs SET scripted=1", ()),
            ("UPDATE sessions SET parent_session_id=?", (MESSAGE,)),
            ("UPDATE sessions SET provider_id='fixture'", ()),
            ("UPDATE sessions SET model=?", (observer.MODEL,)),
            ("UPDATE sessions SET provider_id='foreign',model=?", (observer.MODEL,)),
        )
        for sql, arguments in changes:
            with self.subTest(sql=sql), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                database, _ = self.fixture(root)
                database.execute(sql, arguments)
                result, spawned = self.observe(root, database)
                self.assertIsNone(result)
                spawned.assert_not_called()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database, _ = self.fixture(root)
            database.execute("UPDATE sessions SET provider_id='fixture',model=?", (observer.MODEL,))
            result, _ = self.observe(root, database)
            self.assertEqual((result['turn_id'], result['attempt_id']), (TURN, ATTEMPT))

    def test_wrong_content_reference_and_missing_or_changed_object_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            database, path = self.fixture(root, digest='b' * 64)
            with self.assertRaisesRegex(ValueError, 'fixed content'):
                self.observe(root, database)
        with tempfile.TemporaryDirectory() as directory:
            database, _ = self.fixture(Path(directory))
            with patch.object(observer, '_database', return_value=nullcontext(database)), patch.object(
                    observer, 'read_object', side_effect=FileNotFoundError):
                with self.assertRaises(FileNotFoundError):
                    observer.committed_reply(None, PROJECT, SESSION, 'fixture')


class ReplyRestartTests(unittest.TestCase):
    def test_launcher_requires_sealed_setuid_image_and_current_native_bytes(self):
        bootstrap = {'mode': 0o4755, 'sha256': 'a' * 64}
        binding = {'install_root': '/opt/painted-wolf-code', 'artifact': {'architecture': 'amd64'},
                   'images': {'/opt/painted-wolf-code/bin/pw-launch-entry': {'mode': 0o755},
                              str(desktop_launch.SETUID_LAUNCHER): bootstrap}}
        with patch.object(desktop_launch, 'elf', return_value=bootstrap) as observed:
            self.assertEqual(desktop_launch.protected_launcher(binding)['mode'], 0o4755)
            observed.assert_called_once_with(desktop_launch.SETUID_LAUNCHER, 'amd64', setuid=True)
            observed.return_value = dict(bootstrap, sha256='b' * 64)
            with self.assertRaisesRegex(ValueError, 'selected final bytes'):
                desktop_launch.protected_launcher(binding)
            del binding['images'][str(desktop_launch.SETUID_LAUNCHER)]
            with self.assertRaises(desktop_launch.CaseUnavailable):
                desktop_launch.protected_launcher(binding)


    def test_registry_and_physical_operation_are_fixed(self):
        self.assertEqual(installed_execution.case_family('conversation', 'restart-and-reconnect', 'deb'), 'reply_restart')
        self.assertEqual(steps('reply-fixture-prompt-v1')[0], ('ctrl', 'a'))
        self.assertNotIn(('ret',), steps('reply-fixture-prompt-v1'))
        with self.assertRaises(ValueError):
            steps('reply-fixture-prompt-v1-arbitrary')

    def test_gui_setup_selects_model_then_finishes_optional_setup_before_turn(self):
        app, ui = MagicMock(), MagicMock()
        fixture = SimpleNamespace(server=SimpleNamespace(server_port=12345), base_url='http://127.0.0.1:12345/v1',
            discovery_observations=[{'path': '/v1/model/info', 'model': provider_setup.MODEL, 'fixture_credential_observed': True}])
        ui.__enter__.return_value = ui
        with patch.object(provider_setup, 'DesktopUI', return_value=ui), patch.object(
                provider_setup, 'setup_state', return_value={'onboarding_completed': False}), patch.object(provider_setup, 'physical_input') as physical, patch.object(provider_setup, 'select_default_model') as select_model, patch.object(
                provider_setup, 'find_gui_provider', side_effect=[None, {'provider_id': 'fixture', 'configured_models': [provider_setup.MODEL]}]):
            self.assertEqual(provider_setup.configure_provider(app, fixture)['provider_id'], 'fixture')
        self.assertEqual([call.args[0] for call in ui.click.call_args_list],
                         ['Add AI provider', 'LiteLLM', 'Add AI provider', 'AI providers', 'Skip'])
        select_model.assert_called_once_with(app, ui)
        self.assertEqual([call.args[3] for call in physical.call_args_list],
                         ['provider-name-v1', 'provider-endpoint-v1-12345', 'provider-fixture-credential-v1'])
        self.assertEqual([call.args[2] for call in physical.call_args_list],
                         ['Name (optional) e.g. GPU box, Work account',
                          'Endpoint URL http://localhost:4000/v1', 'API key Paste your API key…'])
        self.assertEqual(physical.call_args_list[-1].kwargs, {'password': True})
        ui.click.reset_mock()
        with patch.object(provider_setup, 'DesktopUI', return_value=ui), patch.object(
                provider_setup, 'setup_state', return_value={'onboarding_completed': False}), patch.object(
                provider_setup, 'select_default_model'), patch.object(
                provider_setup, 'find_gui_provider', return_value={'provider_id': 'fixture', 'configured_models': [provider_setup.MODEL]}):
            self.assertEqual(provider_setup.configure_provider(app, fixture)['provider_id'], 'fixture')
        self.assertEqual([call.args[0] for call in ui.click.call_args_list], ['Skip'])

    def test_physical_input_retains_exact_guest_delivery_records(self):
        app = SimpleNamespace(binding={'native_input': {'binding': 'binding', 'vm_id': 'owned'}}, observations={})
        record = {'transaction_id': 'a' * 32, 'operation': 'reply-fixture-prompt-v1', 'events': []}
        with patch.object(provider_setup, 'EntryFocus'), patch.object(provider_setup, 'perform', return_value=record):
            self.assertIs(provider_setup.physical_input(app, None, 'Describe your idea', 'reply-fixture-prompt-v1'), record)
        self.assertEqual(app.observations['fixed_input'], [record])

    def test_input_without_independently_bound_physical_actor_is_unverified(self):
        with self.assertRaises(provider_setup.CaseUnavailable):
            provider_setup.physical_input(SimpleNamespace(binding={}), None, 'Describe your idea', 'reply-fixture-prompt-v1')

    def execute(self, root, *, after=None, completed=True, same_engine=False, launch_error=None):
        before = {'session_id': SESSION, 'content_blob_sha256': observer.DIGEST,
                  'schema_acquisition': {'invocation_id': OUTPUT}}
        apps = [MagicMock(), MagicMock()]
        for i, app in enumerate(apps):
            app.__enter__.return_value = app
            app.observations = {'engine_identity': {'pid': 1 if same_engine else i + 1}}
            app.streams.return_value = {}
        if launch_error is not None:
            apps[0].__enter__.side_effect = ValueError(launch_error)
        fixture, submission = MagicMock(), MagicMock()
        fixture.__enter__.return_value = fixture
        fixture.observations = [{'completed': completed}]
        fixture.acquisition.witness = {'invocation_id': OUTPUT}
        fixture.discovery_observations = fixture.transport_observations = []
        fixture.transport_clipped = False
        with ExitStack() as stack:
            for name, value in [('protected_launcher', {}), ('interactive_window', {}),
                    ('configure_provider', {'provider_id': 'fixture'}),
                    ('run', {'exit_code': 0, 'timed_out': False, 'output_limit': False})]:
                stack.enter_context(patch.object(owner, name, return_value=value))
            app_constructor = stack.enter_context(patch.object(owner, 'InstalledApp', autospec=True, side_effect=apps))
            stack.enter_context(patch.object(owner.ProviderFixture, 'gui', return_value=fixture))
            stack.enter_context(patch.object(owner, 'submit_project_turn', submission))
            stack.enter_context(patch.object(owner, 'await_fixture_reply', side_effect=[before, after or before]))
            retained = stack.enter_context(patch.object(owner, 'retain', side_effect=lambda *args: args))
            owner.execute({}, 'conversation', 'restart-and-reconnect', root)
            self.assertEqual([call.args for call in app_constructor.call_args_list],
                             [({'launcher': {}},), ({'launcher': {}},)])
        return retained.call_args.args, apps, submission

    def test_restart_requires_same_reply_and_a_new_captured_engine(self):
        with tempfile.TemporaryDirectory() as directory:
            result, apps, submission = self.execute(Path(directory))
            self.assertEqual(result[4], 'passed')
            self.assertEqual(result[5]['reply_before'], result[5]['reply_after'])
            for app in apps:
                app.__exit__.assert_called_once()
            submission.assert_called_once_with(apps[0])
        for changes in ({'after': {'session_id': SESSION, 'content_blob_sha256': 'b' * 64}},
                        {'completed': False}, {'same_engine': True}):
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(ValueError):
                    self.execute(Path(directory), **changes)

    def test_failed_context_entry_retains_partial_launch_and_diagnostic(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(owner.CaseFailed) as failed:
                self.execute(Path(directory), launch_error='fixed launch admission failed')
        self.assertEqual(failed.exception.observations['failure_detail'], 'fixed launch admission failed')
        self.assertEqual(failed.exception.observations['launch_before']['engine_identity'], {'pid': 1})
