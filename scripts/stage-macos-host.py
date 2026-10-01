#!/usr/bin/env python3
"""Package the credential-owning host with its own signing identity and profile."""

import argparse
import datetime
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import tempfile

from release_semver import parse, parse_release_build

BUNDLE_ID = "dev.paintedwolf.code.engine"
BUNDLE_NAME = "Painted Wolf Code engine.app"
ROOT = Path(__file__).resolve().parent.parent


def profile_entitlements(profile, now=None):
    """Constrain distribution authority to this host's private Keychain group."""
    now = now or datetime.datetime.now(datetime.timezone.utc)
    expiry = profile.get("ExpirationDate")
    if not isinstance(expiry, datetime.datetime) or expiry.replace(tzinfo=datetime.timezone.utc) <= now:
        raise ValueError("engine provisioning profile is expired or has no expiration")
    if "OSX" not in profile.get("Platform", []):
        raise ValueError("engine provisioning profile must support macOS")
    if profile.get("ProvisionsAllDevices") is not True or profile.get("ProvisionedDevices"):
        raise ValueError("engine requires a Developer ID distribution profile")
    entitlements = profile.get("Entitlements", {})
    if entitlements.get("get-task-allow") or entitlements.get("com.apple.security.get-task-allow"):
        raise ValueError("engine distribution profile permits debugging")
    prefixes = profile.get("ApplicationIdentifierPrefix", [])
    teams = profile.get("TeamIdentifier", [])
    if len(prefixes) != 1 or len(teams) != 1:
        raise ValueError("engine profile must identify one application prefix and team")
    application = prefixes[0] + "." + BUNDLE_ID
    if entitlements.get("com.apple.application-identifier") != application:
        raise ValueError("engine profile must have the explicit application identifier " + application)
    if entitlements.get("com.apple.developer.team-identifier") != teams[0]:
        raise ValueError("engine profile team does not match its entitlements")
    groups = entitlements.get("keychain-access-groups", [])
    if application not in groups and prefixes[0] + ".*" not in groups:
        raise ValueError("engine profile does not authorize its private Keychain group")
    if not profile.get("DeveloperCertificates"):
        raise ValueError("engine profile has no signing certificates")
    return {
        "com.apple.application-identifier": application,
        "com.apple.developer.team-identifier": teams[0],
        "keychain-access-groups": [application],
    }


def stage(binary, output, version, release):
    identity = os.environ.get("APPLE_SIGNING_IDENTITY", "")
    profile_path = os.environ.get("APPLE_ENGINE_PROVISIONING_PROFILE", "")
    profile = None
    entitlements = None
    if release:
        if not identity or identity == "-" or not profile_path:
            raise ValueError("macOS release requires APPLE_SIGNING_IDENTITY and APPLE_ENGINE_PROVISIONING_PROFILE; use den:app -- --debug for local development")
        decoded = subprocess.check_output(["/usr/bin/security", "cms", "-D", "-i", profile_path], timeout=30)
        profile = plistlib.loads(decoded)
        entitlements = profile_entitlements(profile)
    # Rebuilding the generated helper removes stale provisioning profiles.
    bundle = output / BUNDLE_NAME
    if bundle.exists():
        shutil.rmtree(bundle)
    contents = bundle / "Contents"
    executable = contents / "MacOS" / "pw"
    executable.parent.mkdir(parents=True)
    shutil.copy2(binary, executable)
    executable.chmod(0o755)
    info = {
        "CFBundleIdentifier": BUNDLE_ID,
        "CFBundleName": "Painted Wolf Code engine",
        "CFBundleExecutable": "pw",
        "CFBundlePackageType": "APPL",
        "CFBundleInfoDictionaryVersion": "6.0",
        "CFBundleVersion": str(parse_release_build((ROOT / "RELEASE_BUILD").read_text().strip())),
        "CFBundleShortVersionString": parse(version).core,
        "LSMinimumSystemVersion": (ROOT / "lycaon/internal/platformfloor/macos_floor.txt").read_text().strip(),
        "LSBackgroundOnly": True,
    }
    (contents / "Info.plist").write_bytes(plistlib.dumps(info))
    if not release:
        subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", str(bundle)], check=True)
        return
    shutil.copyfile(profile_path, contents / "embedded.provisionprofile")
    with tempfile.TemporaryDirectory(prefix="engine-signing-") as temporary:
        temporary = Path(temporary)
        entitlement_path = temporary / "entitlements.plist"
        entitlement_path.write_bytes(plistlib.dumps(entitlements))
        subprocess.run([
            "/usr/bin/codesign", "--force", "--timestamp", "--options", "runtime",
            "--entitlements", str(entitlement_path), "--sign", identity, str(bundle),
        ], check=True)
        certificate_prefix = temporary / "certificate"
        subprocess.run([
            "/usr/bin/codesign", "--display", "--extract-certificates", str(certificate_prefix), str(bundle),
        ], check=True)
        certificate = Path(str(certificate_prefix) + "0").read_bytes()
        if certificate not in profile["DeveloperCertificates"]:
            raise ValueError("engine signer is not authorized by its provisioning profile")
    subprocess.run(["/usr/bin/codesign", "--verify", "--strict", str(bundle)], check=True)
    # The native probe verifies OS authorization for the signed helper.
    subprocess.run([str(executable.resolve()), "credentials", "verify-protection"], check=True, timeout=30)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--release", action="store_true")
    args = parser.parse_args()
    try:
        stage(args.binary, args.output, args.version, args.release)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, f"error: {error}\n")


if __name__ == "__main__":
    main()
