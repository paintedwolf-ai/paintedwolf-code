"""Retain verified Nix cache bytes and derive exact final closure identities."""
from __future__ import annotations

import json
from pathlib import Path

from ...nix_closure import nar_digest
from ..trust.cache_policy import load

SYSTEMS = {"x86_64-linux":"amd64", "aarch64-linux":"arm64"}
INSTALLATIONS = ("nixos-system", "nix-package", "nix-profile", "home-manager", "nix-run")


def build(specifications, root, source, *, rehearsal, architectures, source_repository):
    if not isinstance(specifications, list) or len(specifications) > 2:
        raise ValueError("Nix controls require at most one exact application output per native system")
    expected_systems = {system for system, architecture in SYSTEMS.items() if architecture in architectures}
    protected = None if rehearsal else load()
    observed, artifacts, reports = set(), [], []
    for item in specifications:
        if not isinstance(item, dict) or set(item) not in ({"request"}, {"request", "policy", "private_key"}):
            raise ValueError("Nix controls require an exact source request or explicit signed-cache request")
        request = item["request"]
        if not isinstance(request, dict):
            raise ValueError("Nix control request must be an object")
        system = request.get("system")
        if system not in expected_systems or system in observed:
            raise ValueError("Nix controls repeat or omit their selected native system")
        observed.add(system)
        mode = "rehearsal" if rehearsal else "production"
        expected = {"version":source["version"],"application_commit":source["source_commit"],
                    "flake_lock_sha256":source["flake_lock_sha256"]}
        if any(type(request.get(key)) is not type(value) or request[key] != value for key,value in expected.items()):
            raise ValueError("Nix cache request differs from the independently selected immutable application source")
        if set(item) != {"request"} and request.get("mode") != mode:
            raise ValueError("Nix signed-cache mode differs from its selected execution mode")
        outputs = request.get("outputs")
        if not isinstance(outputs,list) or len(outputs) != 1 or not isinstance(outputs[0],str):
            raise ValueError("Nix application controls require one explicit final output per system")
        directory = root / system
        directory.mkdir()
        source_only = set(item) == {"request"}
        if not rehearsal and source_only != (protected is None):
            raise ValueError("Nix controls differ from protected public-cache activation")
        if source_only:
            from scripts.nix_cache.source import describe_verified_source
            report = describe_verified_source(request,source_repository,rehearsal=rehearsal)
            if report.get("status") != "passed" or report.get("content_verified") is not True:
                raise ValueError("Nix source closure content was not independently verified")
        else:
            policy = item["policy"]
            if not rehearsal:
                if policy != protected:
                    raise ValueError("production Nix controls require the protected controller trust policy")
                policy = protected
            from scripts.nix_cache.adapter import construct
            report = construct(request,policy,Path(item["private_key"]),directory / "cache")
        if report.get("request") != request:
            raise ValueError("verified Nix controls changed their selected application request")
        output = outputs[0]
        row = report.get("closure",{}).get(output)
        if not isinstance(row,dict):
            raise ValueError("constructed Nix cache omitted its selected final output")
        identity = {"kind":"nix-closure","sha256":nar_digest(row["nar_hash"]),"store_path":output,
                    "lock_sha256":request["flake_lock_sha256"]}
        for installation in INSTALLATIONS:
            artifacts.append({"id":f"{installation}-{SYSTEMS[system]}-{identity['sha256'][:16]}",
                              "installation":installation,"architecture":SYSTEMS[system],
                              "source_commit":source["source_commit"],"identity":dict(identity)})
        (directory / "report.json").write_text(json.dumps(report,sort_keys=True,indent=2)+"\n")
        reports.append({"system":system,"closure_sha256":report["closure_sha256"],"outputs":outputs,"cache_enabled":not source_only})
    if not rehearsal and observed != expected_systems:
        raise ValueError("production controls require the actual application closure for every selected CPU")
    return artifacts,reports
