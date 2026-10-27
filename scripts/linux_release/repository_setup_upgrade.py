"""Qualify same-application-version setup trust upgrades in private native package roots."""

import subprocess

from .openpgp import selection
from .repository_installation import restore_owner
from .repository_operations import identity
from .repository_setup import build
from .repository_setup_admission import admit_setup
from .repository_setup_identity import PACKAGE, package_identity
from .signing import digest


def rehearse(work, previous, successor, previous_catalog, successor_catalog, origin):
    old_manifest, old_directory = previous
    new_manifest, new_directory = successor
    family = new_manifest["family"]
    if old_manifest["version"] != new_manifest["version"]:
        raise ValueError("setup trust upgrade fixture must preserve the application version")
    root = work / (family + "-setup-upgrade-root")
    root.mkdir()
    sentinel = root / "home/fixture/.local/share/paintedwolf/preserve"
    sentinel.parent.mkdir(parents=True)
    sentinel.write_text("mutable user data\n")
    if family == "deb":
        state = root / "var/lib/dpkg"
        state.mkdir(parents=True)
        (state / "status").touch()
    else:
        subprocess.run(["sudo", "-n", "rpm", "--root", str(root), "--initdb"], check=True)
    try:
        for index, (manifest, directory, catalog) in enumerate([
                (old_manifest, old_directory, previous_catalog), (new_manifest, new_directory, successor_catalog)]):
            signer, public = selection(catalog, True)
            native = package_identity(manifest["version"], family, manifest["channel"], signer["generation"])
            output = work / f"{family}-setup-upgrade-{index}"
            build(manifest, directory, catalog, output, origin, catalog.parent / "rehearsal-secret.asc", True)
            package = output / native["file"]
            verified = admit_setup(package, output / "SHA256SUMS", output / "SHA256SUMS.asc", catalog,
                family=family, source_commit=manifest["source_commit"], version=manifest["version"],
                channel=manifest["channel"], repository_generation=identity(manifest),
                inventory_sha256=manifest["inventory_sha256"], base_url=origin,
                signing_generation=signer["generation"], key_fingerprint=signer["fingerprint"], rehearsal=True)
            if family == "deb":
                install = ["dpkg", "--root=" + str(root), "--log=" + str(root / "dpkg.log"), "--install", str(package)]
                query = ["dpkg-query", "--admindir=" + str(state), "--showformat=${Version}", "--show", PACKAGE]
                expected_version = native["version"]
            else:
                subprocess.run(["sudo", "-n", "rpm", "--root", str(root), "--import", str(public)], check=True)
                install = ["rpm", "--root", str(root), "--nodeps", "--install" if index == 0 else "--upgrade", str(package)]
                query = ["sudo", "-n", "rpm", "--root", str(root), "--qf", "%{VERSION}-%{RELEASE}", "--query", PACKAGE]
                expected_version = native["version"] + "-" + native["release"]
            subprocess.run(["sudo", "-n", *install], check=True)
            if subprocess.check_output(query, text=True) != expected_version:
                raise ValueError("setup trust upgrade did not advance its installed native identity")
            if any(digest(root / row["path"]) != row["sha256"] for row in verified["installed_files"]):
                raise ValueError("setup trust upgrade did not replace its exact key and configuration")
        remove = (["dpkg", "--root=" + str(root), "--log=" + str(root / "dpkg.log"), "--purge", PACKAGE]
                  if family == "deb" else ["rpm", "--root", str(root), "--erase", PACKAGE])
        subprocess.run(["sudo", "-n", *remove], check=True)
        if (any((root / row["path"]).exists() for row in verified["installed_files"])
                or sentinel.read_text() != "mutable user data\n" or (root / "opt/painted-wolf-code").exists()):
            raise ValueError("setup trust upgrade/removal changed application or user-data ownership")
    finally:
        restore_owner(root)
