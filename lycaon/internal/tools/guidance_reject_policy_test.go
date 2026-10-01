package tools_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestGuidanceRejectPolicyEvaluateForListDelegatesAskTools(t *testing.T) {
	t.Parallel()
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	approvalStore := isolatedApprovalStore(t)
	// Standard auto-approves contained delete; assert delegation by explicitly asking on it.
	testutil.FailErr(t, "PutGlobal", approvalStore.PutGlobal(settings.ApprovalConfig{
		Rules: []settings.ApprovalRule{
			{Category: settings.ApprovalCategoryTool, Pattern: "delete", Effect: settings.ApprovalEffectAsk},
		},
	}))
	gate := settings.NewRuleApprovalGate(approvalStore, settings.NoSources())
	wrapped := tools.NewGuidanceRejectPolicy(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate))

	ctx := context.Background()
	eval := platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "delete",
	}
	listDecision, err := wrapped.EvaluateForList(ctx, eval)
	testutil.FailErr(t, "EvaluateForList", err)
	if listDecision == nil || !listDecision.Allowed {
		t.Fatalf("delete should be list-visible on profile; got %+v", listDecision)
	}

	invokeDecision, err := wrapped.Evaluate(ctx, eval)
	testutil.FailErr(t, "Evaluate", err)
	if invokeDecision == nil || invokeDecision.Allowed || !invokeDecision.RequiresApproval {
		t.Fatalf("delete invoke should require approval; got %+v", invokeDecision)
	}
}

func TestGuidanceRejectPolicyEvaluateForListChmodAutoApprove(t *testing.T) {
	t.Parallel()
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	approvalStore := isolatedApprovalStore(t)
	gate := settings.NewRuleApprovalGate(approvalStore, settings.NoSources())
	wrapped := tools.NewGuidanceRejectPolicy(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate))

	ctx := context.Background()
	eval := platform.PolicyContext{
		ProfileID: "implement",
		ToolName:  "chmod",
	}
	decision, err := wrapped.EvaluateForList(ctx, eval)
	testutil.FailErr(t, "EvaluateForList", err)
	if decision == nil || !decision.Allowed {
		t.Fatalf("chmod should be list-visible; got %+v", decision)
	}
	invoke, err := wrapped.Evaluate(ctx, eval)
	testutil.FailErr(t, "Evaluate", err)
	if invoke == nil || !invoke.Allowed {
		t.Fatalf("chmod invoke should auto-approve; got %+v", invoke)
	}
}

func TestGuidanceRejectPolicyListIncludesDeleteOnExecutor(t *testing.T) {
	t.Parallel()
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	approvalStore := isolatedApprovalStore(t)
	gate := settings.NewRuleApprovalGate(approvalStore, settings.NoSources())
	policy := tools.NewGuidanceRejectPolicy(tools.NewApprovalPolicyEngine(tools.NewProfilePolicyEngine(boundary), gate))
	reg := tools.NewDefaultRegistry()
	for _, name := range []string{"chmod", "delete", "write"} {
		n := name
		if err := reg.Register(n, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			return n, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	exec := tools.NewDefaultToolExecutor(policy, reg, "implement")
	listed, err := exec.List(context.Background(), platform.ToolFilter{ProfileID: "implement"})
	testutil.FailErr(t, "List", err)
	names := make(map[string]struct{}, len(listed))
	for _, meta := range listed {
		names[meta.Name] = struct{}{}
	}
	for _, want := range []string{"chmod", "delete", "write"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("List missing %q; have %v", want, names)
		}
	}
}
