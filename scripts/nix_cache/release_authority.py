"""Bind a cache request to a fresh installed-release admission in its controller."""
from __future__ import annotations

import base64
import re

from .admission import validate_request


def nar_sha256(value: str) -> str:
    if not isinstance(value, str) or not value.startswith("sha256-"):
        raise ValueError("qualified cache output requires a SHA-256 SRI NAR identity")
    try:
        digest = base64.b64decode(value[7:], validate=True)
    except (ValueError, TypeError) as error:
        raise ValueError("qualified cache output has an invalid NAR identity") from error
    if len(digest) != 32 or base64.b64encode(digest).decode() != value[7:]:
        raise ValueError("qualified cache output has a noncanonical NAR identity")
    return digest.hex()


def authorize(selection: dict, request: dict, verified_cache_report: dict, *, trust_policy: dict) -> bool:
    validate_request(request, trust_policy)
    if set(verified_cache_report["public_keys"]) != set(trust_policy["public_keys"]):
        raise ValueError("Nix cache report differs from the protected controller's trust policy")
    # The protected controller supplies its selected inputs, never an accepted JSON receipt.
    from scripts.linux_release.release_admission import verify_release
    from scripts.linux_release.installed_evidence import artifacts

    checked = verify_release(**selection)
    if checked.get("accepted") is not True:
        raise ValueError("Nix cache publication requires fresh complete release admission")
    if request["mode"] != "production" or request["application_commit"] != selection["source_commit"] or request["version"] != selection["version"]:
        raise ValueError("Nix cache request differs from the admitted release")
    architecture = {"x86_64-linux": "amd64", "aarch64-linux": "arm64"}.get(request["system"])
    selected = [row["identity"] for row in artifacts(selection["artifacts"], selection["source_commit"]).values()
                if row["architecture"] == architecture and row["identity"]["kind"] == "nix-closure"]
    if not selected or {row["store_path"] for row in selected} != set(request["outputs"]):
        raise ValueError("Nix cache outputs differ from every selected architecture artifact")
    if verified_cache_report["request"] != request or verified_cache_report["status"] != "passed":
        raise ValueError("Nix cache report differs from its admitted request")
    for identity in selected:
        if not re.fullmatch(r"[0-9a-f]{64}", identity["sha256"]) or identity["lock_sha256"] != request["flake_lock_sha256"]:
            raise ValueError("Nix cache lock differs from the installed artifact")
        row = verified_cache_report["closure"].get(identity["store_path"])
        if row is None or nar_sha256(row["nar_hash"]) != identity["sha256"]:
            raise ValueError("Nix cache output differs from the installed NAR identity")
    return True
