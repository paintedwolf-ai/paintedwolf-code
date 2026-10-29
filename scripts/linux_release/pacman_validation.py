"""Verify pacman databases, detached signatures, and every referenced package."""

from __future__ import annotations

import base64
import hashlib
import json
import re
from pathlib import Path
import tarfile

from .pacman_repository import NAME, ARCHITECTURES, package_info
from .signing import digest, verify
from .openpgp import selection


def fields(raw: bytes) -> dict:
    result = {}
    for paragraph in raw.decode().strip().split("\n\n"):
        lines = paragraph.splitlines()
        if len(lines) < 2 or not lines[0].startswith("%") or not lines[0].endswith("%"):
            raise ValueError("invalid pacman database field")
        name = lines[0][1:-1]
        if name in result:
            raise ValueError("duplicate pacman database field")
        result[name] = lines[1:]
    return result


def admit(receipt: dict, directory: Path, catalog: Path, rehearsal=False):
    if (receipt.get("format_version") != 1 or receipt.get("family") != "pacman" or receipt.get("rehearsal") is not rehearsal
            or receipt.get("channel") not in {"stable", "preview"}
            or receipt.get("architecture") not in ARCHITECTURES
            or not re.fullmatch(r"[0-9a-f]{40}", receipt.get("source_commit", ""))
            or not NAME.fullmatch(receipt.get("version", ""))):
        raise ValueError("unsupported or cross-trust pacman generation")
    signer, _ = selection(catalog, rehearsal)
    if receipt.get("signing_generation") != signer["generation"] or receipt.get("key_fingerprint") != signer["fingerprint"]:
        raise ValueError("pacman generation differs from the selected signing identity")
    repository = "painted-wolf-code" + ("-preview" if receipt["channel"] == "preview" else "")
    if receipt.get("repository") != repository:
        raise ValueError("pacman database differs from its selected release channel")
    rows = receipt.get("objects", [])
    measured = hashlib.sha256(json.dumps(rows, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if receipt.get("inventory_sha256") != measured:
        raise ValueError("pacman inventory differs from its selected identity")
    names = [row["path"] for row in rows]
    observed = {p.name for p in directory.iterdir() if p.name != "generation.json"}
    if not rows or len(set(names)) != len(names) or observed != set(names):
        raise ValueError("pacman generation inventory differs from its selected objects")
    for row in rows:
        name = row["path"]
        if not isinstance(name, str) or not NAME.fullmatch(name):
            raise ValueError("pacman inventory object is not a sibling file")
        path = directory / name
        if (type(row["size"]) is not int or row["size"] < 0
                or not path.is_file() or path.resolve().parent != directory.resolve()
                or path.stat().st_size != row["size"] or digest(path) != row["sha256"]):
            raise ValueError("pacman object differs from its selected final bytes")
    packages = receipt.get("packages")
    if not isinstance(packages, list) or {row["file"] for row in packages} != {
            name for name in names if name.endswith(".pkg.tar.zst")}:
        raise ValueError("pacman final package inventory is incomplete")
    for row in packages:
        selected = next(item for item in rows if item["path"] == row["file"])
        if row["sha256"] != selected["sha256"] or row["architecture"] != receipt["architecture"]:
            raise ValueError("pacman final package identity differs from its selected inventory")
    for suffix in ["db", "files"]:
        archive = directory / f"{repository}.{suffix}.tar.gz"
        verify("detached", archive, archive.with_name(archive.name + ".sig"), catalog, rehearsal)
        if ((directory / f"{repository}.{suffix}").read_bytes() != archive.read_bytes()
                or (directory / f"{repository}.{suffix}.sig").read_bytes()
                != archive.with_name(archive.name + ".sig").read_bytes()):
            raise ValueError("pacman alias differs from its signed archive")
    selected = set()
    with tarfile.open(directory / f"{repository}.db.tar.gz", "r:gz") as archive:
        for member in archive:
            if not member.isfile() or not member.name.endswith("/desc"):
                continue
            if member.size > 1024 * 1024:
                raise ValueError("pacman database description exceeds its bound")
            values = fields(archive.extractfile(member).read())
            name, checksum, signature = (values[key] for key in ["FILENAME", "SHA256SUM", "PGPSIG"])
            if any(len(value) != 1 for value in [name, checksum, signature]):
                raise ValueError("pacman package identity is ambiguous")
            filename = name[0]
            if filename not in names or filename in selected or not filename.endswith(".pkg.tar.zst"):
                raise ValueError("pacman database references an unselected or duplicate package")
            package = directory / filename
            if digest(package) != checksum[0]:
                raise ValueError("pacman database checksum differs from its package")
            detached = package.with_name(package.name + ".sig")
            if base64.b64decode(signature[0], validate=True) != detached.read_bytes():
                raise ValueError("pacman embedded signature differs from its detached signature")
            verify("detached", package, detached, catalog, rehearsal)
            info = package_info(package)
            if any(values[key] != [info[field]] for key, field in
                   [("NAME", "pkgname"), ("VERSION", "pkgver"), ("ARCH", "arch")]):
                raise ValueError("pacman database identity differs from its native package")
            selected_package = next(row for row in packages if row["file"] == filename)
            if info["pkgver"] != selected_package["native_version"]:
                raise ValueError("pacman final package version differs from its selected receipt")
            if info["arch"] != receipt["architecture"]:
                raise ValueError("pacman package differs from the selected architecture")
            selected.add(filename)
    if selected != {name for name in names if name.endswith(".pkg.tar.zst")}:
        raise ValueError("pacman database omits a selected package")
