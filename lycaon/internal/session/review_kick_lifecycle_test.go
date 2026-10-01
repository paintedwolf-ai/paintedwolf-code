package session

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestReviewGuidanceFollowsLiveVerdictGate(t *testing.T) {
	for _, id := range []anchor.ID{anchor.ReviewLoopContinue, anchor.ReviewLoopDecide} {
		for _, pending := range []bool{false, true} {
			for _, staged := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/pending=%v/staged=%v", id, pending, staged), func(t *testing.T) {
					mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
					mgr.workflows = verdictPendingView{pending: true}
					kicks := mgr.ensureCoordinatorRuntime().Kicks()
					review := anchor.InformRender(id)
					other := anchor.InformRender(anchor.ComposeDone)
					kicks.QueueDeferred("review-session", review)
					kicks.QueueDeferred("review-session", other)
					kicks.QueueDeferred("other-session", review)
					if staged {
						if got := kicks.TakePendingKickIDUnless("review-session", mgr.coordinatorKickCleared(t.Context(), "review-session")); got != review {
							t.Fatalf("open review guidance = %q, want %q", got, review)
						}
					}
					mgr.workflows = verdictPendingView{pending: pending}
					want := other
					if pending {
						want = review
					}
					if got := mgr.coordinatorKickIDs(t.Context(), "review-session"); len(got) == 0 || got[0] != want {
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
