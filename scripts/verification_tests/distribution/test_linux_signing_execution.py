"""Linux package signatures are admitted only for the catalog-selected key, trust mode and digest."""

import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.linux_release import openpgp, signing, signing_rehearsal

ROOT = Path(__file__).resolve().parents[3]
FINGERPRINT = "A" * 40


def private_key(catalog):
    return catalog.parent / "rehearsal-secret.asc"


def fingerprint(catalog):
    return json.loads(catalog.read_text())["generations"][0]["fingerprint"]


def raw_signature(catalog, subject, digest):
    """Make an unarmored detached signature with the catalog key and the requested digest."""
    selected, public = openpgp.selection(catalog, True)
    output = subject.with_name(f"{subject.name}.{catalog.parent.name}.{digest.lower()}")
    with openpgp.keyring(public, selected["fingerprint"], private_key(catalog).read_bytes()) as home:
        openpgp.run(["gpg", "--batch", "--no-tty", "--yes", "--local-user", selected["fingerprint"],
                     "--digest-algo", digest, "--output", str(output), "--detach-sign", str(subject)], home)
    return output


def rpm_tools(directory, packet, checksig_status=0):
    """PATH shims for the two rpm tools; gpg stays real."""
    log = directory / "calls"
    (directory / "packet").write_text(packet)
    scripts = {
        "rpm": f"""#!/bin/sh
echo "rpm $*" >> '{log}'
case "$*" in *-qp*) printf '%s|(none)|(none)|(none)' "$(cat '{directory}/packet')" ;; esac
""",
        "rpmkeys": f"""#!/bin/sh
echo "rpmkeys $*" >> '{log}'
case "$*" in *--checksig*) exit {checksig_status} ;; esac
""",
    }
    for name, body in scripts.items():
        path = directory / name
        path.write_text(body)
        path.chmod(0o755)
    return log


KEYS = {}


def setUpModule():
    KEYS["directory"] = tempfile.TemporaryDirectory(prefix="pw-linux-signing-test-")
    work = Path(KEYS["directory"].name)
    KEYS.update(work=work, selected=openpgp.ephemeral(work / "selected"), wrong=openpgp.ephemeral(work / "wrong"))


def tearDownModule():
    KEYS.pop("directory").cleanup()


class SigningKeyFixture(unittest.TestCase):
    def setUp(self):
        self.catalog, self.wrong = KEYS["selected"], KEYS["wrong"]
        self.case = Path(tempfile.mkdtemp(dir=KEYS["work"]))
        self.release = self.case / "Release"
        self.release.write_text("Origin: Painted Wolf Code signing test\nSuite: fixture\n")

    def sign(self, kind, destination, catalog=None, private=None):
        catalog = catalog or self.catalog
        return signing.sign(kind, self.release, destination, catalog, private or private_key(catalog), True)


class MetadataSignatureTests(SigningKeyFixture):
    def test_detached_and_clear_signatures_verify_only_for_the_selected_rehearsal_key(self):
        for kind, name in [("detached", "Release.gpg"), ("release", "InRelease")]:
            with self.subTest(kind=kind):
                signature = self.case / name
                receipt = self.sign(kind, signature)
                self.assertEqual((receipt["kind"], receipt["fingerprint"], receipt["digest_algorithm"]),
                                 (kind, fingerprint(self.catalog), "sha512"))
                self.assertEqual(signature.stat().st_mode & 0o777, 0o644)
                signing.verify(kind, self.release, signature, self.catalog, True)
                with self.assertRaises(ValueError):
                    signing.verify(kind, self.release, signature, self.wrong, True)
                with self.assertRaises(ValueError):
                    signing.verify(kind, self.release, signature, signing.CATALOG)
                changed = self.case / ("changed-" + name)
                changed.write_bytes(self.release.read_bytes() + b"Tampered: yes\n")
                with self.assertRaises(ValueError):
                    signing.verify(kind, changed, signature, self.catalog, True)

    def test_sha256_signature_from_the_selected_key_is_refused(self):
        signature = raw_signature(self.catalog, self.release, "SHA256")
        with self.assertRaisesRegex(ValueError, "SHA-512"):
            signing.verify("detached", self.release, signature, self.catalog, True)
        accepted = raw_signature(self.catalog, self.release, "SHA512")
        signing.verify("detached", self.release, accepted, self.catalog, True)

    def test_signature_carrying_an_unselected_second_signer_is_refused(self):
        selected = raw_signature(self.catalog, self.release, "SHA512")
        other = raw_signature(self.wrong, self.release, "SHA512")
        combined = self.case / "Release.sigs"
        combined.write_bytes(selected.read_bytes() + other.read_bytes())
        with self.assertRaises(ValueError):
            signing.verify("detached", self.release, combined, self.catalog, True)

    def test_private_key_of_another_identity_is_refused_without_replacing_the_destination(self):
        destination = self.case / "Release.gpg"
        destination.write_bytes(b"previous signature")
        with self.assertRaisesRegex(ValueError, "private key differs"):
            self.sign("detached", destination, private=private_key(self.wrong))
        self.assertEqual(destination.read_bytes(), b"previous signature")
        self.assertEqual(sorted(path.name for path in self.case.iterdir()), ["Release", "Release.gpg"])

    def test_public_key_must_be_exactly_the_catalog_fingerprint(self):
        extra = self.case / "extra"
        shutil.copytree(self.catalog.parent, extra)
        public = extra / "rehearsal-public.asc"
        public.write_bytes(public.read_bytes() + (self.wrong.parent / "rehearsal-public.asc").read_bytes())
        with self.assertRaisesRegex(ValueError, "extra primary keys"):
            self.sign("detached", self.case / "extra.gpg", catalog=extra / "keys.json")
        swapped = self.case / "swapped"
        shutil.copytree(self.catalog.parent, swapped)
        document = json.loads((swapped / "keys.json").read_text())
        document["generations"][0]["fingerprint"] = fingerprint(self.wrong)
        (swapped / "keys.json").write_text(json.dumps(document))
        with self.assertRaisesRegex(ValueError, "differs from the catalog"):
            self.sign("detached", self.case / "swapped.gpg", catalog=swapped / "keys.json")

    def test_signing_refuses_aliased_or_self_replacing_paths(self):
        alias = self.case / "alias"
        alias.symlink_to(self.release)
        with self.assertRaisesRegex(ValueError, "regular local inputs"):
            signing.sign("detached", alias, self.case / "alias.gpg", self.catalog, private_key(self.catalog), True)
        with self.assertRaisesRegex(ValueError, "cannot replace its subject"):
            self.sign("release", self.release)
        self.assertTrue(self.release.read_text().startswith("Origin:"))


class RpmEnvelopeTests(SigningKeyFixture):
    def packet(self, digest):
        return base64.b64encode(raw_signature(self.catalog, self.release, digest).read_bytes()).decode()

    def test_real_signature_packets_are_admitted_only_with_sha512(self):
        openpgp.require_sha512_packet(self.packet("SHA512"))
        with self.assertRaisesRegex(ValueError, "SHA-512"):
            openpgp.require_sha512_packet(self.packet("SHA256"))

    def test_malformed_packet_framing_is_refused(self):
        packet = bytes([0xC2, 4, 4, 0, 1, 10])
        openpgp.require_sha512_packet(base64.b64encode(packet).decode())
        for changed in [packet[:-1], packet + b"trailing", bytes([0xC2, 224, 4, 0, 1, 10]),
                        bytes([0xC2, 4, 4, 0, 1, 8]), bytes([0xC2, 4, 4, 0, 22, 10]), bytes([0x8B, 4, 4, 0, 1, 10])]:
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                openpgp.require_sha512_packet(base64.b64encode(changed).decode())

    def verify_with(self, packet, checksig_status=0):
        tools = self.case / "tools"
        tools.mkdir()
        log = rpm_tools(tools, packet, checksig_status)
        package = self.case / "fixture.rpm"
        package.write_bytes(b"rpm fixture")
        with patch.dict(os.environ, {"PATH": f"{tools}{os.pathsep}{os.environ['PATH']}"}):
            try:
                openpgp.verify_rpm(package, self.catalog.parent / "rehearsal-public.asc", fingerprint(self.catalog))
            finally:
                calls = log.read_text() if log.exists() else ""
        return calls

    def test_rpm_without_an_openpgp_signature_is_refused_before_digest_checks(self):
        with self.assertRaisesRegex(ValueError, "no OpenPGP signature"):
            self.verify_with("(none)")
        self.assertNotIn("--checksig", (self.case / "tools/calls").read_text())

    def test_rpm_signed_with_sha256_is_refused(self):
        with self.assertRaisesRegex(ValueError, "SHA-512"):
            self.verify_with(self.packet("SHA256"))

    def test_rpm_signature_must_also_pass_the_rpm_keyring_check(self):
        with self.assertRaisesRegex(ValueError, "rpmkeys failed"):
            self.verify_with(self.packet("SHA512"), checksig_status=1)

    def test_selected_sha512_rpm_signature_is_admitted_after_keyring_check(self):
        calls = self.verify_with(self.packet("SHA512"))
        self.assertIn("--initdb", calls)
        self.assertIn("--checksig", calls)

    def test_rpm_verification_refuses_aliases(self):
        alias = self.case / "alias.rpm"
        alias.symlink_to(self.release)
        with self.assertRaisesRegex(ValueError, "regular local package"):
            openpgp.verify_rpm(alias, self.catalog.parent / "rehearsal-public.asc", fingerprint(self.catalog))


class CatalogAndCredentialTests(unittest.TestCase):
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
                             dict(value, format_version=2),
                             dict(value, generations=[dict(row, public_key="../key.asc")]),
                             dict(value, generations=[dict(row, fingerprint="short-key-id")])]:
                path.write_text(json.dumps(mutation))
                with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                    openpgp.selection(path, True)
        with self.assertRaisesRegex(ValueError, "trust mode"):
            openpgp.selection(signing.CATALOG, True)

    def test_primary_fingerprints_ignore_subkeys_but_retain_extra_primary_keys(self):
        data = f"pub:::::::::\nfpr:::::::::{FINGERPRINT}:\nsub:::::::::\nfpr:::::::::{'B' * 40}:\npub:::::::::\nfpr:::::::::{'C' * 40}:\n"
        self.assertEqual(openpgp.primary_fingerprints(data.encode()), [FINGERPRINT, "C" * 40])
        with self.assertRaises(ValueError):
            openpgp.primary_fingerprints(b"pub:::::::::\nfpr:::::::::abc:\n")

    def test_private_material_requires_one_owned_private_bounded_source(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {}, clear=True):
            path = Path(directory) / "private.asc"
            path.write_bytes(b"fixture")
            path.chmod(0o644)
            with self.assertRaisesRegex(ValueError, "private to its owner"):
                signing.private_material(path)
            path.chmod(0o600)
            self.assertEqual(signing.private_material(path), b"fixture")
            alias = path.parent / "alias.asc"
            alias.symlink_to(path)
            with self.assertRaises(OSError):
                signing.private_material(alias)
            large = path.parent / "large.asc"
            large.write_bytes(b"x" * ((1 << 20) + 1))
            large.chmod(0o600)
            with self.assertRaisesRegex(ValueError, "bounded"):
                signing.private_material(large)
            with self.assertRaisesRegex(ValueError, "is required"):
                signing.private_material(None)
            os.environ["LINUX_PACKAGE_SIGNING_KEY"] = "fixture environment"
            with self.assertRaisesRegex(ValueError, "either"):
                signing.private_material(path)
            self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", os.environ)
            os.environ["LINUX_PACKAGE_SIGNING_KEY"] = "fixture environment"
            self.assertEqual(signing.private_material(None), b"fixture environment")
            self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", os.environ)

    def test_tool_processes_never_inherit_publication_credentials(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "fixture secret", "GH_TOKEN": "fixture token"}):
            output = openpgp.run(["env"], Path(directory)).decode()
        self.assertNotIn("fixture secret", output)
        self.assertNotIn("fixture token", output)
        self.assertIn(f"GNUPGHOME={directory}", output.splitlines())

    def test_failed_tools_are_reported_by_name_and_status_only(self):
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(ValueError, r"^false failed with exit status 1$"):
            openpgp.run(["false"], Path(directory))


class CommandAdmissionTests(unittest.TestCase):
    def command(self, module, *arguments, env=None):
        return subprocess.run([sys.executable, "-m", module, *arguments], cwd=ROOT, capture_output=True,
                              text=True, env=env or os.environ.copy())

    def test_signing_command_refuses_ephemeral_keys_outside_rehearsal_and_missing_subjects(self):
        with tempfile.TemporaryDirectory() as directory:
            ephemeral = self.command("scripts.linux_release.signing", "ephemeral-key", "--output", directory + "/key")
            self.assertEqual(ephemeral.returncode, 1)
            self.assertIn("confined to explicit rehearsals", ephemeral.stderr)
            self.assertFalse(Path(directory, "key").exists())
            unsigned = self.command("scripts.linux_release.signing", "sign", "--output", directory + "/out")
            self.assertEqual(unsigned.returncode, 1)
            self.assertIn("subject is required", unsigned.stderr)

    def test_rehearsal_command_requires_its_native_tools_before_writing_a_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            tools = Path(directory) / "tools"
            tools.mkdir()
            (tools / "python3").symlink_to(sys.executable)
            receipt = Path(directory) / "receipt.json"
            result = self.command("scripts.linux_release.signing_rehearsal", "--receipt", str(receipt),
                                  env={**os.environ, "PATH": str(tools)})
        self.assertEqual(result.returncode, 2)
        self.assertIn("gpg is required", result.stderr)
        self.assertFalse(receipt.exists())

    def test_rehearsal_admission_fails_when_an_invalid_case_is_admitted(self):
        signing_rehearsal.rejected(lambda: openpgp.selection(signing.CATALOG, True))
        with self.assertRaisesRegex(ValueError, "invalid signing case was admitted"):
            signing_rehearsal.rejected(lambda: None)

    def test_rehearsal_drops_production_material_and_requires_candidate_rpms(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(os.environ, {"LINUX_PACKAGE_SIGNING_KEY": "production secret"}):
            work, candidates = Path(directory) / "work", Path(directory) / "candidates"
            work.mkdir()
            candidates.mkdir()
            with self.assertRaisesRegex(ValueError, "requires candidate RPMs"):
                signing_rehearsal.rehearse(work, candidates, ("x86_64",))
            self.assertNotIn("LINUX_PACKAGE_SIGNING_KEY", os.environ)
            for name in ["InRelease", "Release.gpg"]:
                signing.verify("release" if name == "InRelease" else "detached", work / "Release",
                               work / name, work / "key/keys.json", True)


if __name__ == "__main__":
    unittest.main()
