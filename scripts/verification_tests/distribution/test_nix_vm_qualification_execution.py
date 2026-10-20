"""VM factories retain exact application custody without claiming installed outcomes."""
import base64
from copy import deepcopy
import hashlib
from pathlib import Path
import tempfile
import unittest

from linux_release.nix_vm import bind
from linux_release.nix_vm_producer import admit, nix_hash, validate_cache

APP = "/nix/store/" + "0" * 32 + "-painted-wolf-code"
VM = "/nix/store/" + "1" * 32 + "-nixos-vm"
HASH = "sha256-" + base64.b64encode(bytes(32)).decode()


class NixVMQualificationTests(unittest.TestCase):
    def setUp(self):
        self.expected = dict(source_commit="a" * 40, source_tree="b" * 40, lock_sha256="c" * 64)
        self.app = {**self.expected, "passed": True, "component": "default",
            "archive": {"publication": "nonrelease"}, "builds": [{"outputs": {"out": APP}}],
            "closure": {APP: {"narHash": HASH, "narSize": 13, "references": [], "deriver": APP + ".drv"}}}
        self.vm = {**deepcopy(self.app), "component": "nixos-gnome-vm", "builds": [{"outputs": {"out": VM}}]}
        self.vm["closure"][VM] = {"narHash": HASH, "narSize": 23, "references": [APP], "deriver": VM + ".drv"}
        self.run = {"conclusion": "success", "head_branch": "linux/nix-closure-admission",
            "path": ".github/workflows/linux-nix-qualification.yml", "head_sha": "d" * 40,
            "repository": {"full_name": "paintedwolf-ai/paintedwolf-code"}}
        self.manifest = {**self.expected, "controller_commit": "d" * 40, "architecture": "x86_64",
            "execution": "native", "publication": "nonrelease"}

    def bind(self, app=None, vm=None):
        return bind(app or self.app, vm or self.vm, **self.expected, desktop="gnome",
                    architecture="x86_64", controller_commit="e" * 40)

    def test_factory_binds_application_and_keeps_installed_qualification_separate(self):
        result = self.bind()
        self.assertEqual(result["application"], {"store_path": APP, "nar_hash": HASH})
        self.assertEqual(result["factory"]["store_path"], VM)
        self.assertFalse(result["installed_qualified"])

    def test_separate_host_factory_pins_preserve_the_exact_application(self):
        role = {"schema_version": 1, "application_commit": self.expected["source_commit"],
                "source_commit": "f" * 40, "source_tree": "1" * 40,
                "channel": "nixos-26.05", "nixpkgs_revision": "2" * 40,
                "lock_sha256": "3" * 64}
        self.vm["factory"] = role
        self.vm["source_commit"] = "4" * 40
        result = bind(self.app, self.vm, **self.expected, desktop="gnome", architecture="x86_64",
                      controller_commit="e" * 40, factory_source={"source_commit": "f" * 40,
                          "source_tree": "1" * 40, "channel": "nixos-26.05"})
        self.assertEqual(result["factory"]["source"], role)
        self.assertEqual(result["application"]["store_path"], APP)
        for field, value in [("channel", "nixos-unstable"), ("source_commit", "5" * 40),
                             ("application_commit", "6" * 40), ("nixpkgs_revision", "HEAD")]:
            vm = deepcopy(self.vm)
            vm["factory"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                bind(self.app, vm, **self.expected, desktop="gnome", architecture="x86_64",
                     controller_commit="e" * 40, factory_source={"source_commit": "f" * 40,
                         "source_tree": "1" * 40, "channel": "nixos-26.05"})

    def test_changed_source_lock_desktop_archive_or_application_bytes_refuses(self):
        for field, value in [("source_commit", "f" * 40), ("source_tree", "f" * 40),
                             ("lock_sha256", "f" * 64), ("component", "nixos-plasma-vm"), ("passed", False),
                             ("archive", {"publication": "release"})]:
            with self.subTest(field=field):
                vm = deepcopy(self.vm)
                vm[field] = value
                with self.assertRaises(ValueError):
                    self.bind(vm=vm)
        for field, value in [("narHash", "sha256-" + base64.b64encode(b"x" * 32).decode()), ("narSize", 14)]:
            vm = deepcopy(self.vm)
            vm["closure"][APP][field] = value
            with self.assertRaises(ValueError):
                self.bind(vm=vm)
        vm = deepcopy(self.vm)
        del vm["closure"][APP]
        with self.assertRaises(ValueError):
            self.bind(vm=vm)

    def test_only_matching_successful_repository_native_producer_is_admitted(self):
        self.assertEqual(admit(self.run, self.manifest, self.app, **self.expected, architecture="x86_64"), APP)
        for field, value in [("conclusion", "failure"), ("head_branch", "other"), ("path", "release.yml"),
                             ("head_sha", "f" * 40), ("repository", {"full_name": "foreign/repo"})]:
            run = {**self.run, field: value}
            with self.subTest(field=field), self.assertRaises(ValueError):
                admit(run, self.manifest, self.app, **self.expected, architecture="x86_64")
        with self.assertRaises(ValueError):
            admit(self.run, self.manifest, self.app, **self.expected, architecture="aarch64")

    def cache(self, root):
        (root / "nar").mkdir()
        (root / "nix-cache-info").write_text("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 50\n")
        payload = b"compressed fixture"
        (root / "nar/0.nar.xz").write_bytes(payload)
        metadata = (root / ("0" * 32 + ".narinfo"))
        metadata.write_text("\n".join(["StorePath: " + APP, "URL: nar/0.nar.xz", "Compression: xz",
            "FileHash: " + nix_hash(hashlib.sha256(payload).digest()), "FileSize: " + str(len(payload)),
            "NarHash: sha256:" + "0" * 52, "NarSize: 13", "References: "]) + "\n")
        return metadata

    def test_cache_metadata_bytes_and_declared_object_boundary_are_verified(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            metadata = self.cache(root)
            validate_cache(root, self.app["closure"])
            original = metadata.read_text()
            for text in [original.replace(APP, VM), original.replace("NarSize: 13", "NarSize: 14"),
                         original.replace("URL: nar/0.nar.xz", "URL: ../outside.nar.xz"),
                         original + "StorePath: " + APP + "\n"]:
                metadata.write_text(text)
                with self.assertRaises(ValueError):
                    validate_cache(root, self.app["closure"])
            metadata.write_text(original)
            payload = root / "nar/0.nar.xz"
            payload.write_bytes(b"X" * payload.stat().st_size)
            with self.assertRaises(ValueError):
                validate_cache(root, self.app["closure"])
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.cache(root)
            (root / "foreign").write_text("not selected")
            with self.assertRaises(ValueError):
                validate_cache(root, self.app["closure"])

    def test_nix_hash_padding_boundaries(self):
        self.assertEqual(nix_hash(bytes(32)), "sha256:" + "0" * 52)
        self.assertEqual(nix_hash(b"\xff" * 32), "sha256:1" + "z" * 51)

    def test_cache_links_refuse_before_following_foreign_payloads(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.cache(root)
            payload = root / "nar/0.nar.xz"
            payload.unlink()
            payload.symlink_to(root / "missing-foreign-payload")
            with self.assertRaisesRegex(ValueError, "only regular files"):
                validate_cache(root, self.app["closure"])


if __name__ == "__main__":
    unittest.main()
