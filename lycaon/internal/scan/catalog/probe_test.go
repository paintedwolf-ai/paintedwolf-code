package catalog

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestProbeScannerReportsWorkingBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh as a stand-in scanner binary")
	}
	res := ProbeScanner(context.Background(), "fake", []string{"sh", "-c", "echo 'fake 1.2.3'"})
	if !res.OK {
		t.Fatalf("OK = false, detail = %q", res.Detail)
	}
	if !res.BinaryFound {
		t.Fatal("BinaryFound = false for a binary on PATH")
	}
	if res.Detail != "fake 1.2.3" {
		t.Fatalf("Detail = %q, want the tool's version line", res.Detail)
	}
}

func TestProbeScannerDistinguishesMissingBinary(t *testing.T) {
	res := ProbeScanner(context.Background(), "absent", []string{"lycaon-no-such-scanner-binary", "--version"})
	if res.OK {
		t.Fatal("OK = true for a binary that is not installed")
	}
	if res.BinaryFound {
		t.Fatal("BinaryFound = true for a binary that is not installed")
	}
	if !strings.Contains(res.Detail, "not on PATH") {
		t.Fatalf("Detail = %q, want a not-installed reason", res.Detail)
	}
}

// Broken installations and missing installations have distinct statuses.
func TestProbeScannerDistinguishesBrokenInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh as a stand-in scanner binary")
	}
	res := ProbeScanner(context.Background(), "broken", []string{"sh", "-c", "echo 'missing shared library' 1>&2; exit 127"})
	if res.OK {
		t.Fatal("OK = true for a probe that exited non-zero")
	}
	if !res.BinaryFound {
		t.Fatal("BinaryFound = false, but the binary is on PATH and ran")
	}
	if !strings.Contains(res.Detail, "missing shared library") {
		t.Fatalf("Detail = %q, want the tool's own error", res.Detail)
	}
}

func TestProbeScannerWithoutProbeCommand(t *testing.T) {
	res := ProbeScanner(context.Background(), "none", nil)
	if res.OK {
		t.Fatal("OK = true without a probe command")
	}
}
