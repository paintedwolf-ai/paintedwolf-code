"""Upload retained pacman objects before signed database activation in existing R2."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from .pacman_publication import publish
from .r2_objects import current, prefix, public_bytes, put
from .repository_pending import activation_plan, marker as activation_marker_record
from .repository_objects import encoded
from .repository_operations import OPERATIONS, transition, withdrawal, version_marker


def publish_r2(receipt: dict, directory: Path, catalog: Path, storage_prefix: str,
               base_url: str, expected: str | None, rehearsal=False, operation="publish", phase="activate", *, authorize=None, transport=None) -> dict:
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
    with tempfile.TemporaryDirectory(prefix="paintedwolf-r2-pacman-") as temporary:
        work = Path(temporary)
        record = publish(receipt, directory, work / "origin", origin, catalog, None, rehearsal)
        identity = record["generation"]
        channel = f"channels/pacman/{receipt['channel']}/{receipt['architecture']}"
        state_key = storage_prefix + "/" + channel + "/current.json"
        generation = work / "origin/generations" / identity
        permission()
        active = read(state_key, work)
        record = transition(receipt, active, expected, operation)
        pending_key = storage_prefix + "/" + channel + "/pending.json"
        plan = activation_plan(record, expected, storage_prefix, base_url, phase, active, read(pending_key, work))

        def retain_activation(state):
            payload = activation_marker_record(plan, state)
            target = work / "pending-state.json"
            target.write_bytes(encoded(payload))
            upload(pending_key, target, True)
            verify_public(base_url.rstrip("/") + "/" + pending_key, target, True)
            return payload

        if read(storage_prefix + "/withdrawn/" + identity + ".json", work) is not None:
            raise ValueError("a withdrawn pacman generation cannot be offered again")
        if operation in {"recover", "withdraw"} and read(
                storage_prefix + "/generations/" + identity + "/generation.json", work) != receipt:
            raise ValueError("R2 pacman recovery requires the exact retained healthy generation")
        # An empty withdrawal generation offers no version, so a retry stays possible.
        if receipt["packages"] and read(storage_prefix + "/" + version_marker(receipt).as_posix(), work) is not None:
            raise ValueError("a withdrawn repository version cannot be offered again")
        for source in sorted(generation.iterdir()):
            relative = "generations/" + identity + "/" + source.name
            upload(storage_prefix + "/" + relative, source)
            verify_public(origin + "/" + relative, source)
        selected = work / "origin" / channel
        # Package files and detached package signatures remain immutable across indexes.
        for row in receipt["objects"]:
            name = row["path"]
            if name.endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")):
                relative = channel + "/" + name
                upload(storage_prefix + "/" + relative, selected / name)
                verify_public(origin + "/" + relative, selected / name)
        if phase == "activate":
            pending = retain_activation("pending")
        if phase == "stage":
            if read(state_key, work) != active:
                raise ValueError("repository changed during immutable staging")
            return {**record, "storage_prefix": storage_prefix, "public_verified": True, "publication_phase": "staged"}
        if read(state_key, work) != active:
            raise ValueError("R2 pacman repository changed while objects were uploading")
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
        # A transient database/signature disagreement refuses refresh; retry after both
        # no-store objects converge. Old package objects remain available throughout.
        for suffix in ["files", "db"]:
            for ending in [f".{suffix}.tar.gz.sig", f".{suffix}.sig", f".{suffix}.tar.gz", f".{suffix}"]:
                name = receipt["repository"] + ending
                relative = channel + "/" + name
                upload(storage_prefix + "/" + relative, selected / name, True)
                verify_public(origin + "/" + relative, selected / name, True)
        state = work / "state.json"
        state.write_bytes(encoded(record))
        upload(state_key, state, True)
        verify_public(base_url.rstrip("/") + "/" + state_key, state, True)
        retain_activation("completed")
        return {**record, "storage_prefix": storage_prefix, "public_verified": True,
                "pair_consistency": "required-signature-fail-closed-retry", "publication_phase": "activated"}


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
        print(json.dumps(publish_r2(receipt, args.directory, args.catalog, args.storage_prefix, args.base_url,
                                    args.expected_generation, args.rehearsal, args.operation, args.phase), indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Pacman R2 publication failed: {exc}\n")


if __name__ == "__main__":
    main()
