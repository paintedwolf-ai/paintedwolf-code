"""The release rehearsal runs the release's own scripts and requires each seeded fault to be refused."""
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release_artifact_limits as limits
import release_rehearsal as rehearsal
from updater_signature import rehearsal_signature, signed_version


class FixtureKey:
    """Version-bound signature documents without a signer; verification is replaced alongside."""

    public = "fixture public key"

    def sign(self, path, version):
        return rehearsal_signature(version, path.name)


class LimitTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.limits = self.root / "limits.json"
        self.limits.write_text(json.dumps({"format_version": 1, "max_download_bytes": 4096, "max_expanded_bytes": 64,
                                           "max_archive_entries": 4}))

    def archive(self, *entries):
        path = self.root / "Product.app.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            for name, kind, value in entries:
                entry = tarfile.TarInfo(name)
                if kind == "file":
                    entry.size = len(value)
                    archive.addfile(entry, io.BytesIO(value))
                else:
                    entry.type, entry.linkname = tarfile.SYMTYPE, value
                    archive.addfile(entry)
        return path

    def check(self, path, version="1.1.0", platform="darwin-aarch64"):
        limits.check(path, version, platform, self.limits)

    def test_a_bundle_with_its_product_version_passes(self):
        self.check(self.archive(("Product.app/Contents/Resources/release-version", "file", b"1.1.0\n"),
                                ("Product.app/Contents/Frameworks/Current", "link", "../Resources")))
        self.check(rehearsal.updater_archive(self.root / "rehearsal.app.tar.gz", "1.1.0"))

    def test_every_client_limit_and_bundle_rule_refuses(self):
        version = ("Product.app/Contents/Resources/release-version", "file", b"1.1.0\n")
        for entries, message in (
                ([("../escape", "file", b"")], "escapes the bundle"),
                ([("Product/file", "file", b"")], "one application bundle"),
                ([version, ("Other.app/file", "file", b"")], "more than one root"),
                ([version, ("Product.app/link", "link", "../../outside")], "leaves the bundle"),
                ([version, ("Product.app/large", "file", b"x" * 65)], "expansion limit"),
                ([version, version], "repeats"),
                ([("Product.app/Contents/Resources/release-version", "file", b"1.0.0\n")], "does not match the release")):
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                self.check(self.archive(*entries))

    def test_size_alone_decides_an_artifact_that_is_not_an_archive(self):
        installer = self.root / "Product.exe"
        installer.write_bytes(b"x" * 4096)
        self.check(installer, platform="windows-x86_64")
        installer.write_bytes(b"x" * 4097)
        with self.assertRaisesRegex(ValueError, "download limit"):
            self.check(installer, platform="windows-x86_64")

    def test_the_command_reports_a_refusal_and_its_status(self):
        artifact = self.root / "Product.app.tar.gz"
        with artifact.open("wb") as sparse:
            sparse.truncate(json.loads(limits.LIMITS.read_text())["max_download_bytes"] + 1)
        result = subprocess.run([sys.executable, str(limits.LIMITS.parents[1] / "scripts/release_artifact_limits.py"),
                                 "--artifact", str(artifact), "--version", "1.1.0", "--platform", "darwin-aarch64"],
                                capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 1)


class AssemblyTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory()))

    def test_a_seeded_fault_that_is_admitted_fails_the_rehearsal(self):
        self.assertTrue(rehearsal.refused("fault", lambda: (_ for _ in ()).throw(ValueError("refused here"))))
        with self.assertRaisesRegex(ValueError, "admitted its seeded fault"):
            rehearsal.refused("fault", lambda: None)

    def test_the_workflows_pass_and_a_reserved_step_variable_is_refused(self):
        checks = rehearsal.check_workflows()
        self.assertEqual(checks["production"], "passed")
        self.assertIn("RUNNER_NAME", checks["seeded_reserved_name"])

    def test_the_release_aggregate_assembles_every_public_platform_and_refuses_a_rebound_signature(self):
        verified = []
        with patch.object(rehearsal.feed_signing, "verify", side_effect=lambda path, signature, key: verified.append(
                (path.name, signed_version(signature), key))):
            checks = rehearsal.check_limits_and_assembly(self.root, FixtureKey())
        release = rehearsal.version()
        self.assertEqual(checks["assembly"]["casks"], ["painted-wolf-code.rb", "painted-wolf-code@preview.rb"])
        self.assertEqual(verified, [(f"painted-wolf-code_v{release}_darwin-aarch64.app.tar.gz", release, FixtureKey.public)])
        self.assertIn("0.0.1", checks["assembly"]["seeded_signature_for_another_version"])
        self.assertIn("download limit", checks["limits"]["seeded_over_upload_limit"])

    def test_the_disposable_key_signs_prehashed_with_a_version_bound_comment(self):
        commands = []
        def run(arguments, **_):
            commands.append(arguments)
            if "-G" in arguments:
                Path(arguments[arguments.index("-p") + 1]).write_text("untrusted comment: key\nRWQ\n")
            else:
                Path(arguments[arguments.index("-x") + 1]).write_text("signature document\n")
        with patch.object(rehearsal, "run", side_effect=run):
            key = rehearsal.Key(self.root / "key")
            artifact = self.root / "artifact"
            artifact.write_bytes(b"artifact")
            document = key.sign(artifact, "1.1.0")
        self.assertEqual(base64.b64decode(key.public), b"untrusted comment: key\nRWQ\n")
        self.assertEqual(base64.b64decode(document), b"signature document\n")
        self.assertNotIn("-l", commands[1])
        self.assertEqual(commands[1][commands[1].index("-t") + 1], "timestamp:0\tfile:artifact\tversion:1.1.0")
        self.assertFalse(list(self.root.glob("*.minisig")))


class MacosTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.home = self.root / "home"
        self.home.mkdir()
        self.application = self.root / "Applications/Painted Wolf Code.app"
        self.taps = self.root / "taps"
        self.dmg = self.root / "Product.dmg"
        self.dmg.write_bytes(b"disk image")
        self.patches = [patch.object(rehearsal, "APPLICATION", self.application),
                        patch.dict(os.environ, {"HOME": str(self.home), "GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted"})]
        for item in self.patches:
            item.start()
            self.addCleanup(item.stop)

    def brew(self, uninstall_leaves=False):
        commands = []
        marker = self.home / ".config/paintedwolf/install-source.json"
        def run(arguments, **_):
            commands.append(arguments)
            if arguments[:2] == ["brew", "tap-new"]:
                self.taps.mkdir()
            if arguments[:2] == ["brew", "--repository"]:
                return subprocess.CompletedProcess(arguments, 0, str(self.taps) + "\n")
            if arguments[:2] == ["bash", str(rehearsal.ROOT / "scripts/release-render-cask.sh")]:
                return subprocess.run(arguments, check=True, capture_output=True, text=True, timeout=60)
            if arguments[:3] == ["brew", "install", "--cask"]:
                cask = (self.taps / "Casks/painted-wolf-code@preview.rb").read_text()
                url = cask.split('url "', 1)[1].split('"', 1)[0].replace("#{version}", rehearsal.version())
                with __import__("urllib.request").request.urlopen(url, timeout=10) as response:
                    self.assertEqual(response.read(), self.dmg.read_bytes())
                (self.application / "Contents").mkdir(parents=True)
                (self.application / "Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleShortVersionString": rehearsal.version()}))
                marker.parent.mkdir(parents=True)
                marker.write_text(json.dumps({"install_source": "homebrew_cask", "release_channel": "preview"}))
                marker.chmod(0o600)
            if arguments[:3] == ["brew", "uninstall", "--cask"] and not uninstall_leaves:
                marker.unlink()
                (self.application / "Contents/Info.plist").unlink()
                (self.application / "Contents").rmdir()
                self.application.rmdir()
            return subprocess.CompletedProcess(arguments, 0, "")
        return commands, run

    def test_the_rendered_preview_cask_installs_the_built_image_and_uninstalls_cleanly(self):
        commands, run = self.brew()
        with patch.object(rehearsal, "run", side_effect=run):
            result = rehearsal.install_cask(self.dmg, rehearsal.version(), self.root / "work")
        self.assertEqual(result, {"token": "painted-wolf-code@preview", "installed_version": rehearsal.version(), "uninstalled": True})
        verbs = [command[1] for command in commands if command[0] == "brew"]
        self.assertEqual(verbs, ["tap-new", "--repository", "install", "uninstall", "untap"])
        cask = (self.taps / "Casks/painted-wolf-code@preview.rb").read_text()
        self.assertIn(hashlib.sha256(self.dmg.read_bytes()).hexdigest(), cask)

    def test_an_uninstall_that_leaves_the_app_fails_and_still_untaps(self):
        commands, run = self.brew(uninstall_leaves=True)
        with patch.object(rehearsal, "run", side_effect=run), self.assertRaisesRegex(ValueError, "left the app"):
            rehearsal.install_cask(self.dmg, rehearsal.version(), self.root / "work")
        self.assertEqual(commands[-1][:2], ["brew", "untap"])

    def test_the_install_rehearsal_needs_a_disposable_runner_without_the_product(self):
        with patch.dict(os.environ, {"RUNNER_ENVIRONMENT": "self-hosted"}), self.assertRaisesRegex(ValueError, "disposable hosted runner"):
            rehearsal.require_disposable_runner()
        (self.home / ".config/paintedwolf").mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "without the product installed"):
            rehearsal.require_disposable_runner()

    def test_the_unsigned_bundle_updater_meets_the_limits_and_its_disposable_signature_is_verified(self):
        app = self.root / "bundle/macos/Painted Wolf Code.app"
        app.mkdir(parents=True)
        rehearsal.updater_archive(app.parent / "Painted Wolf Code.app.tar.gz", rehearsal.version())
        commands = []
        with patch.object(rehearsal, "run", side_effect=lambda arguments, **_: commands.append(arguments)), \
             patch.object(rehearsal, "install_cask", return_value={"uninstalled": True}) as cask:
            result = rehearsal.macos(self.root / "work", app, self.dmg, "cHVibGlj")
        self.assertEqual(result["updater_signature"], "verified")
        self.assertIn("verify_updater_signature", commands[0])
        self.assertEqual((self.root / "work/public.key").read_text(), "cHVibGlj\n")
        cask.assert_called_once()


class CommandTests(unittest.TestCase):
    def test_a_receipt_records_the_checks_or_the_failure(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        with patch.object(rehearsal, "assembly", return_value={"workflows": {"production": "passed"}}), \
             patch.object(rehearsal, "Key"), patch.object(rehearsal.sys, "argv", ["rehearsal", "assembly", "--receipt", str(root / "passed.json")]):
            rehearsal.main()
        self.assertEqual(json.loads((root / "passed.json").read_text())["result"], "passed")
        with patch.object(rehearsal, "assembly", side_effect=ValueError("seeded fault admitted")), \
             patch.object(rehearsal, "Key"), patch.object(rehearsal.sys, "argv", ["rehearsal", "assembly", "--receipt", str(root / "failed.json")]):
            with self.assertRaises(SystemExit):
                rehearsal.main()
        failed = json.loads((root / "failed.json").read_text())
        self.assertEqual((failed["result"], failed["published"]), ("failed", False))
        with patch.object(rehearsal.sys, "argv", ["rehearsal", "assembly", "--receipt", str(root / "failed.json")]), \
             self.assertRaises(SystemExit):
            rehearsal.main()

    def test_the_macos_stage_passes_its_bundle_and_key_through(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        arguments = ["rehearsal", "macos", "--app", str(root / "Product.app"), "--dmg", str(root / "Product.dmg"),
                     "--public-key", "cHVibGlj", "--receipt", str(root / "macos.json")]
        with patch.object(rehearsal, "macos", return_value={"cask": {"uninstalled": True}}) as stage, \
             patch.object(rehearsal.sys, "argv", arguments):
            rehearsal.main()
        self.assertEqual(stage.call_args.args[1:], ((root / "Product.app").resolve(), (root / "Product.dmg").resolve(), "cHVibGlj"))
        self.assertEqual(json.loads((root / "macos.json").read_text())["stage"], "macos")
