"""Source-only Nix delivery cannot inherit signing or publication authority."""
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache.source import describe_verified_source

OUTPUT = "/nix/store/" + "0" * 32 + "-application"
DERIVER = OUTPUT + ".drv"


class NixSourceTests(unittest.TestCase):
    def setUp(self):
        self.request = {"schema_version": 1, "version": "1.0.0", "application_commit": "a" * 40,
            "flake_lock_sha256": hashlib.sha256(b"lock").hexdigest(), "system": "x86_64-linux", "outputs": [OUTPUT]}
        self.closure = {OUTPUT: {"narHash": "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "narSize": 31,
            "references": [], "deriver": DERIVER}}
        self.derivations = {"version": 4, "derivations": {Path(DERIVER).name: {
            "version": 4, "system": "x86_64-linux", "outputs": {"out": {"path": OUTPUT}}}}}

    def git(self, source, *arguments):
        if arguments == ("rev-parse", "a" * 40 + "^{commit}"):
            return ("a" * 40).encode()
        if arguments == ("rev-parse", "a" * 40 + "^{tree}"):
            return ("b" * 40).encode()
        return b"lock" if arguments[-1].endswith(":flake.lock") else b"1.0.0\n"

    def run_source(self, root, request=None, evaluations=None, **kwargs):
        sequence = evaluations or [json.dumps({"output": OUTPUT, "derivation": DERIVER}), "",
                                  json.dumps(self.derivations), "nix fixture"]
        with patch("scripts.nix_cache.source.git", side_effect=self.git), \
             patch("scripts.nix_cache.source.nix", side_effect=sequence) as command, \
             patch("scripts.nix_cache.source.path_info", return_value=self.closure):
            result = describe_verified_source(request or self.request, root, **kwargs)
            return result, command.call_args_list

    def test_exact_git_output_and_recursive_contents_without_public_cache_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            result, calls = self.run_source(Path(directory))
        self.assertTrue(result["content_verified"])
        self.assertFalse(result["installed_qualified"])
        self.assertFalse(result["publisher_or_custody_authority"])
        self.assertEqual(result["source_tree"], "b" * 40)
        self.assertFalse({"public_keys", "objects", "cache_prefix", "url"} & result.keys())
        self.assertIn("?rev=" + "a" * 40 + "#packages.x86_64-linux.default", calls[0].args[7])
        self.assertEqual(calls[1].args[:3], ("store", "verify", "--store"))
        self.assertIn("--recursive", calls[1].args)
        self.assertIn("--no-trust", calls[1].args)

    def test_output_derivation_content_and_lock_mismatch_refuse(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            wrong = deepcopy(self.request)
            wrong["flake_lock_sha256"] = "f" * 64
            with self.assertRaises(ValueError):
                self.run_source(root, wrong)
            for evaluated in [{"output": OUTPUT + "-other", "derivation": DERIVER},
                              {"output": OUTPUT, "derivation": DERIVER + "-other"}]:
                with self.assertRaises(ValueError):
                    self.run_source(root, evaluations=[json.dumps(evaluated), ""])
            with self.assertRaises(ValueError):
                self.run_source(root, evaluations=[json.dumps({"output": OUTPUT, "derivation": DERIVER}),
                                                   ValueError("altered NAR")])

    def test_no_remote_store_or_publication_fields_enter_source_only_verification(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for store in ["https://cache.invalid", "ssh://builder.invalid", "local?root=/tmp/private"]:
                with self.assertRaises(ValueError):
                    self.run_source(root, source_store=store)
            for field in ["public_keys", "cache_prefix", "mode"]:
                with self.assertRaises(ValueError):
                    self.run_source(root, {**self.request, field: "not source authority"})


if __name__ == "__main__":
    unittest.main()
