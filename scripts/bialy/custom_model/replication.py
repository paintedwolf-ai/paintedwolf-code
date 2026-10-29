"""Publish checksum-verified checkpoint generations to independent storage."""
import json
import os
from pathlib import Path
import shutil
import uuid

from checkpoint import sync_directory
from common import file_digest


def verify(directory, identity):
    directory = Path(directory)
    if directory.is_symlink() or (directory / "state.pt").is_symlink():
        raise ValueError("checkpoint links are not allowed")
    manifest = json.loads((directory / "manifest.json").read_text())
    if manifest.get("identity") != identity or manifest.get("state_sha256") != file_digest(directory / "state.pt"):
        raise ValueError("checkpoint identity or checksum mismatch")
    return manifest


def replicate(source, destination, identity):
    source, destination = Path(source), Path(destination)
    if destination.resolve().is_relative_to(source.parent.resolve()):
        raise ValueError("checkpoint replica must be outside the local checkpoint directory")
    expected = verify(source, identity)
    destination.mkdir(parents=True, exist_ok=True)
    target = destination / source.name
    if target.exists():
        if verify(target, identity) != expected:
            raise ValueError("checkpoint replica generation collision")
        return target
    staged = destination / (".pending-" + uuid.uuid4().hex)
    staged.mkdir()
    for name in ("state.pt", "manifest.json"):
        shutil.copyfile(source / name, staged / name)
        with (staged / name).open("rb") as stream:
            os.fsync(stream.fileno())
    if verify(staged, identity) != expected or verify(source, identity) != expected:
        raise ValueError("checkpoint changed during replication")
    sync_directory(staged)
    os.rename(staged, target)
    sync_directory(destination)
    return target
