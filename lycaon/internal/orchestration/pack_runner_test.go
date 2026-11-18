package orchestration

import (
	"context"
	"strings"
	"testing"
)

// TestOrchestratorRunDispatchesEveryPattern derives coverage from orchestration.AllTopologyPatterns
// and the orchestrator's real dispatch behavior: every supported pattern must reach a Run case
// (any error other than "not implemented"), and an unknown pattern must hit the default.
func TestOrchestratorRunDispatchesEveryPattern(t *testing.T) {
	orch := NewOrchestratorImpl(OrchestratorDeps{})
	ctx := context.Background()

	for _, pattern := range AllTopologyPatterns() {
		_, err := orch.Run(ctx, RunRequest{Topology: TopologySpec{Pattern: pattern}})
		if err == nil {
			t.Fatalf("pattern %q: expected a dispatch error from a dependency-less orchestrator, got nil", pattern)
		}
		if strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("pattern %q is in AllTopologyPatterns but Run has no dispatch case: %v", pattern, err)
		}
	}

	_, err := orch.Run(ctx, RunRequest{Topology: TopologySpec{Pattern: "no-such-pattern"}})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unknown pattern should hit the default unimplemented case, got %v", err)
	}
}
