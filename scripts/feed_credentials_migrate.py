#!/usr/bin/env python3
"""Verify feed credentials and export only a GitHub-encrypted secret request."""
from __future__ import annotations

import base64
import ctypes
import ctypes.util
import json
import os
from pathlib import Path
import sys
import tempfile

from feed_signing import credentials, sign
from update_keys import load_registry


def legacy_map(registry: dict) -> str:
    if os.environ.get("FEED_SIGNING_KEYS_JSON"):
        raise ValueError("the feed key map already exists; use verify instead")
    if [row["generation"] for row in registry["generations"]] != [1]:
        raise ValueError("legacy migration requires exactly generation 1")
    private_key = os.environ.get("FEED_SIGNING_PRIVATE_KEY", "")
    if not private_key:
        raise ValueError("the legacy feed private key is missing")
    return json.dumps({"format_version": 1, "generations": {"1": {
        "private_key": private_key,
        "password": os.environ.get("FEED_SIGNING_PRIVATE_KEY_PASSWORD", ""),
    }}})


def prove(document: str, registry: dict) -> None:
    rows = credentials(document)
    if any(str(row["generation"]) not in rows for row in registry["generations"]):
        raise ValueError("the feed key map lacks a retained generation")
    previous = os.environ.get("FEED_SIGNING_KEYS_JSON")
    os.environ["FEED_SIGNING_KEYS_JSON"] = document
    try:
        with tempfile.TemporaryDirectory(prefix="feed-credentials-") as directory:
            for row in registry["generations"]:
                number = row["generation"]
                pointer = Path(directory) / f"latest-stable-key-{number}.json"
                pointer.write_text('{"version":"0.0.0"}')
                sign(pointer, number, registry)
    finally:
        if previous is None:
            os.environ.pop("FEED_SIGNING_KEYS_JSON", None)
        else:
            os.environ["FEED_SIGNING_KEYS_JSON"] = previous


def sealed_request(document: str, target: str) -> dict:
    value = json.loads(target)
    public_key = base64.b64decode(value["key"], validate=True)
    key_id = value["key_id"]
    if len(public_key) != 32 or not isinstance(key_id, str) or not key_id.isascii() or not key_id.isdigit():
        raise ValueError("invalid GitHub environment encryption key")
    library = ctypes.util.find_library("sodium")
    if not library:
        raise ValueError("libsodium is required")
    sodium = ctypes.CDLL(library)
    sodium.sodium_init.restype = ctypes.c_int
    sodium.crypto_box_seal.argtypes = [ctypes.c_void_p, ctypes.c_void_p, ctypes.c_ulonglong, ctypes.c_void_p]
    sodium.crypto_box_seal.restype = ctypes.c_int
    if sodium.sodium_init() < 0:
        raise ValueError("libsodium initialization failed")
    message = document.encode()
    ciphertext = ctypes.create_string_buffer(len(message) + 48)
    if sodium.crypto_box_seal(ciphertext, message, len(message), public_key) != 0:
        raise ValueError("GitHub secret encryption failed")
    return {"key_id": key_id, "encrypted_value": base64.b64encode(ciphertext.raw).decode()}


def main() -> None:
    try:
        registry = load_registry()
        mode = os.environ.get("FEED_CREDENTIAL_MODE")
        if mode == "verify":
            prove(os.environ.get("FEED_SIGNING_KEYS_JSON", ""), registry)
        elif mode == "migrate":
            document = legacy_map(registry)
            prove(document, registry)
            request = sealed_request(document, os.environ["FEED_CREDENTIAL_ENCRYPTION_KEY"])
            target = Path(os.environ["RUNNER_TEMP"]) / "feed-secret-request.json"
            target.write_text(json.dumps(request))
        else:
            raise ValueError("select migrate or verify")
    except (OSError, ValueError, KeyError, TypeError):
        # Do not include exception details: credential-bearing input may appear there.
        print("Feed credential maintenance failed; no secret request was published.", file=sys.stderr)
        raise SystemExit(1) from None
    print("Feed signing credentials verified.")


if __name__ == "__main__":
    main()
