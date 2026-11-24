package completioncue_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/completioncue"
	"github.com/lycaon/lycaon/internal/testutil"
)

type memStore struct {
	states map[string]completioncue.DeliveredState
}

func (m *memStore) GetCompletionCueState(_ context.Context, sessionID string) (completioncue.DeliveredState, error) {
	if m.states == nil {
		return completioncue.DeliveredState{}, nil
	}
	return m.states[sessionID], nil
}

func (m *memStore) SaveCompletionCueState(_ context.Context, sessionID string, state completioncue.DeliveredState) error {
	if m.states == nil {
		m.states = map[string]completioncue.DeliveredState{}
	}
	m.states[sessionID] = state
	return nil
}

func TestTrackerSelectionLifecycleAndPriority(t *testing.T) {
	ctx := t.Context()
	store := &memStore{}
	tracker := completioncue.NewTracker(store)

	const sessID = "sess-1"
	const intent1 = "intent-1"

	// No event -> no cue.
	b := tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 3, true },
	})
	if b.Code != "" {
		t.Fatalf("expected no cue without event, got %q", b.Code)
	}
	testutil.FailErr(t, "commit empty decision", tracker.CommitDecision(ctx, b))

	// Eligible completion with 2 pending -> outcome cue.
	tracker.RecordPlanItemCompleted(sessID, "", intent1)
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != completioncue.CodeOutcomeCoverage {
		t.Fatalf("expected %q, got %q", completioncue.CodeOutcomeCoverage, b.Code)
	}
	testutil.FailErr(t, "commit outcome cue", tracker.CommitDecision(ctx, b))

	// Next decision with no new event -> no replay.
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != "" {
		t.Fatalf("expected no replay on subsequent decision, got %q", b.Code)
	}

	// Another completion still leaving 2 pending -> outcome already delivered.
	tracker.RecordPlanItemCompleted(sessID, "", intent1)
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != "" {
		t.Fatalf("expected outcome cue suppressed after delivery, got %q", b.Code)
	}
	testutil.FailErr(t, "commit suppressed outcome", tracker.CommitDecision(ctx, b))

	// Completion leaving 1 pending -> final item cue.
	tracker.RecordPlanItemCompleted(sessID, "", intent1)
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 1, true },
	})
	if b.Code != completioncue.CodeFinalItemCoverage {
		t.Fatalf("expected %q, got %q", completioncue.CodeFinalItemCoverage, b.Code)
	}
	testutil.FailErr(t, "commit final item cue", tracker.CommitDecision(ctx, b))

	// Plan expands to 3 pending after final-item cue -> no further cue for same intent.
	tracker.RecordPlanItemCompleted(sessID, "", intent1)
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 3, true },
	})
	if b.Code != "" {
		t.Fatalf("expected suppression after final-item cue even when plan expands, got %q", b.Code)
	}

	// Restart (new tracker over same durable store) preserves delivered bits and infers no new event.
	restarted := completioncue.NewTracker(store)
	b = restarted.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 1, true },
	})
	if b.Code != "" {
		t.Fatalf("expected no cue inferred on restart, got %q", b.Code)
	}
	restarted.RecordPlanItemCompleted(sessID, "", intent1)
	b = restarted.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent1, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 1, true },
	})
	if b.Code != "" {
		t.Fatalf("expected delivered bit preserved across restart, got %q", b.Code)
	}

	// New user intent clears both delivered bits and old events.
	const intent2 = "intent-2"
	restarted.RecordPassingCheck(completioncue.CheckFact{
		CheckID: "chk-old", SessionID: sessID, IntentID: intent1,
		SourceRevision: "rev1", SourceRootDigest: "root1",
	})
	b = restarted.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent2, OrdinaryDecision: true,
		PendingCount:        func(context.Context) (int, bool) { return 2, true },
		ConfirmCheckCurrent: func(context.Context, completioncue.CheckFact) bool { return true },
	})
	if b.Code != "" {
		t.Fatalf("expected old-intent check dropped on new intent, got %q", b.Code)
	}
	restarted.RecordPlanItemCompleted(sessID, "", intent2)
	b = restarted.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: sessID, IntentID: intent2, OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != completioncue.CodeOutcomeCoverage {
		t.Fatalf("expected fresh outcome cue on new intent, got %q", b.Code)
	}
}

func TestTrackerExpiresUndeliveredEventOnForcedTurn(t *testing.T) {
	ctx := t.Context()
	tracker := completioncue.NewTracker(nil)

	tracker.RecordPlanItemCompleted("sess-1", "", "intent-1")
	// Forced / non-ordinary decision emits nothing and expires the event.
	b := tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: "sess-1", IntentID: "intent-1", OrdinaryDecision: false,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != "" {
		t.Fatalf("expected no cue on non-ordinary decision, got %q", b.Code)
	}
	testutil.FailErr(t, "commit forced decision", tracker.CommitDecision(ctx, b))

	// Subsequent ordinary decision must not see the expired event.
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: "sess-1", IntentID: "intent-1", OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != "" {
		t.Fatalf("expected expired event to stay dropped, got %q", b.Code)
	}
}

func TestTrackerFinalItemSuppressesOutcomeOnExpansion(t *testing.T) {
	ctx := t.Context()
	tracker := completioncue.NewTracker(nil)

	// One-item plan receives passing check -> final-item cue without prior outcome cue.
	tracker.RecordPassingCheck(completioncue.CheckFact{
		CheckID: "chk-1", SessionID: "sess-1", IntentID: "intent-1",
		SourceRevision: "rev1", SourceRootDigest: "root1",
	})
	b := tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: "sess-1", IntentID: "intent-1", OrdinaryDecision: true,
		PendingCount:        func(context.Context) (int, bool) { return 1, true },
		ConfirmCheckCurrent: func(context.Context, completioncue.CheckFact) bool { return true },
	})
	if b.Code != completioncue.CodeFinalItemCoverage {
		t.Fatalf("expected %q, got %q", completioncue.CodeFinalItemCoverage, b.Code)
	}
	testutil.FailErr(t, "commit final-item cue", tracker.CommitDecision(ctx, b))

	// Even though OutcomeDelivered is false, FinalItemDelivered suppresses outcome cue on expansion.
	tracker.RecordPlanItemCompleted("sess-1", "", "intent-1")
	b = tracker.SelectForDecision(ctx, completioncue.SelectInput{
		SessionID: "sess-1", IntentID: "intent-1", OrdinaryDecision: true,
		PendingCount: func(context.Context) (int, bool) { return 2, true },
	})
	if b.Code != "" {
		t.Fatalf("expected outcome cue suppressed after final-item cue, got %q", b.Code)
	}
}
