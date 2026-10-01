package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	scanexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiagnosticCaptureRetainsEndsWithBoundedStorage(t *testing.T) {
	for _, chunk := range []int{1, 19, 2000, 9000} {
		input := strings.Repeat("begin", 600) + strings.Repeat("middle", 2000) + strings.Repeat("end", 1000)
		var capture diagnosticCapture
		for offset := 0; offset < len(input); offset += chunk {
			part := input[offset:min(offset+chunk, len(input))]
			n, err := capture.Write([]byte(part))
			testutil.FailErr(t, "capture diagnostics", err)
			if n != len(part) || len(capture.head)+len(capture.tail) > 2*diagnosticExcerptBytes {
				t.Fatalf("chunk %d: invalid capture lengths", chunk)
			}
		}
		want := input[:2000] + "\n[diagnostic output truncated]\n" + input[len(input)-2000:]
		if capture.String() != want {
			t.Fatalf("chunk %d: diagnostic ends differ", chunk)
		}
	}
}

func TestDiagnosticCapturePreservesShortOutput(t *testing.T) {
	var capture diagnosticCapture
	for range 3 {
		_, err := capture.Write([]byte(strings.Repeat("x", 1000)))
		testutil.FailErr(t, "capture short diagnostics", err)
	}
	if capture.String() != strings.Repeat("x", 3000) {
		t.Fatal("short diagnostic output changed")
	}
}

func TestEvaluationReportRejectsOversizeBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	file, err := os.Create(path)
	testutil.FailErr(t, "create report", err)
	testutil.FailErr(t, "size sparse report", file.Truncate(scanexec.DefaultMaxScanOutputBytes+1))
	testutil.FailErr(t, "close report", file.Close())
	if raw, err := readEvaluationReport(path); err == nil || raw != nil {
		t.Fatal("oversize report was materialized")
	}
}
