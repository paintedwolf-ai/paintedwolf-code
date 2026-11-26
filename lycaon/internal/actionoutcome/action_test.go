package actionoutcome

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestDispatchSettlesOnlyTheAction(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ctx, action := Capture(t.Context(), api.ContinuationActionFeedback)
	ticket := s.Receive(ctx, "s", 1, "phase_advanced")
	late := Register(ctx, "s")
	turnCtx, turn := BeginTurn(WithTicket(ctx, ticket), s.Turns, "s", "turn")
	action.Commit(s, "s", nil)
	if s.Actions.Snapshot()[0].State != api.ContinuationStatePending {
		t.Fatal("admission settled the action")
	}
	Dispatch(turnCtx)
	got, err := s.Actions.Await(t.Context(), action.ID())
	if err != nil || got.State != api.ContinuationStateContinued || got.TurnID != "turn" {
		t.Fatalf("dispatch: %+v, %v", got, err)
	}
	if !s.Turns.Snapshot()[0].SettledAt.IsZero() {
		t.Fatal("dispatch settled the turn")
	}
	late.Resolve(WakeResult(api.WakeReasonDefect))
	turn.Finish(nil, errors.New("provider failed"), "")
	trail := sessions.Trails()["s"]
	if trail.Turns[0].Outcome != api.TurnOutcomeKindFailed || trail.Continuations[0].State != got.State ||
		trail.Wakes[0].ContinuationID != got.ContinuationID || trail.Wakes[0].TurnID != got.TurnID {
		t.Fatalf("joined trail: %+v", trail)
	}
}

func TestActionUsesRegisteredPathOrder(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ctx, action := Capture(t.Context(), api.ContinuationActionFeedback)
	first := Register(ctx, "s")
	second := Register(ctx, "s")
	Register(ctx, "other")
	second.Resolve(WakeResult(api.WakeReasonNoRequest))
	action.Commit(s, "s", nil)
	if Register(ctx, "s") != nil || s.Actions.Snapshot()[0].State != api.ContinuationStatePending {
		t.Fatal("sealed action lost its outstanding path")
	}
	first.Resolve(WakeResult(api.WakeReasonBudgetExhausted))
	got, err := s.Actions.Await(t.Context(), action.ID())
	if err != nil || got.Reason != api.ContinuationReasonTurnBudgetReached {
		t.Fatalf("action: %+v, %v", got, err)
	}
}

func TestNoticeOrderingAndRecovery(t *testing.T) {
	var n Notices
	render := func(string, api.ContinuationAction) *api.SessionHostError { return &api.SessionHostError{} }
	var events []api.ContinuationNotice
	publish := func(v api.ContinuationNotice) { events = append(events, v) }
	a := api.ActionContinuation{SessionID: "s", ContinuationID: "a", State: api.ContinuationStateNotContinued, Reason: api.ContinuationReasonNoOpenRequest}
	n.Observe(2, a, render, publish)
	older := a
	older.State = api.ContinuationStateContinued
	n.Observe(1, older, render, publish)
	a.ContinuationID = "b"
	n.Observe(3, a, render, publish)
	n.Recover("a")
	n.Recover("b")
	n.Recover("b")
	if len(events) != 3 || events[0].ContinuationID != "a" || events[1].ContinuationID != "b" || events[2].ContinuationID != "" || events[2].Revision != 3 {
		t.Fatalf("notice revisions: %+v", events)
	}
}
