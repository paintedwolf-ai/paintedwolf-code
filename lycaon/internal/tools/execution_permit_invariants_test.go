package tools

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
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
			if reject := finalizeExecutionCapability(changed, changedReq); reject == nil {
				t.Fatal("changed action borrowed authority")
			}
			if reject := finalizeExecutionCapability(tc, req); reject != nil {
				t.Fatalf("rejected probe consumed original authority: %v", reject)
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
	return tc, req
}
