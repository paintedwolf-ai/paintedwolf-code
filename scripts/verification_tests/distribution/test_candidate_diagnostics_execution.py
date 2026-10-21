"""Partial diagnostics preserve bytes and refuse to grant candidate authority."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from linux_release.candidate_diagnostics import retain

SCRIPTS = Path(__file__).resolve().parents[2]


def git(source, *arguments):
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    environment.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null")
    return subprocess.check_output(["git", "-C", str(source), "-c", "user.name=Fixture",
                                    "-c", "user.email=fixture@example.invalid", *arguments],
                                   env=environment, text=True).strip()


def repository(source):
    """A real checkout whose committed identity the report must carry."""
    source.mkdir(parents=True, exist_ok=True)
    git(source, "init", "-q")
    (source / "VERSION").write_text("1.2.3\n")
    git(source, "add", "VERSION")
    git(source, "commit", "-q", "-m", "fixture")
    return git(source, "rev-parse", "HEAD"), git(source, "rev-parse", "HEAD^{tree}")


class PartialCandidateDiagnosticsTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / "source"
        self.commit, self.tree = repository(self.source)
        self.bundle = self.source / "lycaon-den/src-tauri/target/x86_64-unknown-linux-gnu/release/bundle"

    def package(self, family, name, data=b"actual partial bytes"):
        path = self.bundle / family / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return path

    def test_only_canonical_pacman_extension_and_family_can_enter_diagnostics(self):
        package = self.package("pacman", "painted-wolf-code-1.2.3-1-x86_64.pkg.tar.zst", b"actual retained pacman fixture")
        for name in ("painted-wolf-code.pkg.tar.xz", "painted-wolf-code.zst"):
            self.package("pacman", name, b"not the canonical format")
        self.package("rpm", package.name, b"wrong output family")
        self.package("deb", "other-product.deb", b"another product")
        output = self.root / "diagnostics"
        report = retain(self.source, output, "c" * 40)
        self.assertEqual([row["source_path"] for row in report["packages"]], [package.relative_to(self.source).as_posix()])
        self.assertEqual((output / report["packages"][0]["file"]).read_bytes(), package.read_bytes())
        for field in ("complete_candidate", "installed_qualified", "publication_authority"):
            self.assertIs(report[field], False)

    def test_retains_selected_bytes_bound_to_the_real_source_and_controller(self):
        package = self.package("deb", "painted-wolf-code.deb")
        output = self.root / "diagnostics"
        report = retain(self.source, output, "c" * 40)
        self.assertEqual((output / "0-painted-wolf-code.deb").read_bytes(), package.read_bytes())
        self.assertEqual(report["packages"][0]["sha256"], hashlib.sha256(package.read_bytes()).hexdigest())
        self.assertEqual((report["source_commit"], report["source_tree"], report["controller_commit"]),
                         (self.commit, self.tree, "c" * 40))
        self.assertEqual(json.loads((output / "diagnostics.json").read_text()), report)
        self.assertEqual(oct((output / "0-painted-wolf-code.deb").stat().st_mode & 0o777), "0o600")

    def test_a_replacement_ref_cannot_relabel_the_source(self):
        self.package("deb", "painted-wolf-code.deb")
        (self.source / "VERSION").write_text("9.9.9\n")
        git(self.source, "commit", "-q", "-am", "other")
        other = git(self.source, "rev-parse", "HEAD")
        git(self.source, "reset", "-q", "--hard", self.commit)
        git(self.source, "replace", self.commit, other)
        report = retain(self.source, self.root / "diagnostics", "c" * 40)
        self.assertEqual((report["source_commit"], report["source_tree"]), (self.commit, self.tree))

    def test_symlink_payload_and_reused_output_refuse(self):
        outside = self.root / "actual.deb"
        outside.write_bytes(b"outside")
        link = self.bundle / "deb/painted-wolf-code.deb"
        link.parent.mkdir(parents=True)
        link.symlink_to(outside)
        with self.assertRaises((OSError, ValueError)):
            retain(self.source, self.root / "refused", "c" * 40)
        self.assertFalse((self.root / "refused").exists())
        with self.assertRaisesRegex(ValueError, "fresh"):
            retain(self.source, self.source / "output", "c" * 40)
        existing = self.root / "existing"
        existing.mkdir()
        with self.assertRaisesRegex(ValueError, "fresh"):
            retain(self.source, existing, "c" * 40)

    def test_controller_identity_and_a_bounded_nonempty_set_are_required(self):
        with self.assertRaisesRegex(ValueError, "immutable controller commit"):
            retain(self.source, self.root / "short", "main")
        with self.assertRaisesRegex(ValueError, "bounded nonempty"):
            retain(self.source, self.root / "empty", "c" * 40)
        for index in range(17):
            self.package("deb", f"painted-wolf-code-{index}.deb")
        with self.assertRaisesRegex(ValueError, "bounded nonempty"):
            retain(self.source, self.root / "unbounded", "c" * 40)
        self.assertFalse((self.root / "unbounded").exists())

    def test_command_line_retains_through_the_declared_entry(self):
        self.package("deb", "painted-wolf-code.deb")
        output = self.root / "cli"
        result = subprocess.run([sys.executable, "-m", "scripts.linux_release.candidate_diagnostics",
                                 "--source", str(self.source), "--output", str(output), "--controller", "d" * 40],
                                cwd=SCRIPTS.parent, capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((output / "diagnostics.json").read_text())["controller_commit"], "d" * 40)


if __name__ == "__main__":
    unittest.main()
