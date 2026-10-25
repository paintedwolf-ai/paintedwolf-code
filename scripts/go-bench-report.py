#!/usr/bin/env python3
"""Turn Go benchmark text into medians and compare it with a prior report."""

from __future__ import annotations

import argparse
import json
import math
import re
import statistics
from pathlib import Path


REQUIRED_METRICS = {"ns/op": "ns_per_op", "B/op": "bytes_per_op", "allocs/op": "allocs_per_op"}

BENCHMARK_GATES_NS: dict[str, float] = {
    "internal/toolhost/BenchmarkCommandProgram/noop": 50_000_000.0,
    "internal/toolhost/BenchmarkCommandProgram/fifty-statements": 100_000_000.0,
    "internal/toolhost/BenchmarkCommandProgram/native-child-wc": 50_000_000.0,
    "internal/toolhost/BenchmarkStructuredWc": 20_000_000.0,
    "internal/toolhost/BenchmarkCommandProgram/pipeline-cat-grep-wc-8MiB": 500_000_000.0,
    "internal/toolhost/BenchmarkCommandProgram/sort-spill-8MiB": 1_000_000_000.0,
    "internal/toolhost/BenchmarkCommandProgram/loop-9999-iterations": 500_000_000.0,
    "internal/toolhost/BenchmarkCommandSearch/grep-r-10k-indexed=false": 2_000_000_000.0,
    "internal/toolhost/BenchmarkCommandSearch/grep-r-10k-indexed=true": 100_000_000.0,
    "internal/toolhost/BenchmarkCommandSearch/find-100k-entries": 2_500_000_000.0,
}


def parse(path: Path) -> dict[str, list[dict[str, float]]]:
    rows: dict[str, list[dict[str, float]]] = {}
    package = ""
    # A benchmark that writes output while it runs splits its row: the name,
    # then the output, then the iteration count and metrics on a later line.
    pending = ""
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.startswith("pkg: "):
            package = line.removeprefix("pkg: ").removeprefix("github.com/lycaon/lycaon/").strip()
            continue
        fields = line.split()
        if fields and re.fullmatch(r"Benchmark\S*", fields[0]):
            name = re.sub(r"-\d+$", "", fields[0])
            if not package:
                raise ValueError(f"benchmark {name} has no package identity")
            name = f"{package}/{name}"
            if len(fields) < 2 or not re.fullmatch(r"[0-9]+", fields[1]):
                pending = name
                continue
            fields = fields[1:]
        elif pending and len(fields) >= 3 and re.fullmatch(r"[0-9]+", fields[0]) and fields[2] == "ns/op":
            name = pending
        else:
            continue
        pending = ""
        if int(fields[0]) <= 0 or len(fields) % 2 == 0:
            raise ValueError(f"benchmark {name} has a malformed result")
        values = {}
        for raw, unit in zip(fields[1::2], fields[2::2]):
            key = REQUIRED_METRICS.get(unit)
            if key is None:
                continue
            value = float(raw)
            if not math.isfinite(value) or value < 0 or key in values:
                raise ValueError(f"benchmark {name} has an invalid {key} measurement")
            values[key] = value
        for key in REQUIRED_METRICS.values():
            if key not in values:
                raise ValueError(f"benchmark {name} has no {key} measurement")
        rows.setdefault(name, []).append(values)
    return rows


def summarize(rows: dict[str, list[dict[str, float]]]) -> dict[str, dict[str, float | bool]]:
    report: dict[str, dict[str, float | bool]] = {}
    for name, samples in sorted(rows.items()):
        ns_values = sorted(sample["ns_per_op"] for sample in samples)
        median_ns = statistics.median(ns_values)
        entry: dict[str, float | bool] = {
            "count": len(samples),
            "median_ns_per_op": median_ns,
            "min_ns_per_op": ns_values[0],
            "max_ns_per_op": ns_values[-1],
            "median_bytes_per_op": statistics.median(sample["bytes_per_op"] for sample in samples),
            "median_allocs_per_op": statistics.median(sample["allocs_per_op"] for sample in samples),
        }
        if name in BENCHMARK_GATES_NS:
            gate_ns = BENCHMARK_GATES_NS[name]
            entry["gate_ns_per_op"] = gate_ns
            entry["gate_passed"] = median_ns <= gate_ns
        report[name] = entry
    return report


def compare(current: dict, baseline: dict) -> list[dict[str, float | str]]:
    changes = []
    for name, now in current.items():
        before = baseline.get("benchmarks", {}).get(name)
        if not before or before["median_ns_per_op"] <= 0:
            continue
        percent = (now["median_ns_per_op"] / before["median_ns_per_op"] - 1) * 100
        changes.append({"name": name, "time_change_percent": round(percent, 2)})
    return changes


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--baseline", type=Path)
    args = parser.parse_args()

    benchmarks = summarize(parse(args.input))
    if not benchmarks:
        raise SystemExit("no benchmark rows found")
    result: dict = {"version": 1, "benchmarks": benchmarks}
    if args.baseline:
        baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
        result["comparison"] = compare(benchmarks, baseline)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(f"Benchmark report: {args.output}")
    gate_failures: list[str] = []
    for name, values in benchmarks.items():
        gate_suffix = ""
        if "gate_ns_per_op" in values:
            gate_ms = float(values["gate_ns_per_op"]) / 1e6
            status = "PASS" if values["gate_passed"] else "FAIL"
            gate_suffix = f" [gate <= {gate_ms:.1f} ms: {status}]"
            if not values["gate_passed"]:
                gate_failures.append(f"{name}: {float(values['median_ns_per_op']) / 1e6:.3f} ms > {gate_ms:.1f} ms")
        print(
            f"  {name:<58} {float(values['median_ns_per_op']) / 1e6:10.3f} ms/op "
            f"{float(values['median_bytes_per_op']):10.0f} B/op {float(values['median_allocs_per_op']):8.0f} allocs/op{gate_suffix}"
        )
    for change in result.get("comparison", []):
        marker = "REGRESSION" if change["time_change_percent"] > 10 else "change"
        print(f"  {marker}: {change['name']} {change['time_change_percent']:+.2f}%")
    if gate_failures:
        raise SystemExit("benchmark gate exceeded:\n  " + "\n  ".join(gate_failures))


if __name__ == "__main__":
    main()
