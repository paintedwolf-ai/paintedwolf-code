"""Bind a cache request to the selected release and its fresh exact-source Qualification record."""
from __future__ import annotations

from scripts.linux_release import release_qualification

from .admission import validate_request


def authorize(selection: dict, request: dict, verified_cache_report: dict, *, trust_policy: dict,
              qualified=release_qualification.require, rehearsal=False) -> bool:
    if type(rehearsal) is not bool:
        raise ValueError("Nix cache requires an explicit controller trust mode")
    validate_request(request, trust_policy)
    if set(verified_cache_report["public_keys"]) != set(trust_policy["public_keys"]):
        raise ValueError("Nix cache report differs from the protected controller's trust policy")
    if (request["mode"] != ("rehearsal" if rehearsal else "production") or request["application_commit"] != selection["source_commit"]
            or request["version"] != selection["version"]):
        raise ValueError("Nix cache request differs from the selected release")
    if verified_cache_report["request"] != request or verified_cache_report["status"] != "passed":
        raise ValueError("Nix cache report differs from its selected request")
    # The controller supplies the selected release; a retained receipt is never authority.
    qualified(selection["source_commit"])
    return True
