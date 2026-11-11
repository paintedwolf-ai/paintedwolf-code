"""Bind nonpublishing native candidate coverage and retain disposable candidate repositories."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
for entry in (ROOT, ROOT / "scripts"):
    if str(entry) not in sys.path:
        sys.path.insert(0, str(entry))

from release_semver import linux_package_version, parse, parse_release_build

ARCHITECTURES = {"amd64": "amd64", "arm64": "arm64", "x86_64": "amd64", "aarch64": "arm64"}


def inventory(directory, platform, architecture, version, source_commit):
    parse(version)
    if platform != "linux-" + architecture:
        raise ValueError("candidate platform differs from its native architecture")
    if architecture not in {"x86_64", "aarch64"} or not re.fullmatch(r"[a-f0-9]{40}", source_commit):
        raise ValueError("candidate inventory requires its exact source and native architecture")
    stem = f"painted-wolf-code_v{version}_{platform}"
    packages = []
    for family, extension in [("deb", "deb"), ("rpm", "rpm"), ("pacman", "pkg.tar.zst")]:
        path = directory / (stem + "." + extension)
        if path.is_symlink() or not path.is_file():
            raise ValueError("candidate inventory requires every native package family")
        digest = hashlib.sha256()
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1 << 20), b""):
                digest.update(block)
        packages.append({"family": family, "file": path.name, "architecture": architecture,
                         "sha256": digest.hexdigest(), "bytes": path.stat().st_size})
    return {"format_version": 1, "source_commit": source_commit, "version": version,
            "platform": platform, "architecture": architecture, "packages": packages,
            "qualification_status": "unverified", "published": False}


def package_row(path: Path, family: str, package: str, version: str):
    from scripts.linux_release.signing import digest
    if path.is_symlink() or not path.is_file():
        raise ValueError("candidate package must be a regular file")
    if family == "deb":
        command = ["dpkg-deb", "--show", "--showformat=${Package}\\t${Version}\\t${Architecture}", str(path)]
    else:
        command = ["rpm", "-qp", "--nomanifest", "--qf", "%{NAME}\\t%{VERSION}-%{RELEASE}\\t%{ARCH}", str(path)]
    actual = subprocess.check_output(command, text=True).strip().split("\t")
    expected = linux_package_version(parse(version)) + ("-1" if family == "rpm" else "")
    if len(actual) != 3 or actual[:2] != [package, expected] or actual[2] not in ARCHITECTURES:
        raise ValueError("candidate native identity differs from the independently selected product version")
    return {"file": path.name, "sha256": digest(path), "architecture": ARCHITECTURES[actual[2]],
            "native_version": actual[1]}


def retain(candidates: Path, destination: Path, source_commit: str, version: str, channel: str,
           release_build: int, architectures=(), package="painted-wolf-code"):
    from scripts.linux_release.native_repositories import assemble
    from scripts.linux_release.openpgp import ephemeral, selection
    from scripts.linux_release.repository_objects import encoded, immutable
    from scripts.linux_release.repository_operations import archive_trust, identity
    from scripts.linux_release.repository_validation import admit
    if not re.fullmatch(r"[0-9a-f]{40}", source_commit) or channel not in {"stable", "preview"}:
        raise ValueError("select an exact candidate source and release channel")
    parse(version)
    parse_release_build(str(release_build))
    candidates = candidates.resolve(strict=True)
    destination = destination.absolute()
    if not candidates.is_dir() or destination.resolve().is_relative_to(candidates):
        raise ValueError("retain candidate output outside its read-only input directory")
    if destination.exists() or destination.is_symlink():
        raise ValueError("candidate retention requires a new destination")
    rows, paths = {}, {}
    for family in ["deb", "rpm"]:
        selected = sorted(candidates.rglob("*." + family))
        if not selected or len(selected) > 2:
            raise ValueError("select one candidate per supported architecture and native family")
        rows[family] = [package_row(path, family, package, version) for path in selected]
        observed = {row["architecture"] for row in rows[family]}
        if len(observed) != len(selected) or architectures and observed != set(architectures):
            raise ValueError("candidate architectures differ from the explicitly selected coverage")
        paths[family] = selected
    if {row["architecture"] for row in rows["deb"]} != {row["architecture"] for row in rows["rpm"]}:
        raise ValueError("candidate native families cover different architectures")
    destination.mkdir(parents=True, exist_ok=False)
    os.environ.pop("LINUX_PACKAGE_SIGNING_KEY", None)
    result = {"format_version": 1, "rehearsal": True, "application_qualified": False, "published": False,
              "isolated_r2_published": False,
              "source_commit": source_commit, "version": version, "channel": channel,
              "release_build": release_build, "trust_catalog": "trust/key-catalog.json",
              "architectures": sorted({row["architecture"] for row in rows["deb"]}),
              "cases": ["candidate-native-identity", "retained-post-signing-bytes",
                        "retained-public-only-trust", "test-trust-excluded-production"],
              "repositories": [], "packages": []}
    with tempfile.TemporaryDirectory(prefix="paintedwolf-candidate-signing-") as temporary:
        work = Path(temporary)
        catalog = ephemeral(work / "private-trust")
        signer, public = selection(catalog, True)
        archive_trust(destination / "trust", signer, public, True)
        result.update(signing_generation=signer["generation"], key_fingerprint=signer["fingerprint"])
        for family in ["deb", "rpm"]:
            staged = work / family
            staged.mkdir()
            for path in paths[family]:
                shutil.copyfile(path, staged / path.name)
            manifest = {"format_version": 1, "source_commit": source_commit, "version": version,
                        "packages": rows[family]}
            generation = destination / family
            receipt = assemble(family, manifest, staged, generation, channel, catalog,
                               catalog.parent / "rehearsal-secret.asc", True, package)
            immutable(generation / "generation.json", encoded(receipt))
            admit(receipt, generation, destination / "trust/key-catalog.json", True)
            result["repositories"].append({"family": family, "path": family, "generation": identity(receipt),
                                           "inventory_sha256": receipt["inventory_sha256"]})
            inputs = {row["file"]: row for row in rows[family]}
            for row in receipt["packages"]:
                selected = generation / row["file"]
                result["packages"].append({**row, "file": selected.name,
                                           "path": selected.relative_to(destination).as_posix(), "family": family,
                                           "input_sha256": inputs[selected.name]["sha256"], "bytes": selected.stat().st_size})
    for row in result["repositories"]:
        directory = destination / row["path"]
        manifest = json.loads((directory / "generation.json").read_text())
        admit(manifest, directory, destination / result["trust_catalog"], True)
        try:
            admit(manifest, directory, destination / result["trust_catalog"])
        except ValueError:
            pass
        else:
            raise ValueError("retained candidate test trust entered production admission")
    immutable(destination / "candidate-repositories.json", encoded(result))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    for name in ["platform", "architecture", "version", "source-commit"]:
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    result = inventory(args.directory, args.platform, args.architecture, args.version, args.source_commit)
    with (args.directory / "candidate-packages.json").open("x") as output:
        output.write(json.dumps(result, sort_keys=True, indent=2) + "\n")


if __name__ == "__main__":
    main()
