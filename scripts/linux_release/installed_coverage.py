"""Check a row's installed results against every declared release variant."""

from __future__ import annotations

import re

from . import installed_evidence as evidence
from .support_variants import variants

SNAPSHOT = {"package-inventory": re.compile(r"[0-9a-f]{64}\Z"), "nixos-revision": re.compile(r"[0-9a-f]{40}\Z")}
STATUSES = {"passed", "failed", "unverified", "skipped"}


def mismatches(observed: object, variant: dict) -> list[str]:
    actual = evidence.object_fields(observed, {"os_id", "version_id", "snapshot", "display_protocol", "scales_percent"})
    snapshot = evidence.object_fields(actual["snapshot"], {"kind", "value", "channel"})
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
        item = evidence.object_fields(value, {"id", "status", "observed"})
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
