"""Publish admitted immutable setup and distribution bytes through the existing transport."""
from __future__ import annotations

import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import re
from contextlib import contextmanager

from ..trust.authority import PREFIX
from ..trust.repositories import digest
from ...r2_objects import prefix, public_bytes

ROOT = Path(__file__).resolve().parents[4]


class ImmutableOrigin:
    """The maintained publisher has no mutable-pointer or external-dispatch operation."""

    def __init__(self, *, rehearsal=False, storage_prefix=None, origin="https://downloads.paintedwolf.dev"):
        from ..trust.authority import repository_origin
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

    def put(self, name, path, content_type, *, expected_identity):
        if (not isinstance(expected_identity, dict) or set(expected_identity) != {"sha256", "bytes"}
                or not isinstance(expected_identity["sha256"], str)
                or not re.fullmatch("[a-f0-9]{64}", expected_identity["sha256"])
                or type(expected_identity["bytes"]) is not int or not 0 < expected_identity["bytes"] <= 8 << 20):
            raise ValueError("immutable metadata requires its bounded independently admitted identity")
        key = self.key(name)
        with tempfile.TemporaryDirectory(prefix="immutable-metadata-") as temporary:
            frozen = Path(temporary) / path.name
            measured, size = hashlib.sha256(), 0
            with path.open("rb") as source, frozen.open("xb") as destination:
                for chunk in iter(lambda: source.read(1 << 20), b""):
                    size += len(chunk)
                    if size > expected_identity["bytes"]:
                        raise ValueError("immutable input exceeds its admitted size")
                    measured.update(chunk); destination.write(chunk)
            if {"sha256": measured.hexdigest(), "bytes": size} != expected_identity:
                raise ValueError("immutable input differs from independently admitted bytes")
            frozen.chmod(0o400)
            subprocess.run(["bash", str(ROOT / "scripts/release-r2-immutable-put.sh"), "--key", key,
                            "--file", str(frozen), "--content-type", content_type], check=True)

    def verify(self, name, path, identity):
        public_bytes(self.origin + "/" + self.key(name), path, expected_identity=identity)


def identity(path):
    value = hashlib.sha256()
    size = 0
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            value.update(chunk); size += len(chunk)
    return {"sha256": value.hexdigest(), "bytes": size}


@contextmanager
def captured_setup(directory, setup, inputs, base_url, names):
    """Private byte capture is cryptographically admitted before any external write."""
    from ...repository_setup_admission import admit_setup
    from ..trust.generations import CATALOG
    with tempfile.TemporaryDirectory(prefix="immutable-setup-") as temporary:
        frozen = Path(temporary)
        for name in names:
            with (directory / name).open("rb") as source:
                raw = source.read((2 << 20) + 1)
            if len(raw) > 2 << 20:
                raise ValueError("immutable setup input exceeds its captured byte bound")
            (frozen / name).write_bytes(raw)
            (frozen / name).chmod(0o400)
        report = admit_setup(frozen / setup["package"]["file"], frozen / "SHA256SUMS", frozen / "SHA256SUMS.asc", CATALOG,
            family=setup["family"], source_commit=inputs["source_commit"], version=inputs["version"],
            channel=inputs["channel"], repository_generation=setup["repository_generation"],
            inventory_sha256=setup["inventory_sha256"], base_url=base_url,
            signing_generation=inputs["signing_generation"], key_fingerprint=inputs["key_fingerprint"], rehearsal=False)
        if (report != {key: value for key, value in setup.items() if key != "public_key_sha256"}
                or identity(frozen / "package-key.asc")["sha256"] != setup["public_key_sha256"]):
            raise ValueError("captured setup differs from independently admitted package and trust")
        yield frozen


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
        with captured_setup(directory, setup, inputs, selected.repository_base_url, names) as captured:
            for name, content_type in names.items():
                path = captured / name
                frozen = identity(path)
                key = PREFIX + "/generations/" + generation + "/setup/" + name
                current_inputs, current_report = selected.authorize()
                if current_inputs != inputs or current_report != report or identity(directory / name) != frozen:
                    raise ValueError("setup publication authority changed before transport")
                backend.put(key, path, content_type, expected_identity=frozen)
                backend.verify(key, path, frozen)
                current_inputs, current_report = selected.authorize()
                if current_inputs != inputs or current_report != report or identity(directory / name) != frozen:
                    raise ValueError("setup publication authority changed during transport")
                written.append({"key": key, **frozen})
    return written
