package tools

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

// Invalid uses must neither gain authority nor destroy the legitimate caller's
// remaining use. Mint through the real review API rather than constructing a permit.
func TestExecutionPermitRejectsChangedIdentityWithoutConsumingOriginal(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*ToolContext, *confine.Request)
	}{
		{"session", func(tc *ToolContext, _ *confine.Request) { tc.Identity.SessionID = "other" }},
		{"call", func(tc *ToolContext, _ *confine.Request) { tc.Identity.ToolCallID = "other" }},
		{"child", func(tc *ToolContext, _ *confine.Request) { tc.Invocation.ID = "other" }},
		{"arguments", func(tc *ToolContext, _ *confine.Request) {
			tc.Effects.CanonicalArgs = map[string]any{"argv": []string{"fixture", "changed"}}
		}},
		{"root", func(_ *ToolContext, req *confine.Request) { req.Roots = []string{"/different"} }},
		{"capability", func(_ *ToolContext, req *confine.Request) { req.HostExecution = false; req.ProcessControl = true }},
	} {
		t.Run(change.name, func(t *testing.T) {
			tc, req := reviewedPermitFixture()
			changed, changedReq := tc, req
			change.apply(&changed, &changedReq)
			if covered := changed.AuthorizedExecutionCapabilities(changed.Effects.CanonicalArgs, changedReq); len(covered) != 0 {
				t.Fatalf("changed action borrowed policy coverage: %v", covered)
			}
			if changed.CurrentActionReview(changed.Effects.CanonicalArgs, changedReq) != nil {
				t.Fatal("changed action borrowed reviewed policy reasons")
			}
			for range 2 {
				if tc.CurrentActionReview(tc.Effects.CanonicalArgs, req) == nil {
					t.Fatal("inspection lost original reviewed reasons")
				}
				if covered := tc.AuthorizedExecutionCapabilities(tc.Effects.CanonicalArgs, req); !slices.Equal(covered, []string{"host_execution"}) {
					t.Fatalf("inspection lost or expanded original coverage: %v", covered)
				}
			}
			if reject := finalizeExecutionCapability(changed, changedReq); reject == nil {
				t.Fatal("changed action borrowed authority")
			}
			if reject := finalizeExecutionCapability(tc, req); reject != nil {
				t.Fatalf("rejected probe consumed original authority: %v", reject)
			}
			if covered := tc.AuthorizedExecutionCapabilities(tc.Effects.CanonicalArgs, req); len(covered) != 0 {
				t.Fatalf("consumed authority remained available to policy: %v", covered)
			}
			if tc.CurrentActionReview(tc.Effects.CanonicalArgs, req) != nil {
				t.Fatal("consumed invocation retained reviewed policy reasons")
			}
			if reject := finalizeExecutionCapability(tc, req); reject == nil {
				t.Fatal("original authority was reusable")
			}
		})
	}
}

func TestExecutionPermitCopiesHaveOneSuccessfulConsumer(t *testing.T) {
	tc, req := reviewedPermitFixture()
	var successes atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		workers.Add(1)
		go func(copy ToolContext) {
			defer workers.Done()
			<-start
			if finalizeExecutionCapability(copy, req) == nil {
				successes.Add(1)
			}
		}(tc)
	}
	close(start)
	workers.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("shared review authority admitted %d consumers, want one", got)
	}
}

func reviewedPermitFixture() (ToolContext, confine.Request) {
	req := confine.Request{HostExecution: true, Roots: []string{"/project"}}
	args := map[string]any{"argv": []string{"fixture", "original"}}
	tc := ToolContext{
		Identity:   InvocationIdentity{SessionID: "task", ToolCallID: "call"},
		Invocation: Invocation{ID: "child"},
		Execution:  InvocationExecution{HostExecution: true},
		Effects:    InvocationEffects{CanonicalArgs: args},
	}
	tc.StampExecutionApproval(args, hitl.ProposedAction{Execution: hitl.ActionExecution{ExecutionBoundaryDigest: ExecutionBoundaryDigest(req)}})
	tc.RecordCurrentActionReview(args, req, hitl.ProposedAction{
		Scope:      hitl.ActionScope{SessionID: tc.Identity.SessionID},
		Invocation: hitl.ActionInvocation{Tool: "command", ActionID: tc.EffectActionID(), Args: args},
	}, &hitl.ApprovalResult{Decision: &gate.Decision{
		Primary: api.GateUserRule,
		Cited:   []gate.Fact{{Gate: api.GateUserRule, Key: "fixture.rule", Value: "reviewed", Source: "fixture"}},
	}})
	return tc, req
}

func TestExecutionPermitInspectionDoesNotAuthorizeUnreviewedRequest(t *testing.T) {
	tc, req := reviewedPermitFixture()
	unreviewed := tc
	unreviewed.BeginInvocation()
	if covered := unreviewed.AuthorizedExecutionCapabilities(tc.Effects.CanonicalArgs, req); len(covered) != 0 {
		t.Fatalf("new invocation inherited coverage: %v", covered)
	}
	if unreviewed.CurrentActionReview(tc.Effects.CanonicalArgs, req) != nil {
		t.Fatal("new invocation inherited reviewed policy reasons")
	}
	changedArgs := map[string]any{"argv": []string{"fixture", "changed"}}
	if covered := tc.AuthorizedExecutionCapabilities(changedArgs, req); len(covered) != 0 {
		t.Fatalf("changed proposed arguments borrowed coverage: %v", covered)
	}
	ordinary := req
	ordinary.HostExecution = false
	if covered := tc.AuthorizedExecutionCapabilities(tc.Effects.CanonicalArgs, ordinary); len(covered) != 0 {
		t.Fatalf("ordinary action acquired an execution capability: %v", covered)
	}
	if reject := finalizeExecutionCapability(tc, ordinary); reject != nil {
		t.Fatalf("ordinary execution needed an unrelated permit: %v", reject)
	}
	if reject := finalizeExecutionCapability(tc, req); reject != nil {
		t.Fatalf("unrelated inspection or ordinary execution consumed original permit: %v", reject)
	}
}
