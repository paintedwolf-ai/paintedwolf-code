package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestWorkflowManifestAllowedAgentsExist(t *testing.T) {
	t.Parallel()
	catalog := workflowfixture.ContractAllResolvedManifests(t)
	reg := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), reg))
	for key, m := range catalog {
		for _, agentID := range m.AllowedAgents {
			if _, err := reg.Get(agentID); err != nil {
				t.Errorf("%s: agent %q not in stock agents: %v", key, agentID, err)
			}
		}
	}
}

func TestBundledToolProfilesLoad(t *testing.T) {
	t.Parallel()
	_, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
}
