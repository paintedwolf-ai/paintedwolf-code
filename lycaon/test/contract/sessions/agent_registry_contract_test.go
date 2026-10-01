package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/orchestration"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGateRecommendedAgentsRegistered(t *testing.T) {
	t.Parallel()
	reg := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	if err := orchestration.ValidateGateAgents(reg); err != nil {
		contractcheck.FailErr(t, "validate gate agent references in bundled registry", err)
	}
	for gate, agentID := range evidence.GateRecommendedAgents() {
		if _, err := reg.Get(agentID); err != nil {
			t.Fatalf("gate %q agent %q: %v", gate, agentID, err)
		}
	}
}

func TestGateRecommendedAgentsCoverEveryGateType(t *testing.T) {
	t.Parallel()
	var want []string
	for _, gate := range evidence.AllGateTypes() {
		want = append(want, string(gate))
	}
	var got []string
	for gate := range evidence.GateRecommendedAgents() {
		got = append(got, string(gate))
	}
	contractcheck.FailSetEqual(t, "GateRecommendedAgents keys vs evidence.AllGateTypes", want, got)
}
