"""Final Nix manifest identities describe installed ELFs after all fixups."""

import hashlib
import json
from pathlib import Path
import struct
import tempfile
import unittest

from linux_release import nix_layout


class NixFinalLayoutTests(unittest.TestCase):
    def test_plain_cargo_desktop_build_explicitly_selects_production_protocol(self):
        source = Path(__file__).resolve().parents[3] / "packaging/nix/shell.nix"
        recipe = source.read_text()
        self.assertIn('cargoBuildFeatures = [ "tauri/custom-protocol" ];', recipe)
        self.assertIn('build.frontendDist = "${frontend}";', recipe)


    def fixture(self, directory, architecture="x86_64"):
        output = Path(directory).resolve()
        root = output / "lib/painted-wolf-code"
        (root / "bin").mkdir(parents=True)
        (root / "engine-root").mkdir()
        (root / "release-version").write_text("1.0.2-rc6\n")
        (root / "THIRD-PARTY-NOTICES.md").write_text("Dependency notices\n")
        for role in nix_layout.ROLES:
            self.elf(root / "bin" / role, architecture)
        return output, root

    def elf(self, path, architecture):
        header = bytearray(64)
        header[:7] = b"\x7fELF\x02\x01\x01"
        struct.pack_into("<H", header, 18, nix_layout.MACHINES[architecture])
        path.write_bytes(header)
        path.chmod(0o755)

    def seal(self, output, architecture="x86_64"):
        return nix_layout.seal(output, architecture, "1.0.2-rc6", Path("/selected-nix/bin/nix-store"))

    def test_both_native_architectures_retain_final_fixup_bytes_and_generation_paths(self):
        for architecture in nix_layout.MACHINES:
            with self.subTest(architecture=architecture), tempfile.TemporaryDirectory() as directory:
                output, root = self.fixture(directory, architecture)
                executable = root / "bin/pw"
                with executable.open("ab") as target:
                    target.write(b"final ELF fixup bytes")
                rows = self.seal(output, architecture)["executables"]
                row = next(row for row in rows if row["path"] == str(executable))
                self.assertEqual(row["sha256"], hashlib.sha256(executable.read_bytes()).hexdigest())
                self.assertEqual(row["size"], executable.stat().st_size)
                receipt = json.loads((root / "nix-layout.json").read_text())
                self.assertEqual(receipt, {"format_version": 1, "output": str(output),
                    "nix_store": "/selected-nix/bin/nix-store"})
                self.assertNotIn("trusted", receipt)

    def test_roles_cannot_be_wrappers_symlinks_or_another_architecture(self):
        for mutation in ["script", "symlink", "architecture", "mode"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                output, root = self.fixture(directory)
                path = root / "bin/pw"
                if mutation == "script":
                    path.write_bytes(b"#!/bin/sh\nexec /other/engine\n")
                elif mutation == "symlink":
                    path.unlink()
                    path.symlink_to(root / "bin/pw-logs")
                elif mutation == "architecture":
                    self.elf(path, "aarch64")
                else:
                    path.chmod(0o644)
                with self.assertRaises(ValueError):
                    self.seal(output)
                self.assertFalse((root / "manifest.json").exists())

    def test_resource_elves_are_audited_and_privileged_modes_are_refused(self):
        for mutation in ["architecture", "setuid", "version", "notices"]:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as directory:
                output, root = self.fixture(directory)
                helper = root / "engine-root/helper"
                self.elf(helper, "aarch64" if mutation == "architecture" else "x86_64")
                if mutation == "setuid":
                    helper.chmod(0o4755)
                elif mutation == "version":
                    (root / "release-version").write_text("1.0.1\n")
                elif mutation == "notices":
                    (root / "THIRD-PARTY-NOTICES.md").write_text("")
                with self.assertRaises(ValueError):
                    self.seal(output)

    def test_only_real_generation_path_and_explicit_registrar_are_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            output, _ = self.fixture(directory)
            alias = output / "profile"
            alias.symlink_to(output, target_is_directory=True)
            for path, tool in [(alias, Path("/nix/bin/nix-store")), (output, Path("nix-store"))]:
                with self.subTest(path=path), self.assertRaises(ValueError):
                    nix_layout.seal(path, "x86_64", "1.0.2-rc6", tool)
