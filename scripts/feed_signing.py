#!/usr/bin/env python3
"""Select a generation's feed credential and sign a fully prepared pointer."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

from feed_signature import check
from update_keys import load_registry


def credentials(document: str) -> dict:
    try:
        value = json.loads(document)
        if set(value) != {"format_version", "generations"} or value["format_version"] != 1:
            raise ValueError()
        rows = value["generations"]
        if not isinstance(rows, dict) or not rows:
            raise ValueError()
        for number, row in rows.items():
            if not number.isascii() or not number.isdigit() or str(int(number)) != number or int(number) < 1:
                raise ValueError()
            if set(row) != {"private_key", "password"} or not isinstance(row["private_key"], str) or not row["private_key"]:
                raise ValueError()
            if not isinstance(row["password"], str):
                raise ValueError()
        return rows
    except (ValueError, TypeError, AttributeError):
        raise ValueError("FEED_SIGNING_KEYS_JSON must contain format_version 1 and generation credentials") from None


def signing_registry(path: Path | None, prefix: str) -> dict:
    if path is not None and (not prefix.startswith("release-system-tests/") or ".." in prefix.split("/")):
        raise ValueError("a fixture registry requires an isolated release-system-tests prefix")
    return load_registry(path) if path else load_registry()


def sign(pointer: Path, number: int, registry: dict) -> Path:
    rows = credentials(os.environ.get("FEED_SIGNING_KEYS_JSON", ""))
    row = rows.get(str(number))
    if row is None:
        raise ValueError(f"no feed signing credential for generation {number}")
    version = json.loads(pointer.read_text())["version"]
    env = dict(os.environ)
    env.pop("FEED_SIGNING_KEYS_JSON", None)
    env["TAURI_SIGNING_PRIVATE_KEY"] = row["private_key"]
    env["TAURI_SIGNING_PRIVATE_KEY_PASSWORD"] = row["password"]
    # Signer diagnostics can contain credentials.
    result = subprocess.run(["bun", "run", "tauri", "signer", "sign", "--app-version", version, str(pointer.resolve())],
                            cwd=Path(__file__).resolve().parent.parent / "lycaon-den", env=env, capture_output=True)
    if result.returncode:
        raise ValueError(f"feed signing failed for generation {number}")
    signature = Path(str(pointer) + ".sig")
    check(signature.read_text(), file=pointer.name, version=version, number=number, registry=registry)
    return signature


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--file", type=Path, required=True)
    parser.add_argument("--generation", type=int, required=True)
    parser.add_argument("--registry", type=Path)
    parser.add_argument("--storage-prefix", default="")
    args = parser.parse_args()
    try:
        sign(args.file, args.generation, signing_registry(args.registry, args.storage_prefix))
    except (OSError, ValueError, KeyError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
