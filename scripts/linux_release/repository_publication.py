"""Publish verified native generations and atomically select retained configurations."""

from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile
from urllib.parse import urlsplit

from .repository_objects import encoded, immutable, replace
from .repository_validation import admit
from .openpgp import selection
from scripts.release_semver import compare, parse


def configuration(receipt: dict, url: str, fingerprint: str) -> str:
    if receipt["family"] == "deb":
        return (f"Types: deb\nURIs: {url}/\nSuites: {receipt['channel']}\nComponents: main\n"
                "Signed-By: /etc/apt/keyrings/painted-wolf-code.asc\n")
    return (f"[painted-wolf-code-{receipt['channel']}]\nname=Painted Wolf Code\nmirrorlist={url}/mirrorlist\n"
            "enabled=1\ngpgcheck=1\nrepo_gpgcheck=1\ngpgkey=file:///etc/pki/rpm-gpg/painted-wolf-code.asc\n"
            f"# Verify the imported key fingerprint: {fingerprint}\n")


def publish(receipt: dict, directory: Path, store: Path, base_url: str, catalog: Path,
            expected: str | None, rehearsal=False, recover=False) -> dict:
    """One lock owns immutable upload, compare-and-select, and recovery on a local origin."""
    url = urlsplit(base_url)
    if url.scheme != "https" or not url.netloc or url.query or url.fragment or url.username:
        raise ValueError("repository origin requires an HTTPS URL without credentials or query")
    admit(receipt, directory, catalog, rehearsal)
    key, public = selection(catalog, rehearsal)
    identity = hashlib.sha256(encoded(receipt)).hexdigest()
    family, channel = receipt["family"], receipt["channel"]
    store.mkdir(parents=True, exist_ok=True)
    if store.is_symlink():
        raise ValueError("repository origin cannot be a symlink")
    with (store / ".publication.lock").open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        pointer = store / "channels" / family / channel / "current.json"
        current = json.loads(pointer.read_text()) if pointer.exists() else None
        if current and current.get("rehearsal") is not rehearsal:
            raise ValueError("repository channel trust mode cannot be changed")
        if (current or {}).get("generation") != expected:
            if not (current and current.get("generation") == identity and current.get("previous") == expected):
                raise ValueError("active repository changed since the reviewed publication plan")
        if current and not recover and compare(parse(receipt["version"]), parse(current["version"])) < 0:
            raise ValueError("repository rollback requires explicit retained-generation recovery")
        generation = store / "generations" / identity
        retained = generation / "generation.json"
        if recover and not retained.is_file():
            raise ValueError("recovery requires a retained, previously published generation")
        # Nothing references the generation until all bytes have been copied and re-admitted.
        for row in receipt["objects"]:
            immutable(generation / row["path"], (directory / row["path"]).read_bytes())
        immutable(retained, encoded(receipt))
        immutable(generation / "package-key.asc", public.read_bytes())
        # Re-admission excludes the separately distributed key from the format inventory.
        with tempfile.TemporaryDirectory(prefix="paintedwolf-publication-readback-") as temporary:
            readback = Path(temporary)
            for row in receipt["objects"]:
                target = readback / row["path"]
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(generation / row["path"], target)
            admit(receipt, readback, catalog, rehearsal)
        config = configuration(receipt, base_url.rstrip("/") + f"/channels/{family}/{channel}", key["fingerprint"])
        immutable(generation / "client.conf", config.encode())
        record = {"format_version": 1, "generation": identity, "previous": expected,
                  "family": family, "channel": channel, "version": receipt["version"],
                  "source_commit": receipt["source_commit"], "key_fingerprint": key["fingerprint"],
                  "inventory_sha256": receipt["inventory_sha256"], "packages": receipt["packages"],
                  "rehearsal": rehearsal, "recovery": recover,
                  "configuration": f"generations/{identity}/client.conf"}
        # The package manager consumes one final authority object. All objects reached by
        # an older authority remain available after promotion or recovery.
        if family == "deb":
            authority = f"dists/{channel}/InRelease"
            for row in receipt["objects"]:
                name = row["path"]
                if name == authority:
                    continue
                target = pointer.parent / name
                data = (generation / name).read_bytes()
                if "/by-hash/" in name or name.startswith("pool/"):
                    immutable(target, data)
                else:
                    replace(target, data)
            replace(pointer.parent / authority, (generation / authority).read_bytes())
        else:
            replace(pointer.parent / "mirrorlist",
                    (base_url.rstrip("/") + "/generations/" + identity + "/\n").encode())
        replace(pointer, encoded(record))
        parent = os.open(pointer.parent, os.O_RDONLY)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
        return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["directory", "store", "catalog"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--expected-generation")
    parser.add_argument("--rehearsal", action="store_true")
    parser.add_argument("--recover", action="store_true")
    args = parser.parse_args()
    try:
        receipt = json.loads((args.directory / "generation.json").read_text())
        print(json.dumps(publish(receipt, args.directory, args.store, args.base_url, args.catalog,
                                 args.expected_generation, args.rehearsal, args.recover), indent=2))
    except (OSError, ValueError, KeyError) as exc:
        parser.exit(1, f"Native repository publication failed: {exc}\n")


if __name__ == "__main__":
    main()
