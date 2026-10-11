package runtime

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

func TestFilterPersistedFailedLeavesUsesCurrentLiveGates(t *testing.T) {
	snap := inject.WorkflowRuntimeSnapshot{Phases: []inject.WorkflowPhaseRow{
		{ID: "old", Gates: []inject.WorkflowGateState{{ID: "old_gate"}}},
		{ID: "work", Gates: []inject.WorkflowGateState{
			{ID: "worker_cycle_ready", Dormant: true},
			{ID: "live_gate"},
		}},
	}}
	got := filterPersistedFailedLeaves(
		[]string{"old_gate", "worker_cycle_ready", "live_gate"}, "work", snap,
	)
	if len(got) != 1 || got[0] != "live_gate" {
		t.Fatalf("filtered leaves = %v", got)
	}
}
