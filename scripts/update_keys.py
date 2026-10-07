"""Signing generations and permanent bridge feed policy.

Each generation carries two public keys. The artifact key signs release archives in the
signing environment; the feed key signs channel pointers at publication time and never
signs code. Clients embed both keys of their generation.
"""
from __future__ import annotations

import base64
import hashlib
import json
from pathlib import Path

from release_semver import compare, parse

DEFAULT_REGISTRY = Path(__file__).resolve().parent.parent / "packaging/update-keys.json"
GENERATION_FIELDS = {"generation", "public_key", "feed_public_key", "successor", "bridge_version"}


def decode_public_key(key: str) -> bytes:
    decoded = base64.b64decode(key, validate=True).decode("ascii").strip().splitlines()
    if len(decoded) != 2:
        raise ValueError("invalid updater public key document")
    raw = base64.b64decode(decoded[1], validate=True)
    if len(raw) != 42 or raw[:2] != b"Ed":
        raise ValueError("invalid updater public key")
    return raw


def fingerprint(key: str) -> str:
    return hashlib.sha256(decode_public_key(key)).hexdigest()


def key_id(key: str) -> bytes:
    """The eight-byte minisign key id that every signature names."""
    return decode_public_key(key)[2:10]


def load_registry(path: Path = DEFAULT_REGISTRY) -> dict:
    value = json.loads(path.read_text())
    if set(value) != {"schema_version", "signing_generation", "embedded_generation", "generations"} or value["schema_version"] != 1:
        raise ValueError("unsupported update key registry")
    rows = value["generations"]
    if not isinstance(rows, list) or not rows:
        raise ValueError("missing update key generations")
    seen = set()
    prior_bridge = None
    for number, row in enumerate(rows, 1):
        if set(row) != GENERATION_FIELDS or row["generation"] != number:
            raise ValueError("key generations must be complete and ordered")
        for field in ("public_key", "feed_public_key"):
            digest = fingerprint(row[field])
            if digest in seen:
                raise ValueError("key generation reuses a public key")
            seen.add(digest)
        if row["successor"] is None:
            if row["bridge_version"] is not None or number != len(rows):
                raise ValueError("only the latest key generation may remain open")
        elif row["successor"] != number + 1 or number == len(rows):
            raise ValueError("key bridge must lead to the next retained generation")
        else:
            bridge = parse(row["bridge_version"])
            if bridge.channel != "stable":
                raise ValueError("a bridge must be a stable release reachable from both channels")
            if prior_bridge is not None and compare(bridge, prior_bridge) <= 0:
                raise ValueError("successive key bridges must advance the installed version")
            prior_bridge = bridge
    signing, embedded = value["signing_generation"], value["embedded_generation"]
    if type(signing) is not int or type(embedded) is not int or not 1 <= signing <= embedded <= len(rows) or embedded - signing > 1:
        raise ValueError("invalid signing/embedded key generation")
    return value


def generation(registry: dict, number: int) -> dict:
    if type(number) is not int or number < 1 or number > len(registry["generations"]):
        raise ValueError("unknown signing key generation")
    return registry["generations"][number - 1]


def release_binding(registry: dict, version: str) -> dict:
    signing, embedded = registry["signing_generation"], registry["embedded_generation"]
    row = generation(registry, signing)
    if signing != embedded and (row["successor"] != embedded or row["bridge_version"] != version):
        raise ValueError("bridge release must match its registered version and successor")
    if signing == embedded and row["successor"] is not None:
        raise ValueError("ordinary releases cannot publish to a closed signing generation")
    return {
        "signing_generation": signing,
        "embedded_generation": embedded,
        "signing_key_fingerprint": fingerprint(row["public_key"]),
        "embedded_key_fingerprint": fingerprint(generation(registry, embedded)["public_key"]),
    }


def validate_binding(registry: dict, manifest: dict, expected_generation: int | None = None) -> dict:
    binding = manifest["update_keys"]
    if set(binding) != {"signing_generation", "embedded_generation", "signing_key_fingerprint", "embedded_key_fingerprint"}:
        raise ValueError("invalid updater key binding")
    signing, embedded = binding["signing_generation"], binding["embedded_generation"]
    if expected_generation is not None and signing != expected_generation:
        raise ValueError("manifest signed for a different feed generation")
    source, target = generation(registry, signing), generation(registry, embedded)
    if embedded not in (signing, signing + 1):
        raise ValueError("manifest skips a trust generation")
    if binding["signing_key_fingerprint"] != fingerprint(source["public_key"]) or binding["embedded_key_fingerprint"] != fingerprint(target["public_key"]):
        raise ValueError("manifest key fingerprint does not match retained trust")
    if embedded != signing and source["successor"] != embedded:
        raise ValueError("manifest names an unregistered key bridge")
    return binding


def validate_publication(registry: dict, manifest: dict, number: int, *, halt: bool = False) -> None:
    binding = validate_binding(registry, manifest, number)
    source = generation(registry, number)
    if halt:
        return
    if manifest.get("withdrawn", False):
        raise ValueError("withdrawn releases require the controlled halt path")
    if source["successor"] is not None:
        if manifest["version"] != source["bridge_version"] or binding["embedded_generation"] != source["successor"]:
            raise ValueError("closed generation accepts only its registered bridge")
    elif binding["embedded_generation"] != number:
        raise ValueError("unregistered bridge publication")


def feed_key(channel: str, number: int) -> str:
    if channel not in ("stable", "preview") or type(number) is not int or number < 1:
        raise ValueError("invalid update feed")
    return f"updates/{channel}/key-{number}/latest.json"


def validate_bridge_advance(offer: dict, current: dict) -> None:
    if {key: value for key, value in offer.items() if key != "pub_date"} == {key: value for key, value in current.items() if key != "pub_date"}:
        return
    if compare(parse(offer["version"]), parse(current["version"])) <= 0:
        raise ValueError("bridge must exceed every source-channel version")
