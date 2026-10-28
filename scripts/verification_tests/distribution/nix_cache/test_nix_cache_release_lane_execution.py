"""The release lane publishes Nix closures only with provisioned trust and fresh admission."""

import base64
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.nix_cache import release_lane

ROOT = Path(__file__).resolve().parents[4]
SOURCE = "a" * 40
SECRET = bytes(range(64))
PUBLIC = "paintedwolf-cache-1:" + base64.b64encode(SECRET[32:]).decode()


def policy(directory: Path, keys) -> Path:
    path = directory / "nix-cache-trust.json"
    path.write_text(json.dumps({"schema_version": 1, "mode": "production", "public_keys": keys}))
    return path


class NixCacheReleaseLaneTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name)

    def test_committed_trust_refuses_until_the_key_is_provisioned(self):
        committed = json.loads((ROOT / "packaging/linux/nix-cache-trust.json").read_text())
        self.assertEqual(committed, {"schema_version": 1, "mode": "production", "public_keys": []})
        with self.assertRaisesRegex(ValueError, "NIX_CACHE_SIGNING_KEY in the release-publication environment"):
            release_lane.trust_policy()
        with self.assertRaisesRegex(ValueError, "only the paintedwolf-cache-1"):
            release_lane.trust_policy(policy(self.work, ["other-1:" + base64.b64encode(bytes(32)).decode()]))
        self.assertEqual(release_lane.trust_policy(policy(self.work, [PUBLIC]))["public_keys"], [PUBLIC])

    def test_credential_must_be_the_committed_public_key(self):
        trusted = policy(self.work, [PUBLIC])
        secret = "paintedwolf-cache-1:" + base64.b64encode(SECRET).decode()
        self.assertEqual(release_lane.credential(secret, trusted), PUBLIC)
        for wrong in [None, "", "other-1:" + base64.b64encode(SECRET).decode(), "paintedwolf-cache-1:not-base64",
                      "paintedwolf-cache-1:" + base64.b64encode(bytes(64)).decode()]:
            with self.subTest(wrong=wrong), self.assertRaises(ValueError):
                release_lane.credential(wrong, trusted)

    def test_signing_key_is_owner_only_and_consumed(self):
        with patch.dict(os.environ, {"NIX_CACHE_SIGNING_KEY": "secret\n"}):
            with release_lane.signing_key() as path:
                self.assertEqual((path.read_text(), stat.S_IMODE(path.stat().st_mode)), ("secret", 0o600))
                self.assertNotIn("NIX_CACHE_SIGNING_KEY", os.environ)
        with patch.dict(os.environ, {}, clear=True), self.assertRaisesRegex(ValueError, "release-publication"):
            with release_lane.signing_key():
                pass

    def test_publication_covers_both_systems_with_fresh_admission(self):
        lock = self.work / "flake.lock"
        lock.write_text("{}")
        outputs = {system: ["/nix/store/" + "a" * 32 + "-painted-wolf-code"] for system in release_lane.SYSTEMS}
        qualified, delivered = [], []

        def construct(request, trust, key, cache):
            self.assertEqual((request["mode"], request["cache_prefix"], request["application_commit"]),
                             ("production", "nix-cache", SOURCE))
            self.assertEqual(request["flake_lock_sha256"], hashlib.sha256(b"{}").hexdigest())
            return {"request": request, "public_keys": trust["public_keys"], "status": "passed"}

        def deliver(cache, report, *, authorize, backend_factory):
            self.assertIs(authorize(report["request"], report), True)
            delivered.append(report["request"]["system"])
            return {"system": report["request"]["system"]}
        trust = {"schema_version": 1, "mode": "production", "public_keys": [PUBLIC]}
        receipts = release_lane.publish(outputs, SOURCE, "1.2.3", self.work, self.work / "key", qualified.append,
                                        lock=lock, policy=trust, construct=construct, deliver=deliver,
                                        backend_factory=object)
        self.assertEqual(delivered, list(release_lane.SYSTEMS))
        self.assertEqual((len(receipts), qualified), (2, [SOURCE, SOURCE]))
        with self.assertRaisesRegex(ValueError, "every native Linux system"):
            release_lane.publish({"x86_64-linux": []}, SOURCE, "1.2.3", self.work, self.work / "key", qualified.append,
                                 lock=lock, policy=trust, construct=construct, deliver=deliver)
        with self.assertRaisesRegex(ValueError, "not provisioned"):
            release_lane.publish(outputs, SOURCE, "1.2.3", self.work, self.work / "key", qualified.append, lock=lock)

    def test_halt_withdraws_availability_of_each_published_system(self):
        records = {"release-records/1.2.3/x86_64-linux.json": json.dumps({"request": {
            "version": "1.2.3", "system": "x86_64-linux", "mode": "production"}}).encode()}

        class Backend:
            def __init__(self, request):
                self.request = request

            def read(self, name):
                return records.get(name)
        withdrawn = []

        def retract(request, *, authorize, backend_factory):
            self.assertTrue(authorize(request))
            self.assertFalse(authorize({**request, "version": "1.2.4"}))
            withdrawn.append(request["system"])
        self.assertEqual(release_lane.withdraw("1.2.3", backend_factory=Backend, retract=retract), ["x86_64-linux"])
        self.assertEqual(withdrawn, ["x86_64-linux"])
        records["release-records/1.2.4/aarch64-linux.json"] = json.dumps({"request": {
            "version": "1.2.3", "system": "aarch64-linux", "mode": "production"}}).encode()
        with self.assertRaisesRegex(ValueError, "differs from the halted release"):
            release_lane.withdraw("1.2.4", backend_factory=Backend, retract=retract)

    def run_main(self, arguments, environment=None):
        with patch.object(sys, "argv", ["release_lane", *arguments]), patch.dict(os.environ, environment or {}):
            try:
                release_lane.main()
            except SystemExit as exit:
                return exit.code
        return 0

    def test_commands_fail_closed_only_once_linux_is_public(self):
        self.assertEqual(self.run_main(["check-trust"]), 1)
        with patch.object(release_lane, "linux_public", return_value=False):
            self.assertEqual(self.run_main(["check-credential"]), 0)
        with patch.object(release_lane, "linux_public", return_value=True):
            self.assertEqual(self.run_main(["check-credential"]), 1)
        with patch.object(release_lane, "credential", return_value=PUBLIC):
            self.assertEqual(self.run_main(["check-credential"], {"NIX_CACHE_SIGNING_KEY": "x"}), 0)
        self.assertIsInstance(release_lane.linux_public(), bool)
        outputs = self.work / "outputs"
        outputs.mkdir()
        for system in release_lane.SYSTEMS:
            (outputs / (system + ".txt")).write_text("/nix/store/" + "a" * 32 + "-x\n")
        with patch.object(release_lane, "publish", return_value=[]) as published:
            code = self.run_main(["publish", "--outputs", str(outputs), "--source-commit", SOURCE, "--version", "1.2.3"],
                                 {"NIX_CACHE_SIGNING_KEY": "x"})
        self.assertEqual(code, 0)
        self.assertEqual(sorted(published.call_args.args[0]), sorted(release_lane.SYSTEMS))
        with patch.object(release_lane, "withdraw", return_value=[]) as withdrawn:
            self.assertEqual(self.run_main(["withdraw", "--bad", "1.2.3"]), 0)
        withdrawn.assert_called_once_with("1.2.3")


if __name__ == "__main__":
    unittest.main()
