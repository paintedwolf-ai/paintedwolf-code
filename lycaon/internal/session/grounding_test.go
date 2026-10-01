package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveToolProfileDelegationUsesCoordinator(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureOrchestrate}
	if got := ResolveToolProfile(sess, orchestration.NewMemoryAgentRegistry(), ""); got != "coordinator" {
		t.Fatalf("profile = %q want coordinator", got)
	}
}

func TestResolveToolProfileWorkerAgent(t *testing.T) {
	reg := orchestration.NewMemoryAgentRegistryForTest()
	sess := &api.Session{Posture: api.SessionPostureOrchestrate, AgentType: "implementer"}
	if got := ResolveToolProfile(sess, reg, ""); got != "implement" {
		t.Fatalf("profile = %q want implement", got)
	}
}

func TestResolveToolProfilePlanUsesCoordinator(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureSpec}
	if got := ResolveToolProfile(sess, nil, ""); got != "coordinator" {
		t.Fatalf("profile = %q want coordinator", got)
	}
}
