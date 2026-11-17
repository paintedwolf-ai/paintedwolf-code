"""Generation selection, credential proofs and credential isolation at the publication boundary."""
import base64
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import feed_signing


def public_key(number):
    key = b"Ed" + number.to_bytes(8, "little") + bytes([number]) * 32
    return base64.b64encode(b"untrusted comment: test\n" + base64.b64encode(key) + b"\n").decode()


def signature(number, file, version):
    body = b"ED" + number.to_bytes(8, "little") + bytes(64)
    document = "untrusted comment: test\n" + base64.b64encode(body).decode() + f"\ntrusted comment: timestamp:1800000000\tfile:{file}\tversion:{version}\nunused\n"
    return base64.b64encode(document.encode()).decode()


class FeedSigningTests(unittest.TestCase):
    def test_generations_use_distinct_credentials_and_do_not_pass_the_map_to_signer(self):
        keys = {"generations": [{"generation": n, "feed_public_key": public_key(n)} for n in [1, 2]]}
        secret = json.dumps({"format_version": 1, "generations": {str(n): {"private_key": f"key-{n}", "password": f"password-{n}"} for n in [1, 2]}})
        used = []
        def run(command, **kwargs):
            env = kwargs["env"]
            if command[0] == "minisign":
                self.assertEqual(set(env), {"PATH"})
                self.assertTrue(kwargs["capture_output"])
                self.assertEqual(kwargs["timeout"], 30)
                return subprocess.CompletedProcess(command, 0)
            self.assertNotIn("FEED_SIGNING_KEYS_JSON", env)
            number = int(env["TAURI_SIGNING_PRIVATE_KEY"].split("-")[1])
            self.assertEqual(env["TAURI_SIGNING_PRIVATE_KEY_PASSWORD"], f"password-{number}")
            pointer = Path(command[-1])
            Path(str(pointer) + ".sig").write_text(signature(number, pointer.name, "2.0.0"))
            used.append(number)
            return subprocess.CompletedProcess(command, 0)
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {"FEED_SIGNING_KEYS_JSON": secret}), patch.object(feed_signing.subprocess, "run", side_effect=run):
            for number in [1, 2]:
                pointer = Path(tmp) / f"latest-stable-key-{number}.json"
                pointer.write_text(json.dumps({"version": "2.0.0"}))
                feed_signing.sign(pointer, number, keys)
        self.assertEqual(used, [1, 2])

    def test_missing_generation_and_signer_failure_do_not_leak_credentials(self):
        secret = json.dumps({"format_version": 1, "generations": {"1": {"private_key": "secret-key", "password": "secret-password"}}})
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {"FEED_SIGNING_KEYS_JSON": secret}), patch.object(feed_signing.subprocess, "run") as run:
            pointer = Path(tmp) / "latest-stable-key-1.json"
            pointer.write_text('{"version":"2.0.0"}')
            with self.assertRaisesRegex(ValueError, "generation 2"):
                feed_signing.sign(pointer, 2, {})
            run.assert_not_called()
            run.return_value = subprocess.CompletedProcess([], 1, stderr="secret-key")
            with self.assertRaisesRegex(ValueError, "^feed signing failed for generation 1$"):
                feed_signing.sign(pointer, 1, {})

    def test_fixture_registry_is_refused_outside_the_isolated_namespace(self):
        for prefix in ["", "updates", "release-system-tests/../updates"]:
            with self.assertRaisesRegex(ValueError, "isolated"):
                feed_signing.signing_registry(Path("fixture.json"), prefix)

    def test_credential_preflight_checks_every_generation_before_signing(self):
        registry = {"generations": [{"generation": 1}, {"generation": 2}]}
        secret = json.dumps({"format_version": 1, "generations": {"1": {"private_key": "secret", "password": ""}}})
        with patch.dict(os.environ, {"FEED_SIGNING_KEYS_JSON": secret}), patch.object(feed_signing, "sign") as sign:
            with self.assertRaisesRegex(ValueError, "generation 2"):
                feed_signing.check_credentials(registry)
            sign.assert_not_called()

    def test_credential_preflight_signs_only_temporary_probes(self):
        registry = {"generations": [{"generation": 1}, {"generation": 2}]}
        secret = json.dumps({"format_version": 1, "generations": {str(n): {"private_key": "secret", "password": ""} for n in (1, 2)}})
        paths = []
        def sign(pointer, number, keys):
            self.assertEqual(json.loads(pointer.read_text()), {"version": "0.0.0"})
            self.assertEqual(keys, registry)
            paths.append(pointer)
        with patch.dict(os.environ, {"FEED_SIGNING_KEYS_JSON": secret}), patch.object(feed_signing, "sign", side_effect=sign):
            feed_signing.check_credentials(registry)
        self.assertEqual(len(paths), 2)
        self.assertTrue(all(not path.exists() for path in paths))


class ArtifactCredentialTests(unittest.TestCase):
    registry = {"signing_generation": 2,
                "generations": [{"generation": number, "public_key": public_key(number)} for number in (1, 2)]}

    def check(self, signer=0, verifier=0, environment=None):
        seen = {}
        def run(command, **kwargs):
            if command[0] == "minisign":
                seen["public"] = Path(command[8]).read_bytes()
                seen["subject"] = Path(command[4])
                return subprocess.CompletedProcess(command, verifier, stderr=b"secret-key")
            seen["signer"] = kwargs["env"]
            subject = Path(command[-1])
            Path(str(subject) + ".sig").write_text(signature(2, subject.name, "0.0.0"))
            return subprocess.CompletedProcess(command, signer, stderr=b"secret-key")
        secret = {"TAURI_SIGNING_PRIVATE_KEY": "secret-key"} if environment is None else environment
        # The verifier shares the patched module, so no real signer or verifier runs.
        with patch.dict(os.environ, secret), patch.object(feed_signing.subprocess, "run", side_effect=run):
            os.environ.pop("TAURI_SIGNING_PRIVATE_KEY_PASSWORD", None)
            if environment == {}:
                os.environ.pop("TAURI_SIGNING_PRIVATE_KEY", None)
            try:
                return feed_signing.check_artifact_credential(self.registry), seen
            except ValueError as error:
                return error, seen

    def test_the_signing_generation_key_verifies_a_removed_probe(self):
        result, seen = self.check()
        self.assertEqual(result, 2)
        self.assertEqual(seen["public"], base64.b64decode(public_key(2)))
        self.assertEqual(seen["signer"]["TAURI_SIGNING_PRIVATE_KEY_PASSWORD"], "")
        self.assertFalse(seen["subject"].exists())

    def test_an_absent_failing_or_foreign_key_is_refused_without_its_diagnostics(self):
        for arguments in ({"environment": {}}, {"signer": 1}, {"verifier": 1}):
            with self.subTest(**arguments):
                result, seen = self.check(**arguments)
                self.assertIsInstance(result, ValueError)
                self.assertNotIn("secret-key", str(result))
                if "environment" in arguments:
                    self.assertEqual(seen, {})

    def test_the_command_reports_its_generation_or_one_redacted_error(self):
        for outcome, code, stream in ((2, None, "stdout"), (ValueError("no key"), 1, "stderr")):
            with self.subTest(outcome=outcome), patch.object(feed_signing, "signing_registry", return_value=self.registry), \
                    patch.object(feed_signing, "check_artifact_credential", side_effect=[outcome]), \
                    patch.object(feed_signing.sys, stream, new_callable=io.StringIO) as output, \
                    patch.object(feed_signing.sys, "argv", ["feed_signing.py", "--check-artifact-credential"]):
                if code is None:
                    feed_signing.main()
                    self.assertEqual(output.getvalue(), "updater artifact key signs for generation 2\n")
                else:
                    with self.assertRaises(SystemExit) as exit:
                        feed_signing.main()
                    self.assertEqual((exit.exception.code, output.getvalue()), (1, "error: no key\n"))
