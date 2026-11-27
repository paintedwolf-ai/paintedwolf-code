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

func TestLoadingRefusalPreservesAdmittedTurn(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ctx, action := Capture(t.Context(), api.ContinuationActionFeedback)
	ticket := s.Receive(ctx, "s", 1, "phase_advanced")
	_, turn := BeginTurn(WithTicket(ctx, ticket), s.Turns, "s", "turn")
	turn.Finish(nil, ErrNoRequest, "")
	action.Commit(s, "s", nil)
	outcome, err := s.Actions.Await(ctx, action.ID())
	if err != nil || outcome.State != api.ContinuationStateNotContinued || outcome.Reason != api.ContinuationReasonNoOpenRequest || outcome.TurnID != "turn" {
		t.Fatalf("continuation: %+v, %v", outcome, err)
	}
	record, err := s.Turns.Await(ctx, "turn")
	if err != nil || record.Outcome != api.TurnOutcomeKindRefused || !record.DispatchedAt.IsZero() {
		t.Fatalf("turn: %+v, %v", record, err)
	}
}

func TestClosedLeaseReplaysToLateAction(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ctx, action := Capture(t.Context(), api.ContinuationActionWorkflowResume)
	ticket := s.Receive(ctx, "s", 1, "phase_advanced")
	var lease Handoff
	turnCtx, turn := BeginTurn(WithLease(ctx, "lease", &lease), s.Turns, "s", "turn")
	Dispatch(turnCtx)
	turn.Finish(nil, nil, "")
	ticket.Routed("lease", &lease)
	action.Commit(s, "s", nil)
	got, err := s.Actions.Await(t.Context(), action.ID())
	if err != nil || got.State != api.ContinuationStateContinued || got.TurnID != "turn" {
		t.Fatalf("late lease follower: %+v, %v", got, err)
	}
	trail := sessions.Trails()["s"]
	if trail.Turns[0].LeaseID != trail.Wakes[0].LeaseID {
		t.Fatalf("lease trail: %+v", trail)
	}
}

func TestInvalidSecondDispositionPreservesFirst(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ticket := s.Receive(t.Context(), "s", 1, "phase_advanced")
	ticket.Record(api.WakeStateDropped, api.WakeReasonUserStopped, "", "")
	defer func() {
		if recover() == nil {
			t.Fatal("accepted duplicate disposition")
		}
		trail := sessions.Trails()["s"]
		if len(trail.Defects) != 1 || trail.Defects[0].Attempt.State != api.WakeStateDelivered || trail.Wakes[0].Reason != api.WakeReasonUserStopped {
			t.Fatalf("duplicate: %+v", trail)
		}
	}()
	ticket.Record(api.WakeStateDelivered, "", "turn", "")
}

func TestProducerCanAttachAfterCommitAndHandoff(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ctx, a := Capture(t.Context(), api.ContinuationActionPlanApproval)
	callback, finish := Producer(ctx)
	a.Commit(s, "s", nil)
	path := Register(callback, "s")
	if path == nil {
		t.Fatal("late callback lost registration")
	}
	path.Resolve(Result{State: api.ContinuationStateContinued, TurnID: "turn"})
	result, err := s.Actions.Await(t.Context(), a.ID())
	if err != nil || result.TurnID != "turn" {
		t.Fatalf("handoff: %+v, %v", result, err)
	}
	late := s.Receive(callback, "s", 2, "phase_advanced")
	late.Record(api.WakeStateSkipped, api.WakeReasonNoRequest, "", "")
	finish()
	if Register(callback, "s") != nil || s.Wakes.Snapshot()[0].ContinuationID != a.ID() {
		t.Fatal("callback lifetime")
	}
}

func TestDuplicateOperationCannotDecidePendingOriginal(t *testing.T) {
	var sessions Sessions
	s := sessions.For("s")
	ctx, original := CaptureOperation(t.Context(), api.ContinuationActionPlanApproval, "approval")
	owner := Register(ctx, "s")
	original.Commit(s, "s", nil)
	ctx, duplicate := CaptureOperation(t.Context(), api.ContinuationActionPlanApproval, "approval")
	late := Register(ctx, "s")
	duplicate.Commit(s, "s", nil)
	late.Resolve(Result{State: api.ContinuationStateContinued, TurnID: "duplicate"})
	owner.Resolve(Result{State: api.ContinuationStateContinued, TurnID: "original"})
	value, err := s.Actions.Await(t.Context(), duplicate.ID())
	if err != nil || value.TurnID != "original" {
		t.Fatalf("operation outcome = %+v, %v", value, err)
	}
}
