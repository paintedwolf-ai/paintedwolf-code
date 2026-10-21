"""Pacman recipes preserve the sealed common payload without package-time fixups."""

import hashlib
from pathlib import Path
import subprocess
import shutil
import tarfile
import tempfile
import unittest

from linux_release.arch import recipe, validate_resources, write_payload
from release_semver import pacman_package_version, parse

ROOT = Path(__file__).resolve().parents[3]


class ArchPackageTests(unittest.TestCase):
    def test_versions_preserve_release_and_reject_ambiguous_prerelease_ordering(self):
        self.assertEqual(pacman_package_version(parse("1.2.3")), "1.2.3")
        self.assertEqual(pacman_package_version(parse("1.2.3-rc6")), "1.2.3pre.rc6")
        self.assertEqual(pacman_package_version(parse("1.2.3-beta.12+source")), "1.2.3pre.beta.12")
        for version in ["1.2.3-4", "1.2.3-rc.x", "1.2.3-custom", "1.2.3-rc;touch /tmp/invalid"]:
            with self.subTest(version=version), self.assertRaises(ValueError):
                pacman_package_version(parse(version))

    def test_generated_recipe_copies_exact_bytes_modes_and_unicode_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tree = root / "tree"
            binary = tree / "opt/painted-wolf-code/bin/pw"
            binary.parent.mkdir(parents=True)
            binary.write_bytes(b"sealed\x00executable")
            binary.chmod(0o755)
            document = tree / "usr/share/doc/painted-wolf-code/Unicode λ with spaces"
            document.parent.mkdir(parents=True)
            document.write_text("retained notices")
            document.chmod(0o644)
            link = tree / "usr/pw-link"
            link.symlink_to("../opt/painted-wolf-code/bin/pw")
            service = tree / "etc/init.d/paintedwolf-vault-identity"
            service.parent.mkdir(parents=True)
            service.write_text("#!/sbin/openrc-run\n")
            service.chmod(0o755)
            payload = root / "payload.tar.xz"
            write_payload(tree, payload, 1234)
            before = hashlib.sha256(payload.read_bytes()).hexdigest()
            binary.touch()
            write_payload(tree, root / "second.tar.xz", 1234)
            self.assertEqual(before, hashlib.sha256((root / "second.tar.xz").read_bytes()).hexdigest())
            text = recipe(ROOT, payload, "aarch64", "1.2.3-rc6")
            recipe_path = root / "PKGBUILD"
            recipe_path.write_text(text)
            source = root / "src"
            source.mkdir()
            with tarfile.open(payload) as archive:
                self.assertTrue(all(member.uid == member.gid == 0 for member in archive))
                archive.extractall(source)
            installed = root / "package"
            installed.mkdir()
            result = subprocess.run(["bash", "-c", 'source "$1"; srcdir="$2"; pkgdir="$3"; package',
                                     "fixture", str(recipe_path), str(source), str(installed)],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((installed / binary.relative_to(tree)).read_bytes(), binary.read_bytes())
            self.assertEqual((installed / binary.relative_to(tree)).stat().st_mode & 0o777, 0o755)
            self.assertEqual((installed / document.relative_to(tree)).stat().st_mode & 0o777, 0o644)
            self.assertEqual((installed / service.relative_to(tree)).read_bytes(), service.read_bytes())
            self.assertEqual((installed / service.relative_to(tree)).stat().st_mode & 0o777, 0o755)
            self.assertEqual((installed / "usr/pw-link").readlink().as_posix(), "../opt/painted-wolf-code/bin/pw")
            self.assertIn(before, text)
            self.assertIn("'!strip' '!debug' '!lto'", text)
            self.assertIn("arch=(aarch64)", text)
            self.assertIn("'!zipman' '!purge' 'libtool' 'staticlibs' 'emptydirs' 'docs'", text)
            with self.assertRaises(ValueError):
                recipe(ROOT, payload, "any", "1.2.3")

    def test_install_hook_is_a_hashed_makepkg_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative in ["packaging/linux/arch/PKGBUILD.in", "packaging/linux/dependencies.json",
                             "lycaon/internal/platformfloor/linux_floor.txt", "RELEASE_BUILD"]:
                destination = root / relative
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / relative, destination)
            hook = root / "packaging/linux/arch/painted-wolf-code.install"
            hook.write_text("post_install() { :; }\n")
            payload = root / "payload.tar.xz"
            payload.write_bytes(b"payload")
            generated = root / "PKGBUILD"
            generated.write_text(recipe(root, payload, "x86_64", "1.2.3"))
            result = subprocess.run(["bash", "-c", 'source "$1"; printf "%s\\n" "$install" "${source[@]}" "${sha256sums[@]}"',
                                     "fixture", str(generated)], capture_output=True, text=True, check=True)
            self.assertEqual(result.stdout.splitlines(), [hook.name, payload.name, hook.name,
                             hashlib.sha256(payload.read_bytes()).hexdigest(),
                             hashlib.sha256(hook.read_bytes()).hexdigest()])

    def test_incomplete_and_escaping_payloads_fail_before_construction(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tree = root / "tree"
            (tree / "usr").mkdir(parents=True)
            with self.assertRaisesRegex(ValueError, "incomplete"):
                validate_resources(tree)
            (tree / "usr/escape").symlink_to("../../../outside")
            with self.assertRaisesRegex(ValueError, "escapes"):
                write_payload(tree, root / "invalid.tar.xz", 1234)
