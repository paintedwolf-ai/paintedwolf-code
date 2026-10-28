"""Runtime identity fixtures cannot substitute an unrelated installed tree or NAR."""
import hashlib
import json
import os
from pathlib import Path
import stat
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.linux_release.campaign.runtime import (
    ROLES,
    SETUID_LAUNCHER,
    bind,
    elf,
    manifest,
    nix_command,
    unchanged,
)


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
            roles = sorted(ROLES)
            entries = [image(root / "bin" / role) for role in roles]
            payload = [{"path": "bin", "kind": "directory", "mode": 0o755, "sha256": None, "size": None, "target": None},
                       *[{"path": f"bin/{role}", "kind": "file", "mode": 0o755,
                          "sha256": row["sha256"], "size": row["size"], "target": None}
                         for role, row in zip(roles, entries)]]
            data = {"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2",
                    "architecture": "x86_64", "install_root": str(root), "executables": entries, "payload": payload}
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
            roles = sorted(ROLES)
            entries = [image(root / "bin" / role) for role in roles]
            script = root / "resource"
            script.write_bytes(b"#!/bin/sh\nexit 0\n")
            script.chmod(0o755)
            script_sha = hashlib.sha256(script.read_bytes()).hexdigest()
            script_size = script.stat().st_size
            entries.append({"path": str(script), "sha256": script_sha, "size": script_size})
            payload = [{"path": "bin", "kind": "directory", "mode": 0o755, "sha256": None, "size": None, "target": None},
                       {"path": "resource", "kind": "file", "mode": 0o755, "sha256": script_sha, "size": script_size, "target": None},
                       *[{"path": f"bin/{role}", "kind": "file", "mode": 0o755,
                          "sha256": row["sha256"], "size": row["size"], "target": None}
                         for role, row in zip(roles, entries[:-1])]]
            data = {"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2",
                    "architecture": "x86_64", "install_root": str(root), "executables": entries, "payload": payload}
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

    def test_manifest_format_2_binds_payload_and_refuses_unknown_versions_or_mismatches(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "bin").mkdir()
            names = [*sorted(ROLES), "pw-launch-entry"]
            entries = [image(root / "bin" / role) for role in names]
            payload = [{"path": "bin", "kind": "directory", "mode": 0o755, "sha256": None, "size": None, "target": None},
                       {"path": "protected-launch.json", "kind": "file", "mode": 0o644,
                        "sha256": "a" * 64, "size": 12, "target": None},
                       {"path": "bin/current-entry", "kind": "symlink", "mode": 0o777,
                        "sha256": None, "size": None, "target": "pw-launch-entry"},
                       *[{"path": f"bin/{role}", "kind": "file", "mode": 0o755,
                          "sha256": row["sha256"], "size": row["size"], "target": None}
                         for role, row in zip(names, entries)]]
            base = {"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2",
                    "architecture": "x86_64", "install_root": str(root), "executables": entries, "payload": payload}
            path = root / "manifest.json"
            path.write_text(json.dumps(base))
            result = manifest(root, "amd64")
            self.assertIn(str(root / "bin/pw-launch-entry"), result["images"])
            v1 = {k: v for k, v in base.items() if k != "payload"}
            v1["format_version"] = 1
            for invalid in (v1, {**base, "format_version": 0}, {**base, "format_version": 3},
                            {**base, "format_version": True}, {**base, "format_version": "2"},
                            {**base, "format_version": 1},
                            {k: v for k, v in base.items() if k != "payload"},
                            {**base, "payload": []},
                            {**base, "payload": [*payload, {"path": "manifest.json", "kind": "file", "mode": 0o644, "sha256": "a" * 64, "size": 1, "target": None}]},
                            {**base, "payload": [{**payload[0], "mode": 0o4755}, *payload[1:]]},
                            {**base, "payload": [{**payload[0], "mode": 0o775}, *payload[1:]]},
                            {**base, "payload": [{**payload[2], "target": "../../escape"}, *payload[:2], *payload[3:]]},
                            {**base, "payload": [*payload[:-1], {**payload[-1], "sha256": "0" * 64}]}):
                path.write_text(json.dumps(invalid))
                with self.subTest(version=invalid.get("format_version")), self.assertRaises(ValueError):
                    manifest(root, "amd64")

    def test_setuid_launcher_requires_exact_libexec_path_mode_4755_and_root_owner(self):
        header = bytearray(64)
        header[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<HH", header, 16, 2, 62)
        digest = hashlib.sha256(header).hexdigest()
        root = Path("/opt/painted-wolf-code")
        executables = [{"path": str(root / "bin" / role), "sha256": digest, "size": 64} for role in sorted(ROLES)]
        executables.append({"path": str(SETUID_LAUNCHER), "sha256": digest, "size": 64})
        payload = [{"path": f"bin/{role}", "kind": "file", "mode": 0o755, "sha256": digest, "size": 64, "target": None}
                   for role in sorted(ROLES)]
        raw_manifest = json.dumps({"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2",
                                   "architecture": "x86_64", "install_root": str(root),
                                   "executables": executables, "payload": payload})

        def fake_identity(target):
            mode = 0o4755 if target == SETUID_LAUNCHER else 0o755
            return {"sha256": digest, "bytes": 64, "mode": mode, "device": 7, "inode": 11, "header": bytes(header)}

        root_setuid = SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_size=64, st_dev=7, st_ino=11, st_uid=0, st_gid=0)
        with patch.object(Path, "is_symlink", return_value=False), \
                patch.object(Path, "stat", return_value=root_setuid), \
                patch.object(Path, "lstat", return_value=root_setuid), \
                patch.object(Path, "read_text", return_value=raw_manifest), \
                patch("scripts.linux_release.campaign.runtime.file_identity", side_effect=fake_identity):
            observed = manifest(root, "amd64")
            self.assertEqual(observed["images"][str(SETUID_LAUNCHER)]["mode"], 0o4755)
            nix_root = Path("/nix/store/00000000000000000000000000000000-app/lib/painted-wolf-code")
            nix_manifest = json.dumps({"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2",
                                       "architecture": "x86_64", "install_root": str(nix_root),
                                       "executables": [{"path": str(nix_root / "bin" / role), "sha256": digest, "size": 64}
                                                       for role in sorted(ROLES)] + [{"path": str(SETUID_LAUNCHER), "sha256": digest, "size": 64}],
                                       "payload": payload})
            with patch.object(Path, "read_text", return_value=nix_manifest), self.assertRaisesRegex(ValueError, "foreign"):
                manifest(nix_root, "amd64")
            with self.assertRaises(ValueError):
                elf(root / "bin/pw-launch", "amd64", setuid=True)
            for bad_stat in (SimpleNamespace(st_mode=stat.S_IFREG | 0o755, st_size=64, st_dev=7, st_ino=11, st_uid=0, st_gid=0),
                             SimpleNamespace(st_mode=stat.S_IFREG | 0o6755, st_size=64, st_dev=7, st_ino=11, st_uid=0, st_gid=0),
                             SimpleNamespace(st_mode=stat.S_IFREG | 0o4750, st_size=64, st_dev=7, st_ino=11, st_uid=0, st_gid=0),
                             SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_size=64, st_dev=7, st_ino=11, st_uid=1000, st_gid=0),
                             SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_size=64, st_dev=7, st_ino=11, st_uid=0, st_gid=1000)):
                with self.subTest(stat=bad_stat), patch.object(Path, "lstat", return_value=bad_stat):
                    with self.assertRaises(ValueError):
                        elf(SETUID_LAUNCHER, "amd64", setuid=True)

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
                "scripts.linux_release.campaign.runtime.subprocess.run", side_effect=run):
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
        with patch("scripts.linux_release.campaign.runtime.platform.system", return_value="Linux"), patch(
                "scripts.linux_release.campaign.runtime.platform.machine", return_value="x86_64"), patch.object(
                Path, "resolve", lambda path, **kwargs: path), patch(
                "scripts.linux_release.campaign.runtime.inspect", return_value=entries), patch(
                "scripts.linux_release.campaign.runtime.verify", return_value={"status": "passed"}), patch(
                "scripts.linux_release.campaign.runtime.manifest", return_value={"version": "1.0.2", "images": images}):
            result = bind(final, source, artifact["id"], Path("/files"), root)
            self.assertEqual(result["launcher"], {"path": str(launcher), **images[str(launcher)]})
            images.clear()
            with self.assertRaisesRegex(ValueError, "public application launcher"):
                bind(final, source, artifact["id"], Path("/files"), root)

    def test_protected_native_binding_keeps_package_facts_and_selects_bootstrap_runtime(self):
        root = Path('/opt/painted-wolf-code')
        launcher = str(root / 'bin/painted-wolf-code')
        source = 'a' * 40
        artifact = {'id': 'native.deb', 'installation': 'deb', 'architecture': 'amd64',
                    'source_commit': source, 'identity': {'kind': 'file', 'sha256': 'b' * 64}}
        final = {'format_version': 1, 'source_commit': source, 'artifacts': [artifact]}
        entries = {str((root / 'bin' / role).relative_to('/')): {} for role in ROLES}
        entries[str((root / 'manifest.json').relative_to('/'))] = {}
        package_images = {launcher: {'inode': 10}}
        runtime_images = {launcher: {'inode': 20}, str(SETUID_LAUNCHER): {'inode': 30, 'mode': 0o4755}}
        selected = {'root': str(root / 'generations' / ('c' * 64)), 'images': runtime_images}
        with patch('scripts.linux_release.campaign.runtime.platform.system', return_value='Linux'), \
                patch('scripts.linux_release.campaign.runtime.platform.machine', return_value='x86_64'), \
                patch.object(Path, 'resolve', lambda path, **kwargs: path), \
                patch('scripts.linux_release.campaign.runtime.inspect', return_value=entries), \
                patch('scripts.linux_release.campaign.runtime.verify', return_value={'status': 'passed'}), \
                patch('scripts.linux_release.campaign.runtime.manifest', return_value={'version': '1.0.2', 'images': package_images}), \
                patch('scripts.linux_release.installed_generation.bind', return_value=selected) as generation:
            result = bind(final, source, artifact['id'], Path('/files'), root)
        generation.assert_called_once_with(root, package_images, entries)
        self.assertIs(result['package_images'], package_images)
        self.assertIs(result['images'], runtime_images)
        self.assertEqual(result['launcher'], {'path': str(SETUID_LAUNCHER), **runtime_images[str(SETUID_LAUNCHER)]})
        self.assertEqual(result['selected_generation'], selected)

    def test_nix_output_hash_and_native_architecture_are_checked_before_cases(self):
        output = "/nix/store/" + "0" * 32 + "-application"
        source = "a" * 40
        artifact = {"id": "nix-amd64", "installation": "nix-package", "architecture": "amd64",
                    "source_commit": source, "identity": {"kind": "nix-closure", "sha256": "0" * 64,
                    "store_path": output, "lock_sha256": "b" * 64}}
        final = {"format_version": 1, "source_commit": source, "artifacts": [artifact]}
        root = Path(output) / "lib/painted-wolf-code"
        rows = {output: {"narHash": "sha256-" + "A" * 43 + "="}}
        with patch("scripts.linux_release.campaign.runtime.platform.system", return_value="Linux"), patch(
                "scripts.linux_release.campaign.runtime.platform.machine", return_value="x86_64"), patch.object(
                Path, "resolve", lambda path, **kwargs: path), patch(
                "scripts.linux_release.campaign.runtime.elf", return_value={"sha256": "client"}), patch(
                "scripts.linux_release.campaign.runtime.manifest", return_value={"version": "1.0.2", "images": {}}), patch(
                "scripts.linux_release.campaign.runtime.file_identity", return_value={"header": b"#!/bin/sh", "mode": 0o755}), patch(
                "scripts.linux_release.campaign.runtime.nix_command", side_effect=lambda *args: json.dumps(rows).encode()) as query:
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
