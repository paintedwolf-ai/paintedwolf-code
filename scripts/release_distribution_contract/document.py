"""Prepare distribution facts from fresh selected bytes and an admission owner."""
from __future__ import annotations

import copy
import hashlib
import json
import os
from pathlib import Path
import stat

from .document_schema import PLATFORMS, exact, require, validate
from .platforms import parse_platforms

REQUEST = {"mode", "version", "origin", "source_commit", "source_tree", "controller_commit", "signing_generation",
           "package_content_types", "repositories", "nix", "aur"}


def snapshot(path, *, retain=False, limit=64 << 30):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        require(stat.S_ISREG(before.st_mode) and 0 < before.st_size <= limit, "distribution input must be a bounded regular file")
        digest, size, chunks = hashlib.sha256(), 0, []
        for block in iter(lambda: stream.read(8 << 20), b""):
            size += len(block)
            require(size <= limit, "distribution input grew beyond its bound")
            digest.update(block)
            if retain:
                chunks.append(block)
        after = os.fstat(stream.fileno())
        identity = lambda row: (row.st_dev, row.st_ino, row.st_size, row.st_mtime_ns, row.st_ctime_ns)
        require(size == before.st_size and identity(before) == identity(after), "distribution input changed while reading")
    return {"sha256": digest.hexdigest(), "bytes": size}, b"".join(chunks) if retain else None


def construct(request, catalog: Path, updater_manifest: Path, artifacts: Path, *, authorize):
    require(callable(authorize), "distribution preparation requires fresh release admission")
    exact(request, REQUEST)
    selected = copy.deepcopy(request)
    catalog_identity, catalog_raw = snapshot(catalog, retain=True, limit=2 << 20)
    updater_identity, updater_raw = snapshot(updater_manifest, retain=True, limit=2 << 20)
    updater = json.loads(updater_raw)
    require(isinstance(updater, dict) and updater.get("version") == selected["version"]
            and "withdrawn" not in updater, "distribution requires its exact active immutable updater manifest")
    # The catalog is independently selected, not a field inside the input manifest.
    platforms = parse_platforms(catalog_raw)
    public = {key: row for key, row in platforms.items() if row["publication"] == "public"}
    expected = {f"painted-wolf-code_v{selected['version']}_{key}.{extension}"
                for key, row in public.items() for extension in row["package_extensions"]}
    require(isinstance(selected["package_content_types"], dict)
            and set(selected["package_content_types"]) == expected, "distribution content types differ from the public package set")
    require(artifacts.is_dir() and not artifacts.is_symlink(), "distribution artifact root must be a physical directory")
    packages = {}
    for key, row in public.items():
        require(key in PLATFORMS, "distribution catalog names an unsupported canonical platform")
        system, architecture, _ = PLATFORMS[key]
        require(row["goos"] == system and row["artifact_arch"] == architecture, "source catalog platform identity differs")
        entries = {}
        for extension in row["package_extensions"]:
            name = f"painted-wolf-code_v{selected['version']}_{key}.{extension}"
            identity, _ = snapshot(artifacts / name)
            entries[extension] = {"file": name, "url": selected["origin"] + "/releases/v" + selected["version"] + "/" + name,
                                  **identity, "content_type": selected["package_content_types"][name]}
        packages[key] = {"os": system, "architecture": architecture,
                         "update_owner": "package_manager" if row["updater_extension"] is None else "application",
                         "packages": entries}
    from release_semver import parse
    document = validate({"schema_version": 1, "kind": "painted-wolf-distribution", "mode": selected["mode"],
        "version": selected["version"], "channel": parse(selected["version"]).channel,
        "notes": updater.get("notes"), "pub_date": updater.get("pub_date"), "origin": selected["origin"],
        "source": {"repository": "https://github.com/paintedwolf-ai/paintedwolf-code", "commit": selected["source_commit"],
                   "tree": selected["source_tree"], "catalog_sha256": catalog_identity["sha256"]},
        "controller_commit": selected["controller_commit"], "updater_manifest_sha256": updater_identity["sha256"],
        "signing_generation": selected["signing_generation"], "platforms": packages,
        "repositories": selected["repositories"], "nix": selected["nix"], "aur": selected["aur"]})
    binding = {"document": copy.deepcopy(document), "catalog": catalog, "updater_manifest": updater_manifest,
               "artifacts": artifacts, "request": selected}
    require(authorize(binding) is True, "fresh release admission refused distribution facts")
    return document
