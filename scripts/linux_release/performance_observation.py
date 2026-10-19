"""Retain existing sidecar benchmark output with installed artifact and hardware facts."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import sys

from . import installed_evidence as evidence
from .support_matrix import CATALOG, expand

ARCHITECTURES = {"x86_64": "amd64", "aarch64": "arm64"}


def hardware() -> dict:
    if platform.system() != "Linux" or platform.machine() not in ARCHITECTURES:
        raise ValueError("performance observations require a supported Linux host")
    cpu = Path("/proc/cpuinfo").read_text(encoding="utf-8")
    fields = [line.partition(":") for line in cpu.splitlines()]
    models = sorted({value.strip() for key, separator, value in fields
                     if separator and key.strip() in ("model name", "Processor", "Hardware") and value.strip()})
    if not models:
        raise ValueError("CPU model is unavailable")
    limits = {}
    for name in ("cpu.max", "memory.max"):
        path = Path("/sys/fs/cgroup") / name
        limits[name] = path.read_text(encoding="ascii").strip() if path.is_file() else None
    return {"architecture": ARCHITECTURES[platform.machine()], "kernel": platform.release(),
            "cpu_models": models, "online_cpus": os.cpu_count(),
            "allowed_cpus": sorted(os.sched_getaffinity(0)),
            "physical_memory_bytes": os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES"),
            "cgroup_v2_root_limits": limits}


def observe(catalog: dict, final: object, source_commit: str, artifact_id: str,
            row_id: str, report_file: dict, root: Path, facts: dict, execution: str) -> dict:
    """Collect evidence only; complete performance cases require their own runner."""
    artifacts = evidence.artifacts(final, evidence.source(source_commit))
    if artifact_id not in artifacts:
        raise ValueError("benchmark must identify a final artifact")
    artifact = artifacts[artifact_id]
    row = next((row for row in expand(catalog) if row["id"] == row_id), None)
    if row is None or row["capability"] != "performance" or row["tier"] != "native-performance":
        raise ValueError("benchmark must identify a native performance row")
    if (artifact["architecture"] != row["architecture"]
            or artifact["installation"] != row["installation"]
            or facts.get("architecture") != row["architecture"]):
        raise ValueError("artifact, row and observed host architecture must agree")
    if execution not in ("native", "emulated"):
        raise ValueError("runner must declare native or emulated execution")
    evidence.retained_file(report_file, root)
    data = (root / report_file["path"]).read_bytes()
    if len(data) > 16 * 1024 * 1024 or hashlib.sha256(data).hexdigest() != report_file["sha256"]:
        raise ValueError("benchmark report is oversized or changed during collection")
    report = json.loads(data)
    if not isinstance(report, dict) or type(report.get("version")) is not int or report["version"] != 1:
        raise ValueError("unsupported sidecar benchmark report")
    for key in ("metrics", "resources", "correctness", "budgets"):
        if not isinstance(report.get(key), dict) or not report[key]:
            raise ValueError(f"benchmark report has no {key}")
    if type(report.get("iterations")) is not int or report["iterations"] < 1:
        raise ValueError("benchmark report has no iterations")
    if "violations" not in report:
        raise ValueError("benchmark report has no budget outcome")
    violations = report["violations"]
    if violations is None:
        violations = []  # The Go report serializes a nil violation slice as null.
    if not isinstance(violations, list) or any(not isinstance(item, str) or not item for item in violations):
        raise ValueError("benchmark violations must be a string list or null")
    return {"format_version": 1, "source_commit": source_commit, "artifact": artifact,
            "row_id": row_id, "execution": execution, "hardware": facts,
            "benchmark_report": report_file, "reported_budget_status": "failed" if violations else "passed",
            "reported_violations": violations, "qualification_status": "unverified",
            "remaining_cases": row["cases"],
            "limitations": ["report identity does not establish which binary was executed",
                            "sidecar budget outcome does not qualify desktop or Bialy performance",
                            "native hardware-specific limits and sustained installed journeys remain required"]}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--final-artifacts", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--artifact-id", required=True)
    parser.add_argument("--row-id", required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--report", required=True, help="path relative to the retained evidence directory")
    parser.add_argument("--execution", choices=("native", "emulated"), required=True)
    args = parser.parse_args()
    try:
        root = args.evidence_root.resolve(strict=True)
        relative = Path(args.report)
        if relative.is_absolute() or ".." in relative.parts:
            raise ValueError("report must stay within the retained evidence directory")
        path = (root / relative).resolve(strict=True)
        if not path.is_relative_to(root) or not path.is_file() or path.stat().st_size > 16 * 1024 * 1024:
            raise ValueError("report escapes the evidence directory or exceeds 16 MiB")
        record = {"path": args.report, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
        result = observe(json.loads(args.catalog.read_text()), json.loads(args.final_artifacts.read_text()),
                         args.source_commit, args.artifact_id, args.row_id, record, root, hardware(), args.execution)
    except (OSError, ValueError, TypeError, KeyError) as exc:
        result = {"format_version": 1, "qualification_status": "unverified", "errors": [str(exc)]}
    json.dump(result, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 2  # Collection never substitutes for installed qualification.


if __name__ == "__main__":
    raise SystemExit(main())
