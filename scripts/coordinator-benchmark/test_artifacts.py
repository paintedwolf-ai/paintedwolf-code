import pathlib
import shutil
import tempfile
import unittest
from unittest.mock import patch
from artifacts import InvalidCandidate, retain_project, retain_outside


class ArtifactTests(unittest.TestCase):
    def test_capture_survives_original_project_removal_and_detects_changes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            project, capture = root / 'project', root / 'capture'
            project.mkdir()
            capture.mkdir()
            (project / 'answer.py').write_text('answer = 42\n')
            case = {'session_id': 'session', 'project_dir': str(project)}
            retained = retain_project(capture, case)
            shutil.rmtree(project)
            relocated = root / 'relocated'
            capture.rename(relocated)
            retained = retain_project(relocated, case)
            self.assertEqual((retained / 'answer.py').read_text(), 'answer = 42\n')
            (retained / 'answer.py').write_text('answer = 0\n')
            with self.assertRaisesRegex(ValueError, 'changed after capture'):
                retain_project(relocated, case)

    def test_outside_capture_binds_identity_and_detects_mutations_after_source_removal(self):
        for mutation in ('bytes', 'added', 'removed', 'session', 'path'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                outside, capture = root / 'outside', root / 'capture'
                outside.mkdir()
                capture.mkdir()
                (outside / 'receipt.json').write_text('{"accepted":true}')
                case = {'session_id': 'session', 'sandbox': {'path': str(outside)}}
                files = retain_outside(capture, case)
                shutil.rmtree(outside)
                self.assertEqual(retain_outside(capture, case), files)
                if mutation == 'bytes':
                    (files / 'receipt.json').write_text('{"accepted":false}')
                elif mutation == 'added':
                    (files / 'extra.json').write_text('{}')
                elif mutation == 'removed':
                    (files / 'receipt.json').unlink()
                elif mutation == 'session':
                    case['session_id'] = 'another-session'
                else:
                    case['sandbox']['path'] = str(root / 'another-source')
                with self.assertRaises(ValueError):
                    retain_outside(capture, case)

    def test_missing_outside_path_does_not_capture_current_directory(self):
        with tempfile.TemporaryDirectory() as temporary:
            capture = pathlib.Path(temporary)
            for sandbox in (None, {}, {'path': ''}):
                self.assertIsNone(retain_outside(capture, {'session_id': 'session', 'sandbox': sandbox}))
            self.assertEqual(list(capture.iterdir()), [])

    def test_interrupted_publication_preserves_complete_snapshot_or_nothing(self):
        for published in (False, True):
            with self.subTest(published=published), tempfile.TemporaryDirectory() as temporary:
                root = pathlib.Path(temporary)
                source, capture = root / 'source', root / 'capture'
                source.mkdir()
                capture.mkdir()
                (source / 'answer.py').write_text('answer = 42')
                case = {'session_id': 'session', 'project_dir': str(source)}
                rename = pathlib.Path.rename
                def interrupt(path, target):
                    if published:
                        rename(path, target)
                    raise OSError('injected interruption')
                with patch.object(pathlib.Path, 'rename', interrupt):
                    with self.assertRaisesRegex(OSError, 'injected interruption'):
                        retain_project(capture, case)
                self.assertEqual((capture / 'graded-project').exists(), published)
                self.assertEqual(len(list(capture.iterdir())), int(published))
                if published:
                    (source / 'answer.py').write_text('answer = 0')
                files = retain_project(capture, case)
                self.assertEqual((files / 'answer.py').read_text(), 'answer = 42')

    def test_symlink_source_and_retained_root_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            source, capture = root / 'source', root / 'capture'
            source.mkdir()
            capture.mkdir()
            link = root / 'link'
            link.symlink_to(source, target_is_directory=True)
            with self.assertRaises(InvalidCandidate):
                retain_project(capture, {'session_id': 'session', 'project_dir': str(link)})
            (capture / 'graded-project').symlink_to(source, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, 'directory is a symlink'):
                retain_project(capture, {'session_id': 'session', 'project_dir': str(source)})


if __name__ == '__main__':
    unittest.main()
