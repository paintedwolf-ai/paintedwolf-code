"""Source-only Nix delivery cannot inherit signing or publication authority, and its rehearsal uses
a disposable committed flake and rejects changed identity."""
from copy import deepcopy
import hashlib
from io import StringIO
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache import source_rehearsal
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


class NixSourceRehearsalEdges(unittest.TestCase):
    def test_committed_source_is_stable_when_the_worktree_changes(self):
        first_request = []
        observations = []

        def describe(request, source, *, source_store, rehearsal=False):
            if source_store.startswith("ssh:"):
                raise ValueError("remote store refused")
            if first_request and request != first_request[0]:
                raise ValueError("source authority changed")
            if not first_request:
                first_request.append(dict(request))
            observations.append((source, source_store, rehearsal, (source / "VERSION").read_text()))
            return {"closure": {OUTPUT: {}}, "source_commit": request["application_commit"]}

        def nix(*arguments, **_kwargs):
            self.assertEqual(arguments[0], "build")
            self.assertIn("--no-write-lock-file", arguments)
            return json.dumps([{"outputs": {"out": OUTPUT}}])

        with patch.object(source_rehearsal.platform, "system", return_value="Linux"), \
             patch.object(source_rehearsal.platform, "machine", return_value="x86_64"), \
             patch.object(source_rehearsal, "nix", side_effect=nix), \
             patch.object(source_rehearsal, "describe_verified_source", side_effect=describe), \
             patch.object(source_rehearsal, "local_store", side_effect=lambda root: "local?root=" + str(root)), \
             patch("sys.stdout", new_callable=StringIO) as output:
            source_rehearsal.main()
        self.assertEqual(len(observations), 2)
        self.assertEqual([row[3] for row in observations], ["0.0.0-rehearsal\n", "9.9.9\n"])
        self.assertTrue(all(row[2] for row in observations))
        self.assertEqual(first_request[0]["outputs"], [OUTPUT])
        self.assertEqual(json.loads(output.getvalue())["status"], "passed")

    def test_unsupported_host_refuses_before_creating_a_fixture(self):
        with patch.object(source_rehearsal.platform, "system", return_value="Darwin"), \
             patch.object(source_rehearsal, "nix") as nix:
            with self.assertRaisesRegex(ValueError, "supported native Linux"):
                source_rehearsal.main()
        nix.assert_not_called()


if __name__ == "__main__":
    unittest.main()
