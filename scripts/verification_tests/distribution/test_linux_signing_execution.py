"""Trust-mode and cryptographic-envelope admission failures remain fail closed."""

import base64
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release import openpgp, signing

FINGERPRINT = "A" * 40


class LinuxSigningTests(unittest.TestCase):
    def test_fixture_catalog_cannot_enter_production_or_select_an_ambiguous_generation(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "keys.json"
            row = {"generation": 1, "fingerprint": FINGERPRINT, "public_key": "key.asc"}
            value = {"format_version": 1, "rehearsal": True, "signing_generation": 1, "generations": [row]}
            path.write_text(json.dumps(value))
            with self.assertRaises(ValueError):
                openpgp.selection(path)
            self.assertEqual(openpgp.selection(path, True), (row, path.parent / "key.asc"))
            for mutation in [dict(value, generations=[row, row]), dict(value, signing_generation=True),
                             dict(value, generations=[dict(row, public_key="../key.asc")]),
                             dict(value, generations=[dict(row, fingerprint="short-key-id")])]:
                path.write_text(json.dumps(mutation))
                with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                    openpgp.selection(path, True)

    def test_only_one_verified_selected_primary_rsa_sha512_signature_is_accepted(self):
        valid = f"[GNUPG:] VALIDSIG {FINGERPRINT} 2026-10-18 1 0 4 0 1 10 00 {FINGERPRINT}\n".encode()
        with patch.object(openpgp, "run", return_value=valid):
            openpgp.verify_signature(Path("/isolated"), FINGERPRINT, Path("signature"))
        for response in [b"[GNUPG:] GOODSIG short-id identity\n", valid + valid,
                         valid.replace(b" 1 10 00 ", b" 1 8 00 "),
                         valid.replace(b" 4 0 1 10 ", b" 4 0 22 10 "),
                         valid.replace(FINGERPRINT.encode(), b"B" * 40)]:
            with self.subTest(response=response), patch.object(openpgp, "run", return_value=response), self.assertRaises(ValueError):
                openpgp.verify_signature(Path("/isolated"), FINGERPRINT, Path("signature"))

    def test_rpm_envelopes_require_a_bounded_v4_rsa_sha512_packet(self):
        packet = bytes([0xC2, 4, 4, 0, 1, 10])
        openpgp.require_sha512_packet(base64.b64encode(packet).decode())
        for changed in [packet[:-1], packet + b"trailing", bytes([0xC2, 224, 4, 0, 1, 10]),
                        bytes([0xC2, 4, 4, 0, 1, 8]), bytes([0xC2, 4, 4, 0, 22, 10])]:
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                openpgp.require_sha512_packet(base64.b64encode(changed).decode())

    def test_primary_fingerprints_ignore_subkeys_but_retain_extra_primary_keys(self):
        data = f"pub:::::::::\nfpr:::::::::{FINGERPRINT}:\nsub:::::::::\nfpr:::::::::{'B' * 40}:\npub:::::::::\nfpr:::::::::{'C' * 40}:\n"
        self.assertEqual(openpgp.primary_fingerprints(data.encode()), [FINGERPRINT, "C" * 40])

    def test_private_material_requires_an_owned_private_regular_file_and_removes_environment(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True):
            path = Path(directory) / "private.asc"
            path.write_bytes(b"fixture")
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                signing.private_material(path)
            path.chmod(0o600)
            self.assertEqual(signing.private_material(path), b"fixture")
            alias = path.parent / "alias.asc"
            alias.symlink_to(path)
            with self.assertRaises(OSError):
                signing.private_material(alias)
            os.environ["LINUX_PACKAGE_SIGNING_KEY"] = "fixture environment"
            self.assertEqual(signing.private_material(None), b"fixture environment")
            self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", os.environ)

    def test_tool_processes_never_inherit_publication_credentials(self):
        class Result:
            returncode = 0
            stdout = b"public result"
        with patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "fixture secret", "GH_TOKEN": "fixture token"}), patch.object(openpgp.subprocess, "run", return_value=Result()) as command:
            self.assertEqual(openpgp.run(["gpg", "--version"], Path("/private")), b"public result")
            environment = command.call_args.kwargs["env"]
            self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", environment)
            self.assertNotIn("GH_TOKEN", environment)
            self.assertEqual(environment["GNUPGHOME"], "/private")

    def test_unsigned_rpm_is_rejected_even_when_rpm_digest_check_would_succeed(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            package = home / "unsigned.rpm"
            package.write_bytes(b"fixture")
            from contextlib import contextmanager
            @contextmanager
            def ring(*args):
                yield home
            with patch.object(openpgp, "keyring", ring), patch.object(openpgp, "run", return_value=b"(none)|(none)|(none)|(none)") as commands:
                with self.assertRaisesRegex(ValueError, "no OpenPGP signature"):
                    openpgp.verify_rpm(package, home / "public.asc", FINGERPRINT)
                self.assertFalse(any("--checksig" in call.args[0] for call in commands.call_args_list))
