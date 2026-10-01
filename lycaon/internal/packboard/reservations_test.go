package packboard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatReservedPathsLines(t *testing.T) {
	lines := packboard.FormatReservedPathsLines([]api.BoardReservationEntry{
		{Path: "shellsim/builtins.py", JobID: "job-timer", LegLabel: "Timer leg"},
	})
	if len(lines) != 2 {
		t.Fatalf("lines = %#v", lines)
	}
	if lines[0] != "Reserved paths (active):" {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "shellsim/builtins.py") || !strings.Contains(lines[1], "Timer leg") {
		t.Fatalf("detail line = %q", lines[1])
	}
}

func TestReservationsFromSnapshotRoundTrip(t *testing.T) {
	snap := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{}}
	packboard.EnrichSnapshotReservations(&snap, []api.BoardReservationEntry{
		{Path: "pkg/foo.go", JobID: "job-a"},
	})
	got := packboard.ReservationsFromSnapshot(snap)
	if len(got) != 1 || got[0].Path != "pkg/foo.go" {
		t.Fatalf("got = %+v", got)
	}
}
