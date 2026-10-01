import pathlib
import shutil
import tempfile
import unittest
from grading_identity import GRADING_FILES, grading_hash, implementation_files, report_id, retain_grader, verify_grader
from snapshot import identity


class GradingIdentityTests(unittest.TestCase):
    def test_manifest_rules_change_grading_identity_and_require_regrade(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            frozen, current = root / 'frozen', root / 'current'
            frozen.mkdir()
            for name in GRADING_FILES | {'run.py'}:
                (frozen / name).write_text(name)
            shutil.copytree(frozen, current)
            expected = identity(implementation_files(frozen))
            original = grading_hash(frozen)
            first = retain_grader(root, frozen, original)
            (current / 'benchmark.json').write_text('{"operations":[{"decisions":{"worker":{"option":"Enable"}}}]}')
            with self.assertRaisesRegex(ValueError, '--regrade'):
                verify_grader(frozen, current, expected, False)
            changed = verify_grader(frozen, current, expected, True)
            self.assertNotEqual(report_id('execution', original), report_id('execution', changed))
            second = retain_grader(root, current, changed)
            self.assertNotEqual(first, second)
            self.assertEqual((first / 'benchmark.json').read_text(), 'benchmark.json')

    def test_retained_grader_includes_and_identifies_its_pinned_dependencies(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=pathlib.Path(tmp);source=root/'source';source.mkdir()
            for name in GRADING_FILES | {'run.py','benchmark.json'}:
                (source/name).write_text(name)
            digest=grading_hash(source)
            retained=retain_grader(root,source,digest)
            self.assertEqual((retained/'requirements.txt').read_bytes(),(source/'requirements.txt').read_bytes())
            (retained/'requirements.txt').write_text('changed dependency')
            with self.assertRaisesRegex(ValueError,'differs from its identity'):
                retain_grader(root,source,digest)

    def test_regrading_preserves_execution_and_requires_explicit_selection(self):
        with tempfile.TemporaryDirectory() as tmp:
            frozen, current = pathlib.Path(tmp) / 'frozen', pathlib.Path(tmp) / 'current'
            frozen.mkdir()
            for name in GRADING_FILES | {'run.py', 'capture.py', 'snapshot.py'}:
                (frozen / name).write_text(name)
            shutil.copytree(frozen, current)
            expected = identity(implementation_files(frozen))
            original = verify_grader(frozen, current, expected, False)
            (current/'requirements.txt').write_text('updated dependency')
            with self.assertRaisesRegex(ValueError,'--regrade'):
                verify_grader(frozen,current,expected,False)
            self.assertNotEqual(original,verify_grader(frozen,current,expected,True))
            shutil.copyfile(frozen/'requirements.txt',current/'requirements.txt')
            (current / 'report.py').write_text('corrected grader')
            with self.assertRaisesRegex(ValueError, '--regrade'):
                verify_grader(frozen, current, expected, False)
            corrected = verify_grader(frozen, current, expected, True)
            self.assertNotEqual(report_id('execution', original), report_id('execution', corrected))
            self.assertEqual(corrected, verify_grader(frozen, current, expected, True))
            (current / 'run.py').write_text('changed execution')
            with self.assertRaisesRegex(ValueError, 'execution implementation'):
                verify_grader(frozen, current, expected, True)
            (frozen / 'capture.py').write_text('modified original')
            with self.assertRaisesRegex(ValueError, 'frozen execution'):
                verify_grader(frozen, current, expected, True)


if __name__ == '__main__':
    unittest.main()
