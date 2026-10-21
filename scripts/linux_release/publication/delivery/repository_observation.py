"""Observe signed repository activation through authenticated state and public bytes."""
from __future__ import annotations

import copy
import hashlib
import json
import re
from pathlib import Path
import tempfile

from ..trust.authority import PREFIX, repository_origin
from ..trust.generations import CATALOG
from ...repository_objects import encoded
from ...repository_operations import archive_trust, identity, version_marker, OPERATIONS
from ...repository_pending import activation_plan, marker
from ...openpgp import selection


class ReadOnlyOrigin:
    """Reuse native transport reads without exposing its publication operations."""

    def __init__(self, origin, storage_prefix=PREFIX, *, rehearsal=False):
        from ...r2_objects import prefix
        self.origin = repository_origin(origin)
        prefix(storage_prefix, rehearsal)
        if not rehearsal and (storage_prefix != PREFIX or self.origin != "https://downloads.paintedwolf.dev"):
            raise ValueError("production repository observation requires its protected prefix")
        self.prefix = storage_prefix
        self.public = self.origin + "/" + storage_prefix

    def read(self, name):
        from ...r2_objects import current, prefix
        prefix(name, False)
        with tempfile.TemporaryDirectory(prefix="repository-state-read-") as temporary:
            return current(self.prefix + "/" + name, Path(temporary))

    def verify_public(self, name, path, mutable=False, *, expected_identity):
        from ...r2_objects import public_bytes, prefix
        prefix(name, False)
        public_bytes(self.public + "/" + name, path, mutable, expected_identity=expected_identity)


def observe(manifest, directory, operation, *, catalog=CATALOG, backend, rehearsal=False):
    """No local generation or saved report establishes current delivery availability."""
    if type(rehearsal) is not bool or not rehearsal and Path(catalog).resolve() != CATALOG.resolve():
        raise ValueError("repository observation requires protected production trust or explicit rehearsal")
    if not rehearsal and (backend.origin != "https://downloads.paintedwolf.dev" or backend.prefix != PREFIX):
        raise ValueError("production observation requires the maintained protected public root")
    if backend.public != backend.origin + "/" + backend.prefix:
        raise ValueError("repository observation differs from its independently selected public root")
    receipt, record = copy.deepcopy(manifest), copy.deepcopy(operation)
    family = receipt["family"]
    if family == "pacman":
        from ...pacman_validation import admit
        from ...pacman_publication import configuration
    else:
        from ...repository_validation import admit
        from ...repository_publication import configuration
    admit(receipt, directory, Path(catalog), rehearsal)
    generation = identity(receipt)
    bound = {name: receipt[name] for name in ("family", "channel", "version", "source_commit",
             "inventory_sha256", "signing_generation", "key_fingerprint", "rehearsal")}
    fields = set(bound) | {"format_version", "operation", "generation", "previous", "packages"}
    if family == "pacman":
        fields.add("architecture")
    allowed = [fields] if family == "pacman" else [fields, fields | {"configuration"}]
    if (not isinstance(record, dict) or set(record) not in allowed
            or type(record.get("format_version")) is not int or record["format_version"] != 1
            or record.get("operation") not in OPERATIONS - {"withdraw"}
            or record.get("previous") is not None and (not isinstance(record["previous"], str) or not re.fullmatch("[a-f0-9]{64}", record["previous"]))
            or any(record.get(name) != value for name, value in bound.items())
            or record.get("packages") != receipt["packages"] or record.get("generation") != generation):
        raise ValueError("repository activation differs from its selected signed source and generation")
    channel = f"channels/{family}/{receipt['channel']}"
    if family == "pacman":
        if record.get("architecture") != receipt["architecture"]:
            raise ValueError("pacman activation differs from its selected native CPU")
        channel += "/" + receipt["architecture"]
    else:
        configuration_path = f"generations/{generation}/client.conf"
        if "configuration" in record and record["configuration"] != configuration_path:
            raise ValueError("repository configuration differs from its selected generation")
        record["configuration"] = configuration_path
    pending = marker(activation_plan(record, record.get("previous"), backend.prefix, backend.origin,
                                     "activate", None, None), "completed")
    withdrawals = ["withdrawn/" + generation + ".json", version_marker(receipt).as_posix()]

    def state():
        if (backend.read(channel + "/current.json") != record
                or backend.read(channel + "/pending.json") != pending
                or any(backend.read(name) is not None for name in withdrawals)):
            raise ValueError("repository is stale, withdrawn, incomplete or changed from selected activation")

    def verify(name, path, mutable=False, expected_identity=None):
        if expected_identity is None:
            raw = path.read_bytes()
            expected_identity = {"bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()}
        backend.verify_public(name, path, mutable, expected_identity=expected_identity)

    state()
    signer, public = selection(Path(catalog), rehearsal)
    with tempfile.TemporaryDirectory(prefix="repository-observation-") as temporary:
        work = Path(temporary)
        metadata = {}
        for name, value in (("current.json", record), ("pending.json", pending), ("generation.json", receipt)):
            raw = encoded(value)
            metadata[name] = {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)}
            (work / name).write_bytes(raw)
        verify(channel + "/current.json", work / "current.json", True, metadata["current.json"])
        verify(channel + "/pending.json", work / "pending.json", True, metadata["pending.json"])
        verify(f"generations/{generation}/generation.json", work / "generation.json", expected_identity=metadata["generation.json"])
        trust = work / "trust"; trust.mkdir()
        archive_trust(trust, signer, public, rehearsal)
        for path in trust.iterdir():
            verify(f"generations/{generation}/" + path.name, path)
        config = work / "client.conf"
        config.write_text(configuration(receipt, backend.public + "/" + channel, signer["fingerprint"]))
        verify(f"generations/{generation}/client.conf", config)
        for row in receipt["objects"]:
            path = directory / row["path"]
            expected_identity = {"sha256": row["sha256"], "bytes": row["size"]}
            verify(f"generations/{generation}/" + row["path"], path, expected_identity=expected_identity)
            if family == "rpm":
                continue
            immutable = (row["path"].endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")) if family == "pacman"
                         else "/by-hash/" in row["path"] or row["path"].startswith("pool/"))
            verify(channel + "/" + row["path"], path, not immutable, expected_identity=expected_identity)
        if family == "rpm":
            mirror = work / "mirrorlist"
            mirror.write_text(backend.public + f"/generations/{generation}/\n")
            verify(channel + "/mirrorlist", mirror, True)
        admit(receipt, directory, Path(catalog), rehearsal)
        state()
    facts = {"format_version": 1, **bound, "generation": generation,
             "architecture": receipt.get("architecture", "both"), "base_url": backend.public + "/" + channel,
             "activation_sha256": hashlib.sha256(encoded(record)).hexdigest()}
    return {"facts": facts, "admission_sha256": hashlib.sha256(
        json.dumps(facts, sort_keys=True, separators=(",", ":")).encode()).hexdigest()}
