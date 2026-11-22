package packboard_test

import (
	"slices"
	"strings"
	"testing"
	"time"

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

// Every host inject scope carries the held paths enriched into the snapshot,
// and a new hold changes the pulse fingerprint so the next turn sees it.
func TestInjectLinesCarryActiveReservations(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	const held = "sentinel/held-path.go"
	snap := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{}}
	before := packboard.PulseFingerprint(snap, now)
	for _, scope := range []packboard.InjectScope{packboard.InjectScopeFull, packboard.InjectScopePulse} {
		lines := packboard.BuildInjectLines(snap, scope, packboard.OrientOpts{Now: now})
		if slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, held) }) {
			t.Fatalf("scope %d lists %q before it is held: %q", scope, held, lines)
		}
	}
	packboard.EnrichSnapshotReservations(&snap, []api.BoardReservationEntry{{Path: held, JobID: "job-sentinel"}})
	for _, scope := range []packboard.InjectScope{packboard.InjectScopeFull, packboard.InjectScopePulse} {
		lines := packboard.BuildInjectLines(snap, scope, packboard.OrientOpts{Now: now})
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, held) }) {
			t.Fatalf("scope %d omits held path %q: %q", scope, held, lines)
		}
	}
	if packboard.PulseFingerprint(snap, now) == before {
		t.Fatal("pulse fingerprint ignores a new reservation")
	}
}
