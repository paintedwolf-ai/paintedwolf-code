import pathlib
import signal
import subprocess
import tempfile
import unittest
from capture_storage import PRIVATE_CONFIG_FILES, cleanup_private_config, clone_file, share_file, stage_runtime


class CaptureStorageTests(unittest.TestCase):
    def test_forced_launcher_exit_does_not_retain_copied_credentials(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            source, captured = root / 'source', root / 'capture/config'
            source.mkdir()
            captured.mkdir(parents=True)
            for name in PRIVATE_CONFIG_FILES:
                (source / name).write_text('source value')
                (captured / name).write_text('copied value')
            evidence = captured / 'retained-settings.json'
            evidence.write_text('evidence')
            result = subprocess.run(['bash', '-c',
                'trap \'rm -f "$1/config/providers.local.yaml"\' EXIT; kill -KILL $$',
                'launcher', str(captured.parent)])
            self.assertEqual(result.returncode, -signal.SIGKILL)
            self.assertTrue((captured / 'providers.local.yaml').exists())
            cleanup_private_config(captured, {'kind': 'exited', 'returncode': result.returncode})
            cleanup_private_config(captured, {'kind': 'exited', 'returncode': result.returncode})
            self.assertTrue(all(not (captured / name).exists() for name in PRIVATE_CONFIG_FILES))
            self.assertTrue(all((source / name).read_text() == 'source value' for name in PRIVATE_CONFIG_FILES))
            self.assertEqual(evidence.read_text(), 'evidence')

    def test_unknown_execution_preserves_configuration(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = pathlib.Path(tmp)
            path = config / 'providers.local.yaml'
            path.write_text('active configuration')
            for state in ['execution_unknown', 'running', None]:
                cleanup_private_config(config, {'kind': state})
                self.assertEqual(path.read_text(), 'active configuration')

    def test_private_cleanup_never_follows_configuration_links(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            source = root / 'source'
            source.mkdir()
            key = source / 'providers.local.yaml'
            key.write_text('source value')
            linked = root / 'linked-config'
            linked.symlink_to(source, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, 'directory is a symlink'):
                cleanup_private_config(linked, {'kind': 'exited'})
            config = root / 'config'
            config.mkdir()
            (config / key.name).symlink_to(key)
            cleanup_private_config(config, {'kind': 'exited'})
            self.assertFalse((config / key.name).is_symlink())
            self.assertEqual(key.read_text(), 'source value')

    def test_active_runtime_copies_cannot_mutate_the_frozen_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            engine, catalog, capture = root / 'binary', root / 'catalog', root / 'capture'
            engine.write_bytes(b'engine')
            engine.chmod(0o755)
            catalog.mkdir()
            (catalog / 'policy').write_text('original')
            (capture / 'module').mkdir(parents=True)
            stage_runtime(engine, catalog, capture)
            self.assertFalse(engine.samefile(capture / 'engine'))
            self.assertEqual((capture / 'engine').stat().st_mode & 0o777, 0o755)
            (capture / 'engine').write_bytes(b'changed')
            (capture / 'module/config/policy').write_text('changed')
            self.assertEqual(engine.read_bytes(), b'engine')
            self.assertEqual((catalog / 'policy').read_text(), 'original')
            with self.assertRaises(FileExistsError):
                clone_file(engine, capture / 'engine')

    def test_only_identical_completed_files_share_storage(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            original, captured = root / 'original', root / 'captured'
            original.write_bytes(b'frozen runtime')
            captured.write_bytes(b'frozen runtime')
            share_file(original, captured)
            self.assertTrue(original.samefile(captured))
            share_file(original, captured)
            self.assertEqual(captured.read_bytes(), b'frozen runtime')
            changed = root / 'changed'
            changed.write_bytes(b'changed source')
            share_file(original, changed)
            self.assertEqual(changed.read_bytes(), b'changed source')
            self.assertFalse(original.samefile(changed))
            link = root / 'link'
            link.symlink_to(changed)
            share_file(original, link)
            self.assertTrue(link.is_symlink())


if __name__ == '__main__':
    unittest.main()
