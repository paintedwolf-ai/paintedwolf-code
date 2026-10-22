"""Complete resource seals catch changes outside the executable inventory."""
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest

from linux_release.payload_inventory import tree

SPEC = importlib.util.spec_from_file_location("native_manifest_owner", Path(__file__).resolve().parents[2] / "linux-package-manifest.py")
OWNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(OWNER)


class CompletePayloadTests(unittest.TestCase):
    def test_seal_and_verify_bind_non_executable_resources_and_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            payload = root / OWNER.INSTALL_ROOT.lstrip("/")
            required = [OWNER.SHELL_EXECUTABLE, *(OWNER.INSTALL_ROOT + "/bin/" + name for name in OWNER.HELPERS),
                        *("/usr/bin/" + name for name in OWNER.ENTRY_POINTS)]
            for name in required:
                target = root / name.lstrip("/")
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(b"#!/bin/sh\nexit 0\n")
                target.chmod(0o755)
            resources = payload / "engine-root/browser/resources"
            resources.mkdir(parents=True)
            resource = resources / "settings.json"
            resource.write_bytes(b'{"resource":"original"}')
            resource.chmod(0o644)
            alias = resources / "settings-link"
            alias.symlink_to("settings.json")
            for directory in payload.rglob("*"):
                if directory.is_dir() and not directory.is_symlink():
                    directory.chmod(0o755)
            manifest = {"format_version": OWNER.FORMAT_VERSION, "package": OWNER.PACKAGE_NAME,
                        "version": "1.0.2", "architecture": "x86_64", "install_root": OWNER.INSTALL_ROOT,
                        "executables": [OWNER.entry(name, root / name.lstrip("/")) for name in required],
                        "payload": tree(payload)}
            (payload / "manifest.json").write_text(json.dumps(manifest))
            args = types.SimpleNamespace(tree=root, version="1.0.2", architecture="x86_64")
            self.assertEqual(OWNER.seal_tree(args), 0)
            self.assertEqual(OWNER.verify_tree(args), 0)
            original = resource.read_bytes()
            resource.write_bytes(b'{"resource":"replaced"}')
            with self.assertRaisesRegex(ValueError, "payload"):
                OWNER.verify_tree(args)
            resource.write_bytes(original)
            alias.unlink(); alias.symlink_to("../missing")
            with self.assertRaisesRegex(ValueError, "symlink"):
                OWNER.verify_tree(args)

    def test_inventory_never_traverses_out_of_generation_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for target in ("../foreign", "/nix/store/foreign", "cycle"):
                link = root / "cycle"
                link.symlink_to(target)
                with self.assertRaises(ValueError):
                    tree(root)
                link.unlink()
