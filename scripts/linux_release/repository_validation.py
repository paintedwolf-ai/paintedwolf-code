"""Admit signed apt/dnf generations against their selected immutable inventory."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import xml.etree.ElementTree as ET

from .signing import verify


def selected_object(directory: Path, name: str) -> Path:
    relative = PurePosixPath(name)
    if (not name or relative.is_absolute() or relative.as_posix() != name or ".." in relative.parts
            or not (directory / name).resolve().is_relative_to(directory.resolve())):
        raise ValueError("repository object path escapes its selected generation")
    path = directory / name
    if path.is_symlink() or not path.is_file():
        raise ValueError("repository object is not a regular immutable file")
    return path


def check_bytes(path: Path, expected: str, size: int):
    if not re.fullmatch(r"[0-9a-f]{64}", expected) or type(size) is not int or size < 0:
        raise ValueError("repository object identity is invalid")
    value = hashlib.sha256()
    measured = 0
    with path.open("rb") as source:
        while chunk := source.read(1 << 20):
            measured += len(chunk)
            if measured > size:
                raise ValueError("repository object exceeds its selected size")
            value.update(chunk)
    if measured != size or value.hexdigest() != expected:
        raise ValueError("repository object differs from its selected final bytes")


def apt(directory: Path, channel: str, catalog: Path, rehearsal: bool):
    distribution = directory / "dists" / channel
    release = selected_object(distribution, "Release")
    verify("release", release, distribution / "InRelease", catalog, rehearsal)
    verify("detached", release, distribution / "Release.gpg", catalog, rehearsal)
    fields, section, checksums = {}, None, []
    for line in release.read_text().splitlines():
        if line.startswith(" "):
            if section == "SHA256":
                digest, size, name = line.split()
                checksums.append((digest, int(size), name))
        else:
            key, separator, value = line.partition(":")
            if not separator or key in fields:
                raise ValueError("ambiguous signed apt Release fields")
            fields[key] = value.strip()
            section = key
    if fields.get("Acquire-By-Hash") != "yes" or fields.get("Suite") != channel or not checksums:
        raise ValueError("apt Release lacks its selected channel or content-addressed index contract")
    package_indexes = []
    for digest, size, name in checksums:
        index = selected_object(distribution, name)
        check_bytes(index, digest, size)
        if index.name == "Packages":
            check_bytes(selected_object(index.parent, "by-hash/SHA256/" + digest), digest, size)
            package_indexes.append(index)
    if not package_indexes:
        raise ValueError("signed apt generation has no package index")
    for index in package_indexes:
        for paragraph in index.read_text().strip().split("\n\n"):
            if not paragraph:
                continue
            fields = dict(line.split(": ", 1) for line in paragraph.splitlines() if line and not line[0].isspace())
            check_bytes(selected_object(directory, fields["Filename"]), fields["SHA256"], int(fields["Size"]))


def rpm(directory: Path, catalog: Path, rehearsal: bool):
    metadata = selected_object(directory, "repodata/repomd.xml")
    verify("detached", metadata, metadata.with_suffix(".xml.asc"), catalog, rehearsal)
    namespace = {"repo": "http://linux.duke.edu/metadata/repo"}
    entries = ET.fromstring(metadata.read_bytes()).findall("repo:data", namespace)
    if not entries:
        raise ValueError("signed RPM repository has no metadata objects")
    for item in entries:
        location, checksum, size = (item.find("repo:" + name, namespace) for name in ["location", "checksum", "size"])
        if location is None or checksum is None or size is None or checksum.get("type") != "sha256":
            raise ValueError("RPM metadata lacks a SHA256-bound object")
        check_bytes(selected_object(directory, location.attrib["href"]), checksum.text, int(size.text))


def admit(receipt: dict, directory: Path, catalog: Path, rehearsal=False):
    if (receipt.get("format_version") != 1 or receipt.get("family") not in {"deb", "rpm"}
            or receipt.get("channel") not in {"stable", "preview"} or receipt.get("rehearsal") is not rehearsal):
        raise ValueError("unsupported or cross-trust repository generation")
    objects = receipt.get("objects")
    if not isinstance(objects, list) or not objects:
        raise ValueError("selected repository inventory is empty")
    measured = hashlib.sha256(json.dumps(objects, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if receipt.get("inventory_sha256") != measured:
        raise ValueError("repository inventory differs from its selected identity")
    names = [row["path"] for row in objects]
    if len(names) != len(set(names)):
        raise ValueError("repository inventory repeats an object")
    observed = {p.relative_to(directory).as_posix() for p in directory.rglob("*")
                if p.is_file() and p.name != "generation.json"}
    if observed != set(names):
        raise ValueError("selected repository inventory is incomplete or contains unexpected files")
    for row in objects:
        check_bytes(selected_object(directory, row["path"]), row["sha256"], row["size"])
    packages = receipt.get("packages")
    if not isinstance(packages, list) or {row["file"] for row in packages} != {
            name for name in names if name.endswith("." + receipt["family"])}:
        raise ValueError("repository final package inventory is incomplete")
    for row in packages:
        selected = next(item for item in objects if item["path"] == row["file"])
        if row["sha256"] != selected["sha256"]:
            raise ValueError("repository final package hash differs from its inventory")
    if receipt["family"] == "deb":
        apt(directory, receipt["channel"], catalog, rehearsal)
    else:
        rpm(directory, catalog, rehearsal)
        for row in receipt["packages"]:
            verify("rpm", selected_object(directory, row["file"]), None, catalog, rehearsal)
