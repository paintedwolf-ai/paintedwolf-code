"""Validate source, artifact, retained-file, and release-variant coverage for installed results."""

from __future__ import annotations

import hashlib
from pathlib import Path
import re

from .support_matrix import variants

SHA256 = re.compile(r"[0-9a-f]{64}\Z")
COMMIT = re.compile(r"[0-9a-f]{40}\Z")
IDENTIFIER = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9._-]*\Z")
STORE_PATH = re.compile(r"/nix/store/[a-z0-9]{32}-[^/\s]+\Z")
SNAPSHOT = {"package-inventory": re.compile(r"[0-9a-f]{64}\Z"), "nixos-revision": re.compile(r"[0-9a-f]{40}\Z")}
STATUSES = {"passed", "failed", "unverified", "skipped"}


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


def mismatches(observed: object, variant: dict) -> list[str]:
    actual = object_fields(observed, {"os_id", "version_id", "snapshot", "display_protocol", "scales_percent"})
    snapshot = object_fields(actual["snapshot"], {"kind", "value", "channel"})
    release, errors = variant["release"], []
    if actual["os_id"] not in release["os_ids"]:
        errors.append("observed distribution is not the declared release")
    if release["version_id"] is not None and actual["version_id"] != release["version_id"]:
        errors.append("observed release version is not the declared release")
    pattern = SNAPSHOT[release["snapshot_kind"]]
    if (snapshot["kind"] != release["snapshot_kind"] or snapshot["channel"] != release["channel"]
            or not isinstance(snapshot["value"], str) or not pattern.fullmatch(snapshot["value"])):
        errors.append("observed snapshot is not the declared release authority")
    if actual["display_protocol"] != variant["display"]["protocol"]:
        errors.append("observed display protocol is not the declared protocol")
    scales = actual["scales_percent"]
    if (not isinstance(scales, list) or any(type(value) is not int for value in scales)
            or sorted(scales) != sorted(variant["scales_percent"])):
        errors.append("observed monitor scales are not the declared scales")
    return errors


def coverage(raw: object, environment: dict, capability: str, capabilities: set[str]) -> tuple[list[str], bool]:
    """Returns the row's coverage errors and whether any variant failed."""
    if not isinstance(raw, list):
        raise ValueError("variants must be a list")
    required = {item["id"]: item for item in variants(environment, capability, capabilities)}
    seen, snapshots, errors, failed = set(), {}, [], False
    for value in raw:
        item = object_fields(value, {"id", "status", "observed"})
        key = item["id"]
        if not isinstance(key, str) or key not in required or key in seen:
            raise ValueError("unknown or duplicate qualification variant")
        seen.add(key)
        if item["status"] not in STATUSES:
            raise ValueError("unknown variant status")
        failed = failed or item["status"] == "failed"
        if item["status"] != "passed":
            errors.append(f"variant {key}: {item['status']}")
        errors.extend(f"variant {key}: {error}" for error in mismatches(item["observed"], required[key]))
        snapshot = item["observed"]["snapshot"]["value"]
        release = required[key]["release"]["id"]
        if isinstance(snapshot, str) and snapshots.setdefault(snapshot, release) != release:
            errors.append(f"variant {key}: one observed snapshot cannot cover two declared releases")
    errors.extend(f"variant {key}: missing" for key in sorted(required.keys() - seen))
    return errors, failed
