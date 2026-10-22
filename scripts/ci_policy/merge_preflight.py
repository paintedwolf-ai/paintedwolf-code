"""Order exact-source merge checks without changing their coverage."""
from pathlib import Path
import subprocess

from verification_execute import json_stream

ROOT = Path(__file__).resolve().parents[2]


def package_phases(selected):
    patterns = selected or ["./..."]
    result = subprocess.run(["go", "list", "-json", "-tags=integration", *patterns],
                            cwd=ROOT / "lycaon", text=True, capture_output=True, check=True)
    records = list(json_stream(result.stdout))
    if not records or any(row.get("Error") or row.get("DepsErrors") for row in records):
        raise ValueError("merge preflight requires a complete package selection")
    packages = sorted({row["ImportPath"] for row in records})
    contracts = [name for name in packages if "/test/contract/" in name or name.endswith("/test/contract")]
    behavior = [name for name in packages if name not in contracts]
    if not contracts:
        raise ValueError("merge preflight selection omitted repository contracts")
    return [part for part in (contracts, behavior) if part]


def commands(lane, targets, selected=None):
    early = lane.get("early_targets", [])
    remaining = [target for target in targets if target not in early]
    if lane.get("contract_first"):
        return [["./task", "test:full", "--", *part] for part in package_phases(selected)]
    return [["./task", *part] for part in (early, remaining) if part]
