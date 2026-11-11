package toolexecution_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
)

// Authorization can cover a capability without changing what the process can do.
// Observe the real executor's gate inputs and claim the real launch permit; the
// collaborator allows review so this isolates factual projection, not matching.
type containmentObservingGate struct {
	countingApprovalGate
	actions []hitl.ProposedAction
}

func (g *containmentObservingGate) Evaluate(_ context.Context, action hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	g.actions = append(g.actions, action)
	return &hitl.ApprovalResult{}, nil
}

func TestReviewedHostExecutionPreservesPhysicalBoundaryFacts(t *testing.T) {
	root := t.TempDir()
	approvals := &containmentObservingGate{}
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	entered := false
	testutil.FailErr(t, "register command", registry.Register("command", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		req, reject := tools.ConfineRequestForSpawn(ctx, tc, nil)
		if reject != nil {
			return "", reject
		}
		if !req.HostExecution {
			t.Error("approved host execution lost its launch capability")
		}
		entered = true
		return "done", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvals), registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), &asyncHITL{requested: make(chan struct{}, 4)}, approvals)
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "task", ToolCallID: "call", Agent: "implement"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"},
	}
	_, err := executor.Invoke(t.Context(), "command", map[string]any{"command": "printf fixture", "capability_request": map[string]any{"host_execution": true}}, tc)
	testutil.FailErr(t, "invoke authorized host execution", err)
	if !entered || len(approvals.actions) == 0 {
		t.Fatal("test did not reach gate and authorized handler")
	}
	for _, action := range approvals.actions {
		if action.Invocation.Tool != "command" {
			continue
		}
		if !action.Execution.Contained.HostExecution || action.Execution.Contained.FSJailed {
			t.Fatalf("authorization rewrote physical confinement facts: %+v", action.Execution.Contained)
		}
	}
}
