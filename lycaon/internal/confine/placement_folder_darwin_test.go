//go:build darwin

package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

func TestSeatbeltWorkerCanReachOnlyItsPlacedBranch(t *testing.T) {
	self := requireSeatbelt(t)
	f := newPlacementGrantFixture(t)
	boundary := confine.Confinement{Roots: []string{f.branch}, Network: confine.NetworkDeny}
	if code, out := confinedRun(t, self, boundary, "/usr/bin/touch", filepath.Join(f.branch, "written")); code != 0 {
		t.Fatalf("the worker cannot write its branch: exit=%d %s", code, out)
	}
	for _, denied := range []string{filepath.Join(f.other, "written"), filepath.Join(f.folder, "written"), f.marker, f.branch + ".meta"} {
		if code, _ := confinedRun(t, self, boundary, "/usr/bin/touch", denied); code == 0 {
			t.Fatalf("the worker wrote %s", denied)
		}
	}
	if code, _ := confinedRun(t, self, boundary, "/bin/cat", f.marker); code == 0 {
		t.Fatal("the worker read the placement marker")
	}
	if _, err := os.Stat(filepath.Join(f.other, "written")); !os.IsNotExist(err) {
		t.Fatalf("another branch changed: %v", err)
	}
}
