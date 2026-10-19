"""Bind Nix output identities to a complete, canonical runtime closure graph."""

from __future__ import annotations

import base64
import hashlib
import json
import re

STORE_PATH = re.compile(r"/nix/store/[0123456789abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
COMMIT = re.compile(r"[0-9a-f]{40}\Z")


def nar_digest(value: str) -> str:
    if not isinstance(value, str) or not value.startswith("sha256-"):
        raise ValueError("closure NAR identity must use SHA-256 SRI")
    decoded = base64.b64decode(value[7:], validate=True)
    if len(decoded) != 32:
        raise ValueError("closure NAR identity has an invalid digest length")
    return decoded.hex()


def store_path(value: str) -> str:
    if not isinstance(value, str) or not STORE_PATH.fullmatch(value):
        raise ValueError("closure contains an invalid absolute store path")
    return value


def describe(path_info: dict, outputs: list[str], source_commit: str, lock_sha256: str) -> dict:
    """Admit metadata only; content, signatures and installed outcomes are separate."""
    if not COMMIT.fullmatch(source_commit) or not SHA256.fullmatch(lock_sha256):
        raise ValueError("closure requires exact source commit and lock identities")
    if (not isinstance(path_info, dict) or not path_info or not outputs
            or len(set(outputs)) != len(outputs)):
        raise ValueError("closure requires distinct outputs and nonempty path-info metadata")
    roots = sorted(store_path(value) for value in outputs)
    nodes = {}
    for path, value in path_info.items():
        store_path(path)
        if not isinstance(value, dict) or value.get("valid", True) is not True:
            raise ValueError("closure path is invalid or has malformed metadata")
        if "path" in value and value["path"] != path:
            raise ValueError("closure path identity disagrees with its metadata key")
        references = value.get("references")
        size = value.get("narSize")
        if (not isinstance(references, list) or len(set(references)) != len(references)
                or type(size) is not int or size <= 0):
            raise ValueError("closure requires distinct references and a positive NAR size")
        nodes[path] = {"path": path, "nar_sha256": nar_digest(value.get("narHash")),
                       "nar_size": size, "references": sorted(store_path(item) for item in references)}
        if path in roots:
            deriver = value.get("deriver")
            if not isinstance(deriver, str) or not store_path(deriver).endswith(".drv"):
                raise ValueError("application outputs require their exact derivation identity")
            nodes[path]["deriver"] = deriver
    reached, pending = set(), list(roots)
    while pending:
        path = pending.pop()
        if path not in nodes:
            raise ValueError(f"closure omits a required store path: {path}")
        if path in reached:
            continue
        reached.add(path)
        pending.extend(nodes[path]["references"])
    if reached != nodes.keys():
        raise ValueError("closure contains unrelated paths outside the selected outputs")
    entries = [nodes[path] for path in sorted(nodes)]
    encoded = json.dumps(entries, sort_keys=True, separators=(",", ":")).encode()
    return {"format_version": 1, "source_commit": source_commit, "lock_sha256": lock_sha256,
            "closure_sha256": hashlib.sha256(encoded).hexdigest(), "paths": entries,
            "outputs": [{"store_path": path, "identity": {
                "kind": "nix-closure", "sha256": nodes[path]["nar_sha256"],
                "store_path": path, "lock_sha256": lock_sha256}} for path in roots],
            "admission": "metadata-complete", "content_verified": False,
            "cache_trust_verified": False, "installed_qualified": False}


def bind(expected: dict, actual: dict) -> None:
    """Reject a result from another source, lock, derivation, output or closure."""
    fields = ("format_version", "source_commit", "lock_sha256", "closure_sha256", "outputs", "paths")
    if any(field not in expected or expected[field] != actual.get(field) for field in fields):
        raise ValueError("Nix closure differs from the separately selected release artifact")
