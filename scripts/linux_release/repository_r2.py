"""Publish native repository origins through the existing serialized R2 release owner."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from .repository_pending import activation_plan, marker as activation_marker_record
from .repository_objects import encoded
from .repository_transaction import publish
from .r2_objects import prefix, public_bytes, current, put
from .repository_operations import OPERATIONS, transition, identity, withdrawal, version_marker

def publish_r2(receipt: dict, directory: Path, catalog: Path, storage_prefix: str,
               base_url: str, expected: str | None, rehearsal=False, operation="publish", phase="activate", *, authorize=None, transport=None) -> dict:
    """The release workflow owns cross-host serialization and final production admission."""
    read = current if transport is None else transport.current
    upload = put if transport is None else transport.put
    verify_public = public_bytes if transport is None else transport.public_bytes
    if type(rehearsal) is not bool:
        raise ValueError("repository trust mode must be explicit")
    if phase not in {"stage", "activate"}:
        raise ValueError("unsupported repository publication phase")
    prefix(storage_prefix, rehearsal)
    if not rehearsal and not callable(authorize):
        raise ValueError("production repository publication requires release-controller admission")
    receipt = json.loads(encoded(receipt))
    def permission():
        request = {"manifest": json.loads(encoded(receipt)), "directory": directory, "catalog": catalog,
                   "storage_prefix": storage_prefix, "base_url": base_url, "expected": expected,
                   "operation": operation, "phase": phase}
        if authorize is not None and authorize(request) is not True:
            raise ValueError("production repository publication authorization was refused")

    origin = base_url.rstrip("/") + "/" + storage_prefix
    with tempfile.TemporaryDirectory(prefix="paintedwolf-r2-repository-") as temporary:
        work = Path(temporary)
        channel = f"channels/{receipt['family']}/{receipt['channel']}"
        state_key = storage_prefix + "/" + channel + "/current.json"
        record = publish(receipt, directory, work / "origin", origin, catalog, None, rehearsal)
        generation = work / "origin/generations" / identity(receipt)
        permission()
        active = read(state_key, work)
        record = {**transition(receipt, active, expected, operation), "configuration": record["configuration"]}
        pending_key = storage_prefix + "/" + channel + "/pending.json"
        plan = activation_plan(record, expected, storage_prefix, base_url, phase, active, read(pending_key, work))

        def retain_activation(state):
            payload = activation_marker_record(plan, state)
            target = work / "pending-state.json"
            target.write_bytes(encoded(payload))
            upload(pending_key, target, True)
            verify_public(base_url.rstrip("/") + "/" + pending_key, target, True)
            return payload

        selected = identity(receipt)
        if read(storage_prefix + "/withdrawn/" + selected + ".json", work) is not None:
            raise ValueError("a withdrawn R2 generation cannot be offered again")
        # An empty withdrawal generation offers no version, so a retry stays possible.
        if receipt["packages"] and read(storage_prefix + "/" + version_marker(receipt).as_posix(), work) is not None:
            raise ValueError("a withdrawn repository version cannot be offered again")
        if operation in {"recover", "withdraw"}:
            retained_key = storage_prefix + "/generations/" + selected + "/generation.json"
            if read(retained_key, work) != receipt:
                raise ValueError("R2 recovery or withdrawal requires the exact retained healthy generation")
        # Immutable generation contents precede every mutable channel object.
        for source in sorted(generation.rglob("*")):
            if source.is_file():
                relative = source.relative_to(work / "origin").as_posix()
                upload(storage_prefix + "/" + relative, source)
                verify_public(origin + "/" + relative, source)
        authority = f"dists/{receipt['channel']}/InRelease" if receipt["family"] == "deb" else "mirrorlist"
        channels = work / "origin" / channel
        if phase == "activate":
            pending = retain_activation("pending")
        for source in sorted(channels.rglob("*")):
            if not source.is_file():
                continue
            relative = source.relative_to(channels).as_posix()
            if relative in {authority, "current.json"}:
                continue
            immutable = "/by-hash/" in relative or relative.startswith("pool/")
            if phase == "stage" and not immutable:
                continue
            remote = storage_prefix + "/" + channel + "/" + relative
            upload(remote, source, not immutable)
            verify_public(base_url.rstrip("/") + "/" + remote, source, not immutable)
        # Recheck after uploads: interrupted work leaves only unreferenced retained bytes.
        if phase == "stage":
            if read(state_key, work) != active:
                raise ValueError("repository changed during immutable staging")
            return {**record, "storage_prefix": storage_prefix, "public_verified": True, "publication_phase": "staged"}
        if read(state_key, work) != active:
            raise ValueError("R2 repository changed while its generation was uploading")
        if read(pending_key, work) != pending:
            raise ValueError("repository pending activation changed during publication")
        permission()
        marker = withdrawal(record)
        if marker is not None:
            tombstone = work / "withdrawal.json"
            tombstone.write_bytes(encoded(marker))
            marker_key = storage_prefix + "/withdrawn/" + marker["generation"] + ".json"
            upload(marker_key, tombstone)
            verify_public(base_url.rstrip("/") + "/" + marker_key, tombstone)
            version_key = storage_prefix + "/" + version_marker(marker).as_posix()
            upload(version_key, tombstone)
            verify_public(base_url.rstrip("/") + "/" + version_key, tombstone)
        selected = channels / authority
        remote = storage_prefix + "/" + channel + "/" + authority
        upload(remote, selected, True)
        verify_public(base_url.rstrip("/") + "/" + remote, selected, True)
        state = work / "state.json"
        state.write_bytes(encoded(record))
        upload(state_key, state, True)
        verify_public(base_url.rstrip("/") + "/" + state_key, state, True)
        retain_activation("completed")
        return {**record, "storage_prefix": storage_prefix, "public_verified": True, "publication_phase": "activated"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--storage-prefix", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--expected-generation")
    parser.add_argument("--rehearsal", action="store_true")
    parser.add_argument("--operation", choices=sorted(OPERATIONS), default="publish")
    parser.add_argument("--phase", choices=["stage", "activate"], default="activate")
    args = parser.parse_args()
    try:
        receipt = json.loads((args.directory / "generation.json").read_text())
        result = publish_r2(receipt, args.directory, args.catalog, args.storage_prefix, args.base_url,
                            args.expected_generation, args.rehearsal, args.operation, args.phase)
        print(json.dumps(result, indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native R2 repository publication failed: {exc}\n")


if __name__ == "__main__":
    main()
