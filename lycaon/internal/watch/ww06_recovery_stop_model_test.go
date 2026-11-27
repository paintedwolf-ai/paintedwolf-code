package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func ww06OpenStore(t *testing.T, start time.Time) (*Store, *ww02ManualClock, db.Handle, Owner) {
	t.Helper()
	database := testdbfixture.Open(t, "ww06-store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "session-2", testdbseed.DefaultProjectID)
	clock := newWW02ManualClock(start)
	store := NewStore(database, clock)
	owner := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1"}
	return store, clock, database, owner
}

func ww06RestartStore(t *testing.T, database db.Handle, clock Clock) *Store {
	t.Helper()
	restarted := NewStore(database, clock)
	testutil.FailErr(t, "recover restarted watch store", restarted.Recover(t.Context()))
	return restarted
}

func ww06QueryPresentationClaim(t *testing.T, database db.Handle, sessionID, scope, occID string) (claimKind, state, receiptID string, found bool) {
	t.Helper()
	err := database.QueryRowContext(t.Context(),
		`SELECT claim_kind, state, COALESCE(receipt_id, '') FROM watch_presentation_claims
		 WHERE session_id = ? AND destination_scope = ? AND producer_occurrence_id = ?`,
		sessionID, scope, occID).Scan(&claimKind, &state, &receiptID)
	if err != nil {
		return "", "", "", false
	}
	return claimKind, state, receiptID, true
}

func ww06CountCommittedDeliveriesForMatch(t *testing.T, database db.Handle, watchID string) int {
	t.Helper()
	var n int
	err := database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM watch_deliveries
		 WHERE state = 'committed' AND destination_kind != 'final_receipt'
		   AND EXISTS (SELECT 1 FROM json_each(watch_ids_json) WHERE value = ?)`,
		watchID).Scan(&n)
	testutil.FailErr(t, "count committed deliveries", err)
	return n
}

// TestWW06C19RestartBoundaries exercises C19-restart-boundaries:
// restart on EACH side of source publication, match commit, delivery claim, and admission,
// plus failed render then retry across restart, asserting zero loss, zero duplicate presentation,
// and persisted receipt/claim parity.
func TestWW06C19RestartBoundaries(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)

	t.Run("before_and_after_source_publication", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)

		// Register watch before source publication.
		rec, err := store.Register(t.Context(), RegisterRequest{
			Owner:        owner,
			InvocationID: "c19-pub-inv",
			Delivery:     DeliveryCollect,
			Conditions:   []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-pub-1"}}},
		})
		testutil.FailErr(t, "register before publication", err)

		// Boundary 1a: Restart BEFORE source publication.
		store = ww06RestartStore(t, database, clock)
		// Replay registration with same InvocationID must return identical watch/handle.
		replayed, err := store.Register(t.Context(), RegisterRequest{
			Owner:        owner,
			InvocationID: "c19-pub-inv",
			Delivery:     DeliveryCollect,
			Conditions:   []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-pub-1"}}},
		})
		testutil.FailErr(t, "idempotent replay after pre-pub restart", err)
		if replayed.WatchID != rec.WatchID || replayed.Handle != rec.Handle {
			t.Fatalf("pre-pub restart broke registration idempotency: got %+v want %+v", replayed, rec)
		}

		// Publish source completion fact.
		occID := "occ-c19-pub-1"
		pubOut, err := store.RecordProcessCompletion(t.Context(), owner.SessionID, ProcessCompletionFact{
			OccurrenceID: occID,
			Handle:       "cmd-pub-1",
			Outcome:      "satisfied",
			Report:       "exit_code=0",
		})
		testutil.FailErr(t, "publish process completion", err)
		if !pubOut.Claimed || len(pubOut.Matches) != 1 {
			t.Fatalf("expected 1 published match, got %+v", pubOut)
		}

		// Boundary 1b: Restart AFTER source publication (before delivery).
		store = ww06RestartStore(t, database, clock)

		// Duplicate publication after restart must be ignored.
		dupOut, err := store.RecordProcessCompletion(t.Context(), owner.SessionID, ProcessCompletionFact{
			OccurrenceID: occID,
			Handle:       "cmd-pub-1",
			Outcome:      "satisfied",
			Report:       "exit_code=0",
		})
		testutil.FailErr(t, "duplicate publish after restart", err)
		if len(dupOut.Matches) != 0 {
			t.Fatalf("duplicate publication matched after restart: %+v", dupOut)
		}

		// Deliver once via collect batch and verify receipt/claim parity.
		batch, ok, err := ReserveCollectBatch(t.Context(), store, owner, "req-c19-pub-1")
		testutil.FailErr(t, "reserve after post-pub restart", err)
		if !ok || len(batch.Matches) != 1 || batch.Matches[0].OccurrenceID != occID {
			t.Fatalf("unexpected reserved batch after post-pub restart: ok=%v batch=%+v", ok, batch)
		}
		testutil.FailErr(t, "commit after post-pub restart", CommitCollectBatch(t.Context(), store, owner.SessionID, batch.DeliveryID))

		_, state, receiptID, found := ww06QueryPresentationClaim(t, database, owner.SessionID, "session", occID)
		if !found || state != "admitted" || receiptID != batch.DeliveryID {
			t.Fatalf("presentation claim parity mismatch: found=%v state=%q receipt=%q want=%q", found, state, receiptID, batch.DeliveryID)
		}
		if got := ww06CountCommittedDeliveriesForMatch(t, database, rec.WatchID); got != 1 {
			t.Fatalf("committed deliveries = %d, want 1", got)
		}
	})

	t.Run("before_and_after_match_commit", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)

		rec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-match-1"}}},
		})
		testutil.FailErr(t, "register watch", err)

		// Boundary 2a: Restart BEFORE match commit.
		store = ww06RestartStore(t, database, clock)
		occID := "occ-c19-match-1"
		m, inserted, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: occID,
			Outcome:      "satisfied",
			Evidence:     map[string]any{"handle": "cmd-match-1", "exit_code": 0},
		}, nil)
		testutil.FailErr(t, "commit match after restart", err)
		if !inserted || m.OccurrenceID != occID {
			t.Fatalf("expected match commit after restart, got inserted=%v m=%+v", inserted, m)
		}

		// Boundary 2b: Restart AFTER match commit (before claim).
		store = ww06RestartStore(t, database, clock)
		view, err := store.Inspect(t.Context(), owner, rec.Handle)
		testutil.FailErr(t, "inspect after post-match restart", err)
		if view.PendingMatches != 1 || view.State != StateTerminal || view.Outcome != OutcomeCompleted {
			t.Fatalf("unexpected state after post-match restart: %+v", view)
		}

		// Claim via SelectOrPark delivers exactly once.
		sel, err := store.SelectOrPark(t.Context(), SelectRequest{
			Owner:     owner,
			Handles:   []string{rec.Handle},
			AllowPark: false,
			ParkLease: awaitstore.Lease{ToolCallID: "wait-c19-match-1", Deadline: clock.Now().Add(time.Minute)},
		})
		testutil.FailErr(t, "select after post-match restart", err)
		if sel.Parked || len(sel.Matches) != 1 || sel.DeliveryID == "" {
			t.Fatalf("expected single delivery on select, got %+v", sel)
		}
		// Replaying same ToolCallID returns identical delivery receipt without duplicate delivery.
		selReplay, err := store.SelectOrPark(t.Context(), SelectRequest{
			Owner:     owner,
			Handles:   []string{rec.Handle},
			AllowPark: false,
			ParkLease: awaitstore.Lease{ToolCallID: "wait-c19-match-1", Deadline: clock.Now().Add(time.Minute)},
		})
		testutil.FailErr(t, "replay select after commit", err)
		if selReplay.DeliveryID != sel.DeliveryID {
			t.Fatalf("select replay minted new delivery: got %q want %q", selReplay.DeliveryID, sel.DeliveryID)
		}
		if got := ww06CountCommittedDeliveriesForMatch(t, database, rec.WatchID); got != 1 {
			t.Fatalf("committed deliveries = %d, want 1", got)
		}
	})

	t.Run("before_and_after_delivery_claim", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)

		rec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-claim-1"}}},
		})
		testutil.FailErr(t, "register watch", err)

		occID := "occ-c19-claim-1"
		_, inserted, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: occID,
			Outcome:      "satisfied",
			Evidence:     map[string]any{"handle": "cmd-claim-1"},
		}, nil)
		testutil.FailErr(t, "record match", err)
		if !inserted {
			t.Fatal("expected match insertion")
		}

		// Boundary 3a: Restart BEFORE delivery claim.
		store = ww06RestartStore(t, database, clock)
		batch1, ok, err := ReserveCollectBatch(t.Context(), store, owner, "req-c19-claim-key")
		testutil.FailErr(t, "reserve collect batch", err)
		if !ok || len(batch1.Matches) != 1 {
			t.Fatalf("expected 1 reserved match, got ok=%v batch=%+v", ok, batch1)
		}

		// Boundary 3b: Restart AFTER delivery claim (reserved, uncommitted).
		store = ww06RestartStore(t, database, clock)

		// Replaying same requestKey after restart must return the exact frozen batch.
		batchReplay, okReplay, err := ReserveCollectBatch(t.Context(), store, owner, "req-c19-claim-key")
		testutil.FailErr(t, "replay reserved batch after restart", err)
		if !okReplay || batchReplay.DeliveryID != batch1.DeliveryID || len(batchReplay.Matches) != 1 {
			t.Fatalf("reserved batch replay diverged across restart: got %+v want %+v", batchReplay, batch1)
		}

		// A distinct requestKey must NOT steal the already-reserved match.
		otherBatch, okOther, err := ReserveCollectBatch(t.Context(), store, owner, "req-c19-other-key")
		testutil.FailErr(t, "other request key while reserved", err)
		if okOther || len(otherBatch.Matches) != 0 {
			t.Fatalf("distinct request key stole reserved match: %+v", otherBatch)
		}

		// Commit after restart succeeds once; subsequent reserve is empty.
		testutil.FailErr(t, "commit after post-claim restart", CommitCollectBatch(t.Context(), store, owner.SessionID, batch1.DeliveryID))
		afterCommit, okAfter, err := ReserveCollectBatch(t.Context(), store, owner, "req-c19-after-commit")
		testutil.FailErr(t, "reserve after commit", err)
		if okAfter || len(afterCommit.Matches) != 0 {
			t.Fatalf("duplicate delivery available after commit: %+v", afterCommit)
		}
		_, state, receiptID, found := ww06QueryPresentationClaim(t, database, owner.SessionID, "session", occID)
		if !found || state != "admitted" || receiptID != batch1.DeliveryID {
			t.Fatalf("claim parity after post-claim restart: found=%v state=%q receipt=%q want=%q", found, state, receiptID, batch1.DeliveryID)
		}
	})

	t.Run("before_and_after_admission", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)
		var admittedIDs []string
		eligible := false
		wireGate := func(st *Store) {
			st.SetAmbientWakeGate(AmbientWakeGate{
				SessionEligible: func(context.Context, string) (bool, bool) { return eligible, !eligible },
				DispatchTurn: func(_ context.Context, _, submissionID, _ string, onAdmitted func() error) error {
					if err := onAdmitted(); err != nil {
						return err
					}
					admittedIDs = append(admittedIDs, submissionID)
					return nil
				},
			})
		}
		wireGate(store)

		rec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryWake,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-admit-1"}}},
		})
		testutil.FailErr(t, "register wake watch", err)

		occID := "occ-c19-admit-1"
		_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: occID,
			Outcome:      "satisfied",
			Evidence:     map[string]any{"handle": "cmd-admit-1"},
		}, nil)
		testutil.FailErr(t, "record match while busy", err)
		if !ok || len(admittedIDs) != 0 {
			t.Fatalf("expected held match before admission: ok=%v admitted=%v", ok, admittedIDs)
		}

		// Boundary 4a: Restart BEFORE admission.
		store = ww06RestartStore(t, database, clock)
		wireGate(store)
		eligible = true
		woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID)
		testutil.FailErr(t, "trigger ambient wake after pre-admission restart", err)
		if !woke || len(admittedIDs) != 1 {
			t.Fatalf("expected single admission after pre-admission restart: woke=%v admitted=%v", woke, admittedIDs)
		}

		// Boundary 4b: Restart AFTER admission.
		store = ww06RestartStore(t, database, clock)
		wireGate(store)
		wokeAgain, err := store.TriggerAmbientWake(t.Context(), owner.SessionID)
		testutil.FailErr(t, "trigger ambient wake after post-admission restart", err)
		if wokeAgain || len(admittedIDs) != 1 {
			t.Fatalf("duplicate admission after post-admission restart: woke=%v admitted=%v", wokeAgain, admittedIDs)
		}
		if got := ww06CountCommittedDeliveriesForMatch(t, database, rec.WatchID); got != 1 {
			t.Fatalf("committed deliveries = %d, want 1", got)
		}
		_, state, receiptID, found := ww06QueryPresentationClaim(t, database, owner.SessionID, "session", occID)
		if !found || state != "admitted" || receiptID != admittedIDs[0] {
			// Ensure admission parity if presentation claim was created on collect/process path.
			if found && (state != "admitted" || receiptID != admittedIDs[0]) {
				t.Fatalf("presentation claim mismatch: state=%q receipt=%q want=%q", state, receiptID, admittedIDs[0])
			}
		}
	})

	t.Run("failed_render_then_restart_and_retry", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)

		rec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-render-1"}}},
		})
		testutil.FailErr(t, "register watch", err)

		occID := "occ-c19-render-1"
		_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: occID,
			Outcome:      "satisfied",
			Evidence:     map[string]any{"handle": "cmd-render-1", "exit_code": 0},
		}, nil)
		testutil.FailErr(t, "record match", err)
		if !ok {
			t.Fatal("expected match insertion")
		}

		pe := testPromptEngine(t)
		inj := prompts.NewInjectRenderer(pe)
		coordSess := &api.Session{ID: owner.SessionID, ProjectID: owner.ProjectID, Posture: api.SessionPostureBuild}
		history := []api.Message{{
			ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
			Visibility: api.MessageVisibilityTranscript, Content: "continue",
		}}

		newEngine := func(st *Store, fail *bool) *assembly.AssemblyEngine {
			adapter := &Delivery{Store: st}
			eng := &assembly.AssemblyEngine{}
			eng.SetDeps(assembly.AssemblyDeps{
				Prompts:    pe,
				Injects:    inj,
				Limits:     func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
				Deliveries: assembly.Deliveries{Watches: adapter},
				AgentsMDPolicies: func(context.Context, *api.Session, []api.Message) ([]api.Message, error) {
					if *fail {
						return nil, errors.New("injected render failure")
					}
					return nil, nil
				},
			})
			return eng
		}

		// Render fails -> reservation must be released so match is not lost.
		failFlag := true
		eng1 := newEngine(store, &failFlag)
		eng1.BeginPromptTurn(coordSess.ID)
		if _, err := eng1.BuildCompletionMessages(t.Context(), coordSess, history, nil); err == nil {
			t.Fatal("expected injected render failure")
		}
		eng1.EndPromptTurn(coordSess.ID)

		// Restart store after failed render.
		store = ww06RestartStore(t, database, clock)
		actionable, err := HasActionableWatches(t.Context(), store, owner)
		testutil.FailErr(t, "has actionable after failed render + restart", err)
		if !actionable {
			t.Fatal("pending match was lost across failed render + restart")
		}

		// Retry render succeeds and commits once.
		failFlag = false
		eng2 := newEngine(store, &failFlag)
		eng2.BeginPromptTurn(coordSess.ID)
		msgs, err := eng2.BuildCompletionMessages(t.Context(), coordSess, history, nil)
		testutil.FailErr(t, "retry render after restart", err)
		block := findWatchBlock(msgs)
		if !strings.Contains(block, rec.Handle) || !strings.Contains(block, "cmd-render-1") {
			t.Fatalf("retry render missing watch observation block: %q", block)
		}
		testutil.FailErr(t, "commit retry turn", eng2.CommitTurnContext(t.Context(), coordSess.ID, "resp-retry-1"))
		eng2.EndPromptTurn(coordSess.ID)

		view, err := store.Inspect(t.Context(), owner, rec.Handle)
		testutil.FailErr(t, "inspect after retry commit", err)
		if view.PendingMatches != 0 {
			t.Fatalf("pending matches after retry commit = %d, want 0", view.PendingMatches)
		}
		if got := ww06CountCommittedDeliveriesForMatch(t, database, rec.WatchID); got != 1 {
			t.Fatalf("committed deliveries after failed render + retry = %d, want 1", got)
		}
	})
}

// TestWW06C20StopFenceSessionsInProcess exercises C20-stop-fence-sessions (in-process controls, D5):
// Stop before registration commits, during park, after match, and during a watch-triggered turn,
// covering root watch from a prior turn + worker watch, asserting Stop ack, zero watch-originated
// replacement turns, retained evidence, and no implicit rearm on next user message.
func TestWW06C20StopFenceSessionsInProcess(t *testing.T) {
	base := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)

	t.Run("stop_before_registration_commits", func(t *testing.T) {
		store, _, _, owner := ww06OpenStore(t, base)
		testutil.FailErr(t, "fence session before register", store.FenceSessions(t.Context(), []string{owner.SessionID}))

		if _, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-pre-reg"}}},
		}); !errors.Is(err, ErrSessionStopped) {
			t.Fatalf("expected ErrSessionStopped before registration commit, got %v", err)
		}
		stopped, err := store.IsSessionStopped(t.Context(), owner.SessionID)
		testutil.FailErr(t, "is session stopped", err)
		if !stopped {
			t.Fatal("expected session to report stopped")
		}
	})

	t.Run("stop_during_park_with_prior_root_and_worker_watch", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)
		childSess := "ww06-worker-sess-park"
		workerJob := "ww06-worker-job-park"
		testdbseed.InsertChildSession(t, database, childSess, owner.SessionID)
		_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
			(id, project_id, child_session_id, agent_type, prompt, brief, created_at)
			VALUES (?, ?, ?, 'implementer', 'work', 'work', '2026-11-27T15:00:00Z')`,
			workerJob, testdbseed.DefaultProjectID, childSess)
		testutil.FailErr(t, "insert worker job", err)

		var leaseWakes, wakeAdmissions atomic.Int32
		store.SetLeaseWakeHandler(func(context.Context, awaitstore.Lease, awaitstore.Condition) {
			leaseWakes.Add(1)
		})
		store.SetAmbientWakeGate(AmbientWakeGate{
			DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
				wakeAdmissions.Add(1)
				return onAdmitted()
			},
		})

		rootRec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryWake,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"prior-root-cmd"}}},
		})
		testutil.FailErr(t, "register prior-turn root watch", err)

		workerOwner := Owner{Kind: OwnerJob, ProjectID: owner.ProjectID, SessionID: childSess, WorkerJobID: workerJob}
		workerRec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      workerOwner,
			Delivery:   DeliveryCollect,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"worker-cmd"}}},
		})
		testutil.FailErr(t, "register worker watch", err)

		parked, err := store.SelectOrPark(t.Context(), SelectRequest{
			Owner:     owner,
			Handles:   []string{rootRec.Handle},
			AllowPark: true,
			ParkLease: awaitstore.Lease{
				SessionID: owner.SessionID,
				ProjectID: owner.ProjectID,
				Deadline:  clock.Now().Add(5 * time.Minute),
			},
		})
		testutil.FailErr(t, "park root session", err)
		if !parked.Parked {
			t.Fatal("expected parked wait lease")
		}

		// Stop session tree during park.
		summary, err := store.FenceAndCancelSessions(t.Context(), []string{owner.SessionID, childSess})
		testutil.FailErr(t, "stop fence during park", err)
		if summary.CanceledCount != 2 {
			t.Fatalf("stop canceled count = %d, want 2 (%v)", summary.CanceledCount, summary.CanceledHandles)
		}
		if leaseWakes.Load() != 0 || wakeAdmissions.Load() != 0 {
			t.Fatalf("expected zero replacement turns: leaseWakes=%d wakeAdmissions=%d", leaseWakes.Load(), wakeAdmissions.Load())
		}

		// Verify armed lease canceled in SQLite.
		var leaseStatus string
		err = database.QueryRowContext(t.Context(), `SELECT status FROM wait_leases WHERE id = ?`, parked.Lease.ID).Scan(&leaseStatus)
		testutil.FailErr(t, "query lease status", err)
		if leaseStatus != "canceled" {
			t.Fatalf("parked lease status after Stop = %q, want canceled", leaseStatus)
		}

		// Next user message clears stop fence without implicitly rearming prior watches.
		testutil.FailErr(t, "clear stop fence on next user message", store.ClearSessionStopFence(t.Context(), owner.SessionID))
		rootView, err := store.Inspect(t.Context(), owner, rootRec.Handle)
		testutil.FailErr(t, "inspect root watch after user message", err)
		workerView, err := store.Inspect(t.Context(), workerOwner, workerRec.Handle)
		testutil.FailErr(t, "inspect worker watch after user message", err)
		if rootView.State != StateTerminal || rootView.Outcome != OutcomeCanceled {
			t.Fatalf("root watch implicitly rearmed: %+v", rootView)
		}
		if workerView.State != StateTerminal || workerView.Outcome != OutcomeCanceled {
			t.Fatalf("worker watch implicitly rearmed: %+v", workerView)
		}
		if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke {
			t.Fatalf("unexpected wake after clearing stop fence: woke=%v err=%v", woke, err)
		}
	})

	t.Run("stop_after_match_and_during_watch_triggered_turn", func(t *testing.T) {
		store, _, _, owner := ww06OpenStore(t, base)
		eligible := false
		var admitted []string
		store.SetAmbientWakeGate(AmbientWakeGate{
			SessionEligible: func(context.Context, string) (bool, bool) { return eligible, !eligible },
			DispatchTurn: func(_ context.Context, _, subID, _ string, onAdmitted func() error) error {
				if err := onAdmitted(); err != nil {
					return err
				}
				admitted = append(admitted, subID)
				return nil
			},
		})

		// Prior-turn repeating wake watch.
		rec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryWake,
			Repeat:     true,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
		})
		testutil.FailErr(t, "register repeating wake watch", err)

		// First match triggers a watch-originated turn.
		eligible = true
		_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: "occ-turn-1",
			Outcome:      "satisfied",
			Evidence:     map[string]any{"turn": 1},
		}, nil)
		testutil.FailErr(t, "record turn-1 match", err)
		if !ok || len(admitted) != 1 {
			t.Fatalf("expected turn-1 wake admission: ok=%v admitted=%v", ok, admitted)
		}

		// During the watch-triggered turn (session busy), a second match arrives and is reserved (uncommitted).
		eligible = false
		_, ok, err = store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: "occ-post-match",
			Outcome:      "satisfied",
			Evidence:     map[string]any{"turn": 2},
		}, nil)
		testutil.FailErr(t, "record match during turn", err)
		if !ok {
			t.Fatal("expected match during active turn")
		}
		_, reserved, err := ReserveCollectBatch(t.Context(), store, owner, "req-during-turn")
		testutil.FailErr(t, "reserve during turn", err)
		if !reserved {
			t.Fatal("expected reserved batch during active turn")
		}

		// Human Stop during watch-triggered turn.
		summary, err := store.FenceAndCancelSessions(t.Context(), []string{owner.SessionID})
		testutil.FailErr(t, "stop during watch-triggered turn", err)
		if summary.CanceledCount != 1 {
			t.Fatalf("canceled count = %d, want 1", summary.CanceledCount)
		}

		// Even when session becomes eligible again, zero replacement turns are admitted.
		eligible = true
		if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke || len(admitted) != 1 {
			t.Fatalf("watch-originated replacement turn admitted after Stop: woke=%v admitted=%v err=%v", woke, admitted, err)
		}

		// Evidence and prior committed receipt remain retained in Inspect.
		view, err := store.Inspect(t.Context(), owner, rec.Handle)
		testutil.FailErr(t, "inspect stopped watch", err)
		if view.State != StateTerminal || view.Outcome != OutcomeCanceled || view.LastDeliveryRef != admitted[0] || view.FinalReceiptID == "" {
			t.Fatalf("retained evidence/receipt missing after Stop: %+v", view)
		}

		// Next user message clears stop fence; prior watch stays canceled and does not rearm; fresh watch works.
		testutil.FailErr(t, "clear stop fence", store.ClearSessionStopFence(t.Context(), owner.SessionID))
		fresh, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-fresh"}}},
		})
		testutil.FailErr(t, "register fresh watch after user message", err)
		if fresh.Handle == rec.Handle {
			t.Fatalf("fresh watch reused stopped handle %s", fresh.Handle)
		}
	})
}

// TestWW06C21CapacityFairnessAndBudgets exercises C21-capacity-fairness-budgets:
// registration caps (8 live / 32 total), sampler round-robin & subscriber bounds,
// durable hourly wake budget (12/hr), immediate cancel+reuse, other-session fairness
// during a noisy burst, and bounded work counters.
func TestWW06C21CapacityFairnessAndBudgets(t *testing.T) {
	base := time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC)

	t.Run("live_cap_8_and_total_slot_cap_32_with_immediate_reuse", func(t *testing.T) {
		store, _, _, owner := ww06OpenStore(t, base)

		// Fill 8 live explicit watches.
		var liveHandles []string
		for i := range MaxLiveExplicitWatches {
			r, err := store.Register(t.Context(), RegisterRequest{
				Owner:      owner,
				Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{fmt.Sprintf("live-%d", i)}}},
			})
			testutil.FailErr(t, "register live watch", err)
			liveHandles = append(liveHandles, r.Handle)
		}
		// 9th live watch fails with CapacityError.
		_, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"live-over"}}},
		})
		var capErr *CapacityError
		if !errors.As(err, &capErr) || capErr.LiveCount != MaxLiveExplicitWatches {
			t.Fatalf("expected CapacityError at live=%d, got err=%v cap=%+v", MaxLiveExplicitWatches, err, capErr)
		}

		// Cancel 1 -> immediate reuse succeeds.
		_, err = store.Cancel(t.Context(), owner, liveHandles[0])
		testutil.FailErr(t, "cancel live watch", err)
		reused, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"live-reused"}}},
		})
		testutil.FailErr(t, "reuse live slot", err)
		liveHandles[0] = reused.Handle

		// Cancel all 8 live watches so we start cleanly for the 32-total-slot test.
		for _, h := range liveHandles {
			_, err := store.Cancel(t.Context(), owner, h)
			testutil.FailErr(t, "clear live handle", err)
		}

		// Fill MaxTotalExplicitSlots (32) with terminal-but-uncollected one-shot watches (slot_released = 0).
		var slotHandles []string
		for i := range MaxTotalExplicitSlots {
			r, err := store.Register(t.Context(), RegisterRequest{
				Owner:      owner,
				Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{fmt.Sprintf("slot-%d", i)}}},
			})
			testutil.FailErr(t, "register slot watch", err)
			_, ok, err := store.RecordMatch(t.Context(), r.WatchID, MatchInput{
				SourceKind:   ConditionProcessDone,
				OccurrenceID: fmt.Sprintf("occ-slot-%d", i),
				Outcome:      "satisfied",
				Evidence:     map[string]any{"i": i},
			}, nil)
			testutil.FailErr(t, "match slot watch", err)
			if !ok {
				t.Fatalf("slot watch %d did not match", i)
			}
			slotHandles = append(slotHandles, r.Handle)
		}

		// 33rd fails with CapacityError (LiveCount=0, SlotCount=32).
		_, err = store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"slot-over"}}},
		})
		if !errors.As(err, &capErr) || capErr.LiveCount != 0 || capErr.SlotCount != MaxTotalExplicitSlots {
			t.Fatalf("expected CapacityError at slots=%d, got err=%v cap=%+v", MaxTotalExplicitSlots, err, capErr)
		}

		// Cancel 1 uncollected slot -> slot_released becomes 1 -> immediate reuse succeeds.
		_, err = store.Cancel(t.Context(), owner, slotHandles[0])
		testutil.FailErr(t, "cancel uncollected slot", err)
		_, err = store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"slot-reused"}}},
		})
		testutil.FailErr(t, "reuse total slot immediately after cancel", err)
	})

	t.Run("sampler_bounded_concurrency_and_resource_release", func(t *testing.T) {
		store, clock, _, owner := ww06OpenStore(t, base)
		otherOwner := Owner{ProjectID: owner.ProjectID, SessionID: "session-2"}

		// Create 6 distinct file_change watch directories (exceeding MaxConcurrentProbes=4).
		var watchHandles []struct {
			owner  Owner
			handle string
		}
		for i := range 6 {
			dir := t.TempDir()
			testutil.FailErr(t, "write seed file", os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v0\n"), 0o644))
			o := owner
			if i == 5 {
				o = otherOwner
			}
			rec, err := store.Register(t.Context(), RegisterRequest{
				Owner: o,
				Authority: Authority{
					RootSessionID: o.SessionID,
					Roots:         []WatchRootGrant{{Label: "repo", RootID: "repo", Path: dir}},
				},
				Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "repo", Path: "."}},
			})
			testutil.FailErr(t, "register file watch", err)
			watchHandles = append(watchHandles, struct {
				owner  Owner
				handle string
			}{owner: o, handle: rec.Handle})
		}

		store.sampler.mu.Lock()
		initialSubs := store.sampler.subscriberCountLocked()
		initialGroups := len(store.sampler.groupsByID)
		store.sampler.mu.Unlock()
		if initialSubs != 6 || initialGroups != 6 {
			t.Fatalf("sampler subs=%d groups=%d, want 6/6", initialSubs, initialGroups)
		}

		// Advance clock by SamplerMinInterval: one tick must execute at most MaxConcurrentProbes (4).
		clock.Advance(SamplerMinInterval)
		testutil.FailErr(t, "advance due tick 1", store.AdvanceDue(t.Context()))
		store.sampler.mu.Lock()
		runsAfterTick1 := store.sampler.totalRuns
		store.sampler.mu.Unlock()
		if runsAfterTick1 > MaxConcurrentProbes || runsAfterTick1 == 0 {
			t.Fatalf("sampler runs after tick 1 = %d, want in 1..%d", runsAfterTick1, MaxConcurrentProbes)
		}

		// Advance clock again: remaining deferred groups run fairly.
		clock.Advance(SamplerMinInterval)
		testutil.FailErr(t, "advance due tick 2", store.AdvanceDue(t.Context()))
		store.sampler.mu.Lock()
		runsAfterTick2 := store.sampler.totalRuns
		store.sampler.mu.Unlock()
		if runsAfterTick2 < 6 {
			t.Fatalf("expected all 6 groups to make progress by tick 2, got totalRuns=%d", runsAfterTick2)
		}

		// Cancel all watches -> sampler subscribers, groups, and heap entries must drop to 0.
		for _, wh := range watchHandles {
			_, err := store.Cancel(t.Context(), wh.owner, wh.handle)
			testutil.FailErr(t, "cancel file watch", err)
		}
		store.sampler.mu.Lock()
		finalSubs := store.sampler.subscriberCountLocked()
		finalGroups := len(store.sampler.groupsByID)
		store.sampler.mu.Unlock()
		if finalSubs != 0 || finalGroups != 0 || store.Heap().Len() != 0 {
			t.Fatalf("resources leaked after cancel: subs=%d groups=%d heap=%d", finalSubs, finalGroups, store.Heap().Len())
		}
	})

	t.Run("hourly_budget_12_and_other_session_fairness_during_noisy_burst", func(t *testing.T) {
		store, clock, database, owner := ww06OpenStore(t, base)
		otherOwner := Owner{ProjectID: owner.ProjectID, SessionID: "session-2"}

		admittedBySession := map[string][]string{}
		gate := AmbientWakeGate{
			DispatchTurn: func(_ context.Context, sessionID, _, text string, onAdmitted func() error) error {
				if err := onAdmitted(); err != nil {
					return err
				}
				admittedBySession[sessionID] = append(admittedBySession[sessionID], text)
				return nil
			},
		}
		store.SetAmbientWakeGate(gate)

		noisyRec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      owner,
			Delivery:   DeliveryWake,
			Repeat:     true,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
		})
		testutil.FailErr(t, "register noisy session-1 wake watch", err)

		quietRec, err := store.Register(t.Context(), RegisterRequest{
			Owner:      otherOwner,
			Delivery:   DeliveryWake,
			Repeat:     true,
			Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
		})
		testutil.FailErr(t, "register quiet session-2 wake watch", err)

		// Saturate session-1's durable 12/hr budget with a 15-event burst.
		for i := 1; i <= DefaultHourlyWakeBudget+3; i++ {
			clock.Advance(time.Minute)
			_, _, err := store.RecordMatch(t.Context(), noisyRec.WatchID, MatchInput{
				SourceKind:   ConditionProcessDone,
				OccurrenceID: fmt.Sprintf("noisy-%d", i),
				Outcome:      "satisfied",
				Evidence:     map[string]any{"burst": i},
			}, nil)
			testutil.FailErr(t, "record noisy burst match", err)
		}
		if got := len(admittedBySession[owner.SessionID]); got != DefaultHourlyWakeBudget {
			t.Fatalf("session-1 wakes = %d, want %d", got, DefaultHourlyWakeBudget)
		}

		// While session-1 is budget-throttled, session-2 must wake immediately (other-session fairness).
		_, _, err = store.RecordMatch(t.Context(), quietRec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: "quiet-1",
			Outcome:      "satisfied",
			Evidence:     map[string]any{"session": "session-2"},
		}, nil)
		testutil.FailErr(t, "record quiet session-2 match", err)
		if got := len(admittedBySession[otherOwner.SessionID]); got != 1 {
			t.Fatalf("session-2 starved during session-1 noisy burst: wakes=%d, want 1", got)
		}

		// Restart store: durable budget and wake_retry_due_at survive restart.
		store = ww06RestartStore(t, database, clock)
		store.SetAmbientWakeGate(gate)
		if got := len(admittedBySession[owner.SessionID]); got != DefaultHourlyWakeBudget {
			t.Fatalf("budget bypassed on restart: wakes=%d", got)
		}

		// Advance 1 hour -> coalesced retry admits remaining matches 13..15 in one batch.
		clock.Advance(time.Hour)
		testutil.FailErr(t, "advance due after 1h window", store.AdvanceDue(t.Context()))
		if got := len(admittedBySession[owner.SessionID]); got != DefaultHourlyWakeBudget+1 {
			t.Fatalf("coalesced retry count = %d, want %d", got, DefaultHourlyWakeBudget+1)
		}
		last := admittedBySession[owner.SessionID][len(admittedBySession[owner.SessionID])-1]
		if !strings.Contains(last, `"burst":13`) || !strings.Contains(last, `"burst":15`) {
			t.Fatalf("coalesced batch dropped retained burst matches: %s", last)
		}
	})
}

// TestWW06C21CompactionFailureRecovery exercises C21-compaction-failure-recovery:
// overflow compaction beyond 32 buffered matches preserves brief failure-then-recovery
// visibility within the 4096 B batch cap and accounts for all retained history.
func TestWW06C21CompactionFailureRecovery(t *testing.T) {
	base := time.Date(2026, 11, 27, 16, 30, 0, 0, time.UTC)
	store, _, _, owner := ww06OpenStore(t, base)

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: "http_ready", URL: "http://127.0.0.1:8080/health"}},
	})
	testutil.FailErr(t, "register repeating watch", err)

	// Record an oversized failure match (> 2048 B) followed by 36 recovery matches (> 32 buffered).
	mFail, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind:   "http_ready",
		OccurrenceID: "c21-fail-0",
		Outcome:      "failed",
		Evidence:     map[string]any{"status": 503, "report": strings.Repeat("F", 3072)},
	}, nil)
	testutil.FailErr(t, "record oversized failure", err)
	if !ok || len(mFail.Evidence) > MaxInlineMatchBytes || mFail.RetainedRef == "" {
		t.Fatalf("oversized failure match not bounded: len=%d ref=%q", len(mFail.Evidence), mFail.RetainedRef)
	}

	const recoveryCount = 36
	for i := 1; i <= recoveryCount; i++ {
		_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   "http_ready",
			OccurrenceID: fmt.Sprintf("c21-rec-%d", i),
			Outcome:      "satisfied",
			Evidence:     map[string]any{"status": 200, "seq": i, "pad": strings.Repeat("r", 420)},
		}, nil)
		testutil.FailErr(t, "record recovery match", err)
		if !ok {
			t.Fatalf("recovery match %d rejected", i)
		}
	}

	view, err := store.Inspect(t.Context(), owner, rec.Handle)
	testutil.FailErr(t, "inspect compacted watch", err)
	wantCompacted := (1 + recoveryCount) - MaxBufferedMatches
	if view.PendingMatches != MaxBufferedMatches {
		t.Fatalf("pending = %d, want %d", view.PendingMatches, MaxBufferedMatches)
	}
	if view.Overflow == nil || !view.Overflow.HadFailure || !view.Overflow.HadRecovery || view.Overflow.CompactedCount != wantCompacted {
		t.Fatalf("overflow summary lost failure->recovery visibility: %+v (want compacted=%d)", view.Overflow, wantCompacted)
	}

	// Drain all pending matches in bounded collect batches and verify each batch <= 4096 B.
	totalDelivered := 0
	batchNum := 0
	sawFailureRecoveryNotice := false
	for {
		batchNum++
		batch, hasWork, err := ReserveCollectBatch(t.Context(), store, owner, fmt.Sprintf("c21-drain-%d", batchNum))
		testutil.FailErr(t, "reserve drain batch", err)
		if !hasWork {
			break
		}
		batchBytes := 0
		for _, m := range batch.Matches {
			batchBytes += len(m.Evidence)
		}
		if batchBytes > MaxBatchBytes {
			t.Fatalf("batch %d evidence bytes = %d > MaxBatchBytes (%d)", batchNum, batchBytes, MaxBatchBytes)
		}
		formatted := FormatCollectedBatch(batch)
		if strings.Contains(formatted, "had_failure=true") && strings.Contains(formatted, "had_recovery=true") {
			sawFailureRecoveryNotice = true
		}
		testutil.FailErr(t, "commit drain batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch.DeliveryID))
		totalDelivered += len(batch.Matches)
		if batchNum > 40 {
			t.Fatal("drain loop did not terminate")
		}
	}
	if totalDelivered != MaxBufferedMatches {
		t.Fatalf("total delivered = %d, want %d", totalDelivered, MaxBufferedMatches)
	}
	if !sawFailureRecoveryNotice {
		t.Fatal("formatted batch never surfaced had_failure=true, had_recovery=true")
	}
}

// ============================================================================
// C22 · Independent Replay State Model + Delta-Debugging Shrinker
// ============================================================================

type ww06OpKind string

const (
	opRegister  ww06OpKind = "register"
	opPublish   ww06OpKind = "publish"
	opWait      ww06OpKind = "wait"
	opCollect   ww06OpKind = "collect"
	opCancel    ww06OpKind = "cancel"
	opExpire    ww06OpKind = "expire"
	opInterrupt ww06OpKind = "interrupt"
	opRestart   ww06OpKind = "restart"
)

type ww06Op struct {
	Kind         ww06OpKind `json:"kind"`
	SessionIdx   int        `json:"session_idx"`
	WatchSlot    int        `json:"watch_slot,omitempty"`
	Delivery     string     `json:"delivery,omitempty"`
	Repeat       bool       `json:"repeat,omitempty"`
	LifetimeSec  int        `json:"lifetime_sec,omitempty"`
	OccurrenceID string     `json:"occurrence_id,omitempty"`
	AllowPark    bool       `json:"allow_park,omitempty"`
	Commit       bool       `json:"commit,omitempty"`
	AdvanceSec   int        `json:"advance_sec,omitempty"`
	ClearFence   bool       `json:"clear_fence,omitempty"`
}

type ww06RefWatch struct {
	Handle       string
	WatchID      string
	SessionID    string
	Delivery     DeliveryMode
	Repeat       bool
	ExpiresAt    time.Time
	State        State
	Outcome      TerminalOutcome
	SlotReleased bool
	PendingOccs  []string
	SeenOccs     map[string]bool
	DeliveredOcc map[string]bool
}

type ww06RefLease struct {
	Active          bool
	SelectedHandles []string
	Deadline        time.Time
}

type ww06RefSession struct {
	Stopped        bool
	NextSeq        int
	Watches        []*ww06RefWatch
	Lease          ww06RefLease
	WakeAdmissions []time.Time
	WakeRetryDue   time.Time
}

type ww06RefModel struct {
	Now      time.Time
	Sessions map[string]*ww06RefSession
}

func newWW06RefModel(start time.Time) *ww06RefModel {
	return &ww06RefModel{
		Now: start.UTC(),
		Sessions: map[string]*ww06RefSession{
			"session-1": {NextSeq: 1},
			"session-2": {NextSeq: 1},
		},
	}
}

func (m *ww06RefModel) counts(sess *ww06RefSession) (live, slots int) {
	for _, w := range sess.Watches {
		if !w.SlotReleased {
			slots++
			if w.State == StateLive {
				live++
			}
		}
	}
	return live, slots
}

func (m *ww06RefModel) findWatch(sess *ww06RefSession, handle string) *ww06RefWatch {
	for _, w := range sess.Watches {
		if w.Handle == handle {
			return w
		}
	}
	return nil
}

func (m *ww06RefModel) hasWakeDemand(sess *ww06RefSession) bool {
	for _, w := range sess.Watches {
		if w.Delivery == DeliveryWake && len(w.PendingOccs) > 0 {
			return true
		}
	}
	return false
}

func (m *ww06RefModel) tryAmbientWake(sess *ww06RefSession) int {
	if sess.Stopped || sess.Lease.Active || !m.hasWakeDemand(sess) {
		return 0
	}
	cutoff := m.Now.Add(-time.Hour)
	var kept []time.Time
	for _, ts := range sess.WakeAdmissions {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	sess.WakeAdmissions = kept
	if len(kept) >= DefaultHourlyWakeBudget {
		sess.WakeRetryDue = kept[0].Add(time.Hour)
		return 0
	}
	claimed := 0
	for _, w := range sess.Watches {
		if len(w.PendingOccs) == 0 {
			continue
		}
		for _, occ := range w.PendingOccs {
			w.DeliveredOcc[occ] = true
			claimed++
		}
		w.PendingOccs = nil
		if w.State == StateTerminal && w.Outcome == OutcomeCompleted {
			w.SlotReleased = true
		}
	}
	if claimed > 0 {
		sess.WakeAdmissions = append(sess.WakeAdmissions, m.Now)
		sess.WakeRetryDue = time.Time{}
		return 1
	}
	return 0
}

func (m *ww06RefModel) settleSelectedLeaseIfReady(sess *ww06RefSession) bool {
	if !sess.Lease.Active {
		return false
	}
	var regs []*ww06RefWatch
	for _, h := range sess.Lease.SelectedHandles {
		if w := m.findWatch(sess, h); w != nil {
			regs = append(regs, w)
		}
	}
	claimed := 0
	for _, w := range regs {
		for _, occ := range w.PendingOccs {
			w.DeliveredOcc[occ] = true
			claimed++
		}
		w.PendingOccs = nil
		if w.State == StateTerminal && w.Outcome == OutcomeCompleted {
			w.SlotReleased = true
		}
	}
	if claimed > 0 {
		sess.Lease = ww06RefLease{}
		return true
	}
	for _, w := range regs {
		if w.State == StateTerminal {
			sess.Lease = ww06RefLease{}
			return true
		}
	}
	return false
}

func generateWW06History(seed int64, length int) []ww06Op {
	rng := rand.New(rand.NewSource(seed))
	ops := make([]ww06Op, 0, length)
	for step := range length {
		sessIdx := rng.Intn(2)
		roll := rng.Intn(100)
		switch {
		case step < 3 || roll < 22:
			deliv := string(DeliveryCollect)
			if rng.Intn(2) == 0 {
				deliv = string(DeliveryWake)
			}
			ops = append(ops, ww06Op{
				Kind:        opRegister,
				SessionIdx:  sessIdx,
				Delivery:    deliv,
				Repeat:      rng.Intn(2) == 0,
				LifetimeSec: 120 + rng.Intn(600),
			})
		case roll < 46:
			ops = append(ops, ww06Op{
				Kind:         opPublish,
				SessionIdx:   sessIdx,
				WatchSlot:    rng.Intn(6),
				OccurrenceID: fmt.Sprintf("occ-s%d-%d", seed, rng.Intn(10)),
			})
		case roll < 58:
			ops = append(ops, ww06Op{
				Kind:       opWait,
				SessionIdx: sessIdx,
				WatchSlot:  rng.Intn(6),
				AllowPark:  rng.Intn(2) == 0,
			})
		case roll < 72:
			ops = append(ops, ww06Op{
				Kind:       opCollect,
				SessionIdx: sessIdx,
				Commit:     rng.Intn(3) != 0,
			})
		case roll < 82:
			ops = append(ops, ww06Op{
				Kind:       opCancel,
				SessionIdx: sessIdx,
				WatchSlot:  rng.Intn(6),
			})
		case roll < 90:
			ops = append(ops, ww06Op{
				Kind:       opExpire,
				SessionIdx: sessIdx,
				AdvanceSec: 90 + rng.Intn(360),
			})
		case roll < 95:
			ops = append(ops, ww06Op{
				Kind:       opInterrupt,
				SessionIdx: sessIdx,
				ClearFence: rng.Intn(2) == 0,
			})
		default:
			ops = append(ops, ww06Op{
				Kind:       opRestart,
				SessionIdx: sessIdx,
			})
		}
	}
	return ops
}

type ww06Divergence struct {
	Seed        int64          `json:"seed"`
	Step        int            `json:"step"`
	Reason      string         `json:"reason"`
	Choices     []ww06Op       `json:"choices"`
	SourceFacts map[string]any `json:"source_facts"`
}

func runWW06History(t *testing.T, seed int64, ops []ww06Op, injectFault func(step int, op ww06Op) string) *ww06Divergence {
	t.Helper()
	start := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)
	store, clock, database, _ := ww06OpenStore(t, start)
	model := newWW06RefModel(start)
	ctx := t.Context()

	wireCallbacks := func(st *Store) {
		st.SetLeaseWakeHandler(func(context.Context, awaitstore.Lease, awaitstore.Condition) {})
		st.SetAmbientWakeGate(AmbientWakeGate{
			DispatchTurn: func(_ context.Context, _, _, _ string, onAdmitted func() error) error {
				return onAdmitted()
			},
		})
	}
	wireCallbacks(store)

	sessionIDs := []string{"session-1", "session-2"}

	for step, op := range ops {
		if injectFault != nil {
			if msg := injectFault(step, op); msg != "" {
				return &ww06Divergence{
					Seed:        seed,
					Step:        step,
					Reason:      msg,
					Choices:     append([]ww06Op(nil), ops...),
					SourceFacts: map[string]any{"injected_fault": true, "history_len": len(ops)},
				}
			}
		}

		sid := sessionIDs[op.SessionIdx%len(sessionIDs)]
		owner := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: sid}
		sess := model.Sessions[sid]

		switch op.Kind {
		case opRegister:
			cond := awaitstore.Condition{Kind: ConditionProcessDone}
			if !op.Repeat {
				cond.Handles = []string{fmt.Sprintf("cmd-%d-%d", seed, step)}
			}
			rcpt, err := store.Register(ctx, RegisterRequest{
				Owner:      owner,
				Delivery:   DeliveryMode(op.Delivery),
				Repeat:     op.Repeat,
				ExpiresAt:  clock.Now().Add(time.Duration(op.LifetimeSec) * time.Second),
				Conditions: []awaitstore.Condition{cond},
			})

			live, slots := model.counts(sess)
			switch {
			case sess.Stopped:
				if !errors.Is(err, ErrSessionStopped) {
					return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("register while stopped: got err=%v, want ErrSessionStopped", err), Choices: ops, SourceFacts: map[string]any{"session": sid}}
				}
			case live >= MaxLiveExplicitWatches || slots >= MaxTotalExplicitSlots:
				var capErr *CapacityError
				if !errors.As(err, &capErr) {
					return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("register at cap (live=%d slots=%d): got err=%v, want CapacityError", live, slots, err), Choices: ops, SourceFacts: map[string]any{"session": sid, "live": live, "slots": slots}}
				}
			default:
				if err != nil {
					return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("unexpected register error: %v", err), Choices: ops, SourceFacts: map[string]any{"session": sid}}
				}
				wantHandle := fmt.Sprintf("watch-%d", sess.NextSeq)
				sess.NextSeq++
				if rcpt.Handle != wantHandle {
					return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("handle=%q want=%q", rcpt.Handle, wantHandle), Choices: ops, SourceFacts: map[string]any{"session": sid}}
				}
				sess.Watches = append(sess.Watches, &ww06RefWatch{
					Handle:       rcpt.Handle,
					WatchID:      rcpt.WatchID,
					SessionID:    sid,
					Delivery:     DeliveryMode(op.Delivery),
					Repeat:       op.Repeat,
					ExpiresAt:    model.Now.Add(time.Duration(op.LifetimeSec) * time.Second),
					State:        StateLive,
					SeenOccs:     map[string]bool{},
					DeliveredOcc: map[string]bool{},
				})
			}

		case opPublish:
			if len(sess.Watches) == 0 {
				continue
			}
			w := sess.Watches[op.WatchSlot%len(sess.Watches)]
			_, inserted, err := store.RecordMatch(ctx, w.WatchID, MatchInput{
				SourceKind:   ConditionProcessDone,
				OccurrenceID: op.OccurrenceID,
				Outcome:      "satisfied",
				Evidence:     map[string]any{"occ": op.OccurrenceID},
			}, nil)
			if err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("RecordMatch error: %v", err), Choices: ops, SourceFacts: map[string]any{"watch": w.Handle}}
			}

			wantInsert := !sess.Stopped && w.State == StateLive && !w.SeenOccs[op.OccurrenceID]
			if inserted != wantInsert {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("RecordMatch inserted=%v want=%v (stopped=%v state=%s dup=%v)", inserted, wantInsert, sess.Stopped, w.State, w.SeenOccs[op.OccurrenceID]), Choices: ops, SourceFacts: map[string]any{"watch": w.Handle, "occ": op.OccurrenceID}}
			}
			if wantInsert {
				w.SeenOccs[op.OccurrenceID] = true
				w.PendingOccs = append(w.PendingOccs, op.OccurrenceID)
				if !w.Repeat {
					w.State = StateTerminal
					w.Outcome = OutcomeCompleted
				}
				settledLease := false
				if sess.Lease.Active {
					for _, h := range sess.Lease.SelectedHandles {
						if h == w.Handle {
							settledLease = model.settleSelectedLeaseIfReady(sess)
							break
						}
					}
				}
				if !settledLease && w.Delivery == DeliveryWake {
					model.tryAmbientWake(sess)
				}
			}

		case opWait:
			if len(sess.Watches) == 0 {
				continue
			}
			w := sess.Watches[op.WatchSlot%len(sess.Watches)]
			res, err := store.SelectOrPark(ctx, SelectRequest{
				Owner:     owner,
				Handles:   []string{w.Handle},
				AllowPark: op.AllowPark,
				ParkLease: awaitstore.Lease{
					SessionID: sid,
					ProjectID: owner.ProjectID,
					ToolCallID: fmt.Sprintf("wait-%d-%d", seed, step),
					Deadline:  clock.Now().Add(5 * time.Minute),
				},
			})
			if err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("SelectOrPark error: %v", err), Choices: ops, SourceFacts: map[string]any{"watch": w.Handle}}
			}

			// Model transition: expire if due, claim if pending, terminal if terminal, else park.
			if w.State == StateLive && !w.ExpiresAt.After(model.Now) {
				w.State = StateTerminal
				w.Outcome = OutcomeExpired
				if len(w.PendingOccs) == 0 {
					w.SlotReleased = true
				}
			}
			wantParked := false
			if len(w.PendingOccs) > 0 {
				for _, occ := range w.PendingOccs {
					w.DeliveredOcc[occ] = true
				}
				w.PendingOccs = nil
				if w.State == StateTerminal && w.Outcome == OutcomeCompleted {
					w.SlotReleased = true
				}
			} else if w.State == StateTerminal {
				// Immediate terminal selection.
			} else if op.AllowPark {
				wantParked = true
				sess.Lease = ww06RefLease{
					Active:          true,
					SelectedHandles: []string{w.Handle},
					Deadline:        model.Now.Add(5 * time.Minute),
				}
			}
			if res.Parked != wantParked {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("SelectOrPark parked=%v want=%v", res.Parked, wantParked), Choices: ops, SourceFacts: map[string]any{"watch": w.Handle}}
			}

		case opCollect:
			batch, ok, err := ReserveCollectBatch(ctx, store, owner, fmt.Sprintf("collect-%d-%d", seed, step))
			if err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("ReserveCollectBatch error: %v", err), Choices: ops, SourceFacts: map[string]any{"session": sid}}
			}
			wantHasPending := false
			for _, w := range sess.Watches {
				if len(w.PendingOccs) > 0 {
					wantHasPending = true
					break
				}
			}
			if ok != wantHasPending {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("ReserveCollectBatch ok=%v want=%v", ok, wantHasPending), Choices: ops, SourceFacts: map[string]any{"session": sid}}
			}
			if ok {
				if op.Commit {
					if err := CommitCollectBatch(ctx, store, sid, batch.DeliveryID); err != nil {
						return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("CommitCollectBatch error: %v", err), Choices: ops, SourceFacts: map[string]any{"delivery": batch.DeliveryID}}
					}
					for _, w := range sess.Watches {
						for _, occ := range w.PendingOccs {
							w.DeliveredOcc[occ] = true
						}
						w.PendingOccs = nil
						if w.State == StateTerminal && w.Outcome == OutcomeCompleted {
							w.SlotReleased = true
						}
					}
				} else {
					if err := ReleaseCollectBatch(ctx, store, sid, batch.DeliveryID); err != nil {
						return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("ReleaseCollectBatch error: %v", err), Choices: ops, SourceFacts: map[string]any{"delivery": batch.DeliveryID}}
					}
				}
			}

		case opCancel:
			if len(sess.Watches) == 0 {
				continue
			}
			w := sess.Watches[op.WatchSlot%len(sess.Watches)]
			rcpt, err := store.Cancel(ctx, owner, w.Handle)
			if err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("Cancel error: %v", err), Choices: ops, SourceFacts: map[string]any{"watch": w.Handle}}
			}
			if w.State != StateTerminal {
				w.State = StateTerminal
				w.Outcome = OutcomeCanceled
			}
			w.SlotReleased = true
			w.PendingOccs = nil
			if sess.Lease.Active {
				for _, h := range sess.Lease.SelectedHandles {
					if h == w.Handle {
						model.settleSelectedLeaseIfReady(sess)
						break
					}
				}
			}
			if rcpt.Outcome != w.Outcome {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("Cancel outcome=%q want=%q", rcpt.Outcome, w.Outcome), Choices: ops, SourceFacts: map[string]any{"watch": w.Handle}}
			}

		case opExpire:
			delta := time.Duration(op.AdvanceSec) * time.Second
			clock.Advance(delta)
			model.Now = model.Now.Add(delta)
			if err := store.AdvanceDue(ctx); err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("AdvanceDue error: %v", err), Choices: ops, SourceFacts: map[string]any{"advance_sec": op.AdvanceSec}}
			}
			// Mirror priority order: watch expiries first, then wake retries, then wait deadlines.
			for _, id := range sessionIDs {
				s := model.Sessions[id]
				for _, w := range s.Watches {
					if w.State == StateLive && !w.ExpiresAt.After(model.Now) {
						w.State = StateTerminal
						w.Outcome = OutcomeExpired
						if len(w.PendingOccs) == 0 {
							w.SlotReleased = true
						}
						if s.Lease.Active {
							for _, h := range s.Lease.SelectedHandles {
								if h == w.Handle {
									model.settleSelectedLeaseIfReady(s)
									break
								}
							}
						}
					}
				}
				if !s.WakeRetryDue.IsZero() && !s.WakeRetryDue.After(model.Now) {
					model.tryAmbientWake(s)
				}
				if s.Lease.Active && !s.Lease.Deadline.After(model.Now) {
					s.Lease = ww06RefLease{}
				}
			}

		case opInterrupt:
			if _, err := store.FenceAndCancelSessions(ctx, []string{sid}); err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("FenceAndCancelSessions error: %v", err), Choices: ops, SourceFacts: map[string]any{"session": sid}}
			}
			sess.Stopped = true
			sess.Lease = ww06RefLease{}
			sess.WakeRetryDue = time.Time{}
			for _, w := range sess.Watches {
				if w.State == StateLive {
					w.State = StateTerminal
					w.Outcome = OutcomeCanceled
					w.SlotReleased = true
					w.PendingOccs = nil
				}
			}
			if op.ClearFence {
				if err := store.ClearSessionStopFence(ctx, sid); err != nil {
					return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("ClearSessionStopFence error: %v", err), Choices: ops, SourceFacts: map[string]any{"session": sid}}
				}
				sess.Stopped = false
			}

		case opRestart:
			store = NewStore(database, clock)
			wireCallbacks(store)
			if err := store.Recover(ctx); err != nil {
				return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("Recover error: %v", err), Choices: ops, SourceFacts: map[string]any{"session": sid}}
			}
		}

		// Step-end parity check against real SQLite store via Inspect.
		for _, id := range sessionIDs {
			s := model.Sessions[id]
			o := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: id}
			for _, w := range s.Watches {
				view, err := store.Inspect(ctx, o, w.Handle)
				if err != nil {
					return &ww06Divergence{Seed: seed, Step: step, Reason: fmt.Sprintf("Inspect(%s/%s) error: %v", id, w.Handle, err), Choices: ops, SourceFacts: map[string]any{"session": id, "watch": w.Handle}}
				}
				if view.State != w.State || view.Outcome != w.Outcome || view.PendingMatches != len(w.PendingOccs) {
					return &ww06Divergence{
						Seed:   seed,
						Step:   step,
						Reason: fmt.Sprintf("parity mismatch on %s/%s: got(state=%s outcome=%s pending=%d) want(state=%s outcome=%s pending=%d)", id, w.Handle, view.State, view.Outcome, view.PendingMatches, w.State, w.Outcome, len(w.PendingOccs)),
						Choices: ops,
						SourceFacts: map[string]any{
							"session":      id,
							"watch":        w.Handle,
							"got_state":    view.State,
							"want_state":   w.State,
							"got_pending":  view.PendingMatches,
							"want_pending": len(w.PendingOccs),
						},
					}
				}
			}
		}
	}
	return nil
}

// shrinkWW06History applies 1-minimal delta debugging (ddmin) to minimize any divergent history
// while keeping seed, choices, and source facts intact.
func shrinkWW06History(t *testing.T, div *ww06Divergence, testFn func(ops []ww06Op) *ww06Divergence) *ww06Divergence {
	t.Helper()
	best := div
	ops := append([]ww06Op(nil), div.Choices...)
	n := 2
	for len(ops) >= 2 {
		reduced := false
		chunkSize := (len(ops) + n - 1) / n
		for start := 0; start < len(ops); start += chunkSize {
			end := start + chunkSize
			if end > len(ops) {
				end = len(ops)
			}
			candidate := make([]ww06Op, 0, len(ops)-(end-start))
			candidate = append(candidate, ops[:start]...)
			candidate = append(candidate, ops[end:]...)
			if len(candidate) == 0 {
				continue
			}
			if candDiv := testFn(candidate); candDiv != nil {
				best = candDiv
				ops = candidate
				n = max(n-1, 2)
				reduced = true
				break
			}
		}
		if !reduced {
			if n >= len(ops) {
				break
			}
			n = min(len(ops), n*2)
		}
	}
	best.Choices = ops
	return best
}

func saveWW06ShrunkDivergence(t *testing.T, div *ww06Divergence) string {
	t.Helper()
	outDir := os.Getenv("WW06_EVIDENCE_DIR")
	if strings.TrimSpace(outDir) == "" {
		outDir = t.TempDir()
	}
	path := filepath.Join(outDir, fmt.Sprintf("c22-shrunk-seed-%d.json", div.Seed))
	raw, err := json.MarshalIndent(div, "", "  ")
	testutil.FailErr(t, "marshal shrunk divergence", err)
	testutil.FailErr(t, "mkdir divergence dir", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write shrunk divergence", os.WriteFile(path, append(raw, '\n'), 0o644))
	return path
}

// TestWW06C22ModelReplay exercises C22-model-replay across 64 deterministic seeds.
func TestWW06C22ModelReplay(t *testing.T) {
	const numSeeds = 64
	const historyLen = 28

	for seed := int64(1); seed <= numSeeds; seed++ {
		ops := generateWW06History(seed, historyLen)
		if div := runWW06History(t, seed, ops, nil); div != nil {
			shrunk := shrinkWW06History(t, div, func(cand []ww06Op) *ww06Divergence {
				return runWW06History(t, seed, cand, nil)
			})
			savedPath := saveWW06ShrunkDivergence(t, shrunk)
			t.Fatalf("C22 model divergence on seed=%d (shrunk %d -> %d ops, saved %s): %s",
				seed, len(ops), len(shrunk.Choices), savedPath, shrunk.Reason)
		}
	}
}

// TestWW06C22ShrinkerControl verifies that the C22 delta-debugging shrinker minimizes
// a divergent history and persists seed, choices, and source facts intact.
func TestWW06C22ShrinkerControl(t *testing.T) {
	seed := int64(42)
	ops := generateWW06History(seed, 24)
	// Inject a synthetic divergence whenever a history contains both opRegister and opRestart.
	syntheticFault := func(step int, _ ww06Op) string {
		return ""
	}
	_ = syntheticFault

	evalFault := func(cand []ww06Op) *ww06Divergence {
		hasReg, hasRestart := false, false
		for _, op := range cand {
			if op.Kind == opRegister {
				hasReg = true
			}
			if op.Kind == opRestart {
				hasRestart = true
			}
		}
		if hasReg && hasRestart {
			return &ww06Divergence{
				Seed:        seed,
				Step:        len(cand) - 1,
				Reason:      "synthetic_control_divergence",
				Choices:     append([]ww06Op(nil), cand...),
				SourceFacts: map[string]any{"control": "shrinker_verification", "has_register": true, "has_restart": true},
			}
		}
		return nil
	}
	// Ensure initial history triggers the control fault.
	ops = append(ops, ww06Op{Kind: opRestart, SessionIdx: 0})
	initial := evalFault(ops)
	if initial == nil {
		t.Fatal("control history failed to trigger synthetic divergence")
	}

	shrunk := shrinkWW06History(t, initial, evalFault)
	if len(shrunk.Choices) > 2 {
		t.Fatalf("shrinker did not minimize history: got %d ops, want <= 2", len(shrunk.Choices))
	}
	savedPath := saveWW06ShrunkDivergence(t, shrunk)
	raw, err := os.ReadFile(savedPath)
	testutil.FailErr(t, "read saved shrunk artifact", err)
	var decoded ww06Divergence
	testutil.FailErr(t, "decode shrunk artifact", json.Unmarshal(raw, &decoded))
	if decoded.Seed != seed || len(decoded.Choices) != len(shrunk.Choices) || decoded.SourceFacts["control"] != "shrinker_verification" {
		t.Fatalf("shrunk artifact missing seed/choices/source_facts: %+v", decoded)
	}
	kinds := []string{string(decoded.Choices[0].Kind), string(decoded.Choices[1].Kind)}
	sort.Strings(kinds)
	if kinds[0] != string(opRegister) || kinds[1] != string(opRestart) {
		t.Fatalf("shrunk choices unexpected: %v", kinds)
	}
}
