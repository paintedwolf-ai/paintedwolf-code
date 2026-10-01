//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFailedProjectRetainsMeasurements(t *testing.T) {
	for name, report := range map[string]string{
		"timeout":   `{"results":[],"errors":[{"type":"Timeout","level":"error","path":"source/app.js","message":"Timed out"}],"paths":{"scanned":["source/app.js"]}}`,
		"malformed": `{"results":[`,
		"missing":   "",
	} {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "scanner")
			script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = --output ]; then shift; destination=$1; fi\n  shift\ndone\n"
			if report != "" {
				script += "cat > \"$destination\" <<'REPORT'\n" + report + "\nREPORT\n"
			}
			script += "exit 2\n"
			testutil.FailErr(t, "write controlled scanner", os.WriteFile(binary, []byte(script), 0o700))
			project := projectCase{Name: name, Language: "javascript", Files: map[string]string{"app.js": "sink(source())\n"}}
			measurement := measureProject(binary, []byte("rules: []\n"), project, opengrep.Intraprocedural, 1, projectBudget{Effective: defaultProjectTimeout})
			if measurement.Failure == "" || (measurement.MemorySamples == 0 && measurement.MemoryFailure == "") {
				t.Fatalf("failed run lost its resource evidence: %+v", measurement)
			}
			if (measurement.MemorySamples > 0) != (measurement.PeakRSSBytes > 0) {
				t.Fatalf("inconsistent resource evidence: %+v", measurement)
			}
			if report != "" && measurement.OutputBytes != int64(len(report)+1) {
				t.Fatalf("failed report size = %d, want %d", measurement.OutputBytes, len(report)+1)
			}
			if name == "timeout" {
				want := []expectedProjectDiagnostic{{File: "app.js", expectedDiagnostic: expectedDiagnostic{Kind: api.ScanWarningTargetUnscanned}}}
				if !slices.Equal(measurement.Diagnostics, want) {
					t.Fatalf("coverage diagnostics = %+v, want %+v", measurement.Diagnostics, want)
				}
			}
			if len(measurement.Findings) != 0 || measurement.TruePositive != 0 || measurement.FalsePositive != 0 {
				t.Fatal("failed report was adjudicated as a successful scan")
			}
		})
	}
}
