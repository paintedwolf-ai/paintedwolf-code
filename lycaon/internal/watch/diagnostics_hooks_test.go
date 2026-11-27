package watch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/observability/watchdiag"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWatchDiagProductionHooksCleanCollectAndAmbientWake(t *testing.T) {
	store, _, _, owner := openTestWatchStore(t)

	obs := watchdiag.NewObserver(watchdiag.Config{
		Enabled:        true,
		QueueCapacity:  16,
		ReportCapacity: 16,
	})
	restore := watchdiag.SetObserver(obs)
	t.Cleanup(func() {
		restore()
		obs.Close()
	})

	// 1. Clean collect delivery boundary.
	collectRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-collect-1"}}},
	})
	testutil.FailErr(t, "register collect watch", err)
	_, ok, err := store.RecordMatch(t.Context(), collectRec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-collect-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-collect-1"},
	}, nil)
	testutil.FailErr(t, "record collect match", err)
	if !ok {
		t.Fatal("expected collect match recorded")
	}

	batch, hasMatches, err := ReserveCollectBatch(t.Context(), store, owner, "req-clean-1")
	testutil.FailErr(t, "reserve collect batch", err)
	if !hasMatches {
		t.Fatal("expected reserved collect batch")
	}
	testutil.FailErr(t, "commit collect batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch.DeliveryID))

	// 2. Clean ambient wake admission boundary.
	wakeRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-wake-1"}}},
	})
	testutil.FailErr(t, "register wake watch", err)
	_, ok, err = store.RecordMatch(t.Context(), wakeRec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-wake-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-wake-1"},
	}, nil)
	testutil.FailErr(t, "record wake match", err)
	if !ok {
		t.Fatal("expected wake match recorded")
	}

	woke, err := TryScheduleAmbientWake(t.Context(), store, owner.SessionID, AmbientWakeGate{
		DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
			return onAdmitted()
		},
	})
	testutil.FailErr(t, "schedule ambient wake", err)
	if !woke {
		t.Fatal("expected ambient wake scheduled and admitted")
	}

	obs.Close()
	if obs.Stats().Enqueued < 2 {
		t.Fatalf("expected >= 2 production observations enqueued, got %d", obs.Stats().Enqueued)
	}
	if rep := obs.Report(); rep.TotalFindings != 0 {
		t.Fatalf("expected 0 contradictions on clean production paths, got %+v", rep.Findings)
	}
}

func TestWatchDiagProductionHookWrongRecipientAtDeliveryBoundary(t *testing.T) {
	store, _, database, owner := openTestWatchStore(t)
	wrongSess := "wrong-recipient-session-2"
	testdbseed.InsertSession(t, database, wrongSess, testdbseed.DefaultProjectID)

	obs := watchdiag.NewObserver(watchdiag.Config{Enabled: true, QueueCapacity: 16, ReportCapacity: 16})
	restore := watchdiag.SetObserver(obs)
	t.Cleanup(func() {
		restore()
		obs.Close()
	})

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-wr-1"}}},
	})
	testutil.FailErr(t, "register watch", err)

	// Seed a reserved delivery routed to wrongSess while watch_registrations is owned by owner.SessionID.
	batch := CollectedBatch{
		DeliveryID: "deliv-wrong-recipient-1",
		RequestKey: "req-wr-1",
		Matches: []RecordedMatch{
			{MatchID: "m-wr-1", WatchID: rec.WatchID, Handle: rec.Handle, OccurrenceID: "occ-wr-1"},
		},
	}
	pJSON, err := json.Marshal(batch)
	testutil.FailErr(t, "marshal batch", err)
	wJSON, err := json.Marshal([]string{rec.WatchID})
	testutil.FailErr(t, "marshal watch ids", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO watch_deliveries
		(delivery_id, session_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at)
		VALUES (?, ?, 'request', 'req-wr-1', '["m-wr-1"]', ?, ?, 'reserved', ?)`,
		batch.DeliveryID, wrongSess, string(wJSON), string(pJSON), store.clock.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert misrouted delivery", err)

	testutil.FailErr(t, "commit misrouted delivery", CommitCollectBatch(t.Context(), store, wrongSess, batch.DeliveryID))
	obs.Close()

	rep := obs.Report()
	found := false
	for _, f := range rep.Findings {
		if f.RuleID == watchdiag.RuleWrongRecipient && f.Disposition == watchdiag.DispositionContradiction {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected wrong_recipient contradiction from delivery boundary, got %+v", rep.Findings)
	}
}

func TestWatchDiagProductionHookDuplicateCommittedPresentationAtDeliveryBoundary(t *testing.T) {
	store, _, database, owner := openTestWatchStore(t)

	obs := watchdiag.NewObserver(watchdiag.Config{Enabled: true, QueueCapacity: 16, ReportCapacity: 16})
	restore := watchdiag.SetObserver(obs)
	t.Cleanup(func() {
		restore()
		obs.Close()
	})

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-dup-1"}}},
	})
	testutil.FailErr(t, "register watch", err)
	_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-dup-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-dup-1"},
	}, nil)
	testutil.FailErr(t, "record match", err)
	if !ok {
		t.Fatal("expected match recorded")
	}

	firstBatch, hasMatches, err := ReserveCollectBatch(t.Context(), store, owner, "req-dup-1")
	testutil.FailErr(t, "reserve first batch", err)
	if !hasMatches {
		t.Fatal("expected first batch")
	}
	testutil.FailErr(t, "commit first batch", CommitCollectBatch(t.Context(), store, owner.SessionID, firstBatch.DeliveryID))

	// Inject a second reserved delivery for the exact same (watch_id, occurrence_id) and commit it.
	secondBatch := CollectedBatch{
		DeliveryID: "deliv-dup-2",
		RequestKey: "req-dup-2",
		Matches: []RecordedMatch{
			{MatchID: "m-dup-2", WatchID: rec.WatchID, Handle: rec.Handle, OccurrenceID: "occ-dup-1"},
		},
	}
	pJSON, err := json.Marshal(secondBatch)
	testutil.FailErr(t, "marshal second batch", err)
	wJSON, err := json.Marshal([]string{rec.WatchID})
	testutil.FailErr(t, "marshal watch ids", err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO watch_deliveries
		(delivery_id, session_id, destination_kind, destination_id, match_ids_json, watch_ids_json, payload_json, state, created_at)
		VALUES (?, ?, 'request', 'req-dup-2', '["m-dup-2"]', ?, ?, 'reserved', ?)`,
		secondBatch.DeliveryID, owner.SessionID, string(wJSON), string(pJSON), store.clock.Now().UTC().Format(time.RFC3339Nano))
	testutil.FailErr(t, "insert duplicate delivery", err)

	testutil.FailErr(t, "commit duplicate delivery", CommitCollectBatch(t.Context(), store, owner.SessionID, secondBatch.DeliveryID))
	obs.Close()

	rep := obs.Report()
	found := false
	for _, f := range rep.Findings {
		if f.RuleID == watchdiag.RuleDuplicateCommittedPresentation && f.Disposition == watchdiag.DispositionContradiction {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected duplicate_committed_presentation contradiction from delivery boundary, got %+v", rep.Findings)
	}
}

func TestWatchDiagProductionHookWatchTurnPastStopFenceOnConsumerPath(t *testing.T) {
	store, clk, _, owner := openTestWatchStore(t)

	obs := watchdiag.NewObserver(watchdiag.Config{Enabled: true, QueueCapacity: 16, ReportCapacity: 16})
	restore := watchdiag.SetObserver(obs)
	t.Cleanup(func() {
		restore()
		obs.Close()
	})

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-stop-1"}}},
	})
	testutil.FailErr(t, "register wake watch", err)
	_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-stop-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-stop-1"},
	}, nil)
	testutil.FailErr(t, "record wake match", err)
	if !ok {
		t.Fatal("expected wake match")
	}

	// Reserve the ambient wake batch before Stop.
	batch, _, hasWork, err := prepareAmbientWakeBatch(t.Context(), store, owner.SessionID, clk.Now())
	testutil.FailErr(t, "prepare ambient wake batch", err)
	if !hasWork {
		t.Fatal("expected reserved ambient wake batch")
	}

	// Commit Stop fence durably, then advance clock and simulate a bypassed post-fence consumer admission.
	testutil.FailErr(t, "fence session", store.FenceSessions(t.Context(), []string{owner.SessionID}))
	clk.Advance(2 * time.Second)
	testutil.FailErr(t, "commit post-fence ambient wake", commitAmbientWakeAdmission(t.Context(), store, owner.SessionID, batch.DeliveryID))
	obs.Close()

	rep := obs.Report()
	found := false
	for _, f := range rep.Findings {
		if f.RuleID == watchdiag.RuleWatchTurnPastStopFence && f.Disposition == watchdiag.DispositionContradiction {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected watch_turn_past_stop_fence contradiction on consumer path, got %+v", rep.Findings)
	}
}

func TestWatchDiagProductionHooksDisabledZeroOverhead(t *testing.T) {
	store, _, _, owner := openTestWatchStore(t)

	obs := watchdiag.NewObserver(watchdiag.Config{Enabled: false})
	restore := watchdiag.SetObserver(obs)
	t.Cleanup(restore)

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-off-1"}}},
	})
	testutil.FailErr(t, "register watch", err)
	_, _, err = store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-off-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-off-1"},
	}, nil)
	testutil.FailErr(t, "record match", err)

	batch, hasMatches, err := ReserveCollectBatch(t.Context(), store, owner, "req-off-1")
	testutil.FailErr(t, "reserve collect batch", err)
	if !hasMatches {
		t.Fatal("expected reserved batch")
	}
	testutil.FailErr(t, "commit collect batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch.DeliveryID))

	if obs.Stats().Enqueued != 0 || obs.Report().TotalFindings != 0 {
		t.Fatalf("expected zero activity when disabled, stats=%+v report=%+v", obs.Stats(), obs.Report())
	}
}
