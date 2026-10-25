"""Real SQLite WAL reads, bounded identity projections and fail-closed custody."""
from contextlib import ExitStack
import hashlib
import json
import re
import os
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import profile_observer as observer
from linux_release.installed_cases.reply_observer import TERMINAL_COLUMNS

PROJECT = '10000000-0000-4000-8000-000000000001'
ROOT = '10000000-0000-4000-8000-000000000002'
SESSION = '10000000-0000-4000-8000-000000000003'
MESSAGE = '10000000-0000-4000-8000-000000000004'


class ProfileObserverTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.home = Path(self.stack.enter_context(tempfile.TemporaryDirectory())).resolve()
        self.profile = self.home / '.config/paintedwolf'
        self.profile.mkdir(parents=True, mode=0o700)
        self.profile.parent.chmod(0o700)
        self.path = self.profile / 'store.db'
        self.writer = sqlite3.connect(self.path)
        self.addCleanup(self.writer.close)
        self.writer.executescript('''PRAGMA journal_mode=WAL; PRAGMA user_version=5;
          CREATE TABLE projects(id TEXT);
          CREATE TABLE project_roots(id TEXT,project_id TEXT,path TEXT);
          CREATE TABLE sessions(id TEXT,project_id TEXT,status TEXT,provider_id TEXT,model TEXT,parent_session_id TEXT);
          CREATE TABLE messages(id TEXT,session_id TEXT,role TEXT,tool_result_json TEXT,content TEXT);''')
        self.path.chmod(0o600)
        for suffix in ('-wal', '-shm'):
            Path(str(self.path) + suffix).chmod(0o600)
        self.fixture = self.home / 'fixture'
        self.fixture.mkdir()
        self.writer.execute('INSERT INTO projects VALUES(?)', (PROJECT,))
        self.writer.execute('INSERT INTO project_roots VALUES(?,?,?)', (ROOT, PROJECT, str(self.fixture)))
        self.writer.execute('INSERT INTO sessions VALUES(?,?,?,?,?,?)', (SESSION, PROJECT, 'idle', 'fixture-provider', 'fixture-model', None))
        self.writer.execute('INSERT INTO messages VALUES(?,?,?,?,?)', (MESSAGE, SESSION, 'tool',
            json.dumps({'tool_call_id': 'fixed-command-1', 'tool': 'command', 'outcome': 'completed',
                        'content': 'SECRET RESULT', 'tool_args': {'secret': 'SECRET ARG'}}), 'SECRET PROMPT'))
        self.writer.commit()
        self.app = SimpleNamespace(binding={'install_root': '/owned', 'images': {'/owned/bin/pw': {'device': 1, 'inode': 2, 'bytes': 3}}}, observations={'engine_identity': {'pid': 123, 'start_time_ticks': 9}},
                                   require_running=Mock(), require_engine=Mock())
        self.stack.enter_context(patch.object(Path, 'home', return_value=self.home))
        from linux_release.installed_cases.app import InstalledApp
        from linux_release.installed_storage import resolve
        from types import MethodType
        self.app.storage = resolve({'HOME': str(self.home)})
        self.app.observations['storage'] = self.app.storage.facts()
        self.app.selected_storage = MethodType(InstalledApp.selected_storage, self.app)
        self.stack.enter_context(patch.object(observer, 'require_owned_guest'))
        self.stack.enter_context(patch.object(observer, 'engine_image', return_value=(1, 2, 3)))

    def test_live_wal_commit_is_visible_without_copy_checkpoint_or_payload_retention(self):
        self.assertEqual(observer.project_for_owned_root(self.app, self.fixture), {'project_id': PROJECT, 'root_id': ROOT})
        self.assertEqual(observer.sessions_for_project(self.app, PROJECT), [{'id': SESSION, 'status': 'idle',
            'provider_id': 'fixture-provider', 'model': 'fixture-model', 'parent_session_id': None}])
        found = observer.invocations(self.app, SESSION, ['command'])
        self.assertEqual(found, [{'message_id': MESSAGE, 'tool_call_id': 'fixed-command-1', 'tool': 'command', 'outcome': 'completed'}])
        self.assertNotIn('SECRET', json.dumps(found))
        self.assertEqual(self.writer.execute('PRAGMA journal_mode').fetchone(), ('wal',))
        self.app.require_engine.assert_called()

    def test_session_parent_is_an_explicit_canonical_identity(self):
        self.writer.execute('UPDATE sessions SET parent_session_id=?', (MESSAGE,))
        self.writer.commit()
        self.assertEqual(observer.sessions_for_project(self.app, PROJECT)[0]['parent_session_id'], MESSAGE)
        self.writer.execute("UPDATE sessions SET parent_session_id='not-a-session-id'")
        self.writer.commit()
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)

    def test_purpose_columns_require_actual_tables_and_exact_declared_types(self):
        required = {**TERMINAL_COLUMNS, 'project_roots': {'is_primary': 'INTEGER', 'kind': 'TEXT'}}
        for table, fields in required.items():
            if table in {'sessions', 'project_roots'}:
                existing = {row[1] for row in self.writer.execute('PRAGMA table_xinfo(' + table + ')')}
                for name, kind in fields.items():
                    if name not in existing:
                        self.writer.execute('ALTER TABLE ' + table + ' ADD COLUMN ' + name + ' ' + kind)
            else:
                self.writer.execute('CREATE TABLE ' + table + '(' + ','.join(
                    name + ' ' + kind for name, kind in fields.items()) + ')')
        self.writer.commit()
        with observer._database(self.app, required=required):
            pass
        self.writer.execute('ALTER TABLE turns RENAME TO held_turns')
        self.writer.execute('CREATE VIEW turns AS SELECT * FROM held_turns')
        self.writer.commit()
        with self.assertRaisesRegex(ValueError, 'physical tables'):
            with observer._database(self.app, required=required):
                pass
        self.writer.execute('DROP VIEW turns')
        self.writer.execute('ALTER TABLE held_turns RENAME TO turns')
        self.writer.commit()
        for table, column in (('turn_attempts', 'phase'), ('sessions', 'parent_session_id'),
                              ('project_roots', 'is_primary')):
            original = dict(observer.COLUMNS.get(table, {}))
            original.update(required.get(table, {}))
            wrong = {**original, column: 'INTEGER' if original[column] == 'TEXT' else 'TEXT'}
            self.writer.execute('ALTER TABLE ' + table + ' RENAME TO held_table')
            self.writer.execute('CREATE TABLE ' + table + '(' + ','.join(
                name + ' ' + kind for name, kind in wrong.items()) + ')')
            self.writer.commit()
            with self.subTest(table=table), self.assertRaisesRegex(ValueError, 'column contract'):
                with observer._database(self.app, required=required):
                    pass
            self.writer.execute('DROP TABLE ' + table)
            self.writer.execute('ALTER TABLE held_table RENAME TO ' + table)
            self.writer.commit()

    def test_read_connection_refuses_database_mutation(self):
        with observer._database(self.app) as connection:
            with self.assertRaises(sqlite3.OperationalError):
                connection.execute('DELETE FROM projects')
        self.assertEqual(self.writer.execute('SELECT count(*) FROM projects').fetchone(), (1,))

    def test_unknown_revision_refused_without_changing_it(self):
        self.writer.execute('PRAGMA user_version=99')
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)
        self.assertEqual(self.writer.execute('PRAGMA user_version').fetchone(), (99,))

    def test_missing_column_or_view_cannot_impersonate_physical_contract(self):
        self.writer.executescript('DROP TABLE sessions; CREATE VIEW sessions AS SELECT id FROM projects;')
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)

    def test_engine_identity_change_during_read_is_refused(self):
        with self.assertRaises(ValueError):
            with observer._database(self.app):
                self.app.observations['engine_identity'] = {'pid': 456, 'start_time_ticks': 10}

    def test_symlink_profile_and_world_readable_database_are_refused(self):
        self.path.chmod(0o644)
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)
        self.path.chmod(0o600)
        other = self.profile.with_name('original')
        self.profile.rename(other)
        self.profile.symlink_to(other, target_is_directory=True)
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)

    def test_missing_sidecar_is_not_created_by_observer(self):
        sidecar = Path(str(self.path) + '-shm')
        sidecar.rename(sidecar.with_suffix('.held'))
        with self.assertRaises(observer.CaseUnavailable):
            observer.sessions_for_project(self.app, PROJECT)
        self.assertFalse(sidecar.exists())

    def test_private_parent_allows_only_declared_sidecar_modes(self):
        sidecar = Path(str(self.path) + '-wal')
        sidecar.chmod(0o644)
        self.assertEqual(len(observer.sessions_for_project(self.app, PROJECT)), 1)
        for mode in (0o640, 0o664, 0o700):
            with self.subTest(mode=mode):
                sidecar.chmod(mode)
                with self.assertRaises(observer.CaseUnavailable):
                    observer.sessions_for_project(self.app, PROJECT)
        sidecar.chmod(0o600)
        self.profile.chmod(0o710)
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)

    def test_sidecar_replacement_disappearance_mode_and_links_refuse(self):
        sidecar = Path(str(self.path) + '-shm')
        with self.assertRaises(observer.CaseUnavailable):
            with observer.sqlite_sidecars(self.path):
                sidecar.rename(sidecar.with_suffix('.held'))
                sidecar.write_bytes(b'')
                sidecar.chmod(0o600)
        with self.assertRaises(observer.CaseUnavailable):
            with observer.sqlite_sidecars(self.path):
                sidecar.unlink()
        sidecar.write_bytes(b'')
        sidecar.chmod(0o600)
        with self.assertRaises(observer.CaseUnavailable):
            with observer.sqlite_sidecars(self.path):
                sidecar.chmod(0o664)
        sidecar.chmod(0o600)
        os.link(sidecar, sidecar.with_suffix('.link'))
        with self.assertRaises(observer.CaseUnavailable):
            with observer.sqlite_sidecars(self.path):
                self.fail('multiply linked sidecar admitted')

    def test_sidecar_descriptor_custody_survives_live_growth(self):
        with observer.sqlite_sidecars(self.path):
            self.writer.execute('INSERT INTO projects VALUES(?)', ('another-project',))
            self.writer.commit()

    def test_row_overflow_and_undeclared_tools_fail_closed(self):
        self.writer.executemany('INSERT INTO sessions VALUES(?,?,?,?,?,?)',
            [(str(__import__('uuid').uuid4()), PROJECT, 'idle', None, None, None) for _ in range(256)])
        self.writer.commit()
        with self.assertRaises(ValueError):
            observer.sessions_for_project(self.app, PROJECT)
        with self.assertRaises(ValueError):
            observer.invocations(self.app, SESSION, ['arbitrary'])

    def test_same_process_exec_to_another_image_is_refused(self):
        with patch.object(observer, 'engine_image', side_effect=[(1, 2, 3), (1, 5, 8)]):
            with self.assertRaises(ValueError):
                observer.sessions_for_project(self.app, PROJECT)

    def test_oversized_tool_json_is_refused_before_json_projection(self):
        self.writer.execute('UPDATE messages SET tool_result_json=?', (json.dumps({'content': 's' * (1 << 20)}),))
        self.writer.commit()
        with self.assertRaises(ValueError):
            observer.invocations(self.app, SESSION, ['command'])

    def reviewed_presence_schema(self):
        # Exact declarations reviewed in product 837287e5 (schema version 5).
        # Its only v4→v5 change is in vault_unlocks, outside these six projections.
        # Health store_revision is a boot counter, not SQLite's schema version.
        source = Path(__file__).resolve().parents[4] / 'lycaon/internal/db/schema.sql'
        expected = {
            'projects': '08a1563ad14e43ffeea5dccf4e2aacbc63ea3db3b6dd9d8942391098949bdbc1',
            'project_roots': 'f57811324f2908e4cfc92e25dacab8351aab5221469bdd7c3b044cac382169ac',
            'sessions': '1dbdfb687e9077615c035c0efc444463e25846d7461733eb23e19f81fcf0c7ed',
            'messages': 'b9b3f5e8009d24cdbce34e6c81dd55794a718a679ae0035e1b04b9ddf56eb7c9',
            'managed_secrets': '7c632600808d05ac6eb7c7689eee12a0cd9dda4307e1ef00648ef6a15546eb68',
            'managed_secret_reveals': 'fa607b41c709db3e5ee9ef618c30c0c93d504d9c3c285fe3cb66eb0f58af11d5',
        }
        declarations = []
        for table, digest in expected.items():
            matches = re.findall(r'CREATE TABLE IF NOT EXISTS ' + table + r' \([\s\S]+?^\) STRICT;',
                                 source.read_text(), re.MULTILINE)
            self.assertEqual(len(matches), 1, table)
            self.assertEqual(hashlib.sha256(matches[0].encode()).hexdigest(), digest, table)
            declarations.append(matches[0])
        self.writer.close()
        self.path.unlink()
        self.writer = sqlite3.connect(self.path)
        self.addCleanup(self.writer.close)
        self.writer.executescript('PRAGMA journal_mode=WAL; PRAGMA user_version=5;' + '\n'.join(declarations))
        for path in (self.path, Path(str(self.path) + '-wal'), Path(str(self.path) + '-shm')):
            path.chmod(0o600)

    def test_reviewed_product_five_supports_every_presence_projection_read_only(self):
        from linux_release.installed_cases import profile_setup, presence_system
        from linux_release.installed_input.presence_fixture import NAME
        self.reviewed_presence_schema()
        self.fixture.chmod(0o700)
        stamp = '2026-10-25T00:00:00.000000000Z'
        self.writer.execute('INSERT INTO projects(id,last_opened_at,created_at) VALUES(?,?,?)',
                            (PROJECT, stamp, stamp))
        self.writer.execute('INSERT INTO project_roots(id,project_id,path,label,is_primary,added_at) VALUES(?,?,?,?,1,?)',
                            (ROOT, PROJECT, str(self.fixture), 'fixture', stamp))
        self.writer.execute('INSERT INTO sessions(id,project_id,owner_person_id,posture,provider_id,model,created_at,activity_at,updated_at) '
                            'VALUES(?,?,?,?,?,?,?,?,?)',
                            (SESSION, PROJECT, MESSAGE, 'build', 'fixture-provider', 'fixture-model', stamp, stamp, stamp))
        secret, challenge = ('10000000-0000-4000-8000-000000000005', '10000000-0000-4000-8000-000000000006')
        self.writer.execute('INSERT INTO managed_secrets(id,project_id,created_by_person_id,scope,name,purpose,origin,operation_id,created_at) '
                            'VALUES(?,?,?,?,?,?,?,?,?)',
                            (secret, PROJECT, MESSAGE, 'project', NAME, 'Public fixture', 'settings_entered', challenge, stamp))
        self.writer.execute('INSERT INTO managed_secret_reveals VALUES(?,?,?,?,?,?,?)',
                            (challenge, secret, 1, 'linux_user_presence', 'main', MESSAGE, stamp))
        self.writer.commit()
        before = self.writer.total_changes
        self.assertEqual(observer.project_for_owned_root(self.app, self.fixture)['project_id'], PROJECT)
        self.assertEqual(observer.sessions_for_project(self.app, PROJECT)[0]['id'], SESSION)
        selection = {'onboarding_completed': True, 'coordinator': {'provider_id': 'fixture-provider', 'model': 'fixture-model'},
                     'model_policy_sha256': 'fixed-public-policy'}
        with patch.object(profile_setup, 'state', return_value=selection):
            self.assertEqual(profile_setup.fixture_selection(self.app, self.fixture, PROJECT,
                'fixture-provider', 'fixture-model')['root_id'], ROOT)
        audit = presence_system.reveal_audit(self.app, PROJECT)
        self.assertEqual(audit['secret_id'], secret)
        self.assertEqual(audit['reveals'][0]['challenge_id'], challenge)
        self.assertEqual(observer.invocations(self.app, SESSION, ['command']), [])
        with observer._database(self.app) as connection:
            with self.assertRaises(sqlite3.OperationalError):
                connection.execute('DELETE FROM managed_secret_reveals')
        self.assertEqual(self.writer.total_changes, before)
        self.assertEqual(self.writer.execute('PRAGMA user_version').fetchone(), (5,))
        self.assertEqual(self.writer.execute('SELECT count(*) FROM managed_secret_reveals').fetchone(), (1,))

    def test_reviewed_product_five_missing_wrong_type_and_view_refuse(self):
        self.reviewed_presence_schema()
        for replacement in ('CREATE TABLE sessions(id TEXT)',
                            'CREATE TABLE sessions(id TEXT,project_id TEXT,status INTEGER,provider_id TEXT,model TEXT)',
                            'CREATE VIEW sessions AS SELECT * FROM held_sessions'):
            self.writer.execute('ALTER TABLE sessions RENAME TO held_sessions')
            self.writer.execute(replacement)
            self.writer.commit()
            with self.subTest(replacement=replacement), self.assertRaises(ValueError):
                observer.sessions_for_project(self.app, PROJECT)
            self.assertEqual(self.writer.execute('PRAGMA user_version').fetchone(), (5,))
            self.writer.execute('DROP ' + ('VIEW' if replacement.startswith('CREATE VIEW') else 'TABLE') + ' sessions')
            self.writer.execute('ALTER TABLE held_sessions RENAME TO sessions')
            self.writer.commit()

    def test_reviewed_product_unknown_or_previous_schema_refuses_without_mutation(self):
        self.reviewed_presence_schema()
        for revision in (4, 6, 99):
            self.writer.execute('PRAGMA user_version=' + str(revision))
            with self.subTest(revision=revision), self.assertRaisesRegex(ValueError, 'observed.*' + str(revision)):
                observer.sessions_for_project(self.app, PROJECT)
            self.assertEqual(self.writer.execute('PRAGMA user_version').fetchone(), (revision,))
