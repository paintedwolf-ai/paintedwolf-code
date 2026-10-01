package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

// TestTaskSpawnAllowlistForTurnNilVsEmpty pins the nil-vs-empty distinction taskSpawnAllowlistForTurn
// carries: nil when the frame has no roster (declared fallback applies), empty non-nil when the
// roster filtered every agent out — the repoKnownEmpty scenario, where nothing is dispatchable.
func TestTaskSpawnAllowlistForTurnNilVsEmpty(t *testing.T) {
	if got := taskSpawnAllowlistForTurn(inject.CoordinatorTurnFrame{}); got != nil {
		t.Fatalf("no roster must yield nil, got %#v", got)
	}

	filtered := inject.CoordinatorTurnFrame{Roster: &inject.AgentRoster{
		Declared: []string{"implementer", "repo-researcher"},
		Excluded: []inject.ExcludedAgent{
			{Name: "implementer", Code: inject.RosterExcludeRepoEmpty},
			{Name: "repo-researcher", Code: inject.RosterExcludeRepoEmpty},
		},
	}}
	got := taskSpawnAllowlistForTurn(filtered)
	if got == nil || len(got) != 0 {
		t.Fatalf("all-excluded roster must yield empty non-nil, got %#v", got)
	}

	populated := inject.CoordinatorTurnFrame{Roster: &inject.AgentRoster{
		Declared:  []string{"implementer"},
		Effective: []string{"implementer"},
	}}
	if got := taskSpawnAllowlistForTurn(populated); len(got) != 1 || got[0] != "implementer" {
		t.Fatalf("effective roster lost: %#v", got)
	}
}
