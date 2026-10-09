package session

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
)

func TestReviewGuidanceFollowsLiveVerdictGate(t *testing.T) {
	for _, id := range []anchor.ID{anchor.ReviewLoopContinue, anchor.ReviewLoopDecide} {
		for _, pending := range []bool{false, true} {
			for _, staged := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/pending=%v/staged=%v", id, pending, staged), func(t *testing.T) {
					mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
					mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))
					kicks := mgr.Coordinator.Runtime.Kicks()
					review := anchor.InformRender(id)
					other := anchor.InformRender(anchor.ComposeDone)
					kicks.QueueDeferred("review-session", review)
					kicks.QueueDeferred("review-session", other)
					kicks.QueueDeferred("other-session", review)
					if staged {
						if got := kicks.TakePendingKickID("review-session"); got != review {
							t.Fatalf("open review guidance = %q, want %q", got, review)
						}
					}
					mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: pending}))
					want := other
					if pending {
						want = review
					}
					if got := mgr.Coordinator.Guidance.PendingIDs(t.Context(), "review-session"); len(got) == 0 || got[0] != want {
						t.Fatalf("guidance = %v, want %q first for current verdict gate", got, want)
					}
					if got, ok := kicks.PeekPendingKickID("other-session"); !ok || got != review {
						t.Fatalf("another session's guidance changed: %q, %v", got, ok)
					}
				})
			}
		}
	}
}
