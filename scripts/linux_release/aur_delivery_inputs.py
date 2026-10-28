"""Collect the AUR delivery input bundle from published pacman generations and controller files."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re
import urllib.request
from urllib.parse import urlsplit

from .repository_objects import encoded
from .repository_operations import identity

ARCHITECTURES = ("x86_64", "aarch64")
MANIFEST_LIMIT = 1 << 20
ACTIVE_FACTS = ["family", "channel", "architecture", "version", "source_commit", "inventory_sha256",
                "packages", "signing_generation", "key_fingerprint", "rehearsal"]


def https_fetch(url: str, destination: Path, limit: int):
    request = urllib.request.Request(url, headers={"Cache-Control": "no-cache", "User-Agent": "painted-wolf-release/1"})
    with urllib.request.urlopen(request, timeout=60) as response, destination.open("xb") as output:
        measured = 0
        while chunk := response.read(1 << 20):
            measured += len(chunk)
            if measured > limit:
                raise ValueError("published repository object exceeds its selected size")
            output.write(chunk)


def file_sha256(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1 << 20):
            value.update(chunk)
    return value.hexdigest()


def repository_origin(value: str) -> str:
    origin = urlsplit(value)
    if (origin.scheme != "https" or not origin.netloc or origin.query or origin.fragment
            or origin.username or origin.password):
        raise ValueError("select the HTTPS pacman repository origin without credentials or query")
    return value.rstrip("/")


def fetched_json(fetch, url: str, destination: Path) -> dict:
    fetch(url, destination, MANIFEST_LIMIT)
    value = json.loads(destination.read_text())
    if not isinstance(value, dict):
        raise ValueError("published repository record has an unsupported shape")
    return value


def generation(fetch, origin: str, channel: str, architecture: str, source_commit: str, directory: Path) -> dict:
    directory.mkdir()
    active = fetched_json(fetch, f"{origin}/channels/pacman/{channel}/{architecture}/current.json",
                          directory.parent / f".{architecture}-current.json")
    if (active.get("format_version") != 1 or active.get("operation") not in {"publish", "recover", "rotate"}
            or not isinstance(active.get("generation"), str) or not re.fullmatch(r"[0-9a-f]{64}", active["generation"])):
        raise ValueError("published pacman channel state has an unsupported shape")
    if active.get("source_commit") != source_commit:
        raise ValueError("published pacman generation differs from the selected source commit")
    if (active.get("family"), active.get("channel"), active.get("architecture"), active.get("rehearsal")) != (
            "pacman", channel, architecture, False):
        raise ValueError("published pacman channel state differs from its selected channel or trust mode")
    manifest = fetched_json(fetch, f"{origin}/generations/{active['generation']}/generation.json",
                            directory / "generation.json")
    if identity(manifest) != active["generation"]:
        raise ValueError("published pacman generation differs from its active identity")
    if any(type(manifest.get(name)) is not type(active[name]) or manifest.get(name) != active[name]
           for name in ACTIVE_FACTS):
        raise ValueError("published pacman generation differs from its active channel facts")
    objects = manifest.get("objects")
    if not isinstance(objects, list) or not objects:
        raise ValueError("published pacman generation lists no objects")
    for row in objects:
        if not isinstance(row, dict):
            raise ValueError("published pacman object is not a sibling file of its generation")
        name, size = row.get("path"), row.get("size")
        if (not isinstance(name, str) or name in {"", ".", "..", "generation.json"} or "/" in name
                or type(size) is not int or size < 0 or not isinstance(row.get("sha256"), str)):
            raise ValueError("published pacman object is not a sibling file of its generation")
        target = directory / name
        fetch(f"{origin}/generations/{active['generation']}/{name}", target, size)
        if target.stat().st_size != size or file_sha256(target) != row["sha256"]:
            raise ValueError("published pacman object differs from its generation inventory")
    return manifest


def final_inventory(root: Path, source_commit: str) -> bytes:
    """The controller run's retained public files, bound to the commit it was admitted for."""
    resolved = root.resolve(strict=True)
    rows = []
    for path in sorted(resolved.rglob("*")):
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise ValueError("controller final artifacts must be regular files")
        if path.is_file():
            rows.append({"path": path.relative_to(resolved).as_posix(), "sha256": file_sha256(path),
                         "size": path.stat().st_size})
    if not rows:
        raise ValueError("controller run retained no final artifacts")
    return encoded({"format_version": 1, "source_commit": source_commit, "artifacts": rows})


def collect(repository_url: str, channel: str, source_commit: str, final_artifacts: Path, output: Path,
            fetch=https_fetch) -> dict:
    if not isinstance(source_commit, str) or not re.fullmatch(r"[0-9a-f]{40}", source_commit):
        raise ValueError("select an exact release source commit")
    if channel not in {"stable", "preview"}:
        raise ValueError("select the stable or preview release channel")
    origin = repository_origin(repository_url)
    inventory = final_inventory(final_artifacts, source_commit)
    output.mkdir(parents=True, exist_ok=False)
    names, manifests = [], []
    for architecture in ARCHITECTURES:
        name = "pacman-" + architecture
        manifests.append(generation(fetch, origin, channel, architecture, source_commit, output / name))
        names.append(name)
    for path in output.glob(".*-current.json"):
        path.unlink()
    if len({manifest["version"] for manifest in manifests}) != 1:
        raise ValueError("published pacman architectures differ in release version")
    (output / "final-artifacts.json").write_bytes(inventory)
    delivery = {"format_version": 1, "source_commit": source_commit, "base_url": origin, "generations": names}
    (output / "delivery.json").write_bytes(encoded(delivery))
    return delivery
