"""Reverify signed cache bytes against the protected controller's dedicated trust."""

from pathlib import Path

from ..installed_evidence import artifacts
from ..release_admission import document
from .cache_policy import POLICY, load

SYSTEMS = {"amd64": "x86_64-linux", "arm64": "aarch64-linux"}


def verify(root: Path, selection: dict) -> list[dict]:
    selected = artifacts(selection["artifacts"], selection["source_commit"])
    systems = {SYSTEMS[row["architecture"]] for row in selected.values()
               if row["identity"]["kind"] == "nix-closure"}
    if not systems:
        return []
    policy = load(POLICY)
    if policy is None:
        # Source-only Nix still passes the same final artifact and installed closure gates.
        return []
    from scripts.nix_cache.admission import validate_request
    from scripts.nix_cache.publication import verified_report
    from scripts.nix_cache.release_authority import authorize
    verified = []
    for system in sorted(systems):
        directory = root / "nix-cache" / system
        report, _ = document(directory / "report.json")
        if not isinstance(report, dict) or not isinstance(report.get("request"), dict):
            raise ValueError("retained Nix cache lacks its exact publication request")
        request = report["request"]
        validate_request(request, policy)
        if (request.get("mode") != "production" or request.get("system") != system
                or report.get("public_keys") != policy["public_keys"]):
            raise ValueError("retained Nix cache differs from protected production trust or system")
        checked = verified_report(directory / "cache", report)
        if authorize(selection, request, checked, trust_policy=policy) is not True:
            raise ValueError("retained Nix cache was not independently admitted")
        verified.append({"system": system, "closure_sha256": checked["closure_sha256"],
                         "outputs": request["outputs"]})
    return verified
