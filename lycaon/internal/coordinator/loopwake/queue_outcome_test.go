package loopwake

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClaimRequeuesOnlyInItsOriginalGeneration(t *testing.T) {
	for _, reason := range []api.WakeReason{api.WakeReasonPromptInterrupted, api.WakeReasonUserStopped, api.WakeReasonRewound, api.WakeReasonTurnSettled, api.WakeReasonSessionClosed} {
		t.Run(string(reason), func(t *testing.T) {
			engine := NewLoopEngine()
			session := engine.Outcomes.For("s")
			q := engine.Nudges.sessionPendingQueue("s")
			wake := pendingLoopWake{wake: anchor.LegFinished, seq: 1, ticket: session.Receive(t.Context(), "s", 1, string(anchor.LegFinished))}
			q.push(wake)
			q.push(wake)
			claim, ok := q.pop()
			if !ok {
				t.Fatal("missing claim")
			}
			if _, duplicate := q.pop(); duplicate {
				t.Fatal("duplicated retry")
			}
			engine.Nudges.ClearPending("s", reason)
			engine.Nudges.enqueuePending("s", claim)
			result, err := wake.ticket.Await(t.Context())
			if err != nil || result.State != api.WakeStateDropped || result.Reason != reason {
				t.Fatalf("stale claim: %+v, %v", result, err)
			}
			if _, exists := engine.Nudges.Pending("s"); exists {
				t.Fatal("retired claim entered new queue")
			}
		})
	}
}

func TestClearSettlesDeferredWorkerTicket(t *testing.T) {
	engine := NewLoopEngine()
	s := engine.Outcomes.For("s")
	ticket := s.Receive(t.Context(), "s", 2, string(anchor.LegFinished))
	engine.Nudges.deferNudge(t.Context(), "s", pendingLoopWake{seq: 2, ticket: ticket, wake: anchor.LegFinished})
	engine.Nudges.ClearPending("s", api.WakeReasonUserStopped)
	r, err := ticket.Await(t.Context())
	if err != nil || r.Reason != api.WakeReasonUserStopped || len(r.Steps) != 3 || r.Steps[1].State != api.WakeStateDeferred {
		t.Fatalf("deferred wake: %+v, %v", r, err)
	}
}
