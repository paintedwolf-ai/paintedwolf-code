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
from linux_release.module_integrity import abi as INTEGRITY
from linux_release.module_integrity import binding as BINDING
from linux_release.module_integrity import inventory as INVENTORY
from linux_release.payload_inventory import tree as payload_tree

ROOT = Path(__file__).resolve().parents[3]
SOURCE = ROOT / "packaging/linux/module-audit"


@unittest.skipUnless(os.uname().sysname == "Linux", "Linux ELF loader contract")
class NativeModuleAuditTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="paintedwolf-audit-contract-")
        cls.root = Path(cls.directory.name)
        cls.digest = INTEGRITY.build_digest(cls.root / "digest")
        cls.auditor = cls.root / "module-audit.so"
        compiled = subprocess.run([
            "cc", "-shared", "-fPIC", "-nostdlib", "-ffreestanding", "-fno-builtin",
            "-fno-stack-protector", "-O2", "-Wall", "-Wextra", "-Werror",
            str(SOURCE / "audit.c"), str(cls.digest), "-Wl,--exclude-libs,ALL,-Bsymbolic", "-o", str(cls.auditor),
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

    def test_embedded_inventory_precedes_final_payload_seal(self):
        with tempfile.TemporaryDirectory(prefix="paintedwolf-bound-modules-") as scratch:
            output = Path(scratch)
            gate = INTEGRITY.ELF(self.guard)
            offset = next(row[4] for row in gate.symbols() if row[0] == "pw_module_inventory_digest_v1")
            raw = INVENTORY.encode([{"path": "/usr/lib/module.so", "size": 1, "sha256": "a" * 64}],
                                   "/usr/lib/pw-module-guard.so", self.abi["guard_witness_offset"],
                                   offset, self.abi["architecture"])
            result = BINDING.bind(self.auditor, self.guard, raw, output)
            self.assertEqual(result["input_auditor_sha256"], self.abi["auditor_sha256"])
            self.assertNotEqual(result["final_abi"]["auditor_sha256"], self.abi["auditor_sha256"])
            self.assertNotEqual(result["final_abi"]["guard_sha256"], self.abi["guard_sha256"])
            rows = {row["path"]: row for row in payload_tree(output)}
            self.assertEqual(rows["pw-module-audit.so"]["sha256"], result["final_abi"]["auditor_sha256"])
            self.assertEqual(rows["pw-module-guard.so"]["sha256"], result["final_abi"]["guard_sha256"])
            self.assertEqual(rows["module-inventory.bin"]["sha256"], result["inventory_sha256"])
            self.assertEqual(INTEGRITY.describe(self.auditor, self.guard), self.abi)
            with self.assertRaises(ValueError):
                BINDING.bind(self.auditor, self.guard, raw, output)

    def test_vetted_digest_vectors_and_fd_contract(self):
        source = self.root / "digest-probe.c"
        source.write_text(r'''
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
extern int pw_sha256_bytes(const unsigned char *, size_t, unsigned char *);
extern int pw_sha256_fd(int, uint64_t, unsigned char *);
int main(int argc, char **argv) {
 if(argc!=3)return 2;
 unsigned char digest[32];
 if(pw_sha256_bytes((const unsigned char *)"abc",3,digest))return 3;
 for(unsigned i=0;i<32;++i)printf("%02x",digest[i]);
 puts("");
 int fd=open(argv[1],O_RDONLY);uint64_t size=strtoull(argv[2],0,10);
 if(fd<0||lseek(fd,3,SEEK_SET)!=3||pw_sha256_fd(fd,size,digest)||lseek(fd,0,SEEK_CUR)!=3)return 4;
 for(unsigned i=0;i<32;++i)printf("%02x",digest[i]);
 puts("");
 for(unsigned i=0;i<32;++i)digest[i]=0xa5;
 if(pw_sha256_fd(fd,size+1,digest)!=-1)return 5;
 for(unsigned i=0;i<32;++i)if(digest[i]!=0xa5)return 6;
 if(size&&pw_sha256_fd(fd,size-1,digest)!=-1)return 7;
 return close(fd)!=0;
}
''')
        program = self.root / "digest-probe"
        subprocess.run(["cc", str(source), str(self.digest), "-o", str(program)], check=True)
        for data in (b"", b"abc", b"a" * 1000000):
            module = self.root / "digest-input"
            module.write_bytes(data)
            output = subprocess.run([str(program), str(module), str(len(data))], check=True,
                                    capture_output=True, text=True)
            self.assertEqual(output.stdout.splitlines(), [
                "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
                hashlib.sha256(data).hexdigest(),
            ])
