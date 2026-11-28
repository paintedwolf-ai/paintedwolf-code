"""GB03 V07 in-process restart and unreadable-ledger runner (4 in-process slots).

Executes D1's real SQLite evidence store (`internal/session/store`),
`evidence.Resolve`, and `guidance.MidTurnEvidence`/`UnionCloseoutEvidence` via
a test-only Go `-overlay` (`gb03_v07_inprocess_test.go`) against verified D1
`--product-root` (`3bb1464d22` / `a05c37487b`):
- V07-03-inprocess-restart-before-commit-boundary
- V07-04-inprocess-restart-before-resolution-boundary
- V07-05-inprocess-unreadable-ledger-refuses-all-citations
- V07-06-inprocess-unreadable-ledger-denies-exemption
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import platform
import subprocess
import sys
from typing import Any

from grounding.inprocess_driver import (
    D1_EXPECTED_COMMIT,
    D1_EXPECTED_TREE,
    InProcessPlan,
    write_test_overlay,
)
from grounding.suites.gb03_evidence.catalog import V07_SLOTS


V07_INPROCESS_SELECTORS: tuple[str, ...] = tuple(
    s.selector for s in V07_SLOTS if s.lane == "in_process"
)


def run_gb03_v07_inprocess(
    *,
    product_root: str | pathlib.Path,
    out_dir: pathlib.Path,
    run_id: str = "20261127-3bb1464d-gb03-v07-inprocess",
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    results_path = out_dir / "v07-inprocess-results.jsonl"
    divergences_path = out_dir / "v07-inprocess-divergences.jsonl"

    overlay_src = (
        pathlib.Path(__file__).resolve().parents[2] / "overlays" / "gb03_v07_inprocess_test.go"
    )
    prod = pathlib.Path(product_root).resolve()
    overlay_target = prod / "lycaon" / "internal" / "session" / "store" / "gb03_v07_inprocess_test.go"
    overlay_json = out_dir / "gb03-v07-overlay.json"
    write_test_overlay(overlay_json, {str(overlay_target): str(overlay_src)}, product_root=prod)

    plan = InProcessPlan(
        product_root=prod,
        packages=("./internal/session/store",),
        run_pattern="^TestGB03V07InProcessRestartAndUnreadableLedger$",
        overlay_path=overlay_json,
        count=1,
        expected_commit=D1_EXPECTED_COMMIT,
        expected_tree=D1_EXPECTED_TREE,
    )
    verified = plan.verify()
    cmd = plan.build_command()

    env = os.environ.copy()
    env["GND_GB03_V07_RESULTS_JSONL"] = str(results_path)

    proc = subprocess.run(
        cmd,
        cwd=str(plan.go_dir()),
        env=env,
        capture_output=True,
        text=True,
        timeout=300.0,
        check=False,
    )
    (out_dir / "v07-gotest.stdout.jsonl").write_text(proc.stdout, encoding="utf-8")
    if proc.stderr:
        (out_dir / "v07-gotest.stderr.log").write_text(proc.stderr, encoding="utf-8")

    slot_results: list[dict[str, Any]] = []
    divergences: list[dict[str, Any]] = []
    if results_path.is_file():
        with results_path.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                rec = json.loads(line)
                slot_results.append(rec)
                if not rec.get("passed"):
                    divergences.append(rec)

    with divergences_path.open("w", encoding="utf-8") as f:
        for d in divergences:
            f.write(json.dumps(d, sort_keys=True) + "\n")

    executed_selectors = [r.get("selector") for r in slot_results]
    all_expected_executed = set(V07_INPROCESS_SELECTORS).issubset(set(executed_selectors))

    summary = {
        "card": "GB03",
        "family": "V07",
        "lane": "in_process",
        "run_id": run_id,
        "platform": platform.system().lower(),
        "arch": platform.machine(),
        "verified_d1": verified,
        "go_test_returncode": proc.returncode,
        "expected_slots": list(V07_INPROCESS_SELECTORS),
        "executed_slots": len(slot_results),
        "passed_slots": sum(1 for r in slot_results if r.get("passed")),
        "divergence_count": len(divergences),
        "slots": slot_results,
        "divergences": divergences,
        "passed": (
            proc.returncode == 0
            and all_expected_executed
            and len(divergences) == 0
        ),
    }
    (out_dir / "gb03-v07-inprocess-summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB03 V07 in-process restart & unreadable-ledger runner")
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb03-v07-inprocess")
    args = ap.parse_args(argv)
    summary = run_gb03_v07_inprocess(
        product_root=args.product_root,
        out_dir=pathlib.Path(args.out_dir),
        run_id=args.run_id,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
