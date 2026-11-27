package toolexecution

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestPostExecutionAuthorityEffectWEALifecycleAndBypassDetection(t *testing.T) {
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:     "command",
			ActionID: "act-auth-1",
		},
		Scope: hitl.ActionScope{
			ProjectID: "proj-1",
			SessionID: "11111111-2222-3333-4444-555555555555",
		},
	}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "11111111-2222-3333-4444-555555555555",
			ProjectID:  "proj-1",
			ToolCallID: "call-auth-1",
		},
	}
	final := &hitl.CheckpointResponse{
		CheckpointID: "cp-auth-1",
		Status:       hitl.DecisionStatusApproved,
		Result:       &hitl.DecisionResult{Approved: true, OptionID: "allow_once"},
	}

	// 1. Disabled: 0 allocs/op.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	ctxOff := withAuthorityExecutionWitness(context.Background())
	if observePostExecutionAuthorityEffectWEA(ctxOff, tc) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		c := withAuthorityExecutionWitness(context.Background())
		_ = observePostExecutionAuthorityEffectWEA(c, tc)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled post-execution hook allocated %v allocs/op, want 0", allocs)
	}

	// 2. Enabled: verify post-execution semantics and bypass detection.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	// 2a. Approval granted in awaitActionApproval, but execution later fails/cancels:
	//     recordAuthorizedCheckpoint MUST NOT emit an executed-effect diagnostic fact!
	ctxCanceled := withAuthorityExecutionWitness(context.Background())
	recordAuthorizedCheckpoint(ctxCanceled, action, &hitl.CheckpointResponse{
		CheckpointID: "cp-canceled-never-ran",
		Status:       hitl.DecisionStatusApproved,
		Result:       &hitl.DecisionResult{Approved: true},
	})
	// Execution fails or is canceled -> observePostExecutionAuthorityEffectWEA is NOT called.
	// Even though cp-canceled-never-ran was never recorded in hitl, zero findings must be emitted!

	// 2b. Healthy: independent ApprovalPlan from hitl precedes post-execution settlement -> 0 findings.
	planSeq := cmddiag.NextWEASeq()
	cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: tc.Identity.SessionID,
		ScopeKey:  "cp-auth-1",
		Settled:   true,
		ApprovalPlans: []cmddiag.WEAApprovalPlanFact{
			{
				ApprovalPlanID: "cp-auth-1",
				SessionID:      tc.Identity.SessionID,
				ActionDigest:   hitl.GrantKey(action),
				DecisionSeq:    planSeq,
				Approved:       true,
				Settled:        true,
			},
		},
	})
	ctxOK := withAuthorityExecutionWitness(context.Background())
	recordAuthorizedCheckpoint(ctxOK, action, final)
	if !observePostExecutionAuthorityEffectWEA(ctxOK, tc) {
		t.Fatal("expected authorized post-execution effect to enqueue")
	}

	// 2c. Contradiction: real committed effect on a path that bypassed awaitActionApproval
	//     (requiresAuthority=true, no approvalPlanID/grantID) IS emitted and caught!
	ctxBypass := withAuthorityExecutionWitness(context.Background())
	recordAuthorityRequirement(ctxBypass, "command", hitl.GrantKey(action), "")
	tcBypass := tc
	tcBypass.Identity.ToolCallID = "call-bypass-1"
	if !observePostExecutionAuthorityEffectWEA(ctxBypass, tcBypass) {
		t.Fatal("expected bypassed post-execution effect to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding (bypass only; canceled approval must emit 0), got %d: %+v", len(findings), findings)
	}
	if findings[0].RuleID != cmddiag.RuleWEAUnauthorizedEffect || findings[0].Disposition != cmddiag.DispositionContradiction {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
	if findings[0].Reason != "effect_executed_without_matching_approval_or_grant" {
		t.Fatalf("unexpected reason %q, want effect_executed_without_matching_approval_or_grant", findings[0].Reason)
	}
}
