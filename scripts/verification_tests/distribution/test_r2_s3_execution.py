"""Offline contracts for private credentials and bucket-scoped object operations."""
import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

from release_storage import rehearsal, s3


class RehearsalCredentialsTests(unittest.TestCase):
    def setUp(self):
        self.config = dict(schema_version=1, endpoint_url="https://" + "a" * 32 + ".r2.cloudflarestorage.com",
                           bucket="test-bucket", access_key_id="access", secret_access_key="secret",
                           download_base_url="https://test-download.example.com")

    def test_private_json_and_literal_credentials_isolate_inherited_authority(self):
        self.config["secret_access_key"] = "$(touch${IFS}/tmp/never-execute)"
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(self.config))
            path.chmod(0o600)
            env = rehearsal.environment(rehearsal.load_file(path), {
                "AWS_PROFILE": "production", "AWS_SESSION_TOKEN": "old", "CLOUDFLARE_API_TOKEN": "broad",
                "BASH_ENV": "/untrusted", "PATH": os.environ["PATH"]})
        self.assertEqual(env["AWS_SECRET_ACCESS_KEY"], self.config["secret_access_key"])
        for name in ["AWS_PROFILE", "AWS_SESSION_TOKEN", "CLOUDFLARE_API_TOKEN", "BASH_ENV"]:
            self.assertNotIn(name, env)
        self.assertEqual(env["AWS_SHARED_CREDENTIALS_FILE"], os.devnull)
        self.assertEqual(env["RELEASE_R2_TRANSPORT"], "s3")

    def test_refuses_public_files_symlinks_duplicate_fields_and_invalid_schema(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(self.config))
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                rehearsal.load_file(path)
            path.chmod(0o600)
            link = Path(directory) / "link"
            link.symlink_to(path)
            with self.assertRaises(OSError):
                rehearsal.load_file(link)
            path.write_text('{"secret_access_key":"private-value","secret_access_key":"other"}')
            with self.assertRaisesRegex(ValueError, "^duplicate configuration field$"):
                rehearsal.load_file(path)
        for change in [{"schema_version": True}, {"unexpected": "private"},
                       {"endpoint_url": "https://attacker.example"},
                       {"download_base_url": "https://user:secret@test.example"}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                rehearsal.validate({**self.config, **change})

    def test_failed_harness_preserves_private_config_and_cleans_empty_keys(self):
        root = Path(__file__).resolve().parents[3]
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            scripts = work / "scripts"
            scripts.mkdir()
            (work / "packaging").mkdir()
            (work / "lycaon-den").mkdir()
            for name in ["release-r2-live-test.sh", "release-r2-rest.sh"]:
                shutil.copyfile(root / "scripts" / name, scripts / name)
            shutil.copytree(root / "scripts/release_storage", scripts / "release_storage")
            shutil.copyfile(root / "packaging/update-keys.json", work / "packaging/update-keys.json")
            commands = work / "commands"
            commands.mkdir()
            bun = commands / "bun"
            bun.write_text("#!" + sys.executable + "\nimport pathlib,sys\n"
                           "p=pathlib.Path(sys.argv[sys.argv.index('-w')+1])\n"
                           "p.write_text('fixture-private')\n"
                           "pathlib.Path(str(p)+'.pub').write_text('fixture-public')\n")
            aws = commands / "aws"
            aws.write_text("#!/bin/sh\nexit 37\n")
            for command in [bun, aws]:
                command.chmod(0o700)
            config = work / "private.json"
            raw = json.dumps(self.config).encode()
            config.write_bytes(raw)
            config.chmod(0o600)
            before = config.stat()
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith(("AWS_", "CLOUDFLARE_", "RELEASE_TEST_", "PW_R2_REHEARSAL_"))}
            env.update(RELEASE_TEST_CONFIG=str(config), PATH=str(commands) + os.pathsep + os.environ["PATH"])
            result = subprocess.run(["/bin/bash", str(scripts / "release-r2-live-test.sh")],
                                    env=env, capture_output=True, text=True, timeout=30)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("R2 credential probe failed", result.stderr)
            self.assertNotIn("unbound variable", result.stderr)
            self.assertNotIn("cleanup failed", result.stderr)
            self.assertEqual(config.read_bytes(), raw)
            after = config.stat()
            self.assertEqual((after.st_ino, after.st_mode, after.st_mtime_ns),
                             (before.st_ino, before.st_mode, before.st_mtime_ns))

    def test_explicit_config_and_optional_session_token(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps({**self.config, "session_token": "temporary"}))
            path.chmod(0o400)
            configured = rehearsal.configuration({"RELEASE_TEST_CONFIG": str(path)})
            self.assertEqual(rehearsal.environment(configured, {})["AWS_SESSION_TOKEN"], "temporary")


class BucketTransportTests(unittest.TestCase):
    def test_absence_is_structured_and_authentication_failure_is_not_absence(self):
        with patch.object(s3, "request", return_value={"Contents": [{"Key": "prefix/other"}]}):
            self.assertIsNone(s3.object_stat("prefix/key"))
        with patch.object(s3, "request", side_effect=ValueError("authentication refused")):
            with self.assertRaises(ValueError):
                s3.get("prefix/key", Path("unused"))
        with patch.object(s3, "request", return_value={"Contents": ["invalid"]}):
            with self.assertRaises(ValueError):
                s3.object_stat("key")

    def test_signature_neighbor_cannot_hide_newer_pointer(self):
        from release_distribution import read_storage, validate_advance
        raw = b'{"version":"0.0.900002"}'
        key = "prefix/latest.json"
        def response(operation, *arguments):
            if operation == "list-objects-v2":
                return {"Contents": [{"Key": key + ".sig", "Size": 100, "ETag": '"signature"'},
                                     {"Key": key, "Size": len(raw), "ETag": '"pointer"'}],
                        "IsTruncated": False}
            self.assertEqual(operation, "get-object")
            Path(arguments[-1]).write_bytes(raw)
            return {"ETag": '"pointer"'}
        with patch.dict(os.environ, {"RELEASE_R2_TRANSPORT": "s3"}), \
                patch.object(s3, "request", side_effect=response):
            current = read_storage(key)
            self.assertFalse(validate_advance({"version": "0.0.900001"}, current, optional=True))

    def test_exact_key_lookup_follows_pages_and_refuses_incomplete_absence(self):
        key = "prefix/latest.json"
        first = {"Contents": [{"Key": key + ".sig"}], "IsTruncated": True,
                 "NextContinuationToken": "page-two"}
        last = {"Contents": [{"Key": key, "Size": 10, "ETag": '"identity"'}], "IsTruncated": False}
        with patch.object(s3, "request", side_effect=[first, last]) as request:
            self.assertEqual(s3.object_stat(key)["etag"], "identity")
            self.assertEqual(request.call_args.args[-2:], ("--continuation-token", "page-two"))
        for pages in [[{**first, "NextContinuationToken": ""}], [first, first]]:
            with self.subTest(pages=pages), patch.object(s3, "request", side_effect=pages):
                with self.assertRaisesRegex(ValueError, "continuation token"):
                    s3.object_stat(key)

    def test_authenticated_read_refuses_listing_get_race(self):
        def response(operation, *args):
            Path(args[-1]).write_bytes(b"payload")
            return {"ETag": '"changed"'}
        with patch.object(s3, "object_stat", return_value={"size": 7, "etag": "original"}), \
                patch.object(s3, "request", side_effect=response):
            with self.assertRaisesRegex(ValueError, "changed"):
                s3.read_bytes("key")

    def test_multipart_immutable_identity_and_changed_bytes(self):
        import hashlib
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "object"
            source.write_bytes(b"payload")
            metadata = {"ContentLength": 7, "Metadata": {"sha256": hashlib.sha256(b"payload").hexdigest()}}
            with patch.object(s3, "object_stat", return_value={"etag": "multipart-2"}), \
                    patch.object(s3, "request", return_value=metadata), patch.object(s3, "put") as upload:
                s3.immutable("key", source, "text/plain", "immutable")
                upload.assert_not_called()
                source.write_bytes(b"changed")
                with self.assertRaisesRegex(ValueError, "different bytes"):
                    s3.immutable("key", source, "text/plain", "immutable")

    def test_s3_command_keeps_keys_literal_and_redacts_cli_error(self):
        env = {"R2_S3_ENDPOINT": "https://test.invalid", "R2_BUCKET": "test-bucket",
               "AWS_ACCESS_KEY_ID": "access", "AWS_SECRET_ACCESS_KEY": "private-value"}
        with patch.dict(os.environ, env, clear=True), patch.object(subprocess, "run") as run:
            run.return_value = subprocess.CompletedProcess([], 0, b'{}', b'')
            s3.request("delete-object", "--key", "prefix/$(literal)")
            command = run.call_args.args[0]
            self.assertEqual(command[-1], "prefix/$(literal)")
            self.assertNotIn("private-value", command)
            run.return_value = subprocess.CompletedProcess([], 1, b'', b'private-value')
            with self.assertRaisesRegex(ValueError, "^S3 delete-object failed; check bucket credentials, endpoint and object permissions$"):
                s3.request("delete-object", "--key", "key")
