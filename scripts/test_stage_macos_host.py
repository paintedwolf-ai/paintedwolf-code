"""Tests for release profile authority, without touching a real Keychain."""
import copy
import datetime
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("host", Path(__file__).with_name("stage-macos-host.py"))
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)


class ProfileTests(unittest.TestCase):
    def setUp(self):
        self.application = "PREFIX1234." + host.BUNDLE_ID
        self.profile = {
            "ExpirationDate": datetime.datetime(2099, 1, 1),
            "Platform": ["OSX"],
            "ProvisionsAllDevices": True,
            "ApplicationIdentifierPrefix": ["PREFIX1234"],
            "TeamIdentifier": ["TEAM123456"],
            "DeveloperCertificates": [b"certificate"],
            "Entitlements": {
                "com.apple.application-identifier": self.application,
                "com.apple.developer.team-identifier": "TEAM123456",
                "keychain-access-groups": ["PREFIX1234.*", "unrelated.group"],
            },
        }

    def test_emits_only_private_group_and_identity(self):
        actual = host.profile_entitlements(self.profile)
        self.assertEqual(actual, {
            "com.apple.application-identifier": self.application,
            "com.apple.developer.team-identifier": "TEAM123456",
            "keychain-access-groups": [self.application],
        })
        self.profile["Entitlements"]["keychain-access-groups"] = [self.application]
        self.assertEqual(host.profile_entitlements(self.profile), actual)

    def test_rejects_invalid_distribution_authority(self):
        mutations = [
            {"ExpirationDate": datetime.datetime(2000, 1, 1)},
            {"ExpirationDate": None}, {"Platform": ["iOS"]},
            {"ProvisionsAllDevices": False}, {"ProvisionedDevices": ["development-device"]},
            {"ApplicationIdentifierPrefix": []}, {"TeamIdentifier": []},
            {"DeveloperCertificates": []},
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                with self.assertRaises(ValueError):
                    host.profile_entitlements(self.profile | mutation)
        mutations = [
            {"com.apple.application-identifier": "PREFIX1234.*"},
            {"com.apple.application-identifier": "PREFIX1234.dev.paintedwolf.code"},
            {"com.apple.developer.team-identifier": "OTHERTEAM"},
            {"keychain-access-groups": ["OTHERTEAM.*"]},
            {"get-task-allow": True}, {"com.apple.security.get-task-allow": True},
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                profile = copy.deepcopy(self.profile)
                profile["Entitlements"].update(mutation)
                with self.assertRaises(ValueError):
                    host.profile_entitlements(profile)

    def test_release_never_falls_back_to_adhoc(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            for environment in [{}, {"APPLE_SIGNING_IDENTITY": "-", "APPLE_ENGINE_PROVISIONING_PROFILE": "profile"}]:
                with self.subTest(environment=environment), patch.dict("os.environ", environment, clear=True):
                    with self.assertRaisesRegex(ValueError, "macOS release requires"):
                        host.stage(output / "missing", output, "1.0.0", True)
                    self.assertEqual(list(output.iterdir()), [])

    def test_signed_bundle_retains_profile_and_probes_only_authorized_signer(self):
        import plistlib
        for authorized in (True, False):
            with self.subTest(authorized=authorized), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                binary = root / "pw"
                binary.write_bytes(b"synthetic executable")
                profile = root / "profile"
                profile.write_bytes(b"synthetic signed profile")
                output = root / "output"
                bundle = output / host.BUNDLE_NAME
                commands = []
                def run(arguments, **kwargs):
                    commands.append(arguments)
                    if "--sign" in arguments:
                        self.assertEqual((bundle / "Contents/embedded.provisionprofile").read_bytes(), profile.read_bytes())
                        entitlement_path = Path(arguments[arguments.index("--entitlements") + 1])
                        self.assertEqual(plistlib.loads(entitlement_path.read_bytes()), host.profile_entitlements(self.profile))
                    extract = [a for a in arguments if a.startswith("--extract-certificates=")]
                    if extract:
                        prefix = extract[0].split("=", 1)[1]
                        Path(prefix + "0").write_bytes(b"certificate" if authorized else b"other certificate")
                environment = {"APPLE_SIGNING_IDENTITY": "synthetic signer", "APPLE_ENGINE_PROVISIONING_PROFILE": str(profile)}
                with patch.dict("os.environ", environment, clear=True), patch.object(host.subprocess, "check_output", return_value=plistlib.dumps(self.profile)), patch.object(host.subprocess, "run", side_effect=run):
                    if authorized:
                        host.stage(binary, output, "1.2.3-rc.1", True)
                    else:
                        with self.assertRaisesRegex(ValueError, "signer is not authorized"):
                            host.stage(binary, output, "1.2.3-rc.1", True)
                probes = [command for command in commands if "verify-protection" in command]
                self.assertEqual(len(probes), 1 if authorized else 0)
                info = plistlib.loads((bundle / "Contents/Info.plist").read_bytes())
                self.assertEqual(info["CFBundleShortVersionString"], "1.2.3")
                self.assertTrue(info["CFBundleVersion"].isdigit())
                self.assertEqual(info["CFBundleIdentifier"], host.BUNDLE_ID)

    def test_development_rebuild_removes_stale_profile(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "pw"
            binary.write_bytes(b"synthetic executable")
            output = root / "output"
            contents = output / host.BUNDLE_NAME / "Contents"
            contents.mkdir(parents=True)
            (contents / "embedded.provisionprofile").write_bytes(b"stale profile")
            with patch.object(host.subprocess, "run") as run:
                host.stage(binary, output, "1.2.3", False)
            self.assertFalse((contents / "embedded.provisionprofile").exists())
            run.assert_called_once_with(["/usr/bin/codesign", "--force", "--sign", "-", str(output / host.BUNDLE_NAME)], check=True)

    def test_macos_packaging_preserves_the_signed_helper(self):
        import json
        config = json.loads((host.ROOT / "lycaon-den/src-tauri/tauri.macos.conf.json").read_text())
        self.assertEqual(config["bundle"]["externalBin"], ["binaries/pw-logs", "binaries/pw-document-core", "binaries/bialy"])
        self.assertEqual(config["bundle"]["macOS"]["files"], {
            "Helpers/" + host.BUNDLE_NAME: "host-bundle/" + host.BUNDLE_NAME,
        })
