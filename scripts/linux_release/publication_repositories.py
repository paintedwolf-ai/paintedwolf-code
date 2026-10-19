"""Bind a repository publication plan to independently admitted installed packages."""

from __future__ import annotations

import hashlib
import json
from pathlib import PurePosixPath
import re

from .installed_evidence import artifacts, object_fields

FAMILIES = {"deb", "rpm", "pacman"}
ARCHITECTURES = {"x86_64": "amd64", "aarch64": "arm64", "amd64": "amd64", "arm64": "arm64"}


def digest(value, *, inventory=False):
    data = (json.dumps(value, sort_keys=True, separators=(",", ":")) if inventory
            else json.dumps(value, sort_keys=True, indent=2) + "\n")
    return hashlib.sha256(data.encode()).hexdigest()


def verify_repositories(plan, final_artifacts, expected_source, *, version, channel,
                        signing_generation, key_fingerprint, verified_files):
    selected = artifacts(final_artifacts, expected_source)
    measured = {row["id"]: row for row in verified_files}
    required = {name: row for name, row in selected.items() if row["installation"] in FAMILIES}
    data = object_fields(plan, {"format_version", "generations"})
    if type(data["format_version"]) is not int or data["format_version"] != 1:
        raise ValueError("unsupported repository publication plan")
    if not isinstance(data["generations"], list):
        raise ValueError("repository generations must be a list")
    if (type(signing_generation) is not int or signing_generation < 1
            or not isinstance(key_fingerprint, str)
            or not re.fullmatch(r"[0-9A-F]{40}", key_fingerprint)):
        raise ValueError("publication requires an explicitly selected signing identity")
    seen, channels, verified = set(), set(), []
    for entry in data["generations"]:
        pair = object_fields(entry, {"manifest", "operation"})
        manifest, operation = pair["manifest"], pair["operation"]
        if not isinstance(manifest, dict) or not isinstance(operation, dict):
            raise ValueError("repository manifest and operation must be objects")
        expected = {"format_version": 1, "source_commit": expected_source,
                    "version": version, "channel": channel, "rehearsal": False,
                    "signing_generation": signing_generation, "key_fingerprint": key_fingerprint}
        if any(type(manifest.get(key)) is not type(value) or manifest[key] != value
               for key, value in expected.items()):
            raise ValueError("repository differs from the independently selected release")
        family = manifest.get("family")
        if family not in FAMILIES:
            raise ValueError("unknown publication repository family")
        channel_id = (family, manifest.get("architecture"))
        if channel_id in channels:
            raise ValueError("duplicate repository channel")
        channels.add(channel_id)
        objects = manifest.get("objects")
        if not isinstance(objects, list) or not objects or digest(objects, inventory=True) != manifest.get("inventory_sha256"):
            raise ValueError("repository inventory differs from its immutable manifest")
        indexed = {}
        for raw in objects:
            obj = object_fields(raw, {"path", "sha256", "size"})
            path = obj["path"]
            if (not isinstance(path, str) or not path or PurePosixPath(path).is_absolute()
                    or PurePosixPath(path).as_posix() != path
                    or ".." in PurePosixPath(path).parts or path in indexed
                    or type(obj["size"]) is not int or obj["size"] < 0
                    or not isinstance(obj["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", obj["sha256"])):
                raise ValueError("invalid or duplicate immutable repository object")
            indexed[path] = obj
        packages = manifest.get("packages")
        if not isinstance(packages, list) or not packages:
            raise ValueError("repository generation contains no packages")
        for package in packages:
            if not isinstance(package, dict) or not isinstance(package.get("file"), str):
                raise ValueError("repository package is missing its immutable path")
            name = PurePosixPath(package["file"]).name
            row = required.get(name)
            if row is None or name in seen:
                raise ValueError("repository package was not independently admitted or is duplicated")
            arch = ARCHITECTURES.get(package.get("architecture"))
            if (row["installation"] != family or arch != row["architecture"]
                    or row["identity"]["kind"] != "file"
                    or package.get("sha256") != row["identity"]["sha256"]
                    or indexed.get(package["file"], {}).get("sha256") != package["sha256"]
                    or indexed.get(package["file"], {}).get("size") != measured.get(name, {}).get("bytes")
                    or family == "pacman" and manifest.get("architecture") != package.get("architecture")):
                raise ValueError("repository package differs from the installed final artifact")
            seen.add(name)
        generation = digest(manifest)
        facts = ("family", "channel", "version", "source_commit", "inventory_sha256",
                 "packages", "signing_generation", "key_fingerprint", "rehearsal")
        if (type(operation.get("format_version")) is not int or operation.get("format_version") != 1
                or operation.get("operation") != "publish"
                or operation.get("generation") != generation
                or any(type(operation.get(key)) is not type(manifest.get(key))
                       or operation.get(key) != manifest.get(key) for key in facts)
                or operation.get("architecture") != manifest.get("architecture")):
            raise ValueError("ordinary release requires the exact explicit publish operation")
        previous = operation.get("previous")
        if previous is not None and (not isinstance(previous, str) or not re.fullmatch(r"[0-9a-f]{64}", previous)):
            raise ValueError("publication previous generation is invalid")
        verified.append({"family": family, "generation": generation,
                         "inventory_sha256": manifest["inventory_sha256"]})
    if seen != required.keys():
        raise ValueError("repository plan omits independently admitted native packages")
    return verified
