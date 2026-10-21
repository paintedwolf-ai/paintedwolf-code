"""The Nix frontend patches exactly the native npm modules this Linux architecture loads."""

import importlib.util
import json
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[3] / "packaging/nix/native-modules.py"
SPEC = importlib.util.spec_from_file_location("nix_native_modules", SOURCE)
native = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(native)


def elf(machine, elf_class=2, data=1):
    header = bytearray(64)
    header[:4] = b"\x7fELF"
    header[4], header[5] = elf_class, data
    struct.pack_into("<H", header, 18, machine)
    return bytes(header)


class NativeModuleSelectionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        (self.root / "package.json").write_text("{}")

    def package(self, name, metadata, files):
        directory = self.root / name
        directory.mkdir(parents=True)
        (directory / "package.json").write_text(json.dumps(metadata))
        for relative, content in files.items():
            path = directory / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(content)
        return directory

    def test_selects_linux_glibc_modules_for_the_built_architecture_only(self):
        gnu = self.package("@rollup/rollup-linux-x64-gnu", {"os": ["linux"], "cpu": ["x64"], "libc": ["glibc"]},
                           {"rollup.linux-x64-gnu.node": elf(62)})
        self.package("@rollup/rollup-linux-x64-musl", {"os": ["linux"], "cpu": ["x64"], "libc": ["musl"]},
                     {"rollup.linux-x64-musl.node": elf(62)})
        arm = self.package("@rollup/rollup-linux-arm64-gnu", {"os": ["linux"], "cpu": ["arm64"], "libc": ["glibc"]},
                           {"rollup.linux-arm64-gnu.node": elf(183)})
        self.package("fsevents", {"os": ["darwin"]}, {"fsevents.node": elf(62)})
        portable = self.package("esbuild", {}, {"bin/esbuild": elf(62), "lib/main.js": b"module.exports = {}"})
        self.assertEqual(native.select(self.root, "x86_64"),
                         [gnu / "rollup.linux-x64-gnu.node", portable / "bin/esbuild"])
        self.assertEqual(native.select(self.root, "aarch64"), [arm / "rollup.linux-arm64-gnu.node"])

    def test_ignores_other_elf_classes_byte_orders_links_and_truncated_headers(self):
        directory = self.package("mixed", {}, {"32bit.node": elf(62, elf_class=1), "big.node": elf(62, data=2),
                                               "short.node": b"\x7fELF\x02"})
        (directory / "link.node").symlink_to(self.root / "outside.node")
        (self.root / "outside.node").write_bytes(elf(62))
        self.assertEqual(native.select(directory, "x86_64"), [])

    def test_negated_and_positive_platform_lists_follow_npm_semantics(self):
        self.assertTrue(native.matches(None, "linux"))
        self.assertTrue(native.matches("linux", "linux"))
        self.assertTrue(native.matches(["!win32", "!darwin"], "linux"))
        self.assertFalse(native.matches(["!linux"], "linux"))
        self.assertFalse(native.matches(["darwin", "win32"], "linux"))
        self.assertFalse(native.matches(["linux", "!linux"], "linux"))

    def test_an_unknown_architecture_fails_selection(self):
        with self.assertRaises(KeyError):
            native.select(self.root, "riscv64")

    def test_command_writes_nul_separated_absolute_paths(self):
        module = self.package("addon", {"os": "linux"}, {"build/Release/addon.node": elf(62)})
        output = subprocess.run([sys.executable, "-I", str(SOURCE), str(self.root), "x86_64"],
                                check=True, capture_output=True).stdout
        self.assertEqual(output, str(module / "build/Release/addon.node").encode() + b"\0")


if __name__ == "__main__":
    unittest.main()
