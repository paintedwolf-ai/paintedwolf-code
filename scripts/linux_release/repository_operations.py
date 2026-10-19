"""Own explicit repository generation publication, recovery, rotation, and withdrawal."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re

from scripts.release_semver import compare, parse
from .repository_objects import encoded, immutable

OPERATIONS = {"publish", "recover", "rotate", "withdraw"}


def identity(receipt: dict) -> str:
    return hashlib.sha256(encoded(receipt)).hexdigest()


def transition(receipt: dict, current: dict | None, expected: str | None, operation="publish") -> dict:
    if operation not in OPERATIONS:
        raise ValueError("unknown repository generation operation")
    selected = identity(receipt)
    if current is None:
        if expected is not None or operation != "publish":
            raise ValueError("a new repository channel requires an initial publish")
    else:
        if (current.get("format_version") != 1 or current.get("operation") not in OPERATIONS
                or not re.fullmatch(r"[0-9a-f]{64}", current.get("generation", ""))
                or type(current.get("signing_generation")) is not int
                or not re.fullmatch(r"[0-9A-F]{40}", current.get("key_fingerprint", ""))):
            raise ValueError("active repository state has an unsupported shape")
        if any(current.get(name) != receipt.get(name) for name in ["family", "channel", "architecture", "rehearsal"]):
            raise ValueError("repository channel identity or trust mode differs from active state")
        if current["generation"] == selected:
            if (current["operation"] == operation
                    and expected in {current["generation"], current.get("previous")}):
                return current
            raise ValueError("generation operation must select a different retained generation")
        if current["generation"] != expected:
            raise ValueError("repository changed since the reviewed generation plan")
        order = compare(parse(receipt["version"]), parse(current["version"]))
        signer_changed = (receipt["signing_generation"], receipt["key_fingerprint"]) != (
            current["signing_generation"], current["key_fingerprint"])
        if operation == "publish" and (signer_changed or order <= 0):
            raise ValueError("ordinary publication must advance with the existing signing identity")
        if operation == "rotate" and (not signer_changed or order < 0
                or receipt["signing_generation"] <= current["signing_generation"]
                or receipt["key_fingerprint"] == current["key_fingerprint"]):
            raise ValueError("rotation requires a newer signing generation and a distinct key")
    record = {"format_version": 1, "operation": operation, "generation": selected, "previous": expected,
              "family": receipt["family"], "channel": receipt["channel"], "version": receipt["version"],
              "source_commit": receipt["source_commit"], "inventory_sha256": receipt["inventory_sha256"],
              "packages": receipt["packages"], "signing_generation": receipt["signing_generation"],
              "key_fingerprint": receipt["key_fingerprint"], "rehearsal": receipt["rehearsal"]}
    if "architecture" in receipt:
        record["architecture"] = receipt["architecture"]
    if operation == "withdraw":
        record["withdrawn_version"] = current["version"]
    return record


def version_marker(receipt: dict) -> Path:
    return Path("withdrawn-versions") / receipt["family"] / receipt["channel"] / receipt.get("architecture", "all") / (receipt["version"] + ".json")


def retained(store: Path, receipt: dict, operation: str):
    selected = identity(receipt)
    if (store / "withdrawn" / (selected + ".json")).exists() or (store / version_marker(receipt)).exists():
        raise ValueError("a withdrawn generation cannot be offered again")
    if operation in {"recover", "withdraw"}:
        path = store / "generations" / selected / "generation.json"
        if not path.is_file() or path.read_bytes() != encoded(receipt):
            raise ValueError("recovery or withdrawal requires the exact retained healthy generation")


def withdrawal(record: dict) -> dict | None:
    if record["operation"] != "withdraw":
        return None
    return {"format_version": 1, "operation": "withdraw", "generation": record["previous"],
            "replacement_generation": record["generation"], "version": record["withdrawn_version"],
            "family": record["family"], "channel": record["channel"],
            **({"architecture": record["architecture"]} if "architecture" in record else {})}


def mark_withdrawal(store: Path, record: dict):
    marker = withdrawal(record)
    if marker is not None:
        immutable(store / "withdrawn" / (marker["generation"] + ".json"), encoded(marker))
        immutable(store / version_marker(marker), encoded(marker))


def archive_trust(generation: Path, signer: dict, public: Path, rehearsal: bool):
    # Preserve only selected public trust facts; never archive unknown catalog fields.
    row = {"generation": signer["generation"], "fingerprint": signer["fingerprint"], "public_key": public.name}
    catalog = {"format_version": 1, "rehearsal": rehearsal, "signing_generation": signer["generation"],
               "generations": [row]}
    immutable(generation / "key-catalog.json", encoded(catalog))
    immutable(generation / public.name, public.read_bytes())
    immutable(generation / "package-key.asc", public.read_bytes())
