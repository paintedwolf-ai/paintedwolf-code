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

BENCHMARK_GATES: dict[str, dict[str, float]] = {
    "internal/toolhost/BenchmarkCommandProgram/noop": {
        "bytes_per_op": 1_100_000.0,
        "allocs_per_op": 4_000.0,
    },
    "internal/toolhost/BenchmarkCommandProgram/fifty-statements": {
        "bytes_per_op": 1_450_000.0,
        "allocs_per_op": 7_000.0,
    },
    "internal/toolhost/BenchmarkCommandProgram/native-child-wc": {
        "bytes_per_op": 7_200_000.0,
        "allocs_per_op": 14_000.0,
    },
    "internal/toolhost/BenchmarkStructuredWc": {
        "bytes_per_op": 60_000.0,
        "allocs_per_op": 700.0,
    },
    "internal/toolhost/BenchmarkCommandProgram/pipeline-cat-grep-wc-8MiB": {
        "bytes_per_op": 97_000_000.0,
        "allocs_per_op": 482_000.0,
    },
    "internal/toolhost/BenchmarkCommandProgram/sort-spill-8MiB": {
        "bytes_per_op": 138_000_000.0,
        "allocs_per_op": 1_075_000.0,
    },
    "internal/toolhost/BenchmarkCommandProgram/loop-9999-iterations": {
        "bytes_per_op": 48_500_000.0,
        "allocs_per_op": 483_000.0,
    },
    "internal/toolhost/BenchmarkCommandSearch/grep-r-10k-indexed=false": {
        "bytes_per_op": 94_000_000.0,
        "allocs_per_op": 750_000.0,
    },
    "internal/toolhost/BenchmarkCommandSearch/grep-r-10k-indexed=true": {
        "bytes_per_op": 31_000_000.0,
        "allocs_per_op": 205_000.0,
    },
    "internal/toolhost/BenchmarkCommandSearch/find-100k-entries": {
        "bytes_per_op": 137_000_000.0,
        "allocs_per_op": 1_110_000.0,
    },
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
        median_bytes = statistics.median(sample["bytes_per_op"] for sample in samples)
        median_allocs = statistics.median(sample["allocs_per_op"] for sample in samples)
        entry: dict[str, float | bool] = {
            "count": len(samples),
            "median_ns_per_op": median_ns,
            "min_ns_per_op": ns_values[0],
            "max_ns_per_op": ns_values[-1],
            "median_bytes_per_op": median_bytes,
            "median_allocs_per_op": median_allocs,
        }
        if name in BENCHMARK_GATES:
            gate = BENCHMARK_GATES[name]
            gate_bytes = gate["bytes_per_op"]
            gate_allocs = gate["allocs_per_op"]
            entry["gate_bytes_per_op"] = gate_bytes
            entry["gate_allocs_per_op"] = gate_allocs
            entry["gate_passed"] = median_bytes <= gate_bytes and median_allocs <= gate_allocs
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
    present_packages = {name.split("/Benchmark", 1)[0] for name in benchmarks}
    gate_failures: list[str] = [
        f"{name}: missing gated benchmark"
        for name in BENCHMARK_GATES
        if name.split("/Benchmark", 1)[0] in present_packages and name not in benchmarks
    ]
    for name, values in benchmarks.items():
        gate_suffix = ""
        if "gate_passed" in values:
            gate_bytes = float(values["gate_bytes_per_op"])
            gate_allocs = float(values["gate_allocs_per_op"])
            status = "PASS" if values["gate_passed"] else "FAIL"
            gate_suffix = f" [gate <= {gate_bytes:.0f} B/op, <= {gate_allocs:.0f} allocs/op: {status}]"
            if float(values["median_bytes_per_op"]) > gate_bytes:
                gate_failures.append(f"{name}: {float(values['median_bytes_per_op']):.0f} B/op > {gate_bytes:.0f} B/op")
            if float(values["median_allocs_per_op"]) > gate_allocs:
                gate_failures.append(
                    f"{name}: {float(values['median_allocs_per_op']):.0f} allocs/op > {gate_allocs:.0f} allocs/op"
                )
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
