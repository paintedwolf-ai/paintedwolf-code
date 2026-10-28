"""ABI admission reads import requirements, including stripped ELF objects."""

import hashlib
import json
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest

from linux_release.abi_audit import audit, violations
from linux_release.elf import ELF64

FLOORS = {"glibc": "2.39", "glibcxx": "3.4.33", "cxxabi": "1.3.15"}


def elf(requirement="GLIBC_2.39", machine=62):
    data = bytearray(2048)
    data[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<HHIQQQIHHHHHH", data, 16, 3, machine, 1, 0, 64, 0, 0, 64, 56, 2, 0, 0, 0)
    base = 0x400000
    strings = b"\0libc.so.6\0" + requirement.encode() + b"\0$ORIGIN/lib\0"
    data[0x300:0x300 + len(strings)] = strings
    path = strings.index(b"$ORIGIN")
    tags = [(5, base + 0x300), (10, len(strings)), (1, 1), (29, path),
            (0x6FFFFFFE, base + 0x500), (0x6FFFFFFF, 1), (0, 0)]
    struct.pack_into("<IIQQQQQQ", data, 64, 1, 5, 0, base, base, len(data), len(data), 4096)
    struct.pack_into("<IIQQQQQQ", data, 120, 2, 6, 0x100, base + 0x100, 0, len(tags) * 16, len(tags) * 16, 8)
    for index, values in enumerate(tags):
        struct.pack_into("<qQ", data, 0x100 + index * 16, *values)
    struct.pack_into("<HHIII", data, 0x500, 1, 1, 1, 16, 0)
    struct.pack_into("<IHHII", data, 0x510, 0, 0, 2, len(b"\0libc.so.6\0"), 0)
    return data


class LinuxABITests(unittest.TestCase):
    def test_dynamic_facts_do_not_require_section_headers_or_execution(self):
        facts = ELF64(elf()).facts()
        self.assertEqual(facts, {"machine": 62, "needed": ["libc.so.6"],
                                 "runpath": ["$ORIGIN/lib"], "versions": {"libc.so.6": ["GLIBC_2.39"]}})
        self.assertEqual(violations(facts, FLOORS), [])

    def test_numeric_ceilings_and_non_numeric_requirements_fail_closed(self):
        for requirement, accepted in [("GLIBC_2.9", True), ("GLIBC_2.40", False),
                                      ("GLIBCXX_3.4.33", True), ("GLIBCXX_3.4.34", False),
                                      ("CXXABI_1.3.15", True), ("CXXABI_1.3.17", False),
                                      ("GLIBC_PRIVATE", False), ("GLIBC_ABI_DT_RELR", True)]:
            with self.subTest(requirement=requirement):
                self.assertEqual(not violations(ELF64(elf(requirement)).facts(), FLOORS), accepted)
        self.assertTrue(violations(ELF64(elf("GLIBC_ABI_DT_RELR")).facts(), {**FLOORS, "glibc": "2.35"}))

    def test_malformed_header_mapping_strings_and_version_chains_are_rejected(self):
        malformed = [elf()[:60]]
        for offset, shape, value in [(4, "B", 1), (54, "H", 1), (0x108, "Q", 0xFFFFFF),
                                     (0x508, "I", 0), (0x50C, "I", 16), (0x51C, "I", 16),
                                     (0x518, "I", 0xFFFF)]:
            data = elf()
            struct.pack_into("<" + shape, data, offset, value)
            malformed.append(data)
        for data in malformed:
            with self.subTest(data=bytes(data[:64])), self.assertRaises(ValueError):
                ELF64(data).facts()

    def test_complete_tree_reports_hashes_and_refuses_bad_architecture_or_newer_helpers(self):
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            (tree / "engine").mkdir()
            payload = elf()
            (tree / "engine/helper").write_bytes(payload)
            (tree / "notice").write_text("GLIBC_99.0 is ordinary documentation")
            result = audit(tree, "x86_64", FLOORS)
            self.assertTrue(result["passed"], result["errors"])
            self.assertEqual(len(result["objects"]), 1)
            self.assertEqual(result["objects"][0]["sha256"], hashlib.sha256(payload).hexdigest())
            (tree / "engine/other-helper").write_bytes(elf("GLIBC_2.40", machine=183))
            result = audit(tree, "x86_64", FLOORS)
            self.assertFalse(result["passed"])
            self.assertEqual(len(result["errors"]), 2)
            self.assertTrue(all("other-helper" in error for error in result["errors"]))

    def test_cli_retains_failure_report_and_empty_payload_cannot_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tree = root / "tree"
            tree.mkdir()
            self.assertFalse(audit(tree, "aarch64", FLOORS)["passed"])
            (tree / "helper").write_bytes(elf("GLIBC_2.40"))
            floor = root / "floor.txt"
            floor.write_text("".join(f"{key} {value}\n" for key, value in FLOORS.items()))
            report = root / "report.json"
            result = subprocess.run([sys.executable, "-m", "scripts.linux_release.abi_audit",
                                     "--tree", str(tree), "--architecture", "x86_64",
                                     "--floors", str(floor), "--output", str(report)],
                                    cwd=Path(__file__).resolve().parents[3],
                                    capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("above GLIBC_2.39", result.stderr)
            self.assertFalse(json.loads(report.read_text())["passed"])
