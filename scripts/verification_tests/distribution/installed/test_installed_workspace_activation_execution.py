"""Public launcher and workspace evidence cannot replace the captured runtime or active turn."""
from contextlib import ExitStack
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release import installed_execution
from linux_release.installed_cases import desktop_launch, profile_setup, workspace_activation as owner
from linux_release.installed_input import controller, surface_wire


class WorkspaceTests(unittest.TestCase):
    def test_registry_and_active_session_require_exact_project_provider_and_busy_facts(self):
        self.assertEqual(installed_execution.case_family('windowing', 'workspace-activation-and-exit', 'deb'),
                         'workspace_activation')
        self.assertIs(owner.active_session, profile_setup.active_session)
        self.assertIs(owner.await_session, profile_setup.await_session)
        self.assertIs(owner.require_active, profile_setup.require_active)
        session = {'id': 'session', 'status': 'busy', 'provider_id': None, 'model': None,
                   'parent_session_id': None}
        with patch.object(profile_setup, 'project_for_owned_root', return_value={'project_id': 'project'}), patch.object(
                profile_setup, 'fixture_selection', return_value={'model_policy_sha256': 'a' * 64}), patch.object(
                profile_setup, 'renderer_session', return_value={'observer': 'owned-app-state-JSON', 'session_id': 'session', 'status': 'idle'}), patch.object(
                profile_setup, 'sessions_for_project', return_value=[session]) as observed:
            active = owner.require_active(None, None, 'fixture', owner.MODEL)
            self.assertEqual(active['id'], 'session')
            observed.return_value = [{**session, 'status': 'idle'}]
            self.assertEqual(owner.active_session(None, None, 'fixture', owner.MODEL, status='idle')['id'], 'session')
            with self.assertRaises(owner.CaseUnavailable):
                owner.require_active(None, None, 'fixture', owner.MODEL, active)
            observed.return_value = [{**session, 'id': 'replacement'}]
            with self.assertRaisesRegex(ValueError, 'replaced'):
                owner.require_active(None, None, 'fixture', owner.MODEL, active)
            observed.return_value = [session, session]
            with self.assertRaisesRegex(ValueError, 'ambiguous'):
                owner.active_session(None, None, 'fixture', owner.MODEL)
            for changed in ({'provider_id': 'foreign'}, {'provider_id': 'fixture'},
                            {'model': owner.MODEL}, {'parent_session_id': 'child'}):
                observed.return_value = [{**session, **changed}]
                with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, 'selected root'):
                    owner.active_session(None, None, 'fixture', owner.MODEL)
            observed.return_value = [{**session, 'provider_id': 'fixture', 'model': owner.MODEL}]
            self.assertEqual(owner.require_active(None, None, 'fixture', owner.MODEL)['id'], 'session')

    def test_busy_session_cannot_outlive_its_observed_model_selection(self):
        session = {'id': 'session', 'status': 'busy', 'provider_id': None, 'model': None,
                   'parent_session_id': None}
        with patch.object(profile_setup, 'project_for_owned_root', return_value={'project_id': 'project'}), patch.object(
                profile_setup, 'fixture_selection', side_effect=[{'policy': 'before'}, {'policy': 'after'}]), patch.object(
                profile_setup, 'sessions_for_project', return_value=[session]):
            with self.assertRaisesRegex(ValueError, 'selection changed'):
                owner.active_session(None, None, 'fixture', owner.MODEL)

    def test_execute_awaits_opened_idle_session_before_submitting_project_turn(self):
        events = []
        app = Mock(observations={'session': {'id': 'session'}})
        app.__enter__ = Mock(return_value=app)
        app.__exit__ = Mock(return_value=False)
        app.streams.return_value = {}
        fixture = Mock(observations=[])
        context = Mock()
        context.__enter__ = Mock(return_value=fixture)
        context.__exit__ = Mock(return_value=False)
        def await_phase(current_app, project, provider, model, *, status='busy', reason=''):
            events.append(('await', status, reason))
            return {'id': 'opened', 'status': 'idle'}
        def submit_turn(current_app):
            events.append(('submit', None, None))
            raise owner.CaseUnavailable('stop-after-submit')
        with tempfile.TemporaryDirectory() as root, ExitStack() as stack:
            stack.enter_context(patch.object(owner, 'protected_launcher', return_value={'path': '/bin/pw'}))
            stack.enter_context(patch.object(owner, 'load_provider_recovery', return_value=None))
            stack.enter_context(patch.object(owner.ProviderFixture, 'gui', return_value=context))
            stack.enter_context(patch.object(owner, 'InstalledApp', return_value=app))
            stack.enter_context(patch.object(owner, 'interactive_window', return_value={'id': 'window'}))
            stack.enter_context(patch.object(owner, 'configure_provider', return_value={'provider_id': 'fixture'}))
            stack.enter_context(patch.object(owner, 'run',
                return_value={'exit_code': 0, 'timed_out': False, 'output_limit': False}))
            stack.enter_context(patch.object(owner, 'await_session', side_effect=await_phase))
            stack.enter_context(patch.object(owner, 'submit_project_turn', side_effect=submit_turn))
            with self.assertRaisesRegex(owner.CaseUnavailable, 'stop-after-submit') as raised:
                owner.execute({}, 'windowing', 'workspace-activation-and-exit', Path(root))
        self.assertEqual(events, [
            ('await', 'idle', 'workspace-activation-project-session-not-opened'),
            ('submit', None, None),
        ])
        self.assertEqual(raised.exception.observations['opened_session'], {'id': 'opened', 'status': 'idle'})

    def activation(self, *, changed_image=False, changed_session=False, timeout=False, identity_error=False):
        image = {'mode': 0o4755, 'sha256': 'a' * 64}
        app = Mock(binding={}, observations={'session': {'id': 'session'}})
        child = Mock(pid=321)
        child.wait.return_value = 0
        child.poll.return_value = None if timeout or identity_error else 0
        if timeout:
            child.wait.side_effect = [subprocess.TimeoutExpired('launcher', 15), 0]
        with ExitStack() as stack:
            stack.enter_context(patch.object(desktop_launch, 'protected_launcher',
                return_value={'path': '/usr/libexec/painted-wolf-code/launch', **image}))
            stack.enter_context(patch.object(desktop_launch, 'file_identity',
                return_value={**image, 'header': b'ELF', 'mode': 0o755 if changed_image else 0o4755}))
            stack.enter_context(patch.object(desktop_launch, 'graphical_session',
                return_value={'id': 'other' if changed_session else 'session'}))
            stack.enter_context(patch.object(desktop_launch, 'session_environment', return_value={}))
            spawned = stack.enter_context(patch.object(desktop_launch.subprocess, 'Popen', return_value=child))
            stack.enter_context(patch.object(desktop_launch, 'process_identity',
                side_effect=ValueError('identity disappeared') if identity_error else None,
                return_value={'pid': 321, 'parent_pid': 1, 'start_time_ticks': 42}))
            if changed_image or changed_session:
                with self.assertRaises(ValueError):
                    desktop_launch.activate(app)
                spawned.assert_not_called()
            elif timeout or identity_error:
                with self.assertRaises((ValueError, owner.CaseUnavailable)):
                    desktop_launch.activate(app)
                child.terminate.assert_called_once()
            else:
                result = desktop_launch.activate(app)
                self.assertEqual(result['launch_identity']['pid'], 321)
                self.assertEqual(result['exit_code'], 0)
                child.terminate.assert_not_called()
                self.assertEqual(app.require_running.call_count, 2)

    def test_public_activation_rechecks_image_session_and_cleans_only_created_launcher(self):
        self.activation()
        for changes in ({'changed_image': True}, {'changed_session': True}, {'timeout': True}, {'identity_error': True}):
            with self.subTest(changes=changes):
                self.activation(**changes)

    def test_workspace_controller_preserves_exact_provider_then_transition_order(self):
        providers = [{'operation': operation} for operation in
                     ('provider-name-v1', 'provider-endpoint-v1-12345', 'provider-fixture-credential-v1', 'reply-fixture-prompt-v1')]
        operations = ['workspace-open-peer-v1', 'workspace-hide-main-v1', 'workspace-request-quit-v1',
            'workspace-cancel-quit-v1', 'workspace-request-quit-v1', 'workspace-focus-quit-v1', 'workspace-approve-quit-v1']
        transitions = [{'operation': operation} for operation in operations]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'leaf/evidence/windowing/workspace-activation-and-exit/execution.json'
            path.parent.mkdir(parents=True)
            def write(fixed, selected):
                path.write_text(json.dumps({'observations': {'launch': {'fixed_input': fixed}, 'transitions': selected}}))
            write(providers, transitions)
            records, declared = controller.delivery_records(root, {}, 'owned')
            self.assertEqual(records, providers + transitions)
            self.assertEqual(declared[-7:], operations)
            path.write_text(json.dumps({'observations': {'launch': {'fixed_input': providers[:2]},
                'native_input': providers[2]}}))
            records, declared = controller.delivery_records(root, {}, 'owned')
            self.assertEqual(records, providers[:3])
            self.assertEqual(declared[-1], 'provider-fixture-credential-v1')
            write(providers[:3], transitions[:1])
            with self.assertRaises(ValueError):
                controller.delivery_records(root, {}, 'owned')
            write(providers, transitions + transitions[:1])
            with self.assertRaises(ValueError):
                controller.delivery_records(root, {}, 'owned')

    def test_reactivation_requires_original_visible_and_no_new_duplicate_frame(self):
        process = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        main = {'bus_name': ':1.37', 'object_path': '/app/1', 'process': process, 'lineage': [process]}
        def row(identity, active=True, visible=True):
            return {'identity': identity, 'kind': 'frame', 'visible': visible, 'active': active,
                    'defunct': False, 'modal': False, 'parent': None}
        before = {'launch': process, 'engine': process, 'main': main, 'control': None,
                  'surfaces': [row(main, active=False, visible=False)]}
        after = {**before, 'surfaces': [row(main)]}
        self.assertTrue(surface_wire.reopened(before, after))
        self.assertFalse(surface_wire.reopened(before, before))
        duplicate = {**main, 'object_path': '/app/2'}
        self.assertFalse(surface_wire.reopened(before, {**after, 'surfaces': [row(main), row(duplicate)]}))
