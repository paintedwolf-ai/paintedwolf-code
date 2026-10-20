"""Publish admitted immutable setup and distribution bytes through the existing transport."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import subprocess

from .authority import PREFIX
from .repositories import digest
from ..r2_objects import prefix, public_bytes

ROOT = Path(__file__).resolve().parents[3]


class ImmutableOrigin:
    """The maintained publisher has no mutable-pointer or external-dispatch operation."""

    def __init__(self, *, rehearsal=False, storage_prefix=None, origin="https://downloads.paintedwolf.dev"):
        from .authority import repository_origin
        if type(rehearsal) is not bool:
            raise ValueError("immutable transport requires an explicit trust mode")
        self.origin = repository_origin(origin)
        self.prefix = storage_prefix or ""
        if rehearsal:
            prefix(self.prefix, True)
        elif self.prefix or self.origin != "https://downloads.paintedwolf.dev":
            raise ValueError("production immutable metadata requires its protected origin and root")
        self.rehearsal = rehearsal

    def key(self, name):
        prefix(name, False)
        return self.prefix + "/" + name if self.prefix else name

    def put(self, name, path, content_type):
        key = self.key(name)
        subprocess.run(["bash", str(ROOT / "scripts/release-r2-immutable-put.sh"), "--key", key,
                        "--file", str(path), "--content-type", content_type], check=True)

    def verify(self, name, path, identity):
        public_bytes(self.origin + "/" + self.key(name), path, expected_identity=identity)


def identity(path):
    value = hashlib.sha256()
    size = 0
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            value.update(chunk); size += len(chunk)
    return {"sha256": value.hexdigest(), "bytes": size}


def publish_setups(selected, *, backend):
    """Setup publication is opt-in and never enrolls an application installation."""
    inputs, report = selected.authorize()
    entries = {digest(row["manifest"]): row for row in inputs["repositories"]["generations"]}
    written = []
    for setup in report["verified_setups"]:
        generation = setup["repository_generation"]
        if generation not in entries:
            raise ValueError("setup publication has no independently selected repository")
        directory = selected.root / "repository-setup" / generation
        names = {setup["package"]["file"]: ("application/vnd.debian.binary-package" if setup["family"] == "deb" else "application/x-rpm"),
                 "SHA256SUMS": "text/plain", "SHA256SUMS.asc": "application/pgp-signature",
                 "package-key.asc": "application/pgp-keys"}
        for name, content_type in names.items():
            path = directory / name
            frozen = identity(path)
            if name == setup["package"]["file"] and frozen != {key: setup["package"][key] for key in ("sha256", "bytes")}:
                raise ValueError("setup bytes changed after independent admission")
            key = PREFIX + "/generations/" + generation + "/setup/" + name
            current_inputs, current_report = selected.authorize()
            if current_inputs != inputs or current_report != report or identity(path) != frozen:
                raise ValueError("setup publication authority changed before transport")
            backend.put(key, path, content_type)
            backend.verify(key, path, frozen)
            current_inputs, current_report = selected.authorize()
            if current_inputs != inputs or current_report != report or identity(path) != frozen:
                raise ValueError("setup publication authority changed during transport")
            written.append({"key": key, **frozen})
    return written
