"""Execute fixed cases against one exact installed native artifact."""
from __future__ import annotations

import argparse
import hashlib
import importlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys

from . import installed_evidence as evidence
from .installed_cases.products import require_owned_guest
from .installed_cases.records import CaseUnavailable, retain
from .installed_environment import collect
from .installed_runtime import bind
from .support_matrix import CATALOG, expand

SECURITY = {"session", "vault", "confinement", "processes", "browser"}
NIX = {"updates", "development", "distribution"}
FAMILIES = {"documents": "products", "installation": "packages", "conversation": "desktop",
            "git": "desktop", "files": "desktop", "external-files": "desktop", "clipboard": "desktop",
            "input": "desktop", "windowing": "desktop", "accessibility": "desktop",
            "notifications": "desktop", "networking": "desktop", "recovery": "desktop", "performance": "desktop",
            **{capability: "security" for capability in SECURITY},
            **{capability: "packages" for capability in NIX}}


def read(path: Path):
    if not path.is_file() or path.stat().st_size > 16 << 20:
        raise ValueError("installed execution input is not a bounded regular file")
    return json.loads(path.read_text(encoding="utf-8"))


def current_tree(root: Path) -> str:
    result = subprocess.run(["git", "-C", str(root), "rev-parse", "HEAD^{tree}"],
                            capture_output=True, text=True, timeout=15, check=True)
    status = subprocess.run(["git", "-C", str(root), "status", "--porcelain", "--untracked-files=normal", "--",
                             "scripts/linux_release", "packaging/linux/support-matrix.json", "Taskfile.yml",
                             "scripts/verification-plan.json"], capture_output=True, text=True, timeout=15, check=True)
    if status.stdout.strip():
        raise ValueError("installed driver source contains uncommitted changes")
    return evidence.source(result.stdout.strip())


def driver_family(capability: str, installation: str) -> str | None:
    return "nix" if capability in NIX and installation not in {"deb", "rpm", "pacman"} else FAMILIES.get(capability)


def registry(catalog: dict, installation: str) -> dict:
    entries = []
    for capability in catalog["capabilities"]:
        family = driver_family(capability["id"], installation)
        for case in capability["cases"]:
            entries.append({"capability": capability["id"], "case": case, "driver_family": "desktop" if capability["id"] == "installation" and case in {"clean-install-and-launch", "launcher-arguments"} else family})
    return {"format_version": 1, "cases": entries}


def execute_case(binding: dict, capability: str, case: str, root: Path) -> dict:
    family = ("desktop" if capability == "installation" and case in {"clean-install-and-launch", "launcher-arguments"}
              else driver_family(capability, binding["artifact"]["installation"]))
    module_name = "scripts.linux_release.installed_cases." + family if family else None
    if module_name is None or importlib.util.find_spec(module_name) is None:
        return retain(binding, capability, case, root, "unverified", {"reason": "native-case-driver-required"})
    module = importlib.import_module(module_name)
    result = module.execute(binding, capability, case, root)
    if result.get("status") == "unverified" and not result.get("evidence"):
        return retain(binding, capability, case, root, "unverified", {"reason": result.get("reason", "native-case-driver-required")})
    case_result = evidence.object_fields(result, {"id", "status", "evidence"})
    if case_result["id"] != case or case_result["status"] not in {"passed", "failed", "unverified"}:
        raise ValueError("installed case driver returned a different case or invalid status")
    if not isinstance(case_result["evidence"], list) or not case_result["evidence"]:
        raise ValueError("installed case driver omitted retained observations")
    for record in case_result["evidence"]:
        evidence.retained_file(record, root)
    return case_result


def execute(catalog: dict, final: dict, source: str, artifact_id: str, file_root: Path,
            install_root: Path, environment_id: str, output: Path, driver_tree: str,
            nix_program: Path | None = None, case_inputs: dict | None = None) -> dict:
    expected = evidence.artifacts(final, source)
    if artifact_id not in expected:
        raise ValueError("installed execution selected an undeclared final artifact")
    artifact = expected[artifact_id]
    rows = [row for row in expand(catalog) if row["environment"] == environment_id
            and row["installation"] == artifact["installation"] and row["architecture"] == artifact["architecture"]]
    environments = {item["id"]: item for item in catalog["environments"]}
    if not rows or environment_id not in environments:
        raise ValueError("installed execution selected no support rows for its artifact/environment")
    root = Path(__file__).resolve().parents[2]
    if current_tree(root) != evidence.source(driver_tree):
        raise ValueError("installed driver source differs from the independently selected tree")
    guest = require_owned_guest({"install_root": str(install_root), "artifact": artifact})
    binding = bind(final, source, artifact_id, file_root, install_root, nix_program)
    if case_inputs is not None:
        if (not isinstance(case_inputs, dict) or set(case_inputs) - {"native_packages", "nix", "security", "desktop"}
                or any(not isinstance(value, dict) for value in case_inputs.values())):
            raise ValueError("installed case inputs require known typed driver-family objects")
        binding["case_inputs"] = case_inputs
    facts, observations = collect(environments[environment_id], binding, guest)
    if facts.keys() != set(catalog["recorded_environment_facts"]):
        raise ValueError("installed environment collector differs from the selected catalog")
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    retained = output / "evidence"
    retained.mkdir()
    for name, document in (("binding.json", binding), ("environment.json", observations),
                           ("driver-registry.json", registry(catalog, artifact["installation"]))):
        (output / name).write_text(json.dumps(document, sort_keys=True, indent=2) + "\n")
    results = []
    for row in rows:
        cases = []
        for case in row["cases"]:
            try:
                result = execute_case(binding, row["capability"], case, retained)
            except CaseUnavailable as error:
                result = retain(binding, row["capability"], case, retained, "unverified",
                                {"reason": str(error), **getattr(error, "observations", {})}, getattr(error, "streams", None))
            except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
                # A driver may have already retained partial observations; do not replace them.
                reason = {"reason": "native-case-execution-failed", "error_type": type(error).__name__, "error": str(error)}
                existing = retained / row["capability"] / case
                if existing.exists():
                    path = existing / "driver-failure.json"
                    raw = (json.dumps(reason, sort_keys=True) + "\n").encode()
                    with path.open("xb") as stream:
                        stream.write(raw)
                    result = {"id": case, "status": "failed", "evidence": [{"path": path.relative_to(retained).as_posix(), "sha256": hashlib.sha256(raw).hexdigest()}]}
                else:
                    result = retain(binding, row["capability"], case, retained, "failed", reason)
            cases.append(result)
        status = "failed" if any(case["status"] == "failed" for case in cases) else "unverified" if any(case["status"] != "passed" for case in cases) else "passed"
        results.append({"row_id": row["id"], "source_commit": source, "artifact_id": artifact_id,
                        "tier": row["tier"], "execution": "native", "status": status,
                        "cases": cases, "environment_facts": facts})
    campaign = {"format_version": 1, "source_commit": source, "artifacts": final["artifacts"], "results": results}
    (output / "campaign.json").write_text(json.dumps(campaign, indent=2, sort_keys=True) + "\n")
    return campaign


def main(arguments: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--final-artifacts", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--artifact-id", required=True)
    parser.add_argument("--file-root", type=Path, required=True)
    parser.add_argument("--install-root", type=Path, required=True)
    parser.add_argument("--environment-id", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--driver-tree", required=True)
    parser.add_argument("--nix-program", type=Path)
    parser.add_argument("--case-inputs", type=Path)
    args = parser.parse_args(arguments)
    try:
        campaign = execute(read(args.catalog), read(args.final_artifacts), args.source_commit,
                           args.artifact_id, args.file_root, args.install_root, args.environment_id,
                           args.output, args.driver_tree, args.nix_program, read(args.case_inputs) if args.case_inputs else None)
    except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
        print("Installed case execution refused: " + str(error), file=sys.stderr)
        return 2
    print("Installed case campaign: " + str(args.output / "campaign.json"), file=sys.stderr)
    return 0 if all(row["status"] == "passed" for row in campaign["results"]) else 1
