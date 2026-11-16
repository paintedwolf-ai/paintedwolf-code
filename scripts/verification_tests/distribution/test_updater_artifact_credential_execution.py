"""The artifact key probe signs only a temporary file and trusts only the committed key."""
import base64
import io
import os
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import updater_artifact_credential as probe
from verification_tests.distribution.test_feed_signing_execution import public_key, signature


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
        with patch.dict(os.environ, secret), patch.object(probe.subprocess, "run", side_effect=run):
            os.environ.pop("TAURI_SIGNING_PRIVATE_KEY_PASSWORD", None)
            if environment == {}:
                os.environ.pop("TAURI_SIGNING_PRIVATE_KEY", None)
            try:
                return probe.check(self.registry), seen
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
            with self.subTest(outcome=outcome), patch.object(probe, "load_registry", return_value=self.registry), \
                    patch.object(probe, "check", side_effect=[outcome]), \
                    patch.object(probe.sys, stream, new_callable=io.StringIO) as output:
                if code is None:
                    probe.main()
                    self.assertEqual(output.getvalue(), "updater artifact key signs for generation 2\n")
                else:
                    with self.assertRaises(SystemExit) as exit:
                        probe.main()
                    self.assertEqual((exit.exception.code, output.getvalue()), (1, "error: no key\n"))
