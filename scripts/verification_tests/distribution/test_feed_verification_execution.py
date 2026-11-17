"""The release verifier checks public bytes without exposing signing credentials."""
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import feed_signing
from verification_tests.distribution.test_feed_signing_execution import public_key, signature


class FeedVerificationTests(unittest.TestCase):
    def test_verifier_receives_exact_decoded_documents_and_no_credentials(self):
        paths = []
        with tempfile.TemporaryDirectory() as directory:
            pointer = Path(directory) / "latest-stable-key-1.json"
            pointer.write_bytes(b'{"version":"2.0.0"}\n')
            signed = signature(1, pointer.name, "2.0.0")
            public = public_key(1)
            def run(command, **kwargs):
                self.assertEqual(command[:4], ["minisign", "-V", "-q", "-m"])
                self.assertEqual(Path(command[4]).read_bytes(), pointer.read_bytes())
                self.assertEqual(command[5], "-x")
                self.assertEqual(command[7], "-p")
                paths.extend([Path(command[6]), Path(command[8])])
                self.assertEqual(paths[0].read_bytes(), base64.b64decode(signed))
                self.assertEqual(paths[1].read_bytes(), base64.b64decode(public))
                self.assertEqual(set(kwargs["env"]), {"PATH"})
                self.assertTrue(kwargs["capture_output"])
                self.assertEqual(kwargs["timeout"], 30)
                return subprocess.CompletedProcess(command, 0)
            with patch.dict(os.environ, {"FEED_SIGNING_KEYS_JSON": "private", "TAURI_SIGNING_PRIVATE_KEY": "private"}), \
                    patch.object(feed_signing.subprocess, "run", side_effect=run):
                feed_signing.verify(pointer, signed + "\n", public)
        self.assertEqual(len(paths), 2)
        self.assertTrue(all(not path.exists() for path in paths))

    def test_missing_verifier_timeout_and_invalid_signature_fail_closed(self):
        outcomes = [FileNotFoundError("private diagnostic"), subprocess.TimeoutExpired("minisign", 30, stderr=b"private diagnostic"),
                    subprocess.CompletedProcess([], 1, stderr=b"private diagnostic")]
        with tempfile.TemporaryDirectory() as directory:
            pointer = Path(directory) / "pointer.json"
            pointer.write_text("{}")
            for outcome in outcomes:
                with self.subTest(outcome=type(outcome).__name__), patch.object(feed_signing.subprocess, "run") as run:
                    if isinstance(outcome, Exception):
                        run.side_effect = outcome
                    else:
                        run.return_value = outcome
                    with self.assertRaisesRegex(ValueError, "^feed signature verification (failed|could not complete)$"):
                        feed_signing.verify(pointer, signature(1, pointer.name, "2.0.0"), public_key(1))

