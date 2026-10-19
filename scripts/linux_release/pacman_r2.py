"""Upload retained pacman objects before signed database activation in existing R2."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import tempfile

from .pacman_publication import publish
from .r2_objects import current, prefix, public_bytes, put
from .repository_objects import encoded


def publish_r2(receipt: dict, directory: Path, catalog: Path, storage_prefix: str,
               base_url: str, expected: str | None, rehearsal=False, recover=False) -> dict:
    prefix(storage_prefix, rehearsal)
    if not rehearsal:
        raise ValueError("production pacman publication requires release-controller admission")
    origin = base_url.rstrip("/") + "/" + storage_prefix
    with tempfile.TemporaryDirectory(prefix="paintedwolf-r2-pacman-") as temporary:
        work = Path(temporary)
        record = publish(receipt, directory, work / "origin", origin, catalog, None, rehearsal)
        identity = record["generation"]
        channel = f"channels/pacman/{receipt['channel']}/{receipt['architecture']}"
        state_key = storage_prefix + "/" + channel + "/current.json"
        active = current(state_key, work)
        if (active or {}).get("generation") != expected:
            if not (active and active.get("generation") == identity and active.get("previous") == expected):
                raise ValueError("R2 pacman repository changed since the reviewed activation plan")
        if active and active.get("rehearsal") is not rehearsal:
            raise ValueError("R2 pacman trust mode cannot change")
        if active and not recover:
            order = subprocess.check_output(["vercmp", receipt["packages"][0]["native_version"],
                                            active["packages"][0]["native_version"]], text=True).strip()
            if order == "-1":
                raise ValueError("R2 pacman rollback requires explicit retained-generation recovery")
        if recover and current(storage_prefix + "/generations/" + identity + "/generation.json", work) != receipt:
            raise ValueError("R2 pacman recovery requires the exact retained generation")
        record["previous"], record["recovery"] = expected, recover
        generation = work / "origin/generations" / identity
        for source in sorted(generation.iterdir()):
            relative = "generations/" + identity + "/" + source.name
            put(storage_prefix + "/" + relative, source)
            public_bytes(origin + "/" + relative, source)
        selected = work / "origin" / channel
        # Package files and detached package signatures remain immutable across indexes.
        for row in receipt["objects"]:
            name = row["path"]
            if name.endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")):
                relative = channel + "/" + name
                put(storage_prefix + "/" + relative, selected / name)
                public_bytes(origin + "/" + relative, selected / name)
        if current(state_key, work) != active:
            raise ValueError("R2 pacman repository changed while objects were uploading")
        # A transient database/signature disagreement refuses refresh; retry after both
        # no-store objects converge. Old package objects remain available throughout.
        for suffix in ["files", "db"]:
            for ending in [f".{suffix}.tar.gz.sig", f".{suffix}.sig", f".{suffix}.tar.gz", f".{suffix}"]:
                name = receipt["repository"] + ending
                relative = channel + "/" + name
                put(storage_prefix + "/" + relative, selected / name, True)
                public_bytes(origin + "/" + relative, selected / name, True)
        state = work / "state.json"
        state.write_bytes(encoded(record))
        put(state_key, state, True)
        public_bytes(base_url.rstrip("/") + "/" + state_key, state, True)
        return {**record, "storage_prefix": storage_prefix, "public_verified": True,
                "pair_consistency": "required-signature-fail-closed-retry"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--catalog", type=Path, required=True)
    parser.add_argument("--storage-prefix", required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--expected-generation")
    parser.add_argument("--rehearsal", action="store_true")
    parser.add_argument("--recover", action="store_true")
    args = parser.parse_args()
    try:
        receipt = json.loads((args.directory / "generation.json").read_text())
        print(json.dumps(publish_r2(receipt, args.directory, args.catalog, args.storage_prefix, args.base_url,
                                    args.expected_generation, args.rehearsal, args.recover), indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Pacman R2 publication failed: {exc}\n")


if __name__ == "__main__":
    main()
