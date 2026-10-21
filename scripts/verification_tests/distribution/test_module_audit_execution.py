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
        artifact_root = os.environ.get("PW_ARTIFACT_ROOT")
        if artifact_root:
            retained = Path(artifact_root) / "module-audit-components" / uuid.uuid4().hex
            retained.mkdir(parents=True, exist_ok=False)
            shutil.copyfile(cls.auditor, retained / "pw-module-audit.so")
            (retained / "source.json").write_text(json.dumps({
                "kind": "managed-test-built-module-discriminator",
                "publication": "nonrelease",
                "sources": {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                            for p in sorted(SOURCE.iterdir()) if p.is_file()},
                "elf_sha256": hashlib.sha256(cls.auditor.read_bytes()).hexdigest(),
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

    def test_missing_root_policy_refuses_before_executable_entry(self):
        # /usr/bin/true has no dependency on our test program or an injected constructor.
        result = subprocess.run(["/usr/bin/true"], env={"LD_AUDIT": str(self.auditor)},
                                close_fds=True, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 125)
        self.assertIn(b"refused an untrusted native module", result.stderr)

    def test_production_preinit_uses_direct_syscalls_and_fixed_fds(self):
        source = (ROOT / "lycaon-den/src-tauri/src/linux_module_preinit.rs").read_text()
        self.assertIn('section = ".preinit_array"', source)
        self.assertIn('inlateout("rax")', source)
        self.assertIn('inlateout("x0")', source)
        self.assertIn("for fd in [7, 9, 10]", source)
        self.assertNotIn("std::", source)
        self.assertNotIn("assume_init()", source)

    def test_concrete_search_uses_physical_ancestry_and_selected_policy(self):
        # Compile-time checks above prove the actual callbacks link; these fixed guards
        # keep the source owner separate from a generic, user-selected search path.
        source = (SOURCE / "paths.h").read_text()
        self.assertIn("O_PATH|O_NOFOLLOW", source)
        self.assertIn("pw_same_file(&facts,selected)", source)
        self.assertIn("pw_in_policy(physical", source)
        policy = (SOURCE / "policy.h").read_text()
        self.assertIn("facts.stx_uid!=0", policy)
        self.assertIn("(seals&15)!=15", policy)
        self.assertIn("pw_canonical", policy)
