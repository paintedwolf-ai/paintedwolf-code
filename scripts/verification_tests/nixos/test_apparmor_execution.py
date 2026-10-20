import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SOURCE = Path(__file__).resolve().parents[3] / "packaging/nix/apparmor.py"
SPEC = importlib.util.spec_from_file_location("selected_apparmor", SOURCE)
OWNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(OWNER)


class SelectedEngineAppArmorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.package = Path(self.temp.name).resolve() / "generation"
        self.root = self.package / "lib/painted-wolf-code"
        self.engine = self.root / "bin/pw"
        self.engine.parent.mkdir(parents=True)
        data = b"\x7fELF\x02\x01\x01" + b"\0" * 11 + b"\x3e\0"
        self.engine.write_bytes(data)
        self.engine.chmod(0o755)
        self.manifest = {"format_version": 1, "package": "painted-wolf-code",
                         "architecture": "x86_64", "install_root": str(self.root),
                         "executables": [{"path": str(self.engine), "size": len(data),
                                          "sha256": hashlib.sha256(data).hexdigest()}]}
        self.save()

    def save(self):
        (self.root / "manifest.json").write_text(json.dumps(self.manifest))

    def test_only_exact_engine_receives_userns(self):
        result = OWNER.profile(self.package)
        self.assertIn('"' + str(self.engine) + '" flags=(unconfined)', result)
        self.assertEqual(result.count("userns,"), 1)
        self.assertNotIn("**", result)
        self.assertNotIn("pw-vault-identity", result)

    def test_changed_bytes_refused(self):
        self.engine.write_bytes(self.engine.read_bytes() + b"changed")
        with self.assertRaisesRegex(ValueError, "bytes differ"):
            OWNER.profile(self.package)

    def test_symlink_refused(self):
        target = self.engine.with_name("other")
        self.engine.rename(target)
        self.engine.symlink_to(target)
        with self.assertRaisesRegex(ValueError, "physical"):
            OWNER.profile(self.package)

    def test_duplicate_manifest_identity_refused(self):
        self.manifest["executables"] *= 2
        self.save()
        with self.assertRaisesRegex(ValueError, "bytes differ"):
            OWNER.profile(self.package)

    def test_foreign_architecture_refused(self):
        self.manifest["architecture"] = "aarch64"
        self.save()
        with self.assertRaisesRegex(ValueError, "native ELF"):
            OWNER.profile(self.package)

    def test_literal_path_metacharacters_and_controls(self):
        self.assertEqual(OWNER.literal('/nix/store/x*[a]?{b}^"\\/pw'),
                         '"/nix/store/x\\*\\[a\\]\\?\\{b\\}\\^\\"\\\\/pw"')
        for value in ("", "/x\ny", "/x\0y"):
            with self.assertRaises(ValueError):
                OWNER.literal(value)
