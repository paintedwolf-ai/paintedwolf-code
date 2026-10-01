#!/usr/bin/env python3
"""Materialize a retained installation from a versioned fixture."""

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tempfile
import zipfile


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def relative_path(name):
    if not isinstance(name, str) or not name or "\\" in name or ":" in name or "\0" in name:
        raise ValueError("invalid fixture archive path")
    path = PurePosixPath(name)
    if path.is_absolute() or ".." in path.parts or str(path) != name or name == ".":
        raise ValueError("invalid fixture archive path")
    return path


def checked_archive(fixture):
    if (fixture / "MANIFEST.json").is_symlink():
        raise ValueError("fixture manifest must be a regular file")
    metadata = json.loads((fixture / "MANIFEST.json").read_text())
    for name, key in (("backup.zip", "backup_sha256"), ("store.db", "store_sha256"),
                      ("SEMANTICS.json", "semantics_sha256")):
        if (fixture / name).is_symlink() or digest(fixture / name) != metadata[key]:
            raise ValueError("release fixture checksum differs: " + name)
    with zipfile.ZipFile(fixture / "backup.zip") as archive:
        names = archive.namelist()
        if len(set(names)) != len(names):
            raise ValueError("duplicate fixture archive entry")
        manifest = json.loads(archive.read("manifest.json"))
        identity = {"revision": manifest["schema_user_version"], "shape": manifest["schema_shape_digest"]}
        if identity != metadata["schema_identity"] or identity["revision"] != metadata["schema_version"]:
            raise ValueError("fixture archive schema identity differs")
        if manifest["app_version"] != metadata["app_version"] or manifest["format_version"] != 1:
            raise ValueError("fixture archive provenance or format differs")
        entries = {}
        for item in manifest["files"]:
            path = relative_path(item["rel_path"])
            name = str(path)
            if name in entries or name == "manifest.json":
                raise ValueError("duplicate fixture manifest entry")
            if (item["kind"] not in {"file", "symlink"} or type(item["mode"]) is not int
                    or not 0 <= item["mode"] <= 0o777 or type(item["size"]) is not int or item["size"] < 0):
                raise ValueError("invalid fixture file kind or mode")
            entries[name] = item
            info = archive.getinfo(name)
            if info.file_size != item["size"]:
                raise ValueError("fixture archive size differs: " + name)
            value = hashlib.sha256()
            with archive.open(info) as source:
                for block in iter(lambda: source.read(1024 * 1024), b""):
                    value.update(block)
            if value.hexdigest() != item["sha256"]:
                raise ValueError("fixture archive checksum differs: " + name)
        if set(names) != {"manifest.json", *entries} or entries.get("store.db", {}).get("kind") != "file":
            raise ValueError("fixture archive inventory differs")
        if "app-state-v1" in entries:
            raise ValueError("fixture preferences root must be a directory")
        for name in entries:
            if any(str(parent) in entries for parent in PurePosixPath(name).parents):
                raise ValueError("fixture archive writes through another entry")
        for root in manifest.get("retained_branch_trees", []):
            path = relative_path(root)
            if path.parts[0] != "worker-branches" or any(str(parent) in entries for parent in (path, *path.parents)):
                raise ValueError("invalid retained branch tree")
        preferences = fixture / "app-state-v1"
        if not preferences.is_dir() or preferences.is_symlink():
            raise ValueError("fixture application preferences missing or unsafe")
        for path in preferences.rglob("*"):
            if path.is_symlink():
                raise ValueError("fixture application preferences contain a symlink")
            if path.is_file():
                name = path.relative_to(fixture).as_posix()
                item = entries.get(name)
                if item is None or item["kind"] != "file" or digest(path) != item["sha256"]:
                    raise ValueError("fixture application preferences differ from the archive")
        return metadata, manifest


def materialize(fixture, destination):
    _, manifest = checked_archive(fixture)
    destination = destination.resolve()
    if destination.exists() and any(destination.iterdir()):
        raise ValueError("fixture destination must be empty")
    destination.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(fixture / "backup.zip") as archive:
        for root in manifest.get("retained_branch_trees", []):
            destination.joinpath(*relative_path(root).parts).mkdir(parents=True, exist_ok=True)
        for item in manifest["files"]:
            relative = relative_path(item["rel_path"])
            target = destination.joinpath(*relative.parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            with archive.open(str(relative)) as source:
                if item["kind"] == "symlink":
                    os.symlink(source.read().decode(), target)
                else:
                    with target.open("xb") as output:
                        shutil.copyfileobj(source, output, 1024 * 1024)
                    target.chmod(item["mode"])
    shutil.copyfile(fixture / "store.db", destination / "store.db")
    shutil.copytree(fixture / "app-state-v1", destination / "app-state-v1", dirs_exist_ok=True)


def candidate(fixture, sidecar, version):
    metadata, _ = checked_archive(fixture)
    project = fixture / "project-root"
    if not project.is_dir() or project.is_symlink():
        raise ValueError("candidate fixture project root missing or unsafe")
    if metadata["app_version"] != version:
        raise ValueError("fixture app version differs from candidate")
    def diagnostic(*arguments):
        return json.loads(subprocess.check_output([str(sidecar), "diagnostics", *map(str, arguments)], text=True))
    baseline = diagnostic("schema-baseline")
    if metadata["schema_identity"] != baseline:
        raise ValueError("candidate corpus schema is stale; refresh the unreleased fixture before release")
    # Materialization replaces the archived database, so both copies need validation.
    with tempfile.TemporaryDirectory(prefix="upgrade-corpus-validate-") as temporary:
        standalone = Path(temporary) / "standalone.db"
        shutil.copyfile(fixture / "store.db", standalone)
        if not diagnostic("store-baseline", standalone)["compatible"]:
            raise ValueError("candidate fixture store does not match its claimed schema")
        store = Path(temporary) / "archive.db"
        with zipfile.ZipFile(fixture / "backup.zip") as archive, store.open("wb") as target:
            with archive.open("store.db") as source:
                shutil.copyfileobj(source, target, 1024 * 1024)
        if not diagnostic("store-baseline", store)["compatible"]:
            raise ValueError("candidate archive store does not match its claimed schema")
    print("candidate corpus identity and integrity verified: " + str(fixture))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    materializer = commands.add_parser("materialize")
    materializer.add_argument("fixture", type=Path)
    materializer.add_argument("destination", type=Path)
    validator = commands.add_parser("candidate")
    validator.add_argument("fixture", type=Path)
    validator.add_argument("--sidecar", type=Path, required=True)
    validator.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        if args.command == "materialize":
            materialize(args.fixture, args.destination)
        else:
            candidate(args.fixture, args.sidecar, args.version)
    except (ValueError, KeyError, TypeError, OSError, zipfile.BadZipFile, subprocess.CalledProcessError) as error:
        sys.exit("error: " + str(error))
