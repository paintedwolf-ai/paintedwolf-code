import base64
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import feed_credentials_migrate as migration


class FeedCredentialMigrationTests(unittest.TestCase):
    def test_legacy_credentials_survive_json_encoding(self):
        with patch.dict(os.environ, {"FEED_SIGNING_KEYS_JSON": "", "FEED_SIGNING_PRIVATE_KEY": 'key\n"value',
                                     "FEED_SIGNING_PRIVATE_KEY_PASSWORD": 'password\\"'}, clear=True):
            document = migration.legacy_map({"generations": [{"generation": 1}]})
        row = json.loads(document)["generations"]["1"]
        self.assertEqual(row, {"private_key": 'key\n"value', "password": 'password\\"'})

    def test_existing_map_and_multi_generation_registry_refuse_migration(self):
        for existing, numbers in [("already set", [1]), ("", [1, 2]), ("", [2])]:
            with self.subTest(existing=existing, numbers=numbers), patch.dict(os.environ, {
                "FEED_SIGNING_KEYS_JSON": existing, "FEED_SIGNING_PRIVATE_KEY": "private"}, clear=True):
                with self.assertRaises(ValueError):
                    migration.legacy_map({"generations": [{"generation": n} for n in numbers]})

    def test_missing_generation_never_invokes_signer(self):
        with patch.object(migration, "sign") as sign:
            with self.assertRaises(ValueError):
                migration.prove('{"format_version":1,"generations":{"1":{"private_key":"key","password":""}}}',
                                {"generations": [{"generation": 1}, {"generation": 2}]})
        sign.assert_not_called()

    def test_failed_signing_exports_nothing_and_restores_environment(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {
            "RUNNER_TEMP": directory, "FEED_CREDENTIAL_MODE": "migrate", "FEED_SIGNING_KEYS_JSON": "",
            "FEED_SIGNING_PRIVATE_KEY": "private"}, clear=True), patch.object(migration, "load_registry", return_value={
                "generations": [{"generation": 1}]}), patch.object(migration, "sign", side_effect=ValueError("private")), \
                patch.object(migration, "sealed_request") as encrypt, patch("sys.stderr") as stderr:
            with self.assertRaises(SystemExit):
                migration.main()
            self.assertEqual(os.environ["FEED_SIGNING_KEYS_JSON"], "")
            self.assertEqual(list(Path(directory).iterdir()), [])
            encrypt.assert_not_called()
            self.assertNotIn("private", str(stderr.write.call_args_list))

    def test_encryption_uses_sealed_box_and_exports_only_ciphertext(self):
        from unittest.mock import MagicMock
        sodium = MagicMock()
        sodium.sodium_init.return_value = 0
        def seal(output, message, length, public_key):
            self.assertEqual(message, b"private map")
            self.assertEqual(length, len(message))
            self.assertEqual(public_key, bytes(range(32)))
            output.raw = b"x" * (length + 48)
            return 0
        sodium.crypto_box_seal.side_effect = seal
        target = json.dumps({"key_id": "123", "key": base64.b64encode(bytes(range(32))).decode()})
        with patch.object(migration.ctypes.util, "find_library", return_value="sodium"), \
                patch.object(migration.ctypes, "CDLL", return_value=sodium):
            request = migration.sealed_request("private map", target)
        self.assertEqual(request, {"key_id": "123", "encrypted_value": base64.b64encode(b"x" * 59).decode()})

    def test_malformed_encryption_target_is_rejected_before_loading_crypto(self):
        for key_id, key in [("123", "bad"), ("123", ""), (123, base64.b64encode(bytes(32)).decode())]:
            with self.subTest(key_id=key_id, key=key), patch.object(migration.ctypes, "CDLL") as library:
                with self.assertRaises(ValueError):
                    migration.sealed_request("private map", json.dumps({"key_id": key_id, "key": key}))
                library.assert_not_called()
