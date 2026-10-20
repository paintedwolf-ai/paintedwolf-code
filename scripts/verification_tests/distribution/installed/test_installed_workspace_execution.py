"""Native editor observations bind owned bytes, not a successful button action alone."""
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, patch

from linux_release.installed_cases import workspace


class InstalledWorkspaceTests(unittest.TestCase):
    def test_actual_file_observation_requires_exact_saved_fixture_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'fixture.txt'
            path.write_text(workspace.BEFORE)
            self.assertFalse(workspace.observed_file(path)['expected_content'])
            path.write_text(workspace.AFTER)
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
            keyboard = Mock(side_effect=lambda *args: (path.write_text(workspace.AFTER), {'focused_entry': True})[1])
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
            self.assertEqual(ui.fill.call_args_list[0].args, (2, 'Contents of ' + path.name, workspace.AFTER))
            self.assertEqual(ui.fill.call_args_list[1].args, (2, 'Editor command or search', 'qualification needle'))
            self.assertIn('applied-edit-review-interaction', result['remaining_requirements'])
            cli.assert_called_once_with({}, 'pw', ['open', str(path.parent)], timeout=30)
            self.assertEqual([call.args[0] for call in ui.click.call_args_list[:3]], ['Skip', 'Files', path.name])
            self.assertEqual(streams['project-open-stdout'], b'opened')

    def test_fixed_keyboard_requires_owned_focus_and_releases_modifiers_on_failure(self):
        identity = {'pid': 123, 'parent_pid': 1, 'start_time_ticks': 42}
        ui, node, frame = MagicMock(), MagicMock(), MagicMock()
        ui.find.return_value = node
        node.get_process_id.return_value = 123
        node.queryComponent.return_value.grabFocus.return_value = True
        node.getState.return_value.contains.return_value = True
        frame.getState.return_value.contains.return_value = True
        ui.windows.side_effect = lambda: iter([(identity, frame)])
        native = SimpleNamespace(ModifierType=SimpleNamespace(CONTROL=2),
            KeySynthType=SimpleNamespace(LOCKMODIFIERS=5, UNLOCKMODIFIERS=6, SYM=3, STRING=4),
            generate_keyboard_event=Mock(return_value=True))
        with patch.object(workspace, 'process_identity', return_value=identity), \
                patch.object(workspace, 'owned_process', return_value=True):
            result = workspace.fixed_editor_keyboard(SimpleNamespace(), ui, Path('/owned/fixture'), native)
            self.assertTrue(result['normal_keyboard_replace_and_save'])
            self.assertFalse(result['text_readback'])
            self.assertEqual([call.args for call in native.generate_keyboard_event.call_args_list], [
                (4, None, 5), (97, None, 3), (4, None, 6), (0, workspace.AFTER, 4),
                (4, None, 5), (115, None, 3), (4, None, 6)])
            native.generate_keyboard_event.reset_mock()
            native.generate_keyboard_event.side_effect = [True, False, True]
            with self.assertRaises(workspace.CaseUnavailable):
                workspace.fixed_editor_keyboard(SimpleNamespace(), ui, Path('/owned/fixture'), native)
            self.assertEqual(native.generate_keyboard_event.call_args_list[-1].args, (4, None, 6))
            native.generate_keyboard_event.reset_mock()
            node.getState.return_value.contains.return_value = False
            with self.assertRaises(workspace.CaseUnavailable):
                workspace.fixed_editor_keyboard(SimpleNamespace(), ui, Path('/owned/fixture'), native)
            native.generate_keyboard_event.assert_not_called()

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
