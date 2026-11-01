"""Install, upgrade, and remove opt-in setup packages in private package databases."""

import subprocess

from .openpgp import selection
from .repository_installation import restore_owner
from .repository_operations import identity
from .repository_setup import PACKAGE, build, package_identity
from .repository_setup_admission import admit_setup
from .signing import digest, verify


def rehearse(work, manifest, directory, catalog, origin):
    family = manifest["family"]
    output = work / (family + "-setup-package")
    private = catalog.parent / "rehearsal-secret.asc"
    for url, destination in [("https://example.invalid/\nenabled=0", output),
                             ("http://example.invalid", output), (origin, directory / "setup")]:
        try:
            build(manifest, directory, catalog, destination, url, private, True)
        except ValueError:
            pass
        else:
            raise ValueError("setup accepted an unsafe origin or mutable generation destination")
        if destination.exists():
            raise ValueError("refused setup allocated an output directory")
    receipt = build(manifest, directory, catalog, output, origin, private, True)
    artifact = output / receipt["package"]["file"]
    verify("detached", output / "SHA256SUMS", output / "SHA256SUMS.asc", catalog, True)
    authority = {name: receipt[name] for name in ["family", "source_commit", "version", "channel",
                 "repository_generation", "inventory_sha256", "signing_generation", "key_fingerprint"]}
    authority["base_url"] = origin
    verified = admit_setup(artifact, output / "SHA256SUMS", output / "SHA256SUMS.asc", catalog,
                           **authority, rehearsal=True)
    if verified["package"] != receipt["package"] or verified["installed_files"] != receipt["installed_files"]:
        raise ValueError("setup admission failed to reconstruct its native control surface")
    for changed in [{"base_url": "https://wrong-origin.invalid"}, {"channel": "stable"},
                    {"signing_generation": receipt["signing_generation"] + 1},
                    {"source_commit": "arbitrary"}, {"repository_generation": "arbitrary"},
                    {"inventory_sha256": "arbitrary"}, {"key_fingerprint": "0" * 40}]:
        try:
            admit_setup(artifact, output / "SHA256SUMS", output / "SHA256SUMS.asc", catalog,
                        **{**authority, **changed}, rehearsal=True)
        except ValueError:
            pass
        else:
            raise ValueError("setup admission accepted a mismatched independent authority")
    linked = work / artifact.name
    linked.symlink_to(artifact)
    try:
        admit_setup(linked, output / "SHA256SUMS", output / "SHA256SUMS.asc", catalog,
                    **authority, rehearsal=True)
    except (OSError, ValueError):
        pass
    else:
        raise ValueError("setup admission accepted a symlink package")
    linked.unlink()
    root = work / (family + "-setup-root")
    root.mkdir()
    data = root / "home/fixture/.local/share/paintedwolf/preserve"
    data.parent.mkdir(parents=True)
    data.write_text("mutable user data\n")
    if family == "deb":
        state = root / "var/lib/dpkg"
        state.mkdir(parents=True)
        (state / "status").touch()
        install = ["dpkg", "--root=" + str(root), "--log=" + str(root / "dpkg.log"), "--install", str(artifact)]
        remove = ["dpkg", "--root=" + str(root), "--log=" + str(root / "dpkg.log"), "--purge", PACKAGE]
        owned = ["dpkg-query", "--admindir=" + str(state), "--listfiles", PACKAGE]
    else:
        subprocess.run(["sudo", "-n", "rpm", "--root", str(root), "--initdb"], check=True)
        public = output / "package-key.asc"
        subprocess.run(["sudo", "-n", "rpm", "--root", str(root), "--import", str(public)], check=True)
        subprocess.run(["sudo", "-n", "rpmkeys", "--root", str(root), "--checksig", str(artifact)], check=True)
        install = ["rpm", "--root", str(root), "--nodeps", "--install", str(artifact)]
        remove = ["rpm", "--root", str(root), "--erase", PACKAGE]
        owned = ["sudo", "-n", "rpm", "--root", str(root), "--query", "--list", PACKAGE]
    try:
        subprocess.run(["sudo", "-n", *install], check=True)
        ownership = subprocess.check_output(owned, text=True).splitlines()
        for row in receipt["installed_files"]:
            if "/" + row["path"] not in ownership or digest(root / row["path"]) != row["sha256"]:
                raise ValueError("setup package did not install its exact owned configuration and key")
        if (root / "opt/painted-wolf-code").exists():
            raise ValueError("repository setup unexpectedly installed the application")
        subprocess.run(["sudo", "-n", *remove], check=True)
        if any((root / row["path"]).exists() for row in receipt["installed_files"]):
            raise ValueError("setup removal left its unmodified repository configuration or key")
        if data.read_text() != "mutable user data\n":
            raise ValueError("setup package removal changed mutable user data")
    finally:
        restore_owner(root)
    try:
        build(manifest, directory, catalog, work / (family + "-production-setup"), origin, private)
    except ValueError:
        pass
    else:
        raise ValueError("setup package admitted rehearsal trust in production")
    if (work / (family + "-production-setup")).exists():
        raise ValueError("refused setup allocated an output directory")


def rehearse_upgrade(work, previous, successor, previous_catalog, successor_catalog, origin):
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
