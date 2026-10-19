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
from .candidate_repositories import retain as retain_candidates, rehearse as rehearse_candidates
from .openpgp import ephemeral
from .repository_setup_rehearsal import rehearse as rehearse_setup
from .repository_setup_upgrade import rehearse as rehearse_setup_upgrade
from .repository_validation import admit
from .repository_publication import publish
from .repository_origin import serve
from .repository_r2 import publish_r2
from .r2_objects import rehearsal_environment, warm_absence, tamper_fixture_object, delete_fixture_object
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
        if version == "0.0.1":
            first_inputs = packages
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
                restored = publish(old_receipt, old_destination, store, origin,
                                   catalog, record["generation"], True, "recover")
                record = publish(receipt, destination, store, origin, catalog,
                                 restored["generation"], True)
            else:
                retained[family] = receipt, destination
            active[family] = record["generation"]
            if r2_origin is not None:
                if family not in remote_active:
                    authority = f"channels/{family}/preview/" + ("dists/preview/InRelease" if family == "deb" else "mirrorlist")
                    warm_absence(r2_origin + "/" + storage_prefix + "/" + authority)
                staged = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin,
                                    remote_active.get(family), True, phase="stage")
                if family not in remote_active:
                    package_row = receipt["packages"][0]
                    key = storage_prefix + "/generations/" + staged["generation"] + "/" + package_row["file"]
                    tamper_fixture_object(key, destination / package_row["file"], work / (family + "-tampered"))
                    try:
                        publish_r2(receipt, destination, catalog, storage_prefix, r2_origin, None, True)
                    except subprocess.CalledProcessError:
                        pass
                    else:
                        raise ValueError("R2 admitted a tampered immutable package object")
                    finally:
                        delete_fixture_object(key)
                remote = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin,
                                    remote_active.get(family), True)
                if family in remote_active:
                    old_receipt, old_destination = retained[family]
                    restored_remote = publish_r2(old_receipt, old_destination, catalog, storage_prefix,
                                                 r2_origin, remote["generation"], True, "recover")
                    remote = publish_r2(receipt, destination, catalog, storage_prefix, r2_origin,
                                        restored_remote["generation"], True)
                remote_active[family] = remote["generation"]
            if family == "deb":
                # Force actual apt to consume retained by-hash indexes, with no mutable fallback.
                for index in (work / "origin/channels/deb/preview/dists/preview/main").glob("binary-*/Packages*"):
                    index.unlink()
                install_apt(work / "apt-root", origin + "/channels/deb/preview", certificate,
                            catalog, PACKAGE, version)
            else:
                install_dnf(work / "dnf-root", origin + "/channels/rpm/preview/mirrorlist", certificate,
                            catalog, PACKAGE, version)
            if r2_origin is not None:
                installer = install_apt if family == "deb" else install_dnf
                uri = r2_origin + "/" + storage_prefix + ("/channels/deb/preview" if family == "deb" else "/channels/rpm/preview/mirrorlist")
                installer(work / (family + "-r2-root"), uri, None, catalog, PACKAGE, version)
            if version == "0.0.2":
                old_receipt, old_destination = retained[family]
                withdrawn = publish(old_receipt, old_destination, store, origin, catalog,
                                    active[family], True, "withdraw")
                active[family] = withdrawn["generation"]
                rejected(lambda: publish(receipt, destination, store, origin, catalog, active[family], True))
                if r2_origin is not None:
                    withdrawn_remote = publish_r2(old_receipt, old_destination, catalog, storage_prefix,
                                                  r2_origin, remote_active[family], True, "withdraw")
                    remote_active[family] = withdrawn_remote["generation"]
                    installer(work / (family + "-r2-root"), uri, None, catalog, PACKAGE, version)
                    rejected(lambda: publish_r2(receipt, destination, catalog, storage_prefix,
                                                r2_origin, remote_active[family], True))
            if version == "0.0.1":
                rehearse_setup(work, receipt, destination, catalog, origin)
            generations.append({"version": version, "family": family, "objects": len(receipt["objects"])})
    rotation = ephemeral(work / "rotation-key")
    trust = json.loads(rotation.read_text())
    trust["signing_generation"] = trust["generations"][0]["generation"] = 2
    rotation.write_text(json.dumps(trust))
    third = work / "0.0.3"
    third.mkdir()
    packages = fixtures(third, "0.0.3")
    for family in ["deb", "rpm"]:
        source = packages[family]
        manifest = {"format_version": 1, "source_commit": "f" * 40, "version": "0.0.3", "packages": [{
            "file": source.name, "sha256": digest(source), "architecture": packages["architecture"], "native_version": "0.0.3-1"}]}
        setup_previous = work / (family + "-setup-before-rotation")
        setup_manifest = assemble(family, manifest, source.parent, setup_previous, "preview", catalog, private, True, PACKAGE)
        destination = work / (family + "-rotated")
        receipt = assemble(family, manifest, source.parent, destination, "preview", rotation,
                           rotation.parent / "rehearsal-secret.asc", True, PACKAGE)
        rehearse_setup_upgrade(work, (setup_manifest, setup_previous), (receipt, destination), catalog, rotation, origin)
        rejected(lambda: publish(receipt, destination, work / "origin", origin, rotation, active[family], True))
        record = publish(receipt, destination, work / "origin", origin, rotation, active[family], True, "rotate")
        installer = install_apt if family == "deb" else install_dnf
        uri = origin + ("/channels/deb/preview" if family == "deb" else "/channels/rpm/preview/mirrorlist")
        installer(work / (family + "-rotated-root"), uri, certificate, rotation, PACKAGE, "0.0.3")
        try:
            installer(work / (family + "-wrong-key-root"), uri, certificate, catalog, PACKAGE, "0.0.3")
        except subprocess.CalledProcessError:
            pass
        else:
            raise ValueError("native package manager accepted an unavailable successor signing key")
        old_receipt, old_destination = retained[family]
        publish(old_receipt, old_destination, work / "origin", origin, catalog,
                record["generation"], True, "recover")
        installed = work / ("apt-root" if family == "deb" else "dnf-root")
        installer(installed, uri, certificate, catalog, PACKAGE, "0.0.2")
        if r2_origin is not None:
            remote = publish_r2(receipt, destination, rotation, storage_prefix, r2_origin,
                                remote_active[family], True, "rotate")
            publish_r2(old_receipt, old_destination, catalog, storage_prefix, r2_origin,
                       remote["generation"], True, "recover")
        generations.append({"version": "0.0.3", "family": family, "objects": len(receipt["objects"]), "signing_generation": 2})
    rehearse_candidates(work, first_inputs, PACKAGE)
    return {"format_version": 1, "rehearsal": True, "application_qualified": False,
            "architecture": platform.machine(), "published": False, "isolated_r2_published": r2_origin is not None,
            "storage_prefix": storage_prefix if r2_origin is not None else None, "generations": generations,
            "cases": ["native-package-identity", "signed-metadata", "content-addressed-indexes", "apt-by-hash-without-mutable-fallback",
                      "old-and-new-retained", "actual-candidate-retention", "post-signing-rpm-identity-retained",
                      "public-only-candidate-trust", "opt-in-setup-package-install-and-remove",
                      "setup-removal-preserves-user-data", "independent-setup-admission", "signed-hostile-setup-refused", "same-version-setup-trust-upgrade", "wrong-key", "production-refuses-fixture", "changed-package",
                      "missing-object", "restored-generation", "immutable-publication",
                      "stale-activation-refused", "retained-generation-recovery",
                      "apt-signed-repository-installed-upgrade", "dnf-signed-mirrorlist-installed-upgrade",
                      "explicit-fixture-TLS-trust", "explicit-signing-key-rotation",
                      "installed-wrong-successor-key-refused", "withdrawn-generation-reoffer-refused",
                      "withdrawal-and-recovery-preserve-installed-payload"]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--r2", action="store_true")
    parser.add_argument("--candidate-dir", type=Path)
    parser.add_argument("--retain-repositories", type=Path)
    parser.add_argument("--source-commit")
    parser.add_argument("--version")
    parser.add_argument("--channel", choices=["stable", "preview"])
    parser.add_argument("--release-build", type=int)
    parser.add_argument("--architecture", choices=["amd64", "arm64"], action="append", default=[])
    args = parser.parse_args()
    candidate_options = [args.retain_repositories, args.source_commit, args.version, args.channel, args.release_build]
    if args.candidate_dir:
        if args.r2 or any(value is None for value in candidate_options):
            parser.error("candidate mode requires retained output and independent source/version/channel/build, and excludes R2")
        if (args.receipt.resolve().is_relative_to(args.candidate_dir.resolve())
                or args.receipt.resolve().is_relative_to(args.retain_repositories.resolve())):
            parser.error("candidate receipt must remain outside selected inputs and retained repository output")
    elif any(value is not None for value in candidate_options) or args.architecture:
        parser.error("candidate selection options require --candidate-dir")
    required = ["git", "gpg", "gpgv", "gpgconf", "dpkg-deb", "apt-ftparchive", "rpm", "rpmkeys", "rpmsign", "rpmbuild", "createrepo_c", "openssl", "sudo", "apt-get", "dnf", "dpkg-query"]
    if args.candidate_dir:
        required = [name for name in required if name not in {"rpmbuild", "openssl", "sudo", "apt-get", "dnf", "dpkg-query"}]
    for executable in required:
        if shutil.which(executable) is None:
            parser.error(f"{executable} is required")
    try:
        if args.candidate_dir:
            result = retain_candidates(args.candidate_dir, args.retain_repositories, args.source_commit,
                                       args.version, args.channel, args.release_build, args.architecture)
        else:
            r2_origin = rehearsal_environment() if args.r2 else None
            with tempfile.TemporaryDirectory(prefix="paintedwolf-native-repository-rehearsal-") as directory:
                with serve(Path(directory)) as (origin, certificate):
                    result = rehearse(Path(directory), origin, certificate, r2_origin)
            for pattern in ["test_native_repository_publication_execution.py", "test_repository_operations_execution.py"]:
                subprocess.run(["python3", "-m", "unittest", "discover", "-s", "scripts", "-p", pattern],
                               cwd=Path(__file__).resolve().parents[2], check=True)
            result["cases"].append("r2-publication-contracts")
            if args.r2:
                result["cases"] += ["r2-negative-cache-recovery", "r2-staged-publication-resume",
                                    "r2-tampered-object-refused", "r2-missing-object-restored", "r2-package-manager-installation"]
        root = Path(__file__).resolve().parents[2]
        result["verification_source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
        result["verification_source_tree"] = subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], cwd=root, text=True).strip()
        result["verification_source_dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root))
        args.receipt.parent.mkdir(parents=True, exist_ok=True)
        args.receipt.write_text(json.dumps(result, indent=2) + "\n")
        print(f"Native repository rehearsal passed: {args.receipt}")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native repository rehearsal failed: {exc}\n")


if __name__ == "__main__":
    main()
