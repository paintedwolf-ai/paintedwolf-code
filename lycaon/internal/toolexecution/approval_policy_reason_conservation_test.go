package toolexecution_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/pkg/api"
)

// The collaborator represents a current unresolved policy reason. Its fixed
// answer isolates whether the composition layer improperly discards that answer;
// real rule matching and persistence are tested in the companion integration test.
type unresolvedPolicyGate struct {
	countingApprovalGate
	reason api.ApprovalGate
}

func (g *unresolvedPolicyGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	return &hitl.ApprovalResult{Decision: &gate.Decision{
		Primary: g.reason,
		Cited:   []gate.Fact{{Key: "fixture.uncovered", Value: "new-subject", Source: "fixture"}},
	}}, nil
}

func TestBoundaryFlagsCannotDiscardUnresolvedPolicyReasons(t *testing.T) {
	for _, reason := range []api.ApprovalGate{
		api.GateUserRule, api.GateAuthorityMisuse, api.GateIncompleteFacts,
		api.GateOutsideRootsWrite, api.GateAgentPolicyChange,
	} {
		for _, reviewed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prior-review-%t", reason, reviewed), func(t *testing.T) {
				boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
					ID: "implement", Tools: map[string]bool{"command": true},
				}})
				engine := toolexecution.NewApprovalPolicyEngine(
					toolprofiles.NewProfilePolicyEngine(boundary), &unresolvedPolicyGate{reason: reason},
				)
				decision, err := engine.Evaluate(t.Context(), platform.PolicyContext{
					ProfileID: "implement", ToolName: "command",
					ToolArgs:                  map[string]any{"command": "printf fixture"},
					ConfineRequest:            confine.Request{HostExecution: true},
					BoundaryApprovalSatisfied: true, PolicyWritesReviewed: reviewed,
				})
				testutil.FailErr(t, "compose current unresolved reason with prior boundary flags", err)
				if decision == nil || decision.Allowed || !decision.RequiresApproval || decision.Approval == nil || decision.Approval.Decision == nil || decision.Approval.Decision.Primary != reason {
					t.Fatalf("prior boundary flags discarded unresolved %s: %+v", reason, decision)
				}
			})
		}
	}
}
