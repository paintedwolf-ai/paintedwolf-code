"""Runtime identity fixtures cannot substitute an unrelated installed tree or NAR."""
import hashlib
import json
import os
from pathlib import Path
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release.installed_runtime import ROLES, bind, elf, manifest, nix_command, unchanged


def image(path, machine=62):
    header = bytearray(64)
    header[:6] = b"\x7fELF\x02\x01"
    struct.pack_into("<HH", header, 16, 2, machine)
    path.write_bytes(header)
    path.chmod(0o755)
    return {"path": str(path), "sha256": hashlib.sha256(header).hexdigest(), "size": 64}


class InstalledRuntimeTests(unittest.TestCase):
    def test_manifest_binds_every_native_role_and_refuses_replacement_before_probe(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "bin").mkdir()
            entries = [image(root / "bin" / role) for role in sorted(ROLES)]
            data = {"format_version": 1, "package": "painted-wolf-code", "version": "1.0.2",
                    "architecture": "x86_64", "install_root": str(root), "executables": entries}
            path = root / "manifest.json"
            path.write_text(json.dumps(data))
            result = manifest(root, "amd64")
            binding = {"install_root": str(root), "artifact": {"architecture": "amd64"}, **result}
            self.assertEqual(unchanged(binding, "pw"), root / "bin/pw")
            with self.assertRaises(ValueError):
                unchanged(binding, "arbitrary")
            (root / "bin/pw").write_bytes(b"changed")
            with self.assertRaises(ValueError):
                unchanged(binding, "pw")
            image(root / "bin/pw", machine=183)
            with self.assertRaisesRegex(ValueError, "native CPU"):
                manifest(root, "amd64")
            image(root / "bin/pw")
            data["executables"] = entries[:-1]
            path.write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError, "required native helper"):
                manifest(root, "amd64")

    def test_native_manifest_allows_sealed_resource_scripts_but_no_foreign_image(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "bin").mkdir()
            entries = [image(root / "bin" / role) for role in sorted(ROLES)]
            script = root / "resource"
            script.write_bytes(b"#!/bin/sh\nexit 0\n")
            script.chmod(0o755)
            entries.append({"path": str(script), "sha256": hashlib.sha256(script.read_bytes()).hexdigest(), "size": script.stat().st_size})
            data = {"format_version": 1, "package": "painted-wolf-code", "version": "1.0.2",
                    "architecture": "x86_64", "install_root": str(root), "executables": entries}
            (root / "manifest.json").write_text(json.dumps(data))
            json.dumps(manifest(root, "amd64"))
            entries[-1]["path"] = "/usr/bin/unrelated"
            (root / "manifest.json").write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError, "foreign"):
                manifest(root, "amd64")

    def test_elf_refuses_cross_cpu_special_mode_and_a_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "program"
            image(path, machine=183)
            with self.assertRaises(ValueError):
                elf(path, "amd64")
            image(path)
            path.chmod(0o4755)
            with self.assertRaises(ValueError):
                elf(path, "amd64")
            path.unlink()
            path.symlink_to("missing")
            with self.assertRaises(OSError):
                elf(path, "amd64")

    def test_nix_verification_uses_private_configuration_without_inherited_authority(self):
        def run(arguments, *, env, **kwargs):
            self.assertTrue((Path(env["NIX_CONF_DIR"])).is_dir())
            self.assertEqual(env["HOME"], env["NIX_CONF_DIR"])
            self.assertEqual(env["NIX_USER_CONF_FILES"], "/dev/null")
            self.assertEqual(set(env), {"HOME", "NIX_CONF_DIR", "NIX_USER_CONF_FILES", "LC_ALL"})
            self.assertIn("daemon", arguments)
            self.assertIn("substituters", arguments)
            self.assertIn("builders", arguments)
            return SimpleNamespace(returncode=0, stdout=b"{}")
        with patch.dict(os.environ, {"NIX_CONFIG": "plugin-files=/attacker", "NIX_REMOTE": "ssh://foreign",
                                     "GH_TOKEN": "private", "AWS_SECRET_ACCESS_KEY": "private"}), patch(
                "scripts.linux_release.installed_runtime.subprocess.run", side_effect=run):
            self.assertEqual(nix_command(Path("/explicit/nix"), "path-info", "/nix/store/output"), b"{}")

    def test_native_launcher_is_the_exact_sealed_desktop_entry_image(self):
        root = Path("/opt/painted-wolf-code")
        launcher = root / "bin/painted-wolf-code"
        source = "a" * 40
        artifact = {"id": "native-rpm-amd64.rpm", "installation": "rpm", "architecture": "amd64",
                    "source_commit": source, "identity": {"kind": "file", "sha256": "b" * 64}}
        final = {"format_version": 1, "source_commit": source, "artifacts": [artifact]}
        entries = {str((root / "bin" / role).relative_to("/")): {} for role in ROLES}
        entries[str((root / "manifest.json").relative_to("/"))] = {}
        images = {str(launcher): {"sha256": "sealed", "bytes": 64}}
        with patch("scripts.linux_release.installed_runtime.platform.system", return_value="Linux"), patch(
                "scripts.linux_release.installed_runtime.platform.machine", return_value="x86_64"), patch.object(
                Path, "resolve", lambda path, **kwargs: path), patch(
                "scripts.linux_release.installed_runtime.inspect", return_value=entries), patch(
                "scripts.linux_release.installed_runtime.verify", return_value={"status": "passed"}), patch(
                "scripts.linux_release.installed_runtime.manifest", return_value={"version": "1.0.2", "images": images}):
            result = bind(final, source, artifact["id"], Path("/files"), root)
            self.assertEqual(result["launcher"], {"path": str(launcher), **images[str(launcher)]})
            images.clear()
            with self.assertRaisesRegex(ValueError, "public application launcher"):
                bind(final, source, artifact["id"], Path("/files"), root)

    def test_nix_output_hash_and_native_architecture_are_checked_before_cases(self):
        output = "/nix/store/" + "0" * 32 + "-application"
        source = "a" * 40
        artifact = {"id": "nix-amd64", "installation": "nix-package", "architecture": "amd64",
                    "source_commit": source, "identity": {"kind": "nix-closure", "sha256": "0" * 64,
                    "store_path": output, "lock_sha256": "b" * 64}}
        final = {"format_version": 1, "source_commit": source, "artifacts": [artifact]}
        root = Path(output) / "lib/painted-wolf-code"
        rows = {output: {"narHash": "sha256-" + "A" * 43 + "="}}
        with patch("scripts.linux_release.installed_runtime.platform.system", return_value="Linux"), patch(
                "scripts.linux_release.installed_runtime.platform.machine", return_value="x86_64"), patch.object(
                Path, "resolve", lambda path, **kwargs: path), patch(
                "scripts.linux_release.installed_runtime.elf", return_value={"sha256": "client"}), patch(
                "scripts.linux_release.installed_runtime.manifest", return_value={"version": "1.0.2", "images": {}}), patch(
                "scripts.linux_release.installed_runtime.file_identity", return_value={"header": b"#!/bin/sh", "mode": 0o755}), patch(
                "scripts.linux_release.installed_runtime.nix_command", side_effect=lambda *args: json.dumps(rows).encode()) as query:
            result = bind(final, source, "nix-amd64", Path("/files"), root, Path("/explicit/nix"))
            self.assertTrue(result["payload"]["content_verified"])
            self.assertFalse(result["payload"]["publisher_or_custody_authority"])
            rows[output]["narHash"] = "sha256-incorrect"
            with self.assertRaisesRegex(ValueError, "final NAR"):
                bind(final, source, "nix-amd64", Path("/files"), root, Path("/explicit/nix"))
            query.reset_mock()
            artifact["architecture"] = "arm64"
            with self.assertRaisesRegex(ValueError, "native Linux CPU"):
                bind(final, source, "nix-amd64", Path("/files"), root, Path("/explicit/nix"))
            query.assert_not_called()
