"""A Nix receipt cannot omit runtime paths or silently qualify another output."""

import base64
from copy import deepcopy
import unittest

from linux_release.nix_closure import bind, describe, nar_digest

OUTPUT = "/nix/store/" + "0" * 32 + "-painted-wolf-code"
LIBRARY = "/nix/store/" + "1" * 32 + "-runtime"
DERIVER = OUTPUT + ".drv"
HASH = "sha256-" + base64.b64encode(bytes(range(32))).decode()


class NixClosureTests(unittest.TestCase):
    def test_nar_identity_accepts_only_canonical_sha256_sri(self):
        for digest in [bytes(32), bytes(range(32)), b"\xff" * 32]:
            encoded = "sha256-" + base64.b64encode(digest).decode()
            with self.subTest(encoded=encoded):
                self.assertEqual(nar_digest(encoded), digest.hex())
        invalid = [None, b"sha256-", True, 7, {}, "sha512-" + HASH[7:],
                   "sha256-", HASH + "\n", HASH + "=", HASH[:-1],
                   "sha256-" + "!" * 43 + "=",
                   "sha256-" + base64.b64encode(bytes(31)).decode(),
                   "sha256-" + base64.b64encode(bytes(33)).decode(),
                   "sha256-" + "A" * 42 + "B="]
        for value in invalid:
            with self.subTest(value=value), self.assertRaises(ValueError):
                nar_digest(value)

    def info(self):
        return {OUTPUT: {"narHash": HASH, "narSize": 123, "references": [LIBRARY], "deriver": DERIVER},
                LIBRARY: {"narHash": HASH, "narSize": 456, "references": []}}

    def test_canonical_complete_graph_retains_real_output_and_separate_qualification_states(self):
        info = self.info()
        result = describe(info, [OUTPUT], "a" * 40, "b" * 64)
        reordered = dict(reversed(list(info.items())))
        self.assertEqual(result, describe(reordered, [OUTPUT], "a" * 40, "b" * 64))
        self.assertEqual(result["outputs"][0]["identity"]["sha256"], bytes(range(32)).hex())
        self.assertFalse(result["content_verified"])
        self.assertFalse(result["cache_trust_verified"])
        self.assertFalse(result["installed_qualified"])
        bind(result, deepcopy(result))

    def test_missing_and_unrelated_runtime_paths_are_rejected(self):
        info = self.info()
        del info[LIBRARY]
        with self.assertRaisesRegex(ValueError, "omits"):
            describe(info, [OUTPUT], "a" * 40, "b" * 64)
        info = self.info()
        info["/nix/store/" + "2" * 32 + "-unrelated"] = dict(info[LIBRARY])
        with self.assertRaisesRegex(ValueError, "unrelated"):
            describe(info, [OUTPUT], "a" * 40, "b" * 64)

    def test_invalid_nar_path_size_references_and_deriver_fail_closed(self):
        for field, value in [("narHash", "sha256-invalid"),
                             ("narHash", "sha256-" + "A" * 42 + "B="), ("narSize", True),
                             ("references", [LIBRARY, LIBRARY]), ("references", ["/tmp/runtime"]),
                             ("deriver", "unknown-deriver"), ("valid", False), ("path", LIBRARY)]:
            info = self.info()
            info[OUTPUT][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                describe(info, [OUTPUT], "a" * 40, "b" * 64)

    def test_source_locks_and_output_selection_are_required(self):
        for source, lock, outputs in [("main", "b" * 64, [OUTPUT]), ("a" * 40, "lock", [OUTPUT]),
                                      ("a" * 40, "b" * 64, []), ("a" * 40, "b" * 64, [OUTPUT, OUTPUT])]:
            with self.subTest(source=source, outputs=outputs), self.assertRaises(ValueError):
                describe(self.info(), outputs, source, lock)

    def test_stale_or_changed_final_artifact_is_not_accepted(self):
        expected = describe(self.info(), [OUTPUT], "a" * 40, "b" * 64)
        for field in ["source_commit", "lock_sha256", "closure_sha256", "paths", "outputs"]:
            actual = deepcopy(expected)
            actual[field] = "changed"
            with self.subTest(field=field), self.assertRaises(ValueError):
                bind(expected, actual)

    def test_self_references_terminate_without_weakening_graph_completeness(self):
        info = self.info()
        info[LIBRARY]["references"] = [LIBRARY, OUTPUT]
        self.assertEqual(len(describe(info, [OUTPUT], "a" * 40, "b" * 64)["paths"]), 2)
