"""Publish native repository origins through the existing serialized R2 release owner."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
from pathlib import Path, PurePosixPath
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

from .repository_publication import encoded, publish
from scripts.release_semver import compare, parse

ROOT = Path(__file__).resolve().parents[2]


def prefix(value: str, rehearsal: bool):
    path = PurePosixPath(value)
    if (not value or path.is_absolute() or path.as_posix() != value or ".." in path.parts
            or any(not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", part) for part in path.parts)
            or (rehearsal and (len(path.parts) < 2 or path.parts[0] != "release-system-tests"))):
        raise ValueError("repository storage prefix is invalid for the selected trust mode")


def public_bytes(url: str, source: Path, mutable=False):
    expected = hashlib.sha256(source.read_bytes()).hexdigest()
    size = source.stat().st_size
    last = None
    for attempt in range(12):
        try:
            request = urllib.request.Request(url, headers={"Cache-Control": "no-cache, no-store",
                                                           "User-Agent": "painted-wolf-release/1"})
            with urllib.request.urlopen(request, timeout=30) as response:
                control = response.headers.get("Cache-Control", "").lower()
                if (mutable and "no-store" not in control) or (not mutable and "immutable" not in control):
                    raise ValueError("repository public cache policy differs from its object role")
                value, measured = hashlib.sha256(), 0
                while chunk := response.read(1 << 20):
                    measured += len(chunk)
                    if measured > size:
                        raise ValueError("repository public object exceeds its selected size")
                    value.update(chunk)
                if measured == size and value.hexdigest() == expected:
                    return
                last = "public repository bytes differ from selected final bytes"
        except (OSError, ValueError, urllib.error.URLError) as exc:
            last = type(exc).__name__
        if attempt < 11:
            time.sleep(5)
    raise ValueError("repository public object did not converge: " + str(last))


def current(key: str, directory: Path) -> dict | None:
    output = directory / "remote-current.json"
    status = subprocess.check_output(["python3", str(ROOT / "scripts/release_distribution.py"),
                                      "storage-read", "--key", key, "--output", str(output)], text=True).strip()
    if status == "404":
        return None
    if status != "200":
        raise ValueError("cannot establish authenticated repository activation state")
    return json.loads(output.read_text())


def put(key: str, source: Path, mutable=False):
    if mutable:
        subprocess.run(["bunx", "wrangler@4.115.0", "r2", "object", "put", os.environ["R2_BUCKET"] + "/" + key,
                        "--file", str(source), "--content-type", "text/plain",
                        "--cache-control", "no-cache, no-store, must-revalidate", "--remote"], check=True)
    else:
        subprocess.run(["bash", str(ROOT / "scripts/release-r2-immutable-put.sh"), "--key", key,
                        "--file", str(source)], check=True)


def publish_r2(receipt: dict, directory: Path, catalog: Path, storage_prefix: str,
               base_url: str, expected: str | None, rehearsal=False, recover=False) -> dict:
    """The release workflow owns cross-host serialization and final production admission."""
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
        active = current(state_key, work)
        identity = hashlib.sha256(encoded(receipt)).hexdigest()
        if (active or {}).get("generation") != expected:
            if not (active and active.get("generation") == identity and active.get("previous") == expected):
                raise ValueError("R2 repository changed since the reviewed activation plan")
        if active and active.get("rehearsal") is not rehearsal:
            raise ValueError("R2 repository trust mode cannot change")
        if active and not recover and compare(parse(receipt["version"]), parse(active["version"])) < 0:
            raise ValueError("R2 rollback requires explicit retained-generation recovery")
        record = publish(receipt, directory, work / "origin", origin, catalog, None, rehearsal)
        record["previous"], record["recovery"] = expected, recover
        identity = record["generation"]
        generation = work / "origin/generations" / identity
        if recover:
            retained_key = storage_prefix + "/generations/" + identity + "/generation.json"
            retained = current(retained_key, work)
            if retained != receipt:
                raise ValueError("R2 recovery requires the exact previously retained generation")
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
            remote = storage_prefix + "/" + channel + "/" + relative
            put(remote, source, not immutable)
            public_bytes(base_url.rstrip("/") + "/" + remote, source, not immutable)
        # Recheck after uploads: interrupted work leaves only unreferenced retained bytes.
        if current(state_key, work) != active:
            raise ValueError("R2 repository changed while its generation was uploading")
        selected = channels / authority
        remote = storage_prefix + "/" + channel + "/" + authority
        put(remote, selected, True)
        public_bytes(base_url.rstrip("/") + "/" + remote, selected, True)
        state = work / "state.json"
        state.write_bytes(encoded(record))
        put(state_key, state, True)
        public_bytes(base_url.rstrip("/") + "/" + state_key, state, True)
        return {**record, "storage_prefix": storage_prefix, "public_verified": True}


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
        result = publish_r2(receipt, args.directory, args.catalog, args.storage_prefix, args.base_url,
                            args.expected_generation, args.rehearsal, args.recover)
        print(json.dumps(result, indent=2))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Native R2 repository publication failed: {exc}\n")


if __name__ == "__main__":
    main()
