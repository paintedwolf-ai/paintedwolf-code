package testutil

import (
	"os"
	"strings"
	"testing"
)

// EnvScannerSuiteRequired makes an unprovisioned scanner resource fatal instead
// of a skip. Jobs that promise bundled-scanner coverage set it, matching
// LYCAON_GIT_PARITY_REQUIRED on the git-parity job.
const EnvScannerSuiteRequired = "LYCAON_SCANNER_SUITE_REQUIRED"

// scannerSuiteRequired reports whether the caller is inside a job that promises
// bundled-scanner coverage.
func scannerSuiteRequired() bool {
	return strings.TrimSpace(os.Getenv(EnvScannerSuiteRequired)) == "1"
}

// MissingScannerResource skips the test on a host with no scanner engine
// staged, and fails it where the suite was promised. The engines are binaries
// fetched by manifest pin: absent on a laptop is ordinary, absent in the job
// that exists to run them is a provisioning failure.
func MissingScannerResource(t testing.TB, resource string, err error) {
	t.Helper()
	if scannerSuiteRequired() {
		t.Fatalf("%s unavailable but %s=1: stage the pinned engine "+
			"(./task scan:opengrep:stage) before running the bundled-scanner suite: %v",
			resource, EnvScannerSuiteRequired, err)
	}
	t.Skipf("%s unavailable: %v", resource, err)
}
