"""Exercise native signed repository formats with explicitly disposable fixtures."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import uuid

from .native_repositories import assemble
from .openpgp import ephemeral
from .repository_validation import admit
from .repository_publication import publish
from .repository_origin import serve
from .repository_r2 import publish_r2
from .r2_objects import rehearsal_environment
from .repository_installation import apt as install_apt, dnf as install_dnf
from .signing import digest

PACKAGE = "paintedwolf-repository-fixture"


def fixtures(work: Path, version: str) -> dict:
    architectures = {"x86_64": "amd64", "aarch64": "arm64"}
    architecture = platform.machine()
    if architecture not in architectures:
        raise ValueError("native repository rehearsal requires Linux amd64 or arm64")
    native = architectures[architecture]
    debroot = work / "deb-root"
    (debroot / "DEBIAN").mkdir(parents=True)
    (debroot / "DEBIAN/control").write_text(
        f"Package: {PACKAGE}\nVersion: {version}-1\nArchitecture: {native}\nMaintainer: Painted Wolf Code\n"
        "Description: Disposable repository fixture, not an application candidate\n")
    payload = debroot / "usr/share" / PACKAGE
    payload.mkdir(parents=True)
    (payload / "value").write_text(version + "\n")
    deb = work / f"{PACKAGE}_{version}-1_{native}.deb"
    subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(debroot), str(deb)], check=True)
    top = work / "rpmbuild"
    for name in ["BUILD", "RPMS", "SOURCES", "SPECS", "SRPMS", "BUILDROOT"]:
        (top / name).mkdir(parents=True)
    spec = top / "SPECS/fixture.spec"
    spec.write_text(f"""Name: {PACKAGE}
Version: {version}
Release: 1
Summary: Disposable repository fixture
License: MIT
BuildArch: {architecture}
%description
Disposable repository fixture, not a Painted Wolf Code application candidate.
%install
mkdir -p %{{buildroot}}/usr/share/{PACKAGE}
printf '{version}\\n' > %{{buildroot}}/usr/share/{PACKAGE}/value
%files
/usr/share/{PACKAGE}/value
""")
    subprocess.run(["rpmbuild", "--define", f"_topdir {top}", "-bb", str(spec)], check=True)
    packages = list((top / "RPMS").rglob("*.rpm"))
    if len(packages) != 1:
        raise ValueError("native RPM fixture construction is ambiguous")
    return {"deb": deb, "rpm": packages[0], "architecture": native}


def rejected(action):
    try:
        action()
    except (ValueError, OSError, KeyError):
        return
    raise ValueError("invalid repository generation was admitted")


def rehearse(work: Path, origin: str, certificate: Path, r2_origin: str | None = None):
    os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    catalog = ephemeral(work / "key")
    wrong = ephemeral(work / "wrong-key")
    private = catalog.parent / "rehearsal-secret.asc"
    generations = []
    active = {}
    retained = {}
    remote_active = {}
    storage_prefix = "release-system-tests/native-repositories-" + uuid.uuid4().hex
    for version in ["0.0.1", "0.0.2"]:
        fixture_work = work / version
        fixture_work.mkdir()
        packages = fixtures(fixture_work, version)
        for family in ["deb", "rpm"]:
            source = packages[family]
            row = {"file": source.name, "sha256": digest(source), "architecture": packages["architecture"],
                   "native_version": version + "-1"}
            manifest = {"format_version": 1, "source_commit": "f" * 40, "version": version, "packages": [row]}
            destination = work / (family + "-" + version)
            receipt = assemble(family, manifest, source.parent, destination, "preview", catalog, private, True, PACKAGE)
            admit(receipt, destination, catalog, True)
            rejected(lambda: admit(receipt, destination, wrong, True))
            rejected(lambda: admit(receipt, destination, catalog))
            package = destination / receipt["packages"][0]["file"]
            original = package.read_bytes()
            package.write_bytes(original + b"changed")
            rejected(lambda: admit(receipt, destination, catalog, True))
            package.write_bytes(original)
            missing = package.with_name(".withheld-" + package.name)
            package.rename(missing)
            rejected(lambda: admit(receipt, destination, catalog, True))
            missing.rename(package)
            admit(receipt, destination, catalog, True)
            store = work / "origin"
            previous = active.get(family)
            record = publish(receipt, destination, store, origin, catalog,
                             previous, True)
            rejected(lambda: publish(receipt, destination, store, origin,
                                     catalog, "0" * 64, True))
            if family in retained:
                old_receipt, old_destination = retained[family]
                restored = publish(old_receipt, old_destination, store, "https://repository.example.invalid",
                                   catalog, record["generation"], True, True)
                record = publish(receipt, destination, store, origin, catalog,
                                 restored["generation"], True)
            else:
                retained[family] = receipt, destination
            active[family] = record["generation"]
            if r2_origin is not None:
                remote = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin,
                                    remote_active.get(family), True)
                if family in remote_active:
                    old_receipt, old_destination = retained[family]
                    restored_remote = publish_r2(old_receipt, old_destination, catalog, storage_prefix,
                                                 r2_origin, remote["generation"], True, True)
                    remote = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin,
                                        restored_remote["generation"], True)
                remote_active[family] = remote["generation"]
            if family == "deb":
                install_apt(work / "apt-root", origin + "/channels/deb/preview", certificate,
                            catalog, PACKAGE, version)
            else:
                install_dnf(work / "dnf-root", origin + "/channels/rpm/preview/mirrorlist", certificate,
                            catalog, PACKAGE, version)
            generations.append({"version": version, "family": family, "objects": len(receipt["objects"])})
    return {"format_version": 1, "rehearsal": True, "application_qualified": False,
            "published": False, "isolated_r2_published": r2_origin is not None,
            "storage_prefix": storage_prefix if r2_origin is not None else None, "generations": generations,
            "cases": ["native-package-identity", "signed-metadata", "content-addressed-indexes",
                      "old-and-new-retained", "wrong-key", "production-refuses-fixture", "changed-package",
                      "missing-object", "restored-generation", "immutable-publication",
                      "stale-activation-refused", "retained-generation-recovery",
                      "apt-signed-repository-installed-upgrade", "dnf-signed-mirrorlist-installed-upgrade",
                      "explicit-fixture-TLS-trust"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--r2", action="store_true")
    args = parser.parse_args()
    required = ["gpg", "gpgv", "gpgconf", "dpkg-deb", "apt-ftparchive", "rpm", "rpmkeys", "rpmsign", "rpmbuild", "createrepo_c", "openssl", "sudo", "apt-get", "dnf", "dpkg-query"]
    for executable in required:
        if shutil.which(executable) is None:
            parser.error(f"{executable} is required")
    try:
        r2_origin = rehearsal_environment() if args.r2 else None
        with tempfile.TemporaryDirectory(prefix="paintedwolf-native-repository-rehearsal-") as directory:
            with serve(Path(directory)) as (origin, certificate):
                result = rehearse(Path(directory), origin, certificate, r2_origin)
        subprocess.run(["python3", "-m", "unittest", "discover", "-s", "scripts", "-p",
                        "test_native_repository_publication_execution.py"],
                       cwd=Path(__file__).resolve().parents[2], check=True)
        result["cases"].append("r2-publication-contracts")
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_text(json.dumps(result, indent=2) + "\n")
        print(f"Native repository rehearsal passed: {args.receipt}")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native repository rehearsal failed: {exc}\n")


if __name__ == "__main__":
    main()
