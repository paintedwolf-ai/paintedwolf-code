"""Validate source, artifact and retained-file identities for installed results."""

from __future__ import annotations

import hashlib
from pathlib import Path
import re

SHA256 = re.compile(r"[0-9a-f]{64}\Z")
COMMIT = re.compile(r"[0-9a-f]{40}\Z")
IDENTIFIER = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9._-]*\Z")
STORE_PATH = re.compile(r"/nix/store/[a-z0-9]{32}-[^/\s]+\Z")


def object_fields(value: object, required: set[str], optional: set[str] = frozenset()) -> dict:
    if not isinstance(value, dict) or not required <= value.keys() or value.keys() - required - optional:
        raise ValueError(f"expected fields {sorted(required)}; optional {sorted(optional)}")
    return value


def source(value: object) -> str:
    if not isinstance(value, str) or not COMMIT.fullmatch(value):
        raise ValueError("source_commit must be an exact lowercase commit SHA")
    return value


def identity(value: object) -> dict:
    record = object_fields(value, {"kind", "sha256"}, {"store_path", "lock_sha256"})
    if not isinstance(record["sha256"], str) or not SHA256.fullmatch(record["sha256"]):
        raise ValueError("artifact sha256 must identify final bytes or closure NAR bytes")
    if record["kind"] == "file":
        if record.keys() != {"kind", "sha256"}:
            raise ValueError("file identity contains closure fields")
    elif record["kind"] == "nix-closure":
        if (not isinstance(record.get("store_path"), str)
                or not STORE_PATH.fullmatch(record["store_path"])
                or not isinstance(record.get("lock_sha256"), str)
                or not SHA256.fullmatch(record["lock_sha256"])):
            raise ValueError("closure identity requires exact store path, NAR sha256 and lock sha256")
    else:
        raise ValueError("unknown artifact identity kind")
    return record


def artifacts(document: object, expected_source: str) -> dict[str, dict]:
    data = object_fields(document, {"format_version", "source_commit", "artifacts"})
    if type(data["format_version"]) is not int or data["format_version"] != 1:
        raise ValueError("unsupported artifact manifest format")
    if source(data["source_commit"]) != expected_source:
        raise ValueError("artifact manifest belongs to another source")
    if not isinstance(data["artifacts"], list) or not data["artifacts"]:
        raise ValueError("artifact manifest must contain the final release artifact set")
    result = {}
    for raw in data["artifacts"]:
        item = object_fields(raw, {"id", "installation", "architecture", "source_commit", "identity"})
        key = item["id"]
        if not isinstance(key, str) or not IDENTIFIER.fullmatch(key) or key in result:
            raise ValueError("invalid or duplicate artifact id")
        if source(item["source_commit"]) != expected_source:
            raise ValueError(f"artifact {key} belongs to another source")
        if any(not isinstance(item[name], str) or not IDENTIFIER.fullmatch(item[name])
               for name in ("installation", "architecture")):
            raise ValueError(f"artifact {key} has invalid installation/architecture")
        identity(item["identity"])
        result[key] = item
    return result


def retained_file(record: object, root: Path) -> None:
    item = object_fields(record, {"path", "sha256"})
    if not isinstance(item["path"], str) or not item["path"]:
        raise ValueError("evidence path is missing")
    relative = Path(item["path"])
    if relative.is_absolute() or ".." in relative.parts:
        raise ValueError("evidence must stay inside the retained campaign directory")
    path = (root / relative).resolve(strict=True)
    if not path.is_relative_to(root.resolve()) or not path.is_file():
        raise ValueError("evidence path escapes the campaign or is not a regular file")
    if not isinstance(item["sha256"], str) or not SHA256.fullmatch(item["sha256"]):
        raise ValueError("evidence sha256 is invalid")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != item["sha256"]:
        raise ValueError("retained evidence bytes changed")
