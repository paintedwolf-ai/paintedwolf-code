package orchestration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateSupervisorSpecMaxAgents(t *testing.T) {
	err := validateSupervisorSpec(SupervisorSpec{
		ProfileIDs: []string{"a", "b", "c", "d", "e", "f"},
		MaxAgents:  MaxTeamAgents,
	})
	if err == nil {
		t.Fatal("expected max agents error")
	}
}

func TestEffectiveSupervisorMaxAgents(t *testing.T) {
	if got := effectiveSupervisorMaxAgents(SupervisorSpec{MaxAgents: 3}); got != 3 {
		t.Fatalf("max = %d want 3", got)
	}
	if got := effectiveSupervisorMaxAgents(SupervisorSpec{MaxAgents: 0}); got != MaxTeamAgents {
		t.Fatalf("max = %d want %d", got, MaxTeamAgents)
	}
}

func TestLoadSupervisorParallelTopology(t *testing.T) {
	spec, err := LoadTopologyFromFile(bundledTopologyPath(t, "supervisor-parallel.yaml"))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pattern != TopologySupervisor || spec.Supervisor == nil {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if len(spec.Supervisor.ProfileIDs) != 3 {
		t.Fatalf("profile_ids = %d want 3", len(spec.Supervisor.ProfileIDs))
	}
}
