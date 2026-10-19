"""Release metadata comes from exact immutable objects, independent of checkout state."""

import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from linux_release.publication_source import facts


class PublicationSourceTests(unittest.TestCase):
    def git(self, root, *arguments):
        return subprocess.check_output(["git", "-C", str(root), "-c", "user.name=Fixture",
            "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", *arguments])

    def fixture(self, root):
        self.git(root, "init", "--quiet")
        (root / "VERSION").write_text("1.2.3-rc.6\n")
        (root / "RELEASE_BUILD").write_text("14\n")
        (root / "flake.lock").write_text('{"nodes":{}}\n')
        return self.commit(root)

    def commit(self, root):
        self.git(root, "add", "--", "VERSION", "RELEASE_BUILD", "flake.lock")
        self.git(root, "commit", "--quiet", "-m", "Fixture")
        return self.git(root, "rev-parse", "HEAD").decode().strip()

    def test_dirty_checkout_and_git_environment_cannot_change_selected_release_facts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            selected = self.fixture(root)
            (root / "VERSION").write_text("9.9.9\n")
            (root / "RELEASE_BUILD").write_text("22\n")
            (root / "flake.lock").write_text("dirty\n")
            with patch.dict(os.environ, {"GIT_DIR": "/nonexistent", "GIT_WORK_TREE": "/nonexistent"}):
                result = facts(root, selected)
            self.assertEqual(result["version"], "1.2.3-rc.6")
            self.assertEqual(result["release_build"], 14)
            self.assertEqual(result["channel"], "preview")
            self.assertEqual(result["flake_lock_sha256"], hashlib.sha256(b'{"nodes":{}}\n').hexdigest())
            self.assertEqual(result["source_tree"], self.git(root, "rev-parse", selected + "^{tree}").decode().strip())

    def test_local_replacement_ref_cannot_relabel_selected_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            selected = self.fixture(root)
            (root / "VERSION").write_text("9.9.9\n")
            replacement = self.commit(root)
            self.git(root, "replace", selected, replacement)
            self.assertEqual(facts(root, selected)["version"], "1.2.3-rc.6")

    def test_symlink_metadata_and_oversize_metadata_are_refused(self):
        for mutation in ["symlink", "oversize"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.fixture(root)
                if mutation == "symlink":
                    (root / "VERSION").unlink()
                    (root / "VERSION").symlink_to("1.2.3")
                else:
                    (root / "VERSION").write_text("1" * 1025)
                selected = self.commit(root)
                with self.assertRaises(ValueError):
                    facts(root, selected)
