package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func testComposer(t *testing.T) *workflowcomposition.Composer {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	return &workflowcomposition.Composer{
		SessionStore: workflowdrafts.NewMemory(),
		Registry:     reg,
		Agents:       agents,
		Policy:       testComposePolicy(t),
	}
}

func testComposePolicy(t *testing.T) *workflowcomposition.ComposePolicy {
	t.Helper()
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	return policy
}

func testPersister(t *testing.T) (*workflowcomposition.Persister, *workflowdrafts.Memory) {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	store := workflowdrafts.NewMemory()
	return &workflowcomposition.Persister{
		SessionStore: store,
		Registry:     reg,
		Agents:       agents,
		Policy:       testComposePolicy(t),
	}, store
}
