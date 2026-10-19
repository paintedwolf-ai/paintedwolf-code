"""Assemble signed apt/dnf generations without activating a public repository."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess

from .signing import CATALOG, digest, sign
from .openpgp import selection

SAFE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9+._~-]*\Z")
ARCHITECTURES = {"amd64": "x86_64", "arm64": "aarch64"}


def checked_input(row: dict, directory: Path, family: str, package: str) -> Path:
    name = row["file"]
    if not isinstance(name, str) or not SAFE_NAME.fullmatch(name) or not name.endswith("." + family):
        raise ValueError("repository input must be a sibling package file")
    path = directory / name
    if path.is_symlink() or not path.is_file() or digest(path) != row["sha256"]:
        raise ValueError("repository package differs from the selected immutable input")
    if row["architecture"] not in ARCHITECTURES:
        raise ValueError("repository package architecture is unsupported")
    if family == "deb":
        arguments = ["dpkg-deb", "--show", "--showformat=${Package}\\t${Version}\\t${Architecture}", str(path)]
        expected_arch = row["architecture"]
    else:
        arguments = ["rpm", "-qp", "--nomanifest", "--qf", "%{NAME}\\t%{VERSION}-%{RELEASE}\\t%{ARCH}", str(path)]
        expected_arch = ARCHITECTURES[row["architecture"]]
    actual = subprocess.check_output(arguments, text=True, timeout=60).strip().split("\t")
    if actual != [package, row["native_version"], expected_arch]:
        raise ValueError("native package identity disagrees with its selected input receipt")
    return path


def apt_indexes(destination: Path, channel: str, architectures: list[str], catalog: Path,
                private: Path | None, rehearsal: bool):
    distribution = destination / "dists" / channel
    for arch in architectures:
        binary = distribution / "main" / ("binary-" + arch)
        binary.mkdir(parents=True)
        data = subprocess.check_output(["apt-ftparchive", "-a", arch, "packages", "pool"], cwd=destination)
        (binary / "Packages").write_bytes(data)
        (binary / "Packages.gz").write_bytes(gzip.compress(data, mtime=0))
        for path in [binary / "Packages", binary / "Packages.gz"]:
            hashed = binary / "by-hash/SHA256" / digest(path)
            hashed.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, hashed)
    # InRelease is the one mutable signed object; detached pairs stay generation-local.
    release = distribution / "Release"
    data = subprocess.check_output(["apt-ftparchive", "-o", "APT::FTPArchive::Release::Acquire-By-Hash=yes",
                                   "-o", "APT::FTPArchive::Release::Origin=Painted Wolf Code",
                                   "-o", "APT::FTPArchive::Release::Suite=" + channel,
                                   "-o", "APT::FTPArchive::Release::Codename=" + channel,
                                   "release", str(distribution.relative_to(destination))], cwd=destination)
    release.write_bytes(data)
    if b"Acquire-By-Hash: yes\n" not in data:
        raise ValueError("apt Release must advertise content-addressed indexes")
    sign("release", release, distribution / "InRelease", catalog, private, rehearsal)
    sign("detached", release, distribution / "Release.gpg", catalog, private, rehearsal)


def assemble(family: str, manifest: dict, inputs: Path, destination: Path, channel: str,
             catalog: Path = CATALOG, private: Path | None = None, rehearsal=False,
             package="painted-wolf-code") -> dict:
    if private is None:
        raise ValueError("batch repository signing requires an owner-only private key file")
    if family not in {"deb", "rpm"} or channel not in {"stable", "preview"}:
        raise ValueError("select a supported repository family and release channel")
    if (manifest.get("format_version") != 1 or not re.fullmatch(r"[0-9a-f]{40}", manifest.get("source_commit", ""))
            or not SAFE_NAME.fullmatch(manifest.get("version", ""))):
        raise ValueError("repository generation requires an exact source and product version")
    rows = manifest.get("packages")
    if not isinstance(rows, list) or not rows or len({row["file"] for row in rows}) != len(rows):
        raise ValueError("repository input receipt requires distinct packages")
    signer, _ = selection(catalog, rehearsal)
    sources = [(row, checked_input(row, inputs, family, package)) for row in rows]
    destination.mkdir(parents=True, exist_ok=False)
    architectures = sorted({row["architecture"] for row in rows})
    final = []
    for row, source in sources:
        stage = destination / ".signing" / source.name
        stage.parent.mkdir(exist_ok=True)
        if family == "rpm":
            sign("rpm", source, stage, catalog, private, rehearsal)
        else:
            shutil.copyfile(source, stage)
        target = destination / "pool" / digest(stage) / source.name
        target.parent.mkdir(parents=True)
        stage.replace(target)
        final.append({**row, "input_sha256": row["sha256"], "sha256": digest(target),
                      "file": target.relative_to(destination).as_posix()})
    (destination / ".signing").rmdir()
    if family == "deb":
        apt_indexes(destination, channel, architectures, catalog, private, rehearsal)
    else:
        subprocess.run(["createrepo_c", "--checksum", "sha256", "--unique-md-filenames", str(destination)], check=True)
        metadata = destination / "repodata/repomd.xml"
        sign("detached", metadata, metadata.with_suffix(".xml.asc"), catalog, private, rehearsal)
    result = {"format_version": 1, "family": family,
            "signing_generation": signer["generation"], "key_fingerprint": signer["fingerprint"], "channel": channel, "rehearsal": rehearsal,
            "source_commit": manifest["source_commit"], "version": manifest["version"], "packages": final,
            "activation": "not-published", "publication_order": "content-before-signed-metadata",
            "reader_consistency": "apt-inrelease-by-hash" if family == "deb" else "dnf-versioned-baseurl-mirrorlist",
            "objects": [{"path": path.relative_to(destination).as_posix(), "sha256": digest(path),
                         "size": path.stat().st_size} for path in sorted(destination.rglob("*")) if path.is_file()]}
    result["inventory_sha256"] = hashlib.sha256(json.dumps(result["objects"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("family", choices=["deb", "rpm"])
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--inputs", type=Path, required=True)
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--channel", choices=["stable", "preview"], required=True)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--private-key", type=Path, required=True)
    parser.add_argument("--rehearsal", action="store_true")
    args = parser.parse_args()
    try:
        result = assemble(args.family, json.loads(args.manifest.read_text()), args.inputs, args.destination,
                          args.channel, args.catalog, args.private_key, args.rehearsal)
        (args.destination / "generation.json").write_text(json.dumps(result, indent=2) + "\n")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native repository assembly failed: {exc}\n")


if __name__ == "__main__":
    main()
