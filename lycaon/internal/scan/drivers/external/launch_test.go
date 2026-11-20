package external

import (
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// An operator-installed scanner launches as an external scanner, attributed to
// its catalog id and never under agent or bundled confinement.
func TestExternalScannerLaunchesUnconfined(t *testing.T) {
	scanner, err := NewScanner(newEntry(t, []string{"scanner"}), t.TempDir(), "", "")
	testutil.FailErr(t, "new scanner", err)
	plan := scanner.launch()
	if plan.Kind != exec.LaunchExternalScanner || plan.Subject != "fake-ext" || plan.Confinement != nil {
		t.Fatalf("launch = %s/%q confinement=%v, want %s/fake-ext unconfined", plan.Kind, plan.Subject, plan.Confinement, exec.LaunchExternalScanner)
	}
}
