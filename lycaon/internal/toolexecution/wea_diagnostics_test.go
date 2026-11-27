package toolexecution

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestObserveSettledAuthorityEffectWEACrossOwnerJoinAndDisabledZeroAlloc(t *testing.T) {
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
	if observeSettledAuthorityEffectWEA(action, tc, final) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeSettledAuthorityEffectWEA(action, tc, final)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeSettledAuthorityEffectWEA allocated %v allocs/op, want 0", allocs)
	}

	// 2. Enabled: verify cross-owner correlation through cmddiag.Observer.
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

	// 2a. Healthy: independent ApprovalPlan from hitl precedes settled effect -> 0 findings.
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
	if !observeSettledAuthorityEffectWEA(action, tc, final) {
		t.Fatal("expected authorized settled effect to enqueue")
	}

	// 2b. Contradiction: settled effect references unrecorded approval plan -> contradiction.
	unauthAction := action
	unauthAction.Invocation.ActionID = "act-auth-unrecorded"
	unauthFinal := &hitl.CheckpointResponse{
		CheckpointID: "cp-never-recorded",
		Status:       hitl.DecisionStatusApproved,
		Result:       &hitl.DecisionResult{Approved: true},
	}
	if !observeSettledAuthorityEffectWEA(unauthAction, tc, unauthFinal) {
		t.Fatal("expected unrecorded-plan settled effect to enqueue")
	}

	// 2c. Contradiction: settled effect digest disagrees with recorded approval plan -> contradiction.
	mismatchAction := action
	mismatchAction.Invocation.ActionID = "act-auth-mismatch"
	mismatchSeq := cmddiag.NextWEASeq()
	cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: tc.Identity.SessionID,
		ScopeKey:  "cp-auth-mismatch",
		Settled:   true,
		ApprovalPlans: []cmddiag.WEAApprovalPlanFact{
			{
				ApprovalPlanID: "cp-auth-mismatch",
				SessionID:      tc.Identity.SessionID,
				ActionDigest:   "sha256:tampered-different-digest",
				DecisionSeq:    mismatchSeq,
				Approved:       true,
				Settled:        true,
			},
		},
	})
	mismatchFinal := &hitl.CheckpointResponse{
		CheckpointID: "cp-auth-mismatch",
		Status:       hitl.DecisionStatusApproved,
		Result:       &hitl.DecisionResult{Approved: true},
	}
	if !observeSettledAuthorityEffectWEA(mismatchAction, tc, mismatchFinal) {
		t.Fatal("expected digest-mismatch settled effect to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 2 {
		t.Fatalf("expected 2 contradictions (unrecorded + digest_mismatch), got %d: %+v", len(findings), findings)
	}
	reasons := map[string]bool{}
	for _, f := range findings {
		if f.RuleID != cmddiag.RuleWEAUnauthorizedEffect || f.Disposition != cmddiag.DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		reasons[f.Reason] = true
	}
	if !reasons["approval_plan_not_recorded"] || !reasons["approval_plan_digest_mismatch"] {
		t.Fatalf("expected both approval_plan_not_recorded and approval_plan_digest_mismatch, got %+v", findings)
	}
}
