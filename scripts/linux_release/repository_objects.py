"""Canonical inventory identities, immutable storage, and pending activation markers."""

import hashlib
import json
import os
from pathlib import Path
import re
import tempfile

FIELDS = {"format_version", "generation", "activation_sha256", "expected", "operation",
          "storage_prefix", "base_url", "phase"}


def encoded(value):
    return (json.dumps(value, sort_keys=True, indent=2) + "\n").encode()


def immutable(path: Path, data: bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as staging:
        staging.write(data)
        staging.flush()
        os.fsync(staging.fileno())
        staged = Path(staging.name)
    try:
        try:
            os.link(staged, path)
        except FileExistsError:
            if path.is_symlink() or path.read_bytes() != data:
                raise ValueError("immutable repository object collision")
    finally:
        staged.unlink()


def replace(path: Path, data: bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as staging:
        staging.write(data)
        staging.flush()
        os.fsync(staging.fileno())
        staged = Path(staging.name)
    os.replace(staged, path)


def activation_plan(record, expected, storage_prefix, base_url, phase, active, pending):
    plan = {"format_version": 1, "generation": record["generation"],
            "activation_sha256": hashlib.sha256(encoded(record)).hexdigest(), "expected": expected,
            "operation": record["operation"], "storage_prefix": storage_prefix,
            "base_url": base_url, "phase": phase}
    if pending is None:
        return plan
    if (not isinstance(pending, dict) or set(pending) != {"format_version", "state", "plan"}
            or type(pending["format_version"]) is not int or pending["format_version"] != 1
            or pending["state"] not in {"pending", "completed"}
            or not isinstance(pending["plan"], dict) or set(pending["plan"]) != FIELDS):
        raise ValueError("repository pending activation has an unsupported shape")
    previous = pending["plan"]
    if (type(previous["format_version"]) is not int or previous["format_version"] != 1
            or previous["phase"] != "activate" or previous["operation"] not in {"publish", "recover", "rotate", "withdraw"}
            or any(not isinstance(previous[field], str) or not re.fullmatch("[0-9a-f]{64}", previous[field])
                   for field in ["generation", "activation_sha256"])
            or (previous["expected"] is not None and (not isinstance(previous["expected"], str)
                or not re.fullmatch("[0-9a-f]{64}", previous["expected"])))
            or previous["storage_prefix"] != storage_prefix or previous["base_url"] != base_url):
        raise ValueError("repository pending activation differs from the protected origin")
    if pending["state"] == "pending":
        if previous != plan:
            raise ValueError("repository pending activation requires exact operation recovery")
    elif active is None or hashlib.sha256(encoded(active)).hexdigest() != previous["activation_sha256"]:
        raise ValueError("completed repository activation differs from the active authority state")
    return plan


def marker(plan, state):
    if state not in {"pending", "completed"}:
        raise ValueError("unsupported repository activation marker state")
    return {"format_version": 1, "state": state, "plan": plan}
