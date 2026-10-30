"""The pre-runtime intake accepts only a bounded static native ELF transition."""

from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from linux_release import mount_intake as intake


def static_transition(architecture="amd64"):
    raw = bytearray(449)
    raw[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<HHIQQQ", raw, 16, 2, intake.MACHINES[architecture], 1, 0x400000, 64, 176)
    struct.pack_into("<6H", raw, 52, 64, 56, 2, 64, 3, 2)
    struct.pack_into("<II6Q", raw, 64, 1, 5, 448, 0x400000, 0, 1, 1, 4096)
    struct.pack_into("<II6Q", raw, 120, 0x6474E551, 6, 0, 0, 0, 0, 0, 16)
    struct.pack_into("<II4QII2Q", raw, 240, 1, 1, 6, 0x400000, 448, 1, 0, 0, 1, 0)
    names = b"\0.text\0.shstrtab\0"
    struct.pack_into("<II4QII2Q", raw, 304, 7, 3, 0, 0, 400, len(names), 0, 0, 1, 0)
    raw[400:400 + len(names)] = names
    raw[448] = 0xC3
    return raw


def changed(offset, format, value):
    raw = static_transition()
    struct.pack_into(format, raw, offset, value)
    return raw


class StaticEntryTests(unittest.TestCase):
    def test_both_native_architectures_accept_a_static_entry_and_nonexecuting_stack(self):
        for architecture in intake.MACHINES:
            with self.subTest(architecture=architecture):
                intake.inspect(static_transition(architecture), architecture)
        raw = static_transition()
        struct.pack_into("<I", raw, 244, 8)  # NOBITS occupies memory, not file bytes.
        struct.pack_into("<QQ", raw, 264, 1 << 30, 4096)
        intake.inspect(raw, "amd64")

    def test_wrong_identity_and_unbounded_files_are_refused(self):
        cases = {"truncated": b"\x7fELF", "too large": bytes((1 << 20) + 1),
                 "shared object": changed(16, "<H", 3), "wrong cpu": static_transition("arm64")}
        magic = static_transition()
        magic[0] = 0
        cases["wrong magic"] = magic
        for name, raw in cases.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                intake.inspect(raw, "amd64")

    def test_header_tables_must_have_bounded_native_rows(self):
        cases = {"header size": (52, "<H", 63), "program row size": (54, "<H", 55),
                 "no segments": (56, "<H", 0), "too many segments": (56, "<H", 129),
                 "program table outside file": (32, "<Q", 448), "section row size": (58, "<H", 63),
                 "no sections": (60, "<H", 0), "too many sections": (60, "<H", 1025),
                 "missing name section": (62, "<H", 3), "section table outside file": (40, "<Q", 448)}
        for name, mutation in cases.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                intake.inspect(changed(*mutation), "amd64")

    def test_loader_startup_writable_code_and_executable_stacks_are_refused(self):
        cases = {"segment outside file": (72, "<Q", 449), "file exceeds memory": (104, "<Q", 0),
                 "writable executable code": (68, "<I", 7), "entry outside code": (24, "<Q", 0x400001),
                 "missing stack contract": (120, "<I", 0), "executable stack": (124, "<I", 7)}
        cases.update({name: (64, "<I", kind) for name, kind in {"dynamic": 2, "interpreter": 3, "TLS": 7}.items()})
        for name, mutation in cases.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                intake.inspect(changed(*mutation), "amd64")

    def test_sections_cannot_hide_initializers_or_truncated_names(self):
        cases = {"name table outside file": changed(328, "<Q", 449),
                 "section outside file": changed(264, "<Q", 449),
                 "name offset outside table": changed(240, "<I", 100)}
        unterminated = static_transition()
        unterminated[416] = ord("x")
        cases["unterminated name"] = unterminated
        for label in (b".init", b".fini"):
            raw = static_transition()
            raw[401:406] = label
            cases[label.decode()] = raw
        for kind in (6, 14, 15, 16):
            cases[f"dynamic section type {kind}"] = changed(244, "<I", kind)
        for name, raw in cases.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                intake.inspect(raw, "amd64")


class BuildTests(unittest.TestCase):
    def test_build_uses_freestanding_sources_and_validates_before_executable_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "nested/transition"
            def compile_transition(argv, *, check):
                self.assertTrue(check)
                self.assertEqual(argv[0], "/native/cc")
                for flag in ("-ffreestanding", "-fno-builtin", "-fno-stack-protector", "-fno-pie",
                             "-nostdlib", "-static", "-no-pie", "-Wl,--no-undefined", "-Wl,-z,noexecstack"):
                    self.assertIn(flag, argv)
                sources = intake.ROOT / "packaging/linux/mount-intake"
                self.assertIn(str(sources / "transition.c"), argv)
                self.assertIn(str(sources / "entry.S"), argv)
                self.assertEqual(argv[-2:], ["-o", str(output)])
                output.write_bytes(static_transition())
            with patch.object(intake.shutil, "which", return_value="/native/cc"), \
                    patch.object(intake.subprocess, "run", side_effect=compile_transition):
                intake.build(output, "amd64")
            self.assertEqual(output.stat().st_mode & 0o777, 0o755)
            intake.inspect(output.read_bytes(), "amd64")

    def test_missing_compiler_compilation_failure_and_wrong_architecture_stop_the_build(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "transition"
            with patch.object(intake.shutil, "which", return_value=None), self.assertRaises(ValueError):
                intake.build(output, "amd64")
            error = subprocess.CalledProcessError(1, ["cc"])
            with patch.object(intake.shutil, "which", return_value="cc"), \
                    patch.object(intake.subprocess, "run", side_effect=error), self.assertRaises(subprocess.CalledProcessError):
                intake.build(output, "amd64")
            def wrong_architecture(*_args, **_kwargs):
                output.write_bytes(static_transition("arm64"))
            with patch.object(intake.shutil, "which", return_value="cc"), \
                    patch.object(intake.subprocess, "run", side_effect=wrong_architecture), self.assertRaises(ValueError):
                intake.build(output, "amd64")
            self.assertFalse(output.stat().st_mode & 0o111)

    def test_cli_selects_only_a_declared_architecture_and_output_path(self):
        with patch.object(sys, "argv", ["mount-intake", "--output", "out/transition", "--architecture", "arm64"]), \
                patch.object(intake, "build") as build:
            intake.main()
        build.assert_called_once_with(Path("out/transition"), "arm64")
        with patch.object(sys, "argv", ["mount-intake", "--output", "out/transition", "--architecture", "other"]), \
                patch.object(intake, "build") as build, self.assertRaises(SystemExit) as rejected:
            intake.main()
        self.assertEqual(rejected.exception.code, 2)
        build.assert_not_called()
