package hitl

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestObserveApprovalResolutionWEAParityAndRevalidation(t *testing.T) {
	plan1 := &ApprovalPlan{ID: "cp-1", ActionDigest: "sha256:action-1"}
	plan2 := &ApprovalPlan{ID: "cp-2", ActionDigest: "sha256:action-1"}
	option := ApprovalOption{
		ID:    "allow_chat",
		Kind:  ApprovalOptionLease,
		Scope: ApprovalGrantScopeChat,
		Authority: []ApprovalAuthorityDelta{
			{
				Grant: &ApprovalGrant{
					ID:            "grant-chat-1",
					Scope:         ApprovalGrantScopeChat,
					ChatSessionID: "11111111-2222-3333-4444-555555555555",
					ProjectID:     "proj-1",
				},
			},
		},
	}
	approved := &DecisionResult{Approved: true, OptionID: "allow_chat"}
	refused := &DecisionResult{Approved: false, OptionID: "deny"}

	// Disabled: 0 allocs/op.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if observeApprovalResolutionWEA("11111111-2222-3333-4444-555555555555", "cp-1", plan1, option, approved) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeApprovalResolutionWEA("11111111-2222-3333-4444-555555555555", "cp-1", plan1, option, approved)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeApprovalResolutionWEA allocated %v allocs/op, want 0", allocs)
	}

	// Enabled: refusal followed by superseding revalidation emits 0 warnings.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 8,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	if !observeApprovalResolutionWEA("11111111-2222-3333-4444-555555555555", "cp-1", plan1, option, refused) {
		t.Fatal("expected refusal observation to enqueue")
	}
	if !observeApprovalResolutionWEA("11111111-2222-3333-4444-555555555555", "cp-2", plan2, option, approved) {
		t.Fatal("expected superseding approval observation to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings on refusal + superseding approval, got %+v", findings)
	}
}
