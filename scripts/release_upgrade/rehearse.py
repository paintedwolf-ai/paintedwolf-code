"""Run the signed desktop upgrade gate on a disposable native macOS runner."""
from __future__ import annotations

import argparse
import json
from pathlib import Path, PurePosixPath
import shutil
import signal
import tempfile

from release_upgrade.case import run_case
from release_upgrade.host import local_origin, require_disposable_host
from release_upgrade.prepare import file_digest


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
    plan = json.loads((root / "plan.json").read_text())
    if not isinstance(plan, dict) or type(plan.get("format_version")) is not int or plan["format_version"] != 1:
        raise ValueError("unsupported upgrade fixture format")
    if not isinstance(plan.get("cases"), list):
        raise ValueError("upgrade fixture cases must be a list")
    if plan["cases"] and plan.get("skip_reason") is not None:
        raise ValueError("nonempty upgrade fixtures cannot declare an exclusion")
    if not plan["cases"] and plan.get("skip_reason") != "no_automatic_upgrade_source":
        raise ValueError("upgrade fixture has no supported exclusion reason")
    return plan


def verify_requests(path: Path, case: dict, start: int):
    from release_upgrade.prepare import archive_path
    requests = [json.loads(line) for line in path.read_text().splitlines()][start:]
    observed = {row["path"] for row in requests if row["status"] == 200}
    for step in case["steps"]:
        number = step["update_keys"]["signing_generation"]
        feed = f"/updates/{case['channel']}/key-{number}/latest.json"
        required = {feed, feed + ".sig", "/" + archive_path(step)}
        if not required <= observed:
            raise ValueError("upgrade did not fetch every authenticated fixture route")


def rehearse(root: Path, evidence: Path, case_id: str):
    evidence.mkdir(parents=True, exist_ok=False)
    receipt = {"format_version": 1, "result": "failed", "case_id": case_id, "cases": []}
    config = None
    try:
        plan = verify_inventory(root)
        receipt.update({"source_sha": plan["source_sha"], "candidate_version": plan["candidate"]["version"],
                        "excluded_sources": plan["excluded_sources"], "inventory_sha256": file_digest(root / "inventory.json")})
        shutil.copyfile(root / "plan.json", evidence / "plan.json")
        if not plan["cases"]:
            if case_id != "not_applicable":
                raise ValueError("the fixture has no executable upgrade cases")
            receipt.update({"result": "not_applicable", "reason": plan["skip_reason"]})
            return receipt
        selected = [case for case in plan["cases"] if case["id"] == case_id]
        if len(selected) != 1:
            raise ValueError("select exactly one case from the prepared upgrade plan")
        from release_semver import parse
        case = selected[0]
        parse(case["source_version"])
        if case["channel"] not in ("stable", "preview") or case_id != f"{case['source_version']}-{case['channel']}":
            raise ValueError("upgrade case identity is invalid")
        config = require_disposable_host()
        with tempfile.TemporaryDirectory(prefix="bundle-upgrade-") as directory:
            work = Path(directory).resolve()
            with local_origin(root, work, evidence):
                for case in selected:
                    case_work = work / case["id"]
                    case_work.mkdir()
                    receipt["cases"].append({"id": case["id"], "result": "failed"})
                    request_log = evidence / "requests.jsonl"
                    start = len(request_log.read_text().splitlines()) if request_log.exists() else 0
                    try:
                        result = run_case(case, plan, root, case_work, config, evidence / case["id"])
                        verify_requests(request_log, case, start)
                    except (Exception, KeyboardInterrupt) as error:
                        receipt["cases"][-1]["error"] = str(error)
                        raise
                    receipt["cases"][-1] = result
                    shutil.rmtree(config)
                    shutil.rmtree(case_work)
        receipt["result"] = "passed"
        return receipt
    except KeyboardInterrupt as error:
        receipt.update({"result": "cancelled", "error": str(error)})
        raise
    except Exception as error:
        receipt["error"] = str(error)
        raise
    finally:
        (evidence / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixtures", type=Path, required=True)
    parser.add_argument("--case", dest="case_id", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    def interrupted(number, _frame):
        raise KeyboardInterrupt(f"Desktop rehearsal interrupted by signal {number}")
    previous = {number: signal.signal(number, interrupted) for number in (signal.SIGINT, signal.SIGTERM)}
    try:
        rehearse(args.fixtures.resolve(), args.evidence.resolve(), args.case_id)
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)


if __name__ == "__main__":
    main()
