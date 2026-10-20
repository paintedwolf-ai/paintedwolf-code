"""Run the signed desktop upgrade gate on a disposable native macOS runner."""
from __future__ import annotations

import argparse
import json
from pathlib import Path, PurePosixPath
import shutil
import tempfile

from bundle_upgrade_case import run_case
from bundle_upgrade_host import local_origin, require_disposable_host
from bundle_upgrade_prepare import file_digest


def verify_inventory(root: Path) -> dict:
    inventory = json.loads((root / "inventory.json").read_text())
    present = {path.relative_to(root).as_posix() for path in root.rglob("*") if path.is_file()}
    if present != {*inventory, "inventory.json"}:
        raise ValueError("upgrade fixture inventory differs")
    for name, expected in inventory.items():
        path = PurePosixPath(name)
        if path.is_absolute() or ".." in path.parts or str(path) != name:
            raise ValueError("upgrade fixture inventory contains an unsafe path")
        target = root / name
        if target.is_symlink() or file_digest(target) != expected:
            raise ValueError("upgrade fixture checksum differs: " + name)
    return json.loads((root / "plan.json").read_text())


def rehearse(root: Path, evidence: Path):
    evidence.mkdir(parents=True, exist_ok=False)
    receipt = {"format_version": 1, "result": "failed", "cases": []}
    config = None
    try:
        plan = verify_inventory(root)
        receipt.update({"source_sha": plan["source_sha"], "candidate_version": plan["candidate"]["version"],
                        "excluded_sources": plan["excluded_sources"], "inventory_sha256": file_digest(root / "inventory.json")})
        shutil.copyfile(root / "plan.json", evidence / "plan.json")
        if not plan["cases"]:
            receipt.update({"result": "not_applicable", "reason": plan["skip_reason"]})
            return receipt
        config = require_disposable_host()
        with tempfile.TemporaryDirectory(prefix="bundle-upgrade-") as directory:
            work = Path(directory)
            with local_origin(root, work, evidence):
                for case in plan["cases"]:
                    case_work = work / case["id"]
                    case_work.mkdir()
                    result = run_case(case, plan, root, case_work, config, evidence / case["id"])
                    receipt["cases"].append(result)
                    shutil.rmtree(config)
                    shutil.rmtree(case_work)
        receipt["result"] = "passed"
        return receipt
    except Exception as error:
        receipt["error"] = str(error)
        raise
    finally:
        # The store is created only after the disposable-host guard has admitted this run.
        if config is not None and config.is_dir():
            shutil.rmtree(config)
        (evidence / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixtures", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    rehearse(args.fixtures.resolve(), args.evidence.resolve())


if __name__ == "__main__":
    main()
