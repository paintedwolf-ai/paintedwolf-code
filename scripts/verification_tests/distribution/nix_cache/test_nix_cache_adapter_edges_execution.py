"""Cache construction binds staged bytes to an admitted local closure."""

import base64
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts.nix_cache import adapter


OUTPUT = "/nix/store/" + "0" * 32 + "-application"
PUBLIC = "paintedwolf-cache-1:" + base64.b64encode(bytes(32)).decode()


class NixCacheAdapterEdges(unittest.TestCase):
    def request(self):
        return {"schema_version": 1, "mode": "production", "version": "1.0.0",
                "application_commit": "a" * 40, "flake_lock_sha256": "b" * 64,
                "system": "x86_64-linux", "outputs": [OUTPUT], "cache_prefix": "nix-cache"}

    def policy(self):
        return {"schema_version": 1, "mode": "production", "public_keys": [PUBLIC]}

    def test_nix_failure_does_not_echo_private_input_or_inherited_configuration(self):
        secret = "private signing material"
        with patch.object(adapter.subprocess, "run", return_value=SimpleNamespace(returncode=19, stdout="", stderr=secret)) as run:
            with self.assertRaisesRegex(ValueError, "status 19") as failure:
                adapter.nix("key", "convert-secret-to-public", stdin=secret)
        self.assertNotIn(secret, str(failure.exception))
        self.assertNotIn("NIX_CONFIG", run.call_args.kwargs["env"])
        self.assertEqual(run.call_args.kwargs["input"], secret)

    def test_signing_identity_reads_only_an_owner_private_regular_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            key = Path(temporary) / "key"
            key.write_text("private key")
            key.chmod(0o600)
            with patch.object(adapter, "nix", return_value=PUBLIC + "\n") as command:
                self.assertEqual(adapter.signing_identity(key, Path(temporary) / "cache"), PUBLIC)
            self.assertEqual(command.call_args.kwargs["stdin"], "private key")

    def test_construct_copies_verifies_and_atomically_selects_matching_closure(self):
        closure = {OUTPUT: {"derivation": OUTPUT + ".drv", "nar_hash": "sha256:fixture",
                            "nar_bytes": 17, "references": []}}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            key = root / "key"
            key.write_text("private key")
            key.chmod(0o600)
            cache = root / "cache"
            calls = []

            def nix(*arguments, **kwargs):
                calls.append((arguments, kwargs))
                return json.dumps({"version": 4, "derivations": {}}) if arguments[:2] == ("derivation", "show") else "nix 2.0"

            with patch.object(adapter, "signing_identity", return_value=PUBLIC), \
                 patch.object(adapter, "path_info", return_value={OUTPUT: "raw"}) as path_info, \
                 patch.object(adapter, "closure_projection", return_value=closure) as project, \
                 patch.object(adapter, "derivation_projection", return_value={OUTPUT + ".drv": {"system": "x86_64-linux"}}), \
                 patch.object(adapter, "verify") as verify, patch.object(adapter, "inventory", return_value=[{"name": "nix-cache-info"}]), \
                 patch.object(adapter, "nix", side_effect=nix):
                report = adapter.construct(self.request(), self.policy(), key, cache)
            self.assertTrue(cache.is_dir())
            self.assertEqual(report["closure"], closure)
            self.assertEqual(report["objects"], [{"name": "nix-cache-info"}])
            self.assertEqual(path_info.call_count, 2)
            self.assertEqual(project.call_count, 2)
            self.assertEqual(verify.call_count, 1)
            self.assertTrue(any(args[0] == "copy" and "--to" in args for args, _ in calls))

    def test_construct_refuses_cache_drift_and_cleans_stage(self):
        source = {OUTPUT: {"derivation": OUTPUT + ".drv", "nar_hash": "sha256:original",
                           "nar_bytes": 17, "references": []}}
        changed = {OUTPUT: {**source[OUTPUT], "nar_hash": "sha256:changed"}}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            key = root / "key"
            key.write_text("private key")
            key.chmod(0o600)
            cache = root / "cache"
            with patch.object(adapter, "signing_identity", return_value=PUBLIC), \
                 patch.object(adapter, "path_info", return_value={}), \
                 patch.object(adapter, "closure_projection", side_effect=[source, changed]), \
                 patch.object(adapter, "derivation_projection", return_value={}), \
                 patch.object(adapter, "verify"), \
                 patch.object(adapter, "nix", return_value="{}"):
                with self.assertRaisesRegex(ValueError, "differs from its selected source"):
                    adapter.construct(self.request(), self.policy(), key, cache)
            self.assertFalse(cache.exists())
            self.assertEqual(list(root.glob(".nix-cache-stage-*")), [])

    def test_cli_refuses_report_inside_cache_before_using_key(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            request, policy = root / "request.json", root / "policy.json"
            request.write_text(json.dumps(self.request()))
            policy.write_text(json.dumps(self.policy()))
            cache = root / "cache"
            arguments = ["nix-cache-adapter", "--request", str(request), "--trust-policy", str(policy),
                         "--signing-key-file", str(root / "key"), "--cache", str(cache),
                         "--report", str(cache / "report.json")]
            with patch.object(sys, "argv", arguments), patch.object(adapter, "construct") as construct:
                with self.assertRaisesRegex(ValueError, "outside the cache"):
                    adapter.main()
            construct.assert_not_called()


if __name__ == "__main__":
    unittest.main()
