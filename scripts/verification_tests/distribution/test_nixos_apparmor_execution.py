import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from linux_release import nix_apparmor

SOURCE = Path(__file__).resolve().parents[3] / "packaging/nix/apparmor.py"
SPEC = importlib.util.spec_from_file_location("selected_apparmor", SOURCE)
APPARMOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(APPARMOR)


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
        digest = hashlib.sha256(data).hexdigest()
        self.manifest = {"format_version": 2, "package": "painted-wolf-code",
                         "version": "1.0.2",
                         "architecture": "x86_64", "install_root": str(self.root),
                         "executables": [{"path": str(self.engine), "size": len(data),
                                          "sha256": digest}],
                         "payload": [{"path": "bin", "kind": "directory", "mode": 0o755},
                                     {"path": "bin/pw", "kind": "file", "mode": 0o755,
                                      "size": len(data), "sha256": digest}]}
        self.save()

    def save(self):
        (self.root / "manifest.json").write_text(json.dumps(self.manifest))

    def test_requires_sealed_format_2_and_refuses_other_versions(self):
        v1 = {k: v for k, v in self.manifest.items() if k != "payload"}
        v1["format_version"] = 1
        (self.root / "manifest.json").write_text(json.dumps(v1))
        with self.assertRaisesRegex(ValueError, "manifest does not identify"):
            APPARMOR.profile(self.package)

        for bad_version in (0, 1, 3, 99, True, "2"):
            with self.subTest(format_version=bad_version):
                self.manifest["format_version"] = bad_version
                self.save()
                with self.assertRaisesRegex(ValueError, "manifest does not identify"):
                    APPARMOR.profile(self.package)
        self.manifest["format_version"] = 2
        self.manifest["payload"] = []
        self.save()
        with self.assertRaisesRegex(ValueError, "manifest does not identify"):
            APPARMOR.profile(self.package)
        del self.manifest["payload"]
        self.save()
        with self.assertRaisesRegex(ValueError, "manifest does not identify"):
            APPARMOR.profile(self.package)

    def test_only_exact_engine_receives_userns(self):
        result = APPARMOR.profile(self.package)
        self.assertIn('"' + str(self.engine) + '" flags=(unconfined)', result)
        self.assertEqual(result.count("userns,"), 1)
        self.assertNotIn("**", result)
        self.assertNotIn("pw-vault-identity", result)

    def test_changed_bytes_refused(self):
        self.engine.write_bytes(self.engine.read_bytes() + b"changed")
        with self.assertRaisesRegex(ValueError, "bytes differ"):
            APPARMOR.profile(self.package)

    def test_symlink_refused(self):
        target = self.engine.with_name("other")
        self.engine.rename(target)
        self.engine.symlink_to(target)
        with self.assertRaisesRegex(ValueError, "physical"):
            APPARMOR.profile(self.package)

    def test_duplicate_manifest_identity_refused(self):
        self.manifest["executables"] *= 2
        self.save()
        with self.assertRaisesRegex(ValueError, "bytes differ"):
            APPARMOR.profile(self.package)

    def test_foreign_architecture_refused(self):
        self.manifest["architecture"] = "aarch64"
        self.save()
        with self.assertRaisesRegex(ValueError, "native ELF"):
            APPARMOR.profile(self.package)

    def test_manifest_directory_refused(self):
        manifest = self.root / "manifest.json"
        manifest.unlink()
        manifest.mkdir()
        with self.assertRaisesRegex(ValueError, "regular file"):
            APPARMOR.profile(self.package)

    def test_literal_path_metacharacters_and_controls(self):
        self.assertEqual(APPARMOR.literal('/nix/store/x*[a]?{b}^"\\/pw'),
                         '"/nix/store/x\\*\\[a\\]\\?\\{b\\}\\^\\"\\\\/pw"')
        for value in ("", "/x\ny", "/x\0y"):
            with self.assertRaises(ValueError):
                APPARMOR.literal(value)

    def test_both_consumers_use_the_same_literal_contract(self):
        path = '/nix/store/a"b\\c*d?e[f]g{h}i^j/bin/pw'
        expected = '"/nix/store/a\\"b\\\\c\\*d\\?e\\[f\\]g\\{h\\}i\\^j/bin/pw"'
        for encoder in (APPARMOR.literal, nix_apparmor.literal):
            self.assertEqual(encoder(path), expected)
            for character in [*(chr(value) for value in range(32)), chr(127)]:
                with self.subTest(character=ord(character)), self.assertRaises(ValueError):
                    encoder('/nix/store/a' + character + '/bin/pw')

    def test_standalone_renderer_needs_only_the_staged_shared_grammar(self):
        with tempfile.TemporaryDirectory() as directory:
            staged = Path(directory)
            grammar = staged / 'modules/linux_release'
            grammar.mkdir(parents=True)
            shutil.copyfile(SOURCE.parents[1].parent / 'scripts/linux_release/apparmor_attachment.py',
                            grammar / 'apparmor_attachment.py')
            renderer = staged / 'renderer.py'
            shutil.copyfile(SOURCE, renderer)
            result = subprocess.run([sys.executable, str(renderer), str(self.package)], cwd=staged,
                env={'PATH': os.defpath, 'PYTHONPATH': str(grammar.parent)},
                capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, 'abi <abi/4.0>,\n' + APPARMOR.profile(self.package) + '\n')
