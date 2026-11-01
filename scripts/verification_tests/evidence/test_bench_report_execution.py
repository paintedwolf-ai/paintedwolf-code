import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[2].joinpath("go-bench-report.py")
SPEC = importlib.util.spec_from_file_location("go_bench_report", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def gated_bench_text(overrides=None, omit=(), extra_lines=()):
    overrides = overrides or {}
    lines = ["pkg: github.com/lycaon/lycaon/internal/toolhost"]
    for full_name, gate in MODULE.BENCHMARK_GATES.items():
        if full_name in omit:
            continue
        bench_name = full_name.removeprefix("internal/toolhost/")
        bytes_per_op = overrides.get(full_name, {}).get("bytes_per_op", int(gate["bytes_per_op"]) - 100)
        allocs_per_op = overrides.get(full_name, {}).get("allocs_per_op", int(gate["allocs_per_op"]) - 10)
        lines.append(f"{bench_name}-8 \t 10\t 5000 ns/op\t {bytes_per_op} B/op\t {allocs_per_op} allocs/op")
    lines.extend(extra_lines)
    return "\n".join(lines) + "\n"


class BenchReportTests(unittest.TestCase):
    def report(self, text):
        with tempfile.TemporaryDirectory() as directory:
            raw = Path(directory, "bench.txt")
            raw.write_text(text, encoding="utf-8")
            output = Path(directory, "report.json")
            result = subprocess.run(
                [sys.executable, str(SCRIPT), "--input", str(raw), "--output", str(output)],
                text=True, capture_output=True, check=False,
            )
            report = json.loads(output.read_text(encoding="utf-8")) if output.exists() else None
            return result, report

    def test_output_written_during_a_benchmark_keeps_its_result(self):
        result, report = self.report(
            "pkg: github.com/lycaon/lycaon/internal/example\n"
            "BenchmarkLogs \t2026/09/27 05:39:44 INFO catalog walked entries=201\n"
            "2026/09/27 05:39:44 INFO catalog walked entries=201\n"
            "     120\t   9000 ns/op\t  512 B/op\t   4 allocs/op\n"
            "BenchmarkQuiet-8 \t    1000\t   1500 ns/op\t   64 B/op\t   1 allocs/op\n"
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        benchmarks = report["benchmarks"]
        self.assertEqual(benchmarks["internal/example/BenchmarkLogs"]["median_ns_per_op"], 9000)
        self.assertEqual(benchmarks["internal/example/BenchmarkQuiet"]["median_allocs_per_op"], 1)

    def test_gated_benchmarks_pass_and_report_ungated_benchmarks_only(self):
        result, report = self.report(
            gated_bench_text(extra_lines=["BenchmarkUngatedHelper-8 \t 20\t 1200 ns/op\t 128 B/op\t 2 allocs/op"])
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        noop = report["benchmarks"]["internal/toolhost/BenchmarkCommandProgram/noop"]
        self.assertTrue(noop["gate_passed"])
        self.assertEqual(noop["gate_bytes_per_op"], 1_100_000.0)
        self.assertEqual(noop["gate_allocs_per_op"], 4_000.0)
        ungated = report["benchmarks"]["internal/toolhost/BenchmarkUngatedHelper"]
        self.assertEqual(ungated["median_bytes_per_op"], 128.0)
        self.assertEqual(ungated["median_allocs_per_op"], 2.0)
        self.assertNotIn("gate_passed", ungated)
        self.assertNotIn("gate_bytes_per_op", ungated)
        self.assertNotIn("gate_allocs_per_op", ungated)

    def test_gated_benchmark_allocation_failure_fails_report(self):
        target = "internal/toolhost/BenchmarkCommandProgram/noop"
        result, report = self.report(
            gated_bench_text(overrides={target: {"allocs_per_op": 4_001}})
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(f"{target}: 4001 allocs/op > 4000 allocs/op", result.stderr)
        self.assertFalse(report["benchmarks"][target]["gate_passed"])

    def test_gated_benchmark_bytes_failure_fails_report(self):
        target = "internal/toolhost/BenchmarkCommandProgram/sort-spill-8MiB"
        result, report = self.report(
            gated_bench_text(overrides={target: {"bytes_per_op": 138_000_001}})
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(f"{target}: 138000001 B/op > 138000000 B/op", result.stderr)
        self.assertFalse(report["benchmarks"][target]["gate_passed"])

    def test_missing_gated_benchmark_fails_report(self):
        missing = "internal/toolhost/BenchmarkStructuredWc"
        result, _ = self.report(gated_bench_text(omit=(missing,)))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(f"{missing}: missing gated benchmark", result.stderr)

    def test_malformed_results_still_fail(self):
        for text in (
            "pkg: github.com/lycaon/lycaon/internal/example\nBenchmarkZero \t 0\t 10 ns/op\t 1 B/op\t 1 allocs/op\n",
            "pkg: github.com/lycaon/lycaon/internal/example\nBenchmarkShort \t 10\t 10 ns/op\t 1 B/op\n",
            "BenchmarkOrphan \t 10\t 10 ns/op\t 1 B/op\t 1 allocs/op\n",
        ):
            with self.subTest(text=text):
                result, _ = self.report(text)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("ValueError", result.stderr)


if __name__ == "__main__":
    unittest.main()
