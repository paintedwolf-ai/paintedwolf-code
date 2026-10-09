"""Generation selection and credential isolation at the publication boundary."""
import base64
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
