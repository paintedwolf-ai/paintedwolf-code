import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1].joinpath("go-bench-report.py")


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
            report = json.loads(output.read_text(encoding="utf-8")) if result.returncode == 0 else None
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
