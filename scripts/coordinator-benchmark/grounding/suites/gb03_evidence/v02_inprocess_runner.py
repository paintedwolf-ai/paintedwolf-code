"""GB03 V02 generated in-process lookalike runner (3,000 instances per seed set).

Executes D1's real `internal/evidence.BuildEvidenceRecordRooted` and `Resolve`
via a test-only Go `-overlay` (`gb03_v02_inprocess_test.go`) against verified
D1 `--product-root` (`3bb1464d22` / `a05c37487b`), recording any promoted
paths, line ranges, delivery tags, or grounded spoofed coordinates as
divergences.
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

from grounding.generators import generate_v02
from grounding.inprocess_driver import (
    D1_EXPECTED_COMMIT,
    D1_EXPECTED_TREE,
    InProcessPlan,
    write_test_overlay,
)


def run_gb03_v02_inprocess(
    *,
    product_root: str | pathlib.Path,
    out_dir: pathlib.Path,
    base_seed: str = "20261127",
    count: int = 3000,
    run_id: str = "20261127-3bb1464d-gb03-v02-inprocess",
) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    cases_path = out_dir / f"v02-cases-{base_seed}.jsonl"
    results_path = out_dir / f"v02-results-{base_seed}.jsonl"
    divergences_path = out_dir / f"v02-divergences-{base_seed}.jsonl"

    with cases_path.open("w", encoding="utf-8") as f:
        for i in range(count):
            c = generate_v02(base_seed, i)
            f.write(
                json.dumps(
                    {
                        "base_seed": base_seed,
                        "instance_index": i,
                        "payload": c.payload.decode("utf-8", errors="replace"),
                        "imitated_markers": list(c.imitated_markers),
                    },
                    sort_keys=True,
                )
                + "\n"
            )

    overlay_src = (
        pathlib.Path(__file__).resolve().parents[2] / "overlays" / "gb03_v02_inprocess_test.go"
    )
    prod = pathlib.Path(product_root).resolve()
    overlay_target = prod / "lycaon" / "internal" / "evidence" / "gb03_v02_inprocess_test.go"
    overlay_json = out_dir / "gb03-v02-overlay.json"
    write_test_overlay(overlay_json, {str(overlay_target): str(overlay_src)}, product_root=prod)

    plan = InProcessPlan(
        product_root=prod,
        packages=("./internal/evidence",),
        run_pattern="^TestGB03V02InProcessLookalikes$",
        overlay_path=overlay_json,
        count=1,
        expected_commit=D1_EXPECTED_COMMIT,
        expected_tree=D1_EXPECTED_TREE,
    )
    verified = plan.verify()
    cmd = plan.build_command()

    env = os.environ.copy()
    env["GND_GB03_V02_CASES_JSONL"] = str(cases_path)
    env["GND_GB03_V02_RESULTS_JSONL"] = str(results_path)

    proc = subprocess.run(
        cmd,
        cwd=str(plan.go_dir()),
        env=env,
        capture_output=True,
        text=True,
        timeout=300.0,
        check=False,
    )
    (out_dir / f"v02-gotest-{base_seed}.stdout.jsonl").write_text(proc.stdout, encoding="utf-8")
    if proc.stderr:
        (out_dir / f"v02-gotest-{base_seed}.stderr.log").write_text(proc.stderr, encoding="utf-8")

    total = 0
    passed_count = 0
    divergences: list[dict[str, Any]] = []
    if results_path.is_file():
        with results_path.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                rec = json.loads(line)
                total += 1
                if rec.get("passed"):
                    passed_count += 1
                else:
                    divergences.append(rec)

    with divergences_path.open("w", encoding="utf-8") as f:
        for d in divergences:
            f.write(json.dumps(d, sort_keys=True) + "\n")

    summary = {
        "card": "GB03",
        "family": "V02",
        "lane": "in_process",
        "run_id": run_id,
        "base_seed": base_seed,
        "platform": platform.system().lower(),
        "arch": platform.machine(),
        "verified_d1": verified,
        "go_test_returncode": proc.returncode,
        "requested_instances": count,
        "executed_instances": total,
        "passed_instances": passed_count,
        "divergence_count": len(divergences),
        "sample_divergences": divergences[:10],
        "passed": proc.returncode == 0 and total == count and len(divergences) == 0,
    }
    (out_dir / f"gb03-v02-inprocess-summary-{base_seed}.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True), encoding="utf-8"
    )
    return summary


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description="GB03 V02 in-process lookalike runner")
    ap.add_argument("--product-root", required=True)
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--base-seed", default="20261127")
    ap.add_argument("--count", type=int, default=3000)
    ap.add_argument("--run-id", default="20261127-3bb1464d-gb03-v02-inprocess")
    args = ap.parse_args(argv)
    summary = run_gb03_v02_inprocess(
        product_root=args.product_root,
        out_dir=pathlib.Path(args.out_dir),
        base_seed=args.base_seed,
        count=args.count,
        run_id=args.run_id,
    )
    print(json.dumps(summary, indent=2, sort_keys=True))
    return 0 if summary["go_test_returncode"] == 0 and summary["executed_instances"] == args.count else 1


if __name__ == "__main__":
    sys.exit(main())
