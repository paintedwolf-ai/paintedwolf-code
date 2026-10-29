"""Assemble signed apt/dnf generations without activating a public repository."""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
from email.utils import format_datetime, parsedate_to_datetime
import gzip
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess

from .signing import CATALOG, digest, sign, verify
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


def signed_rpm(path: Path, catalog: Path, rehearsal: bool) -> bool:
    try:
        verify("rpm", path, None, catalog, rehearsal)
    except ValueError:
        return False
    return True


def apt_indexes(destination: Path, channel: str, architectures: list[str], catalog: Path,
                private: Path | None, rehearsal: bool):
    distribution = destination / "dists" / channel
    # Release files are named by platform, so apt-ftparchive's filename-based -a filter would drop
    # them; each index selects its stanzas by their own Architecture field instead.
    listing = subprocess.check_output(["apt-ftparchive", "packages", "pool"], cwd=destination).decode()
    stanzas = [stanza.strip("\n") for stanza in listing.split("\n\n") if stanza.strip()]
    for arch in architectures:
        binary = distribution / "main" / ("binary-" + arch)
        binary.mkdir(parents=True)
        data = "".join(stanza + "\n\n" for stanza in stanzas
                       if re.search(r"^Architecture: (\S+)$", stanza, re.M).group(1) in {arch, "all"}).encode()
        (binary / "Packages").write_bytes(data)
        (binary / "Packages.gz").write_bytes(gzip.compress(data, mtime=0))
        for path in [binary / "Packages", binary / "Packages.gz"]:
            for algorithm in ["SHA256", "SHA512"]:
                value = hashlib.new(algorithm.lower(), path.read_bytes()).hexdigest()
                hashed = binary / "by-hash" / algorithm / value
                hashed.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(path, hashed)
    apt_release(destination, channel, catalog, private, rehearsal)


def release_date(raw: bytes) -> datetime:
    match = re.search(rb"^Date: (.+)$", raw, re.M)
    if match is None:
        raise ValueError("apt Release has no Date")
    return parsedate_to_datetime(match.group(1).decode())


def apt_release(destination: Path, channel: str, catalog: Path, private: Path | None, rehearsal: bool,
                *, after: datetime | None = None):
    """Write and sign the generation's Release; `after` makes its Date later than an earlier Release."""
    distribution = destination / "dists" / channel
    # InRelease is the one mutable signed object; detached pairs stay generation-local.
    release = distribution / "Release"
    data = subprocess.check_output(["apt-ftparchive", "-o", "APT::FTPArchive::Release::Acquire-By-Hash=yes",
                                   "-o", "APT::FTPArchive::Release::Origin=Painted Wolf Code",
                                   "-o", "APT::FTPArchive::Release::Suite=" + channel,
                                   "-o", "APT::FTPArchive::Release::Codename=" + channel,
                                   "release", str(distribution.relative_to(destination))], cwd=destination)
    if b"Acquire-By-Hash: yes\n" not in data:
        raise ValueError("apt Release must advertise content-addressed indexes")
    if after is not None and release_date(data) <= after:
        stamp = format_datetime(after.astimezone(timezone.utc) + timedelta(seconds=1)).encode()
        data = re.sub(rb"^Date: .+$", b"Date: " + stamp, data, count=1, flags=re.M)
    release.write_bytes(data)
    sign("release", release, distribution / "InRelease", catalog, private, rehearsal)
    sign("detached", release, distribution / "Release.gpg", catalog, private, rehearsal)


def authority(channel: str) -> set[str]:
    return {f"dists/{channel}/{name}" for name in ("Release", "InRelease", "Release.gpg")}


def inventory(destination: Path) -> tuple[list[dict], str]:
    objects = [{"path": path.relative_to(destination).as_posix(), "sha256": digest(path), "size": path.stat().st_size}
               for path in sorted(destination.rglob("*")) if path.is_file() and path.name != "generation.json"]
    return objects, hashlib.sha256(json.dumps(objects, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def same_restore(retained: dict, resigned: dict):
    """A re-signed apt restore keeps every fact and index of its retained generation; only Release changes."""
    signed = authority(retained["channel"])
    facts = lambda receipt: {key: value for key, value in receipt.items() if key not in {"objects", "inventory_sha256"}}
    unsigned = lambda receipt: sorted((row for row in receipt["objects"] if row["path"] not in signed), key=lambda row: row["path"])
    if (retained.get("family") != "deb" or not retained.get("packages") or facts(retained) != facts(resigned)
            or unsigned(retained) != unsigned(resigned)
            or not signed <= {row["path"] for row in resigned["objects"]}):
        raise ValueError("re-signed apt restore differs from its retained generation")


def resign(retained: dict, directory: Path, catalog: Path, private: Path, rehearsal: bool, *, after: datetime) -> dict:
    """Sign a fresh Release over a retained apt generation so clients drop an index cached from a later one."""
    signer, _ = selection(catalog, rehearsal)
    if (retained.get("family") != "deb" or not retained.get("packages") or retained.get("rehearsal") is not rehearsal
            or (retained.get("signing_generation"), retained.get("key_fingerprint")) != (signer["generation"], signer["fingerprint"])):
        raise ValueError("only a retained apt offer under the selected signing identity can be re-signed")
    for name in authority(retained["channel"]):
        (directory / name).unlink(missing_ok=True)
    (directory / "generation.json").unlink(missing_ok=True)
    apt_release(directory, retained["channel"], catalog, private, rehearsal, after=after)
    objects, inventory_sha256 = inventory(directory)
    resigned = {**retained, "objects": objects, "inventory_sha256": inventory_sha256}
    same_restore(retained, resigned)
    return resigned


def assemble(family: str, manifest: dict, inputs: Path, destination: Path, channel: str,
             catalog: Path = CATALOG, private: Path | None = None, rehearsal=False,
             package="painted-wolf-code", architectures: list[str] | None = None) -> dict:
    """An empty package selection is a withdrawal generation for the named architectures."""
    if private is None:
        raise ValueError("batch repository signing requires an owner-only private key file")
    if family not in {"deb", "rpm"} or channel not in {"stable", "preview"}:
        raise ValueError("select a supported repository family and release channel")
    if (manifest.get("format_version") != 1 or not re.fullmatch(r"[0-9a-f]{40}", manifest.get("source_commit", ""))
            or not SAFE_NAME.fullmatch(manifest.get("version", ""))):
        raise ValueError("repository generation requires an exact source and product version")
    rows = manifest.get("packages")
    if (not isinstance(rows, list) or len({row["file"] for row in rows}) != len(rows)
            or bool(rows) == (architectures is not None)):
        raise ValueError("repository input receipt requires distinct packages or explicit withdrawal architectures")
    if architectures is not None and (not architectures or set(architectures) - set(ARCHITECTURES)):
        raise ValueError("withdrawal generation requires supported architectures")
    signer, _ = selection(catalog, rehearsal)
    sources = [(row, checked_input(row, inputs, family, package)) for row in rows]
    destination.mkdir(parents=True, exist_ok=False)
    architectures = sorted(architectures or {row["architecture"] for row in rows})
    (destination / "pool").mkdir()
    final = []
    for row, source in sources:
        stage = destination / ".signing" / source.name
        stage.parent.mkdir(exist_ok=True)
        if family == "rpm" and not signed_rpm(source, catalog, rehearsal):
            sign("rpm", source, stage, catalog, private, rehearsal)
        else:
            # A release RPM already carries the selected signature; its published bytes stay identical.
            shutil.copyfile(source, stage)
        target = destination / "pool" / digest(stage) / source.name
        target.parent.mkdir()
        stage.replace(target)
        final.append({"architecture": row["architecture"], "native_version": row["native_version"],
                      "input_sha256": row["sha256"], "sha256": digest(target),
                      "file": target.relative_to(destination).as_posix()})
    if final:
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
            "publication_order": "content-before-signed-metadata",
            "reader_consistency": "apt-inrelease-by-hash" if family == "deb" else "dnf-versioned-baseurl-mirrorlist",
            "objects": []}
    result["objects"], result["inventory_sha256"] = inventory(destination)
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
