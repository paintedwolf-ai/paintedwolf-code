"""Native editor observations bind owned bytes, not a successful button action alone."""
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import workspace
from linux_release.installed_input import focus as focus_owner


class InstalledWorkspaceTests(unittest.TestCase):
    def test_passive_focus_retry_never_mutates_and_respects_one_operation_deadline(self):
        actor = focus_owner.EntryFocus.__new__(focus_owner.EntryFocus)
        actor.deadline = 90
        retry = focus_owner._FocusUnavailable('typed missing focus')
        with patch.object(actor, '_observe_once', side_effect=[retry, {'focused': True}]) as observe, \
                patch.object(focus_owner.time, 'monotonic', side_effect=[0, 1]), \
                patch.object(focus_owner.time, 'sleep'):
            self.assertEqual(actor.observe(), {'focused': True})
            self.assertEqual(observe.call_count, 2)
        with patch.object(actor, '_observe_once', side_effect=retry) as observe, \
                patch.object(focus_owner.time, 'monotonic', side_effect=[89, 90]), \
                patch.object(focus_owner.time, 'sleep') as sleep:
            with self.assertRaises(focus_owner._FocusUnavailable):
                actor.observe()
            observe.assert_called_once_with()
            sleep.assert_not_called()

    def test_native_observer_exception_preserves_partial_delivery_evidence(self):
        ui = MagicMock()
        ui.__enter__.return_value = ui
        error = RuntimeError('unretained native diagnostic prose')
        error.observations = {'native_input': {'completed': False, 'events': [{'ordinal': 0}]}}
        error.streams = {'fixed-proof': b'owned partial observation'}
        with patch.object(workspace, 'DesktopUI', return_value=ui), \
                patch.object(workspace, 'interactive_window', return_value={}), \
                patch.object(workspace, 'run', return_value={'exit_code': 0, 'timed_out': False,
                    'output_limit': False, 'stdout': b'', 'stderr': b''}), \
                patch.object(workspace, 'fixed_editor_keyboard', side_effect=error):
            with self.assertRaises(workspace.CaseUnavailable) as refusal:
                workspace.edit_and_find(SimpleNamespace(binding={}), Path('/owned/fixture'))
        self.assertEqual(refusal.exception.observations['native_input'], error.observations['native_input'])
        self.assertEqual(refusal.exception.streams['fixed-proof'], error.streams['fixed-proof'])
        self.assertNotIn('unretained native diagnostic prose', str(refusal.exception.observations))

    def test_actual_file_observation_requires_exact_saved_fixture_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'fixture.txt'
            path.write_text(workspace.BEFORE)
            self.assertFalse(workspace.observed_file(path)['expected_content'])
            path.write_text(workspace.EDITOR_TEXT)
            self.assertTrue(workspace.observed_file(path)['expected_content'])
            link = Path(directory) / 'link'
            link.symlink_to(path)
            with self.assertRaises(OSError):
                workspace.observed_file(link)
            path.write_bytes(b'x' * 65537)
            with self.assertRaises(ValueError):
                workspace.observed_file(path)

    def test_editor_uses_exact_owned_label_then_save_and_normal_find(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'fixture.txt'
            path.write_text(workspace.BEFORE)
            ui = MagicMock()
            ui.__enter__.return_value = ui
            ui.api.ROLE_TEXT, ui.api.ROLE_ENTRY = 1, 2
            keyboard = Mock(side_effect=lambda *args: (path.write_text(workspace.EDITOR_TEXT), {'focused_entry': True})[1])
            with patch.object(workspace, 'DesktopUI', return_value=ui), \
                    patch.object(workspace, 'interactive_window', return_value={'visible': True}), \
                    patch.object(workspace, 'fixed_editor_keyboard', keyboard), \
                    patch.object(workspace, 'run', return_value={'exit_code': 0, 'timed_out': False,
                        'output_limit': False, 'stdout': b'opened', 'stderr': b''}) as cli:
                result, streams = workspace.edit_and_find(SimpleNamespace(binding={}), path)
            self.assertTrue(result['saved_file']['expected_content'])
            self.assertTrue(result['normal_optional_setup_skip'])
            self.assertEqual(ui.click.call_args_list[0].args, ('Skip',))
            ui.find.assert_called_once_with(ui.api.ROLE_PUSH_BUTTON, 'Open a folder…')
            self.assertEqual(ui.fill.call_args_list[0].args, (2, 'Contents of ' + path.name, workspace.EDITOR_TEXT))
            self.assertEqual(ui.fill.call_args_list[1].args, (2, 'Find', 'qualification needle'))
            self.assertIn('applied-edit-review-interaction', result['remaining_requirements'])
            cli.assert_called_once_with({}, 'pw', ['open', str(path.parent)], timeout=30)
            self.assertEqual([call.args[0] for call in ui.click.call_args_list[:3]], ['Skip', 'Files', path.name])
            self.assertEqual(streams['project-open-stdout'], b'opened')

    def test_saved_bytes_remain_observed_when_later_search_is_unavailable(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'fixture.txt'
            path.write_text(workspace.EDITOR_TEXT)
            ui = MagicMock()
            ui.__enter__.return_value = ui
            ui.fill.side_effect = [None, workspace.CaseUnavailable('search unavailable')]
            with patch.object(workspace, 'DesktopUI', return_value=ui), \
                    patch.object(workspace, 'interactive_window', return_value={}), \
                    patch.object(workspace, 'fixed_editor_keyboard', return_value={'completed': True}), \
                    patch.object(workspace, 'run', return_value={'exit_code': 0, 'timed_out': False,
                        'output_limit': False, 'stdout': b'', 'stderr': b''}):
                with self.assertRaises(workspace.CaseUnavailable) as refusal:
                    workspace.edit_and_find(SimpleNamespace(binding={}), path)
            self.assertTrue(refusal.exception.observations['saved_file']['expected_content'])
            self.assertEqual(refusal.exception.observations['saved_file']['bytes'], len(workspace.EDITOR_TEXT.encode()))

    def test_fixed_keyboard_requires_owned_focus_and_releases_modifiers_on_failure(self):
        identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        ui, node, frame = MagicMock(), MagicMock(), MagicMock()
        ui.find.return_value = node
        node.get_process_id.return_value = 123
        node.queryComponent.return_value.grabFocus.return_value = True
        node.getState.return_value.contains.return_value = True
        frame.getState.return_value.contains.return_value = True
        ui.api.ROLE_FRAME, ui.api.ROLE_DIALOG = 23, 16
        node.get_parent.return_value = frame
        frame.getRole.return_value = 23
        frame.get_process_id.return_value = 123
        app = SimpleNamespace(observations={'launch_identity': identity}, require_running=lambda: None)
        native = SimpleNamespace(ModifierType=SimpleNamespace(CONTROL=2),
            KeySynthType=SimpleNamespace(LOCKMODIFIERS=5, UNLOCKMODIFIERS=6, SYM=3, STRING=4),
            generate_keyboard_event=Mock(return_value=True))
        with patch.object(focus_owner, 'process_identity', return_value=identity), \
                patch.object(focus_owner, 'owned_process', return_value=True):
            result = workspace.fixed_editor_keyboard(app, ui, Path('/owned/fixture'), native)
            self.assertTrue(result['normal_keyboard_replace_and_save'])
            self.assertFalse(result['text_readback'])
            self.assertEqual([call.args for call in native.generate_keyboard_event.call_args_list], [
                (4, None, 5), (97, None, 3), (4, None, 6), (0, workspace.EDITOR_TEXT, 4),
                (4, None, 5), (115, None, 3), (4, None, 6)])
            native.generate_keyboard_event.reset_mock()
            native.generate_keyboard_event.side_effect = [True, False, True]
            with self.assertRaises(workspace.CaseUnavailable):
                workspace.fixed_editor_keyboard(app, ui, Path('/owned/fixture'), native)
            self.assertEqual(native.generate_keyboard_event.call_args_list[-1].args, (4, None, 6))
            native.generate_keyboard_event.reset_mock()
            node.getState.return_value.contains.return_value = False
            with self.assertRaises(workspace.CaseUnavailable):
                workspace.fixed_editor_keyboard(app, ui, Path('/owned/fixture'), native)
            native.generate_keyboard_event.assert_not_called()
            node.getState.return_value.contains.return_value = True
            frame.getState.return_value.contains.return_value = False
            with self.assertRaises(workspace.CaseUnavailable) as refusal:
                workspace.fixed_editor_keyboard(app, ui, Path('/owned/fixture'), native)
            self.assertEqual(refusal.exception.observations['editor_focus']['container_identity'], identity)
            self.assertFalse(refusal.exception.observations['editor_focus']['container_active'])
            native.generate_keyboard_event.assert_not_called()
            frame.getState.return_value.contains.return_value = True
            node.get_process_id.return_value = 456
            child = {'pid': 456, 'parent_pid': 123, 'start_time_ticks': 43}
            native.generate_keyboard_event.side_effect = None
            with patch.object(focus_owner, 'process_identity', side_effect=lambda pid: child if pid == 456 else identity):
                result = workspace.fixed_editor_keyboard(app, ui, Path('/owned/fixture'), native)
            self.assertEqual(result['ui_identity'], child)
            self.assertEqual(native.generate_keyboard_event.call_count, 7)

    def test_replaced_accessible_entry_is_observed_without_refocusing_or_replaying(self):
        identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        ui, original, replacement, frame = MagicMock(), MagicMock(), MagicMock(), MagicMock()
        ui.api.ROLE_FRAME, ui.api.ROLE_DIALOG = 23, 16
        ui.find.side_effect = [original, replacement]
        original.get_process_id.return_value = replacement.get_process_id.return_value = 123
        original.queryComponent.return_value.grabFocus.return_value = True
        original.getState.return_value.contains.return_value = False
        replacement.getState.return_value.contains.return_value = True
        replacement.get_parent.return_value = frame
        frame.get_process_id.return_value = 123
        frame.getRole.return_value = 23
        frame.getState.return_value.contains.return_value = True
        app = SimpleNamespace(observations={'launch_identity': identity}, require_running=lambda: None)
        with patch.object(focus_owner, 'process_identity', return_value=identity), \
                patch.object(focus_owner, 'owned_process', return_value=True):
            selected = focus_owner.EntryFocus(app, ui, 'Contents of fixture')
            self.assertEqual(selected.observe()['entry'], identity)
        replacement.queryComponent.assert_not_called()
        original.queryComponent.return_value.grabFocus.assert_called_once_with()

    def test_accepted_keyboard_dispatch_without_saved_bytes_never_passes(self):
        ui = MagicMock()
        ui.__enter__.return_value = ui
        with patch.object(workspace, 'DesktopUI', return_value=ui), \
                patch.object(workspace, 'interactive_window', return_value={}), \
                patch.object(workspace, 'run', return_value={'exit_code': 0, 'timed_out': False,
                    'output_limit': False, 'stdout': b'', 'stderr': b''}), \
                patch.object(workspace, 'fixed_editor_keyboard', return_value={'normal_keyboard_replace_and_save': True}), \
                patch.object(workspace, 'observed_file', return_value={'expected_content': False, 'bytes': 57}), \
                patch.object(workspace.time, 'monotonic', side_effect=[0, 1, 31]), \
                patch.object(workspace.time, 'sleep'):
            with self.assertRaises(workspace.CaseUnavailable) as refusal:
                workspace.edit_and_find(SimpleNamespace(binding={}), Path('/owned/fixture'))
        self.assertEqual(refusal.exception.observations['keyboard_delivery'], {
            'dispatch_accepted': True, 'effect_observed': False})
        self.assertEqual(refusal.exception.observations['saved_file']['bytes'], 57)
        self.assertNotIn('Find in file', [call.args[0] for call in ui.click.call_args_list])

    def test_missing_editor_retains_bound_process_and_capture_evidence(self):
        app = MagicMock()
        app.__enter__.return_value = app
        app.observations = {'engine_identity': {'pid': 123}, 'capture_complete': True}
        app.streams.return_value = {'app-stderr': b'owned capture'}
        error = workspace.CaseUnavailable('missing')
        error.observations = {'desktop_control': {'role': 1, 'name': 'Contents of fixture', 'match_count': 0}}
        with patch.object(workspace, 'InstalledApp', return_value=app), patch.object(
                workspace, 'fixture_path', return_value=Path('/owned/fixture')), patch.object(
                workspace, 'edit_and_find', side_effect=error):
            with self.assertRaises(workspace.CaseUnavailable) as refusal:
                workspace.execute({}, 'files', 'edit-search-review', None)
        self.assertEqual(refusal.exception.observations['engine_identity'], {'pid': 123})
        self.assertEqual(refusal.exception.observations['desktop_control']['match_count'], 0)
        self.assertEqual(refusal.exception.streams, {'app-stderr': b'owned capture'})

    def test_owned_project_fixture_is_retained_inside_evidence_with_private_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            path = workspace.fixture_path(root)
            self.assertTrue(path.is_relative_to(root / 'workspace-fixtures'))
            self.assertEqual(path.read_text(), workspace.BEFORE)
            self.assertEqual((root / 'workspace-fixtures').stat().st_mode & 0o777, 0o700)
            self.assertTrue(path.parent.exists())

    def test_refused_or_overflowed_cli_cannot_reach_editor_mutation(self):
        for code, output in [(1, b'refused'), (0, b'x' * ((512 << 10) + 1))]:
            ui = MagicMock()
            ui.__enter__.return_value = ui
            with patch.object(workspace, 'DesktopUI', return_value=ui), patch.object(
                    workspace, 'interactive_window', return_value={}), patch.object(
                    workspace, 'run', return_value={'exit_code': code, 'timed_out': False,
                        'output_limit': False, 'stdout': output, 'stderr': b''}):
                with self.assertRaises(workspace.CaseFailed) as refusal:
                    workspace.edit_and_find(SimpleNamespace(binding={}), Path('/owned/fixture.txt'))
            ui.fill.assert_not_called()
            self.assertEqual([call.args[0] for call in ui.click.call_args_list], ['Skip'])
            self.assertLessEqual(len(refusal.exception.streams['project-open-stdout']), 512 << 10)
            self.assertEqual(refusal.exception.observations['project_open']['exit_code'], code)
