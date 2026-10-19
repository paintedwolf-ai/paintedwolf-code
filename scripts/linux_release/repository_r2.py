"""Publish native repository origins through the existing serialized R2 release owner."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from .repository_objects import encoded
from .repository_publication import publish
from .r2_objects import prefix, public_bytes, current, put
from .repository_operations import OPERATIONS, transition, identity, withdrawal, version_marker

def publish_r2(receipt: dict, directory: Path, catalog: Path, storage_prefix: str,
               base_url: str, expected: str | None, rehearsal=False, operation="publish", phase="activate") -> dict:
    """The release workflow owns cross-host serialization and final production admission."""
    if phase not in {"stage", "activate"}:
        raise ValueError("unsupported repository publication phase")
    prefix(storage_prefix, rehearsal)
    # Until the release controller supplies final-artifact admission, this entry point
    # exposes only the isolated rehearsal namespace.
    if not rehearsal:
        raise ValueError("production repository publication requires release-controller admission")
    origin = base_url.rstrip("/") + "/" + storage_prefix
    with tempfile.TemporaryDirectory(prefix="paintedwolf-r2-repository-") as temporary:
        work = Path(temporary)
        channel = f"channels/{receipt['family']}/{receipt['channel']}"
        state_key = storage_prefix + "/" + channel + "/current.json"
        record = publish(receipt, directory, work / "origin", origin, catalog, None, rehearsal)
        active = current(state_key, work)
        record = {**transition(receipt, active, expected, operation), "configuration": record["configuration"]}
        selected = identity(receipt)
        if current(storage_prefix + "/withdrawn/" + selected + ".json", work) is not None:
            raise ValueError("a withdrawn R2 generation cannot be offered again")
        if current(storage_prefix + "/" + version_marker(receipt).as_posix(), work) is not None:
            raise ValueError("a withdrawn repository version cannot be offered again")
        generation = work / "origin/generations" / selected
        if operation in {"recover", "withdraw"}:
            retained_key = storage_prefix + "/generations/" + selected + "/generation.json"
            if current(retained_key, work) != receipt:
                raise ValueError("R2 recovery or withdrawal requires the exact retained healthy generation")
        # Immutable generation contents precede every mutable channel object.
        for source in sorted(generation.rglob("*")):
            if source.is_file():
                relative = source.relative_to(work / "origin").as_posix()
                put(storage_prefix + "/" + relative, source)
                public_bytes(origin + "/" + relative, source)
        authority = f"dists/{receipt['channel']}/InRelease" if receipt["family"] == "deb" else "mirrorlist"
        channels = work / "origin" / channel
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
            put(remote, source, not immutable)
            public_bytes(base_url.rstrip("/") + "/" + remote, source, not immutable)
        # Recheck after uploads: interrupted work leaves only unreferenced retained bytes.
        if phase == "stage":
            if current(state_key, work) != active:
                raise ValueError("repository changed during immutable staging")
            return {**record, "storage_prefix": storage_prefix, "public_verified": True, "publication_phase": "staged"}
        if current(state_key, work) != active:
            raise ValueError("R2 repository changed while its generation was uploading")
        marker = withdrawal(record)
        if marker is not None:
            tombstone = work / "withdrawal.json"
            tombstone.write_bytes(encoded(marker))
            marker_key = storage_prefix + "/withdrawn/" + marker["generation"] + ".json"
            put(marker_key, tombstone)
            public_bytes(base_url.rstrip("/") + "/" + marker_key, tombstone)
            version_key = storage_prefix + "/" + version_marker(marker).as_posix()
            put(version_key, tombstone)
            public_bytes(base_url.rstrip("/") + "/" + version_key, tombstone)
        selected = channels / authority
        remote = storage_prefix + "/" + channel + "/" + authority
        put(remote, selected, True)
        public_bytes(base_url.rstrip("/") + "/" + remote, selected, True)
        state = work / "state.json"
        state.write_bytes(encoded(record))
        put(state_key, state, True)
        public_bytes(base_url.rstrip("/") + "/" + state_key, state, True)
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
