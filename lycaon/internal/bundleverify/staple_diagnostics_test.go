package bundleverify

import (
	"errors"
	"testing"
)

func TestStapleFailureRetainsToolDiagnostics(t *testing.T) {
	t.Parallel()
	runner := signedOK()
	runner.err["xcrun"] = errors.New("exit status 65")
	runner.stdout["xcrun"] = []byte("  Processing: release.dmg\n")
	runner.stderr["xcrun"] = []byte("  ticket validation failed\n")
	report := runVerify(t, buildFakeApp(t, defaultAppOptions()), true, runner)
	finding := requireCodeCount(t, report, CodeNotStapled, 1)[0]
	for key, want := range map[string]string{
		"reason": "exit status 65", "stdout": "Processing: release.dmg", "stderr": "ticket validation failed",
	} {
		if got := finding.Detail[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if !report.Failed() {
		t.Fatal("retaining diagnostics must not admit a failed staple")
	}
}
