#!/usr/bin/env python3
"""Check a signed feed pointer's bindings before it is published.

`tauri signer sign` writes a base64 minisign document whose trusted comment is
`timestamp:<unix>\tfile:<name>\tversion:<version>`. Cryptographic verification happens
on clients; this check establishes that the pointer was signed under the right name and
version and by the registered feed key, including isolated rehearsals.
"""
from __future__ import annotations

import argparse
import base64
import binascii
from pathlib import Path
import sys

from update_keys import generation, key_id, load_registry

TRUSTED_COMMENT = "trusted comment: "


def parse_document(document: str) -> tuple[bytes, dict[str, str]]:
    try:
        text = base64.b64decode(document.strip(), validate=True).decode("utf-8")
    except (binascii.Error, UnicodeDecodeError) as exc:
        raise ValueError(f"feed signature is not a base64 UTF-8 document: {exc}") from exc
    lines = text.splitlines()
    if len(lines) != 4 or not lines[2].startswith(TRUSTED_COMMENT):
        raise ValueError("feed signature does not have the minisign document shape")
    try:
        signature = base64.b64decode(lines[1], validate=True)
    except binascii.Error as exc:
        raise ValueError("feed signature line is not base64") from exc
    # "ED" is minisign's prehashed Ed25519 mode, which the Tauri signer emits.
    if len(signature) != 74 or signature[:2] not in (b"Ed", b"ED"):
        raise ValueError("feed signature is not an Ed25519 minisign signature")
    fields: dict[str, str] = {}
    for field in lines[2][len(TRUSTED_COMMENT):].split("\t"):
        name, separator, value = field.partition(":")
        if not separator or name in fields:
            raise ValueError("feed signature comment is malformed")
        fields[name] = value
    if set(fields) != {"timestamp", "file", "version"} or not fields["timestamp"].isdigit():
        raise ValueError("feed signature comment must bind timestamp, file, and version")
    return signature[2:10], fields


def check(document: str, *, file: str, version: str, number: int, registry: dict | None = None) -> int:
    signer, fields = parse_document(document)
    if fields["file"] != file:
        raise ValueError(f"feed signature is bound to {fields['file']!r}, not {file!r}")
    if fields["version"] != version:
        raise ValueError(f"feed signature is bound to version {fields['version']}, not {version}")
    if signer != key_id(generation(registry if registry is not None else load_registry(), number)["feed_public_key"]):
        raise ValueError(f"feed signature was not made by the registered feed key of generation {number}")
    return int(fields["timestamp"])


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--signature", type=Path, required=True)
    parser.add_argument("--file", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--generation", type=int, required=True)
    parser.add_argument("--registry", type=Path)
    parser.add_argument("--storage-prefix", default="")
    args = parser.parse_args()
    try:
        from feed_signing import signing_registry
        registry = signing_registry(args.registry, args.storage_prefix)
        timestamp = check(args.signature.read_text(encoding="utf-8"), file=args.file, version=args.version,
                          number=args.generation, registry=registry)
    except (OSError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(timestamp)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
