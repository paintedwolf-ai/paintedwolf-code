"""Configured profiles are selected by bounded typed persisted facts, never missing UI controls."""
from contextlib import nullcontext
import hashlib
import json
import sqlite3
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import profile_setup as owner, provider_setup
from linux_release.installed_cases.provider import MODEL
from linux_release.installed_cases.records import CaseUnavailable

REF = {'provider_id': 'fixed', 'model': MODEL}


class ProfileSetupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name).resolve()
        self.slices = self.directory / 'app-state-v1'
        self.slices.mkdir(mode=0o700)
        self.onboarding = self.slices / owner.ONBOARDING
        self.policy = self.directory / 'model-policy.yaml'
        context = patch.object(owner, 'profile', side_effect=lambda app: nullcontext(self.directory))
        context.start()
        self.addCleanup(context.stop)

    def write(self, path, value):
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def completed(self):
        self.write(self.onboarding, {'key': 'onboarding', 'value': {'firstRunSetupCompleted': True}})
        self.write(self.policy, {'coordinator': REF, 'lite': {'provider_id': '', 'model': ''},
                                'agent_pool': {'selection': 'first', 'models': [REF]}})

    def test_missing_files_are_incomplete_and_never_created(self):
        facts = owner.state(None)
        self.assertIs(facts['onboarding_completed'], False)
        self.assertIsNone(facts['coordinator'])
        self.assertFalse(self.onboarding.exists())
        self.assertFalse(self.policy.exists())

    def test_actual_slice_envelope_and_coordinator_are_readonly_with_exact_digests(self):
        self.completed()
        before = self.onboarding.read_bytes(), self.policy.read_bytes()
        facts = owner.state(None)
        self.assertIs(facts['onboarding_completed'], True)
        self.assertEqual(facts['coordinator'], REF)
        self.assertEqual(facts['onboarding_sha256'], hashlib.sha256(before[0]).hexdigest())
        self.assertEqual(facts['model_policy_sha256'], hashlib.sha256(before[1]).hexdigest())
        self.assertEqual(before, (self.onboarding.read_bytes(), self.policy.read_bytes()))
        self.assertFalse((self.directory / 'store.db').exists())

    def test_ambiguous_unknown_or_nonboolean_onboarding_refuses(self):
        for value in ({'key': 'other', 'value': {}}, {'key': 'onboarding', 'value': {'other': True}},
                      {'key': 'onboarding', 'value': {'firstRunSetupCompleted': 1}}, [],
                      {'key': 'onboarding', 'value': []}):
            self.write(self.onboarding, value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                owner.state(None)
        for raw in ('{"key":"onboarding","key":"other","value":{}}', 'x' * 4097):
            self.onboarding.write_text(raw)
            with self.assertRaises(ValueError):
                owner.state(None)

    def test_unknown_duplicate_or_wrong_coordinator_policy_refuses(self):
        for value in ({'coordinator': REF, 'other': {}}, {'coordinator': {'provider_id': 'fixed'}},
                      {'coordinator': {'provider_id': 1, 'model': MODEL}}, {'coordinator': None}):
            self.write(self.policy, value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                owner.state(None)
        self.policy.write_text('coordinator: {}\ncoordinator: {}')
        with self.assertRaises(ValueError):
            owner.state(None)

    def test_nonprivate_or_symlink_state_never_becomes_setup_proof(self):
        self.completed()
        self.onboarding.chmod(0o644)
        with self.assertRaises(ValueError):
            owner.state(None)
        self.onboarding.chmod(0o600)
        actual = self.onboarding.with_suffix('.retained')
        self.onboarding.rename(actual)
        self.onboarding.symlink_to(actual)
        with self.assertRaises(OSError):
            owner.state(None)
        self.assertTrue(actual.exists())

    def test_explicit_completed_matching_profile_skips_setup_and_requires_fresh_catalog(self):
        app = SimpleNamespace(observations={})
        fixture = SimpleNamespace(base_url='http://127.0.0.1:41234/v1')
        witness = {'provider_id': 'fixed'}
        state = {'onboarding_completed': True, 'coordinator': REF}
        with patch.object(provider_setup, 'find_gui_provider', return_value=witness), patch.object(
                provider_setup, 'setup_state', return_value=state), patch.object(
                provider_setup, 'await_discovery', return_value=witness) as discovery, patch.object(
                provider_setup, 'DesktopUI') as desktop:
            self.assertEqual(provider_setup.configure_provider(app, fixture), witness)
        discovery.assert_called_once_with(app, fixture)
        desktop.assert_not_called()
        self.assertEqual(app.observations['provider_setup_state'], state)

    def test_completed_foreign_default_is_not_reconfigured_or_treated_as_ready(self):
        fixture = SimpleNamespace(base_url='http://127.0.0.1:41234/v1')
        for existing, coordinator in ((None, REF), ({'provider_id': 'fixed'}, dict(REF, model='foreign'))):
            with patch.object(provider_setup, 'find_gui_provider', return_value=existing), patch.object(
                    provider_setup, 'setup_state', return_value={'onboarding_completed': True, 'coordinator': coordinator}), patch.object(
                    provider_setup, 'DesktopUI') as desktop, patch.object(provider_setup, 'await_discovery') as discovery:
                with self.assertRaises(CaseUnavailable):
                    provider_setup.configure_provider(SimpleNamespace(observations={}), fixture)
            desktop.assert_not_called()
            discovery.assert_not_called()

    def test_first_time_tips_and_renderer_session_validate_owned_slices(self):
        tips = self.slices / owner.FIRST_TIME_TIPS
        self.assertIs(owner.first_time_tips_enabled(None), True)
        self.write(tips, {'key': 'firstTimeTips', 'value': {'enabled': False, 'dismissed': ['files-ai-editor']}})
        self.assertIs(owner.first_time_tips_enabled(None), False)
        for bad in ({'key': 'other', 'value': {}}, {'key': 'firstTimeTips', 'value': {'other': True}},
                    {'key': 'firstTimeTips', 'value': {'enabled': 1}}, {'key': 'firstTimeTips', 'value': []}):
            self.write(tips, bad)
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                owner.first_time_tips_enabled(None)
        pid, sid, root = '10000000-0000-4000-8000-000000000001', '20000000-0000-4000-8000-000000000002', str(self.directory / 'w')
        active_file, snap_file = self.slices / owner.LAST_ACTIVE_PROJECT, self.slices / owner.LAST_SESSION_SNAPSHOT
        self.assertIsNone(owner.renderer_session(None, root, pid, sid))
        self.write(active_file, {'key': 'lastActiveProjectId', 'value': pid})
        valid = {'scope': {'projectId': pid, 'sessionId': sid},
                 'session': {'id': sid, 'project_id': pid, 'workspace_path': root, 'status': 'idle'},
                 'activeWorkflowRun': {'project_id': pid, 'session_id': sid, 'ui': {'request': {'question': 'What would you like to work on?'}}}}
        for status in ('idle', 'preparing'):
            self.write(snap_file, {'key': 'lastSessionSnapshot', 'value': {**valid, 'session': {**valid['session'], 'status': status}}})
            self.assertEqual(owner.renderer_session(None, root, pid, sid), {
                'observer': 'owned-app-state-JSON', 'project_id': pid, 'session_id': sid, 'status': status,
                'workspace_path': root, 'composer_question': 'What would you like to work on?',
                'last_active_project_sha256': hashlib.sha256(active_file.read_bytes()).hexdigest(),
                'last_session_snapshot_sha256': hashlib.sha256(snap_file.read_bytes()).hexdigest()})
        for pending in ({**valid, 'scope': {'projectId': pid, 'sessionId': 'other'}}, {**valid, 'activeWorkflowRun': None},
                        {**valid, 'session': {**valid['session'], 'status': 'busy'}}, {**valid, 'session': {**valid['session'], 'status': None}},
                        {**valid, 'activeWorkflowRun': {**valid['activeWorkflowRun'], 'ui': {'request': {'question': 'Other'}}}}):
            self.write(snap_file, {'key': 'lastSessionSnapshot', 'value': pending})
            self.assertIsNone(owner.renderer_session(None, root, pid, sid))
        for invalid in ([], {**valid, 'scope': {'projectId': pid}}, {**valid, 'session': {**valid['session'], 'workspace_path': '/bad'}},
                        {**valid, 'session': {**valid['session'], 'status': 'error'}},
                        {**valid, 'activeWorkflowRun': {**valid['activeWorkflowRun'], 'session_id': 'other'}}):
            self.write(snap_file, {'key': 'lastSessionSnapshot', 'value': invalid})
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                owner.renderer_session(None, root, pid, sid)
        self.write(active_file, {'key': 'lastActiveProjectId', 'value': 123})
        with self.assertRaises(ValueError):
            owner.renderer_session(None, root, pid, sid)


class FixtureSelectionTests(unittest.TestCase):
    def test_single_primary_root_and_persisted_default_are_independent_authorities(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            root.chmod(0o700)
            database = sqlite3.connect(':memory:')
            self.addCleanup(database.close)
            database.execute('CREATE TABLE project_roots(id,project_id,path,is_primary,kind)')
            project = '10000000-0000-4000-8000-000000000001'
            database.execute('INSERT INTO project_roots VALUES(?,?,?,?,?)', (project, project, str(root), 1, 'attached'))
            state = {'onboarding_completed': True, 'coordinator': REF, 'model_policy_sha256': 'a' * 64}
            with patch.object(owner, '_database', side_effect=lambda app, **kwargs: nullcontext(database)), patch.object(
                    owner, 'state', return_value=state):
                facts = owner.fixture_selection(None, root, project, 'fixed', MODEL)
                self.assertEqual(facts['model_policy_sha256'], 'a' * 64)
                for key, value in (('onboarding_completed', False), ('coordinator', {'provider_id': 'other', 'model': MODEL}),
                                   ('model_policy_sha256', None)):
                    with self.subTest(key=key), patch.object(owner, 'state', return_value={**state, key: value}), self.assertRaises(ValueError):
                        owner.fixture_selection(None, root, project, 'fixed', MODEL)
                for column, value in (('is_primary', 0), ('kind', 'draft'), ('path', '/foreign')):
                    database.execute('SAVEPOINT invalid')
                    database.execute('UPDATE project_roots SET ' + column + '=?', (value,))
                    with self.subTest(column=column), self.assertRaises(ValueError):
                        owner.fixture_selection(None, root, project, 'fixed', MODEL)
                    database.execute('ROLLBACK TO invalid')
                    database.execute('RELEASE invalid')
                database.execute('INSERT INTO project_roots VALUES(?,?,?,?,?)', (project, project, '/second', 0, 'attached'))
                with self.assertRaises(ValueError):
                    owner.fixture_selection(None, root, project, 'fixed', MODEL)
                database.execute("DELETE FROM project_roots WHERE path='/second'")
                overlay = root / '.paintedwolf'
                overlay.mkdir()
                with self.assertRaises(ValueError):
                    owner.fixture_selection(None, root, project, 'fixed', MODEL)
                overlay.rmdir()
                overlay.symlink_to('/missing')
                with self.assertRaises(ValueError):
                    owner.fixture_selection(None, root, project, 'fixed', MODEL)


class ActiveSessionTests(unittest.TestCase):
    def test_active_session_requires_exact_project_provider_and_phase_facts(self):
        session = {'id': 'session', 'status': 'busy', 'provider_id': None, 'model': None,
                   'parent_session_id': None}
        rendered = {'observer': 'owned-app-state-JSON', 'session_id': 'session', 'status': 'idle'}
        with patch.object(owner, 'project_for_owned_root', return_value={'project_id': 'project'}), patch.object(
                owner, 'fixture_selection', return_value={'model_policy_sha256': 'a' * 64}), patch.object(
                owner, 'renderer_session', return_value=rendered) as render_obs, patch.object(
                owner, 'sessions_for_project', return_value=[session]) as observed:
            active = owner.require_active(None, None, 'fixture', MODEL)
            self.assertEqual(active['id'], 'session')
            self.assertNotIn('renderer_session', active)
            self.assertIsNone(owner.active_session(None, None, 'fixture', MODEL, status='idle'))
            observed.return_value = [{**session, 'status': 'idle'}]
            idle_obs = owner.active_session(None, None, 'fixture', MODEL, status='idle')
            self.assertEqual(idle_obs['id'], 'session')
            self.assertEqual(idle_obs['renderer_session'], rendered)
            render_obs.return_value = None
            self.assertIsNone(owner.active_session(None, None, 'fixture', MODEL, status='idle'))
            with self.assertRaises(owner.CaseUnavailable):
                owner.require_active(None, None, 'fixture', MODEL, active)
            with self.assertRaisesRegex(ValueError, 'idle or busy'):
                owner.active_session(None, None, 'fixture', MODEL, status='other')
            observed.return_value = [{**session, 'id': 'replacement'}]
            with self.assertRaisesRegex(ValueError, 'replaced'):
                owner.require_active(None, None, 'fixture', MODEL, active)
            observed.return_value = [session, session]
            with self.assertRaisesRegex(ValueError, 'ambiguous'):
                owner.active_session(None, None, 'fixture', MODEL)
            for changed in ({'provider_id': 'foreign'}, {'provider_id': 'fixture'},
                            {'model': MODEL}, {'parent_session_id': 'child'}):
                observed.return_value = [{**session, **changed}]
                with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, 'selected root'):
                    owner.active_session(None, None, 'fixture', MODEL)
            observed.return_value = [{**session, 'provider_id': 'fixture', 'model': MODEL}]
            self.assertEqual(owner.require_active(None, None, 'fixture', MODEL)['id'], 'session')

    def test_busy_session_cannot_outlive_its_observed_model_selection(self):
        session = {'id': 'session', 'status': 'busy', 'provider_id': None, 'model': None,
                   'parent_session_id': None}
        with patch.object(owner, 'project_for_owned_root', return_value={'project_id': 'project'}), patch.object(
                owner, 'fixture_selection', side_effect=[{'policy': 'before'}, {'policy': 'after'}]), patch.object(
                owner, 'sessions_for_project', return_value=[session]):
            with self.assertRaisesRegex(ValueError, 'selection changed'):
                owner.active_session(None, None, 'fixture', MODEL)

    def test_await_session_polls_requested_phase_and_refuses_timeout(self):
        idle = {'id': 'session', 'status': 'idle'}
        with patch.object(owner, 'active_session', side_effect=[None, idle]) as active, patch.object(owner.time, 'sleep') as sleep:
            self.assertEqual(owner.await_session(None, None, 'fixture', MODEL, status='idle', reason='keepawake-project-session-not-opened'), idle)
            self.assertEqual((active.call_count, sleep.call_args.args), (2, (0.05,)))
        with patch.object(owner, 'active_session', return_value=None), patch.object(owner.time, 'monotonic', side_effect=[0.0, 30.0]), patch.object(owner.time, 'sleep'):
            with self.assertRaisesRegex(owner.CaseUnavailable, 'keepawake-project-session-not-opened'):
                owner.await_session(None, None, 'fixture', MODEL, status='idle', reason='keepawake-project-session-not-opened')
