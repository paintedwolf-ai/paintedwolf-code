package security

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
)

func TestSharedTreeFanOutAdvisoryOnly(t *testing.T) {
	spec := orchestration.TopologySpec{
		Pattern:       orchestration.TopologyFanOut,
		WorkspaceMode: orchestration.WorkspaceShared,
		FanOut: &orchestration.FanOutSpec{
			ProfileID: orchestration.ProfileImplementer,
			Subtasks:  []string{"approach A"},
		},
	}
	if orchestration.TopologyRequiresIsolation(spec) {
		t.Fatal("shared fan-out must remain advisory-only without isolated workspace_mode")
	}
}

func TestIsolatedFanOutRequiresWorkspaceBinder(t *testing.T) {
	ctx := context.Background()
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Agents: orchestration.NewMemoryAgentRegistryForTest(),
	})
	_, err := orch.Run(ctx, orchestration.RunRequest{
		SessionID: "sess-e2e",
		Topology: orchestration.TopologySpec{
			Pattern:       orchestration.TopologyFanOut,
			WorkspaceMode: orchestration.WorkspaceIsolated,
			FanOut: &orchestration.FanOutSpec{
				ProfileID: orchestration.ProfileImplementer,
				Subtasks:  []string{"approach A", "approach B"},
			},
		},
		Input: map[string]any{"project_dir": t.TempDir()},
	})
	if err == nil {
		t.Fatal("isolated fan-out without workspace manager must fail closed")
	}
}
