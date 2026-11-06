//go:build linux

package confine_test

import (
	"testing"
)

func TestLinuxWorkerCanReachOnlyItsPlacedBranch(t *testing.T) {
	f := newPlacementGrantFixture(t)
	denied := []string{
		"create " + f.other + "/written",
		"create " + f.folder + "/written",
		"create " + f.marker,
		"mkdir " + f.branch + ".meta",
	}
	results := floorsRun(t, denyConfinement(f.branch), f.branch, append([]string{"create written"}, denied...)...)
	if results["create written"] != "ok" {
		t.Fatalf("the worker cannot write its branch: %v", results)
	}
	for _, line := range denied {
		if results[line] == "ok" || results[line] == "" {
			t.Errorf("%s: %q", line, results[line])
		}
	}
}
