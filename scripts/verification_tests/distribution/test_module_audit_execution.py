"""Native ABI contracts for the dependency-free protected loader boundary."""
import os
import hashlib
import json
import shutil
import uuid
from pathlib import Path
import subprocess
import tempfile
import unittest
import linux_module_integrity as INTEGRITY

ROOT = Path(__file__).resolve().parents[3]
SOURCE = ROOT / "packaging/linux/module-audit"


@unittest.skipUnless(os.uname().sysname == "Linux", "Linux ELF loader contract")
class NativeModuleAuditTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="paintedwolf-audit-contract-")
        cls.root = Path(cls.directory.name)
        cls.auditor = cls.root / "module-audit.so"
        compiled = subprocess.run([
            "cc", "-shared", "-fPIC", "-nostdlib", "-ffreestanding", "-fno-builtin",
            "-fno-stack-protector", "-O2", "-Wall", "-Wextra", "-Werror",
            str(SOURCE / "audit.c"), "-o", str(cls.auditor),
        ], capture_output=True, text=True)
        if compiled.returncode:
            raise RuntimeError(compiled.stderr)
        cls.guard = cls.root / "pw-module-guard.so"
        compiled = subprocess.run([
            "cc", "-shared", "-fPIC", "-nostdlib", "-ffreestanding", "-fno-builtin",
            "-fno-stack-protector", "-O2", "-Wall", "-Wextra", "-Werror",
            "-Wl,-z,initfirst,-z,relro,-z,now,-Bsymbolic,-soname,pw-module-guard.so",
            str(SOURCE / "guard.c"), "-o", str(cls.guard),
        ], capture_output=True, text=True)
        if compiled.returncode:
            raise RuntimeError(compiled.stderr)
        cls.abi = INTEGRITY.describe(cls.auditor, cls.guard)
        artifact_root = os.environ.get("PW_ARTIFACT_ROOT")
        if artifact_root:
            retained = Path(artifact_root) / "module-audit-components" / uuid.uuid4().hex
            retained.mkdir(parents=True, exist_ok=False)
            components = {"pw-module-audit.so": cls.auditor, "pw-module-guard.so": cls.guard}
            for name, source in components.items():
                shutil.copyfile(source, retained / name)
            (retained / "module-integrity.json").write_text(json.dumps(cls.abi, indent=2) + "\n")
            (retained / "source.json").write_text(json.dumps({
                "kind": "managed-test-built-module-discriminator",
                "publication": "nonrelease",
                "sources": {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                            for p in sorted(SOURCE.iterdir()) if p.is_file()},
                "elf_sha256": {name: hashlib.sha256(source.read_bytes()).hexdigest()
                               for name, source in components.items()},
            }, indent=2) + "\n")

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def test_auditor_has_no_dynamic_dependencies_or_undefined_functions(self):
        dynamic = subprocess.run(["readelf", "--dynamic", str(self.auditor)],
                                 check=True, capture_output=True, text=True).stdout
        self.assertNotIn("(NEEDED)", dynamic)
        symbols = subprocess.run(["nm", "--dynamic", "--undefined-only", str(self.auditor)],
                                 check=True, capture_output=True, text=True).stdout
        self.assertEqual(symbols.strip(), "")

    def test_abi_describes_actual_final_guard_bytes(self):
        self.assertEqual(self.abi["schema_version"], 2)
        self.assertEqual(self.abi["architecture"], os.uname().machine)
        self.assertEqual(self.abi["guard_witness_offset"] % 8, 0)
        self.assertEqual(self.abi["guard_sha256"], hashlib.sha256(self.guard.read_bytes()).hexdigest())
        with self.assertRaises(ValueError):
            INTEGRITY.describe(self.guard, self.auditor)

    def test_policy_keeps_exact_selected_guard_and_roots(self):
        nonce = bytes(range(32))
        raw = INTEGRITY.policy_bytes(["/usr/lib", "/opt/painted-wolf-code"],
                                     "/opt/painted-wolf-code/lib/pw-module-guard.so", 16056, nonce)
        self.assertEqual(raw[:8], b"PWMOD02\0")
        self.assertEqual(int.from_bytes(raw[8:12], "little"), len(raw))
        self.assertEqual(raw[16:48], nonce)
        self.assertEqual(int.from_bytes(raw[48:56], "little"), 16056)
        for roots, guard in [(["/usr/lib", "/usr/lib"], "/usr/lib/guard.so"),
                             (["/"], "/guard.so"), (["/usr/lib"], "/home/user/guard.so"),
                             (["/usr/../lib"], "/usr/lib/guard.so")]:
            with self.assertRaises(ValueError):
                INTEGRITY.policy_bytes(roots, guard, 16056, nonce)
        with self.assertRaises(ValueError):
            INTEGRITY.policy_bytes(["/" + str(i) + "/" + "a" * 4000 for i in range(20)],
                                   "/0/" + "a" * 4000 + "/guard.so", 16056, nonce)
