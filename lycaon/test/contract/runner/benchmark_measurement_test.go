package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBenchmarkReportRequiresDeclaredMeasurements(t *testing.T) {
	script := filepath.Join(contractcheck.RepoRoot(t), "scripts", "go-bench-report.py")
	const header = "pkg: example.test/bench\n"
	for _, tc := range []struct {
		name, input string
		valid       bool
		wantBytes   float64
	}{
		{"missing package", "BenchmarkWork-6 100 42 ns/op 0 B/op 0 allocs/op\n", false, 0},
		{"missing bytes", header + "BenchmarkWork-6 100 42 ns/op 0 allocs/op\n", false, 0},
		{"missing allocations", header + "BenchmarkWork-6 100 42 ns/op 0 B/op\n", false, 0},
		{"explicit zero", header + "BenchmarkWork-6 100 42 ns/op 0 B/op 0 allocs/op\n", true, 0},
		{"single CPU", header + "BenchmarkWork 100 42 ns/op 0 B/op 0 allocs/op\n", true, 0},
		{"scientific bytes", header + "BenchmarkWork-6 100 42 ns/op 1e+20 B/op 0 allocs/op\n", true, 1e20},
		{"negative bytes", header + "BenchmarkWork-6 100 42 ns/op -42 B/op 0 allocs/op\n", false, 0},
		{"negative allocations", header + "BenchmarkWork-6 100 42 ns/op 0 B/op -4 allocs/op\n", false, 0},
		{"nonfinite time", header + "BenchmarkWork-6 100 NaN ns/op 0 B/op 0 allocs/op\n", false, 0},
		{"overflow bytes", header + "BenchmarkWork-6 100 42 ns/op 1e999 B/op 0 allocs/op\n", false, 0},
		{"duplicate bytes", header + "BenchmarkWork-6 100 42 ns/op 0 B/op 4 B/op 0 allocs/op\n", false, 0},
		{"malformed row after valid", header + "BenchmarkWork-6 100 42 ns/op 0 B/op 0 allocs/op\nBenchmarkOther-6 100 bad ns/op 0 B/op 0 allocs/op\n", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "bench.txt"), filepath.Join(dir, "report.json")
			contractcheck.FailErr(t, "write benchmark input", os.WriteFile(input, []byte(tc.input), 0o600))
			data, err := exec.CommandContext(t.Context(), "python3", script, "--input", input, "--output", output).CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("report error = %v; valid = %t; output: %s", err, tc.valid, data)
			}
			if !tc.valid {
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("invalid input published a report: %v", err)
				}
				return
			}
			data, err = os.ReadFile(output)
			contractcheck.FailErr(t, "read benchmark report", err)
			var result struct {
				Benchmarks map[string]struct {
					Count  int     `json:"count"`
					Bytes  float64 `json:"median_bytes_per_op"`
					Allocs float64 `json:"median_allocs_per_op"`
				} `json:"benchmarks"`
			}
			contractcheck.FailErr(t, "decode benchmark report", json.Unmarshal(data, &result))
			row, ok := result.Benchmarks["example.test/bench/BenchmarkWork"]
			if !ok || row.Count != 1 || row.Bytes != tc.wantBytes || row.Allocs != 0 {
				t.Fatalf("measurement changed: %+v", result)
			}
		})
	}
}
