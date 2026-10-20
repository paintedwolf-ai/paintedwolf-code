"""Nix cache admission fails before private-key use when release trust is invalid."""
import base64
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache.admission import closure_projection, derivation_projection, inventory, validate_request
from scripts.nix_cache.adapter import construct, signing_identity


OUTPUT = "/nix/store/" + "0" * 32 + "-application"
DEPENDENCY = "/nix/store/" + "1" * 32 + "-dependency"
PUBLIC = "paintedwolf-cache-1:" + base64.b64encode(bytes(32)).decode()
FIXTURE = "paintedwolf-rehearsal-fixture:" + base64.b64encode(bytes(32)).decode()


class NixCacheAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.request = {"schema_version": 1, "mode": "production", "version": "1.0.0", "application_commit": "a" * 40,
                        "flake_lock_sha256": "b" * 64, "system": "x86_64-linux", "outputs": [OUTPUT], "cache_prefix": "nix-cache"}
        self.policy = {"schema_version": 1, "mode": "production", "public_keys": [PUBLIC]}

    def test_explicit_production_and_isolated_rehearsal_are_distinct(self):
        validate_request(self.request, self.policy)
        request = {**self.request, "mode": "rehearsal", "cache_prefix": "release-system-tests/fixture/nix-cache"}
        policy = {**self.policy, "mode": "rehearsal", "public_keys": [FIXTURE]}
        validate_request(request, policy)
        for left, right in [(self.request, policy), (request, self.policy), (self.request, {**self.policy, "public_keys": [FIXTURE]}),
                            (request, {**policy, "public_keys": [PUBLIC]}), ({**request, "cache_prefix": "nix-cache"}, policy)]:
            with self.subTest(request=left, policy=right), self.assertRaises(ValueError):
                validate_request(left, right)

    def test_invalid_release_inputs_fail_before_private_key_access(self):
        for field, value in [("system", "aarch64-darwin"), ("application_commit", "main"), ("flake_lock_sha256", "bad"),
                             ("outputs", [OUTPUT, OUTPUT]), ("outputs", ["nixpkgs#hello"]), ("cache_prefix", "release-system-tests/f/nix-cache")]:
            request = {**self.request, field: value}
            with self.subTest(field=field), patch("scripts.nix_cache.adapter.signing_identity") as read:
                with self.assertRaises(ValueError):
                    construct(request, self.policy, Path("/missing/key"), Path("/missing/cache"))
                read.assert_not_called()

    def test_private_keys_require_owner_only_regular_files_outside_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            key = root / "key"
            key.write_text("never print private input")
            key.chmod(0o644)
            with patch("scripts.nix_cache.adapter.nix") as command:
                with self.assertRaises(ValueError):
                    signing_identity(key, root / "cache")
                command.assert_not_called()
                key.chmod(0o600)
                with self.assertRaises(ValueError):
                    signing_identity(key, root)
                alias = root / "cache-alias"
                alias.symlink_to(root, target_is_directory=True)
                with self.assertRaises(ValueError):
                    signing_identity(key, alias)
                link = root / "link"
                link.symlink_to(key)
                with self.assertRaises(ValueError):
                    signing_identity(link, root / "cache")
                command.assert_not_called()

    def test_closure_requires_all_outputs_and_references(self):
        rows = {OUTPUT: {"narHash": "sha256:fixture", "narSize": 17, "references": [DEPENDENCY], "deriver": OUTPUT + ".drv"},
                DEPENDENCY: {"narHash": "sha256:dependency", "narSize": 9, "references": []}}
        projection = closure_projection(rows, [OUTPUT])
        self.assertEqual(projection[OUTPUT]["references"], [DEPENDENCY])
        for modified in [{OUTPUT: rows[OUTPUT]}, {DEPENDENCY: rows[DEPENDENCY]}, {**rows, OUTPUT: {**rows[OUTPUT], "narSize": 0}}]:
            with self.subTest(rows=modified), self.assertRaises(ValueError):
                closure_projection(modified, [OUTPUT])

    def test_inventory_admits_content_before_metadata_and_refuses_missing_objects(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = Path(directory)
            (cache / "nar").mkdir()
            nar = cache / "nar/fixture.nar.xz"
            nar.write_bytes(b"fixture")
            (cache / "nix-cache-info").write_text("StoreDir: /nix/store\n")
            metadata = cache / ("0" * 32 + ".narinfo")
            original = f"StorePath: {OUTPUT}\nURL: nar/fixture.nar.xz\nFileSize: 7\nSig: paintedwolf-cache-1:fixture\n"
            metadata.write_text(original)
            objects = inventory(cache, {OUTPUT: {}}, [PUBLIC])
            self.assertEqual([row["phase"] for row in objects], ["nar", "narinfo", "cache-info"])
            self.assertIn("immutable", objects[0]["cache_control"])
            self.assertIn("no-store", objects[1]["cache_control"])
            for content in [original.replace("nar/fixture.nar.xz", "nar/../../key"), original.replace("FileSize: 7", "FileSize: 8"),
                            original.replace("paintedwolf-cache-1", "foreign"), original + "URL: nar/fixture.nar.xz\n"]:
                metadata.write_text(content)
                with self.subTest(content=content), self.assertRaises((ValueError, OSError)):
                    inventory(cache, {OUTPUT: {}}, [PUBLIC])
            metadata.write_text(original)
            private = cache / "unexpected-secret"
            private.write_text("must never publish")
            with self.assertRaises(ValueError):
                inventory(cache, {OUTPUT: {}}, [PUBLIC])
            private.unlink()
            nar.unlink()
            with self.assertRaises(OSError):
                inventory(cache, {OUTPUT: {}}, [PUBLIC])

    def test_derivation_requires_the_actual_system_and_output(self):
        path = OUTPUT + ".drv"
        closure = {OUTPUT: {"derivation": path}}
        row = {"version": 4, "system": "x86_64-linux", "outputs": {"out": {"path": Path(OUTPUT).name}}}
        document = {"version": 4, "derivations": {Path(path).name: row}}
        self.assertEqual(derivation_projection(document, closure, self.request)[path]["system"], "x86_64-linux")
        for changed in [{**row, "system": "aarch64-linux"}, {**row, "outputs": {"out": {"path": DEPENDENCY}}}, {**row, "version": 5}]:
            with self.subTest(row=changed), self.assertRaises(ValueError):
                derivation_projection({"version": 4, "derivations": {Path(path).name: changed}}, closure, self.request)
        with self.assertRaises(ValueError):
            derivation_projection({**document, "version": 5}, closure, self.request)
