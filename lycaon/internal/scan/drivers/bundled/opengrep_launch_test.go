package bundleddriver

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// OpenGrep is a bundled scanner: it launches as one, confined without egress
// to reading the project and writing its run files.
func TestOpengrepLaunchesAsConfinedBundledScanner(t *testing.T) {
	runFiles, cleanup, err := newOpengrepRunFiles()
	testutil.FailErr(t, "create run files", err)
	defer cleanup()
	projectDir := t.TempDir()
	plan, err := opengrepLaunch(projectDir, runFiles)
	testutil.FailErr(t, "build launch", err)
	if plan.Kind != exec.LaunchBundledScanner || plan.Subject != "opengrep" {
		t.Fatalf("launch = %s/%q, want %s/opengrep", plan.Kind, plan.Subject, exec.LaunchBundledScanner)
	}
	box := plan.Confinement
	if box == nil {
		if confine.RequireApplied(false) != nil {
			t.Fatal("bundled scanner launch carries no confinement where confinement is required")
		}
		return
	}
	if box.Network != confine.NetworkDeny || box.HostExecution {
		t.Fatalf("confinement network = %v host execution = %t, want denied egress", box.Network, box.HostExecution)
	}
	if slices.Contains(box.Roots, projectDir) || len(box.ReadRoots) == 0 {
		t.Fatalf("confinement roots = %v read roots = %v, want the project read-only", box.Roots, box.ReadRoots)
	}
}
