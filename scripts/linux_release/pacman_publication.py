"""Retain signed pacman generations and activate database pairs with fail-closed retries."""

from __future__ import annotations

import argparse
import fcntl
import json
import hashlib
from pathlib import Path
from urllib.parse import urlsplit

from .openpgp import selection
from .pacman_validation import admit
from .repository_objects import encoded, immutable, replace
from .repository_operations import OPERATIONS, identity, transition, retained, mark_withdrawal, archive_trust


def configuration(receipt: dict, url: str, fingerprint: str) -> str:
    return (f"[{receipt['repository']}]\nSigLevel = Required DatabaseRequired\nServer = {url}/\n"
            f"# Verify and locally trust the selected package key fingerprint: {fingerprint}\n")


def stage(receipt: dict, directory: Path, store: Path, base_url: str, catalog: Path,
          rehearsal=False) -> tuple[str, Path]:
    url = urlsplit(base_url)
    if url.scheme != "https" or not url.netloc or url.query or url.fragment or url.username:
        raise ValueError("pacman repository origin requires an HTTPS URL without credentials or query")
    admit(receipt, directory, catalog, rehearsal)
    selected, public = selection(catalog, rehearsal)
    selected_identity = identity(receipt)
    generation = store / "generations" / selected_identity
    for row in receipt["objects"]:
        immutable(generation / row["path"], (directory / row["path"]).read_bytes())
    immutable(generation / "generation.json", encoded(receipt))
    archive_trust(generation, selected, public, rehearsal)
    channel = f"channels/pacman/{receipt['channel']}/{receipt['architecture']}"
    immutable(generation / "client.conf", configuration(receipt, base_url.rstrip("/") + "/" + channel,
                                                        selected["fingerprint"]).encode())
    # Aliases are regular uploaded bytes; verify the exact retained archive inventory.
    for row in receipt["objects"]:
        target = generation / row["path"]
        if target.stat().st_size != row["size"] or hashlib.sha256(target.read_bytes()).hexdigest() != row["sha256"]:
            raise ValueError("retained pacman object differs from the selected inventory")
    return selected_identity, generation


def activate(receipt: dict, generation: Path, channel: Path):
    # Pacman fetches database and signature separately. Publish signatures first,
    # keep every package immutable, and require readers to retry any mismatched pair.
    for row in receipt["objects"]:
        name = row["path"]
        if name.endswith((".pkg.tar.zst", ".pkg.tar.zst.sig")):
            immutable(channel / name, (generation / name).read_bytes())
    for suffix in ["files", "db"]:
        for ending in [f".{suffix}.tar.gz.sig", f".{suffix}.sig", f".{suffix}.tar.gz", f".{suffix}"]:
            name = receipt["repository"] + ending
            replace(channel / name, (generation / name).read_bytes())


def publish(receipt: dict, directory: Path, store: Path, base_url: str, catalog: Path,
            expected: str | None, rehearsal=False, operation="publish") -> dict:
    admit(receipt, directory, catalog, rehearsal)
    store.mkdir(parents=True, exist_ok=True)
    if store.is_symlink():
        raise ValueError("pacman origin cannot be a symlink")
    with (store / ".publication.lock").open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        channel = store / "channels/pacman" / receipt["channel"] / receipt["architecture"]
        pointer = channel / "current.json"
        current = json.loads(pointer.read_text()) if pointer.exists() else None
        record = transition(receipt, current, expected, operation)
        retained(store, receipt, operation)
        _, generation = stage(receipt, directory, store, base_url, catalog, rehearsal)
        mark_withdrawal(store, record)
        activate(receipt, generation, channel)
        replace(pointer, encoded(record))
        return record


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["directory", "store", "catalog"]:
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--expected-generation")
    parser.add_argument("--rehearsal", action="store_true")
    parser.add_argument("--operation", choices=sorted(OPERATIONS), default="publish")
    args = parser.parse_args()
    try:
        receipt = json.loads((args.directory / "generation.json").read_text())
        print(json.dumps(publish(receipt, args.directory, args.store, args.base_url, args.catalog,
                                 args.expected_generation, args.rehearsal, args.operation), indent=2))
    except (OSError, ValueError, KeyError) as exc:
        parser.exit(1, f"Pacman publication failed: {exc}\n")


if __name__ == "__main__":
    main()
