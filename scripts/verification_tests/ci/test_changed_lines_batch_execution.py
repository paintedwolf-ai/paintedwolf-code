"""Batched change accounting preserves the per-file coverage and budget scope."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import change_report


class ChangedLinesBatchTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "user.name", "Fixture")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], text=True)

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "fixture")
        return self.git("rev-parse", "HEAD").strip()

    def reference(self, base, paths, head=None):
        result = {}
        for path in paths:
            exists = subprocess.run(["git", "-C", str(self.root), "cat-file", "-e", f"{base}:{path}"],
                                    capture_output=True).returncode == 0
            if not exists:
                if head:
                    shown = subprocess.run(["git", "-C", str(self.root), "show", f"{head}:{path}"],
                                           capture_output=True, text=True)
                    text = shown.stdout if shown.returncode == 0 else None
                else:
                    file = self.root / path
                    text = file.read_text(errors="replace") if file.is_file() else None
                if text is not None:
                    result[path] = set(range(1, len(text.splitlines()) + 1))
                continue
            diff = self.git("diff", "--no-color", "--no-ext-diff", "--no-renames", "-U0", base,
                            *([head] if head else []), "--", path)
            lines = set()
            for line in diff.splitlines():
                match = change_report.HUNK.match(line)
                if match:
                    start, count = int(match.group(1)), int(match.group(2) or "1")
                    lines.update(range(start, start + count))
            if lines:
                result[path] = lines
        return result

    def test_working_and_committed_ranges_match_per_file_accounting(self):
        names = ["spaces here.py", "quote\"tab\t.py", "new\nline.py", "unicode-λ.py",
                 "deleted.py", "renamed.py", "mode.py", "binary.bin"]
        for name in names:
            (self.root / name).write_text("one\ntwo\nthree\nfour\n")
        (self.root / "binary.bin").write_bytes(b"old\0binary\n")
        base = self.commit()
        for name in names[:4]:
            (self.root / name).write_text("ONE\ntwo\nthree\nFOUR\nextra")
        (self.root / "deleted.py").unlink()
        (self.root / "renamed.py").rename(self.root / "renamed target.py")
        os.chmod(self.root / "mode.py", 0o755)
        (self.root / "binary.bin").write_bytes(b"new\0binary\n")
        (self.root / "new empty.py").touch()
        (self.root / "new binary.bin").write_bytes(b"a\0b\nc\n")
        (self.root / "new\naddition.py").write_text("new\nfile\n")
        paths = change_report.changed_paths(self.root, base)
        self.assertEqual(change_report.changed_lines(self.root, base, paths), self.reference(base, paths))
        head = self.commit()
        (self.root / "new\naddition.py").write_text("uncommitted\n")
        (self.root / "unselected.py").write_text("not in range\n")
        self.assertEqual(change_report.changed_lines(self.root, base, paths, head),
                         self.reference(base, paths, head))
        subset = paths[:2]
        self.assertEqual(change_report.changed_lines(self.root, base, subset, head),
                         self.reference(base, subset, head))

    def test_git_process_count_is_bounded_for_large_ranges(self):
        for number in range(64):
            (self.root / f"existing-{number}.py").write_text("old\n")
        base = self.commit()
        for number in range(64):
            (self.root / f"existing-{number}.py").write_text("new\n")
            (self.root / f"added-{number}.py").write_text("first\nsecond\n")
        head = self.commit()
        paths = change_report.changed_paths(self.root, base, head)
        with patch.object(change_report.subprocess, "run", wraps=subprocess.run) as calls:
            lines = change_report.changed_lines(self.root, base, paths, head)
        self.assertEqual(len(lines), 128)
        self.assertTrue(all(lines[f"existing-{i}.py"] == {1} for i in range(64)))
        self.assertTrue(all(lines[f"added-{i}.py"] == {1, 2} for i in range(64)))
        self.assertEqual(calls.call_count, 4)
        with patch.object(change_report.subprocess, "run", wraps=subprocess.run) as calls:
            self.assertEqual(change_report.changed_lines(self.root, base, []), {})
        calls.assert_not_called()
