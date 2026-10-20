"""Native editor observations bind owned bytes, not a successful button action alone."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, patch

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
            ui.click.side_effect = lambda name: path.write_text(workspace.AFTER) if name == 'Save' else None
            with patch.object(workspace, 'DesktopUI', return_value=ui), \
                    patch.object(workspace, 'interactive_window', return_value={'visible': True}):
                result = workspace.edit_and_find(None, path)
            self.assertTrue(result['saved_file']['expected_content'])
            self.assertTrue(result['normal_optional_setup_skip'])
            self.assertEqual(ui.click.call_args_list[0].args, ('Skip',))
            self.assertEqual(ui.fill.call_args_list[0].args, (1, 'Contents of ' + str(path), workspace.AFTER))
            self.assertEqual(ui.fill.call_args_list[1].args, (2, 'Editor command or search', 'qualification needle'))
            self.assertIn('applied-edit-review-interaction', result['remaining_requirements'])
