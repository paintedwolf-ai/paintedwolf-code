"""Normal launch storage is shared, explicit and independent of discarded ambient vars."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release import installed_storage


class InstalledStorageTests(unittest.TestCase):
    def test_release_identifier_and_linux_storage_overrides_match_the_declared_contract(self):
        root = Path(__file__).resolve().parents[4]
        base = json.loads((root / 'lycaon-den/src-tauri/tauri.conf.json').read_text())
        linux = json.loads((root / 'lycaon-den/src-tauri/tauri.linux.conf.json').read_text())
        self.assertEqual(base['identifier'], installed_storage.APPLICATION_IDENTIFIER)
        self.assertNotIn('identifier', linux)
        for document in (base, linux):
            self.assertNotIn('appDirectoriesOverride', document.get('app', {}))
            for window in document.get('app', {}).get('windows', []):
                self.assertNotIn('dataDirectory', window)

    def test_actual_selected_environment_ignores_discarded_ambient_overrides(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = str(Path(temporary).resolve())
            selected_home = patch.object(Path, 'home', return_value=Path(directory))
            selected_home.start()
            self.addCleanup(selected_home.stop)
            Path(directory).chmod(0o700)
            with patch.dict(os.environ, {key: '/not-selected' for key in installed_storage.PROFILE_OVERRIDES}):
                storage = installed_storage.resolve({'HOME': directory})
            self.assertEqual(storage.config, Path(directory) / '.config/paintedwolf')
            self.assertEqual(storage.webview, Path(directory) / '.local/share/dev.paintedwolf.code')
            self.assertEqual(storage.uid, os.getuid())
            self.assertEqual(storage.facts()['home'], directory)
            for key in installed_storage.PROFILE_OVERRIDES:
                with self.subTest(key=key), self.assertRaises(ValueError):
                    installed_storage.resolve({'HOME': directory, key: '/explicit-unsupported'})

    def test_foreign_home_symlink_or_writable_home_cannot_select_profile_storage(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            home = root / 'home'
            home.mkdir(mode=0o700)
            link = root / 'alias'
            link.symlink_to(home, target_is_directory=True)
            with patch.object(Path, 'home', return_value=home):
                with self.assertRaises(ValueError):
                    installed_storage.resolve({'HOME': str(root)})
                home.chmod(0o777)
                with self.assertRaises(ValueError):
                    installed_storage.resolve({'HOME': str(home)})
            with patch.object(Path, 'home', return_value=link), self.assertRaises(ValueError):
                installed_storage.resolve({'HOME': str(link)})
