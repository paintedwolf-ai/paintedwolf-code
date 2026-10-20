"""Execute fixed cases against one exact installed native artifact."""
from __future__ import annotations

import argparse
import hashlib
import importlib
import importlib.util
import json
import re
from pathlib import Path
import subprocess
import sys

from . import installed_evidence as evidence
from .installed_cases.products import require_owned_guest
from .installed_cases.records import CaseFailed, CaseUnavailable, retain
from .installed_environment import collect
from .installed_runtime import bind
from .support_matrix import CATALOG, expand
from .installed_variants import requirements, observe
from .installed_monitors import prepare as prepare_monitors

SECURITY = {"session", "vault", "confinement", "processes", "browser"}
NIX = {"updates", "development", "distribution"}
FAMILIES = {"documents": "products", "installation": "packages", "conversation": "conversation",
            "git": "desktop", "files": "workspace", "external-files": "desktop", "clipboard": "desktop",
            "input": "desktop", "windowing": "desktop", "accessibility": "desktop",
            "notifications": "desktop", "networking": "desktop", "recovery": "desktop", "performance": "performance",
            **{capability: "security" for capability in SECURITY},
            **{capability: "packages" for capability in NIX}}


def read(path: Path):
    maximum = (128 if path.name == "campaign.json" else 16) << 20
    if not path.is_file() or path.stat().st_size > maximum:
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
    return "nix_workflows" if capability in NIX and installation not in {"deb", "rpm", "pacman"} else FAMILIES.get(capability)


def case_family(capability: str, case: str, installation: str) -> str | None:
    if capability == "installation" and case in {"clean-install-and-launch", "launcher-arguments"}:
        return "desktop"
    if capability == "documents" and case in {"redacted-send", "ocr-and-documents"}:
        return "documents"
    return driver_family(capability, installation)


def registry(catalog: dict, installation: str) -> dict:
    entries = []
    for capability in catalog["capabilities"]:
        for case in capability["cases"]:
            entries.append({"capability": capability["id"], "case": case, "driver_family": case_family(capability["id"], case, installation)})
    return {"format_version": 1, "cases": entries}


def execute_case(binding: dict, capability: str, case: str, root: Path) -> dict:
    family = case_family(capability, case, binding["artifact"]["installation"])
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
            nix_program: Path | None = None, case_inputs: dict | None = None, execution_nonce: str | None = None, variant_id: str | None = None, snapshot: dict | None = None, input_vm_id: str | None = None) -> dict:
    if not isinstance(execution_nonce, str) or not re.fullmatch(r"[a-f0-9]{32}", execution_nonce):
        raise ValueError("installed execution requires its fresh controller nonce")
    expected = evidence.artifacts(final, source)
    if artifact_id not in expected:
        raise ValueError("installed execution selected an undeclared final artifact")
    artifact = expected[artifact_id]
    rows = [row for row in expand(catalog) if row["environment"] == environment_id
            and row["installation"] == artifact["installation"] and row["architecture"] == artifact["architecture"]]
    environments = {item["id"]: item for item in catalog["environments"]}
    if not rows or environment_id not in environments:
        raise ValueError("installed execution selected no support rows for its artifact/environment")
    variants = {item["id"]: item for row in rows for item in requirements(catalog, environment_id, row["capability"])}
    if variant_id not in variants:
        raise ValueError("installed execution requires a declared qualification variant")
    variant = variants[variant_id]
    rows = [row for row in rows if variant_id in {item["id"] for item in requirements(catalog, environment_id, row["capability"])}]
    root = Path(__file__).resolve().parents[2]
    if current_tree(root) != evidence.source(driver_tree):
        raise ValueError("installed driver source differs from the independently selected tree")
    guest = require_owned_guest({"install_root": str(install_root), "artifact": artifact})
    if case_inputs is not None and (not isinstance(case_inputs, dict)
            or set(case_inputs) - {"native_packages", "nix", "security", "desktop"}
            or any(not isinstance(value, dict) for value in case_inputs.values())):
        raise ValueError("installed case inputs require known typed driver-family objects")
    output.mkdir(parents=True, mode=0o700, exist_ok=False)
    retained = output / "evidence"
    retained.mkdir()
    from .installed_cases.native_workflows import prepare_install
    transaction = prepare_install(final, source, artifact_id, file_root, install_root, case_inputs, retained)
    if transaction is not None and transaction["status"] != "passed":
        raise ValueError("fresh native installation failed; retained transaction requires recovery review")
    binding = bind(final, source, artifact_id, file_root, install_root, nix_program)
    binding['execution_nonce'] = execution_nonce
    if input_vm_id is not None:
        from .installed_input.mailbox import Mailbox
        execution_binding = {'format_version': 1, 'execution_nonce': execution_nonce, 'source_commit': source,
            'driver_tree': driver_tree, 'artifact_id': artifact_id, 'environment_id': environment_id,
            'variant_id': variant_id}
        mailbox = Mailbox(execution_binding, input_vm_id)
        mailbox.create()
        binding['native_input'] = {'format_version': 1, 'vm_id': input_vm_id, 'binding': execution_binding}
    if case_inputs is not None:
        binding["case_inputs"] = case_inputs
    if transaction is not None:
        binding["installation_transaction"] = transaction
        binding["execution_nonce"] = execution_nonce
        from .installed_cases.desktop import prepare_first_launch
        binding["first_launch_transaction"] = prepare_first_launch(binding, retained)
    facts, observations = collect(environments[environment_id], binding, guest, variant["display"]["protocol"])
    if facts.keys() != set(catalog["recorded_environment_facts"]):
        raise ValueError("installed environment collector differs from the selected catalog")
    variant_error = None
    try:
        observations["monitor_preparation"] = prepare_monitors(binding, observations["session"], variant["scales_percent"])
        variant_observation = observe(variant, snapshot, observations, binding)
    except CaseUnavailable as error:
        variant_error = str(error)
        variant_observation = {"format_version": 1, "variant": variant, "snapshot": snapshot, "unverified": variant_error}
    for name, document in (("binding.json", binding), ("environment.json", observations), ("environment-facts.json", facts),
                           ("driver-registry.json", registry(catalog, artifact["installation"])), ("variant.json", variant_observation)):
        (output / name).write_text(json.dumps(document, sort_keys=True, indent=2) + "\n")
    results = []
    for row in rows:
        cases = []
        for case in row["cases"]:
            try:
                result = (retain(binding, row["capability"], case, retained, "unverified", {"reason": variant_error})
                          if variant_error else execute_case(binding, row["capability"], case, retained))
            except CaseFailed as error:
                failed_case = case if not (retained / row["capability"] / case).exists() else case + "-product-failure"
                result = retain(binding, row["capability"], failed_case, retained, "failed",
                                {"reason": str(error), **getattr(error, "observations", {})},
                                getattr(error, "streams", None))
                result["id"] = case
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
    execution = {"format_version": 1, "execution_nonce": execution_nonce, "source_commit": source,
                 "driver_tree": driver_tree, "artifact_id": artifact_id, "environment_id": environment_id, "variant_id": variant_id}
    with (output / "execution.json").open("x") as stream:
        json.dump(execution, stream, sort_keys=True)
        stream.write("\n")
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
    parser.add_argument("--execution-nonce", required=True)
    parser.add_argument("--variant-id", required=True)
    parser.add_argument("--snapshot", type=json.loads, required=True)
    parser.add_argument("--nix-program", type=Path)
    parser.add_argument("--case-inputs", type=Path)
    parser.add_argument('--input-vm-id')
    args = parser.parse_args(arguments)
    try:
        campaign = execute(read(args.catalog), read(args.final_artifacts), args.source_commit,
                           args.artifact_id, args.file_root, args.install_root, args.environment_id,
                           args.output, args.driver_tree, args.nix_program, read(args.case_inputs) if args.case_inputs else None, args.execution_nonce, args.variant_id, args.snapshot, args.input_vm_id)
    except (OSError, ValueError, TypeError, subprocess.SubprocessError) as error:
        print("Installed case execution refused: " + str(error), file=sys.stderr)
        return 2
    print("Installed case campaign: " + str(args.output / "campaign.json"), file=sys.stderr)
    return 0 if all(row["status"] == "passed" for row in campaign["results"]) else 1
