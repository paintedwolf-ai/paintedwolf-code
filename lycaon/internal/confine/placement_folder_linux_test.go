//go:build linux

package confine_test

import (
	"testing"
)

func TestLinuxWorkerCanReachOnlyItsPlacedBranch(t *testing.T) {
	f := newPlacementGrantFixture(t)
	expectFloorTable(t, denyConfinement(f.branch), f.branch, []floorStep{
		{"create written", "ok"},
		{"create " + f.other + "/written", "EACCES"},
		{"create " + f.folder + "/written", "EACCES"},
		{"create " + f.marker, "EACCES"},
		{"mkdir " + f.branch + ".meta", "EACCES"},
	})
}
