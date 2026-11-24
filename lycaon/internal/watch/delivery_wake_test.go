package watch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func testPromptEngine(t *testing.T) prompts.PromptTemplateEngine {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
}

func TestCollectCoordinatorAndWorkerAssemblyResourcePresenceAndFailedRender(t *testing.T) {
	store, _, database, owner := openTestWatchStore(t)
	workerSessID := "worker-sess-1"
	workerJobID := "worker-job-1"
	testdbseed.InsertChildSession(t, database, workerSessID, owner.SessionID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, child_session_id, agent_type, prompt, brief, created_at)
		VALUES (?, ?, ?, 'implementer', 'implement feature', 'implement feature', '2026-11-23T12:00:00Z')`,
		workerJobID, testdbseed.DefaultProjectID, workerSessID)
	testutil.FailErr(t, "insert worker job", err)

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-1"}}},
	})
	testutil.FailErr(t, "register coordinator watch", err)
	_, ok, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "occ-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-1", "exit_code": 0},
	}, nil)
	testutil.FailErr(t, "record coordinator match", err)
	if !ok {
		t.Fatal("expected match insertion")
	}

	adapter := &Delivery{
		Store: store,
		WorkerJobID: func(_ context.Context, childID string) string {
			if childID == workerSessID {
				return workerJobID
			}
			return ""
		},
	}
	pe := testPromptEngine(t)
	inj := prompts.NewInjectRenderer(pe)
	coordSess := &api.Session{ID: owner.SessionID, ProjectID: owner.ProjectID, Posture: api.SessionPostureBuild}
	history := []api.Message{{
		ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Visibility: api.MessageVisibilityTranscript, Content: "continue",
	}}

	var failRender bool
	var presenceDuringRender bool
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(assembly.AssemblyDeps{
		Prompts:    pe,
		Injects:    inj,
		Limits:     func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		Deliveries: assembly.Deliveries{Watches: adapter},
		AgentsMDPolicies: func(ctx context.Context, sess *api.Session, _ []api.Message) ([]api.Message, error) {
			var hErr error
			presenceDuringRender, hErr = adapter.HasActionableWatches(ctx, sess)
			if hErr != nil {
				return nil, hErr
			}
			if failRender {
				return nil, errors.New("simulated render failure")
			}
			return nil, nil
		},
	})

	// Preview renders never reserve or consume matches.
	_, err = eng.BuildCompletionMessages(assembly.WithPreviewOnly(t.Context()), coordSess, history, nil)
	testutil.FailErr(t, "preview render", err)
	actionableAfterPreview, err := adapter.HasActionableWatches(t.Context(), coordSess)
	testutil.FailErr(t, "actionable after preview", err)
	if !actionableAfterPreview {
		t.Fatal("preview render consumed pending one-shot match")
	}

	// Failed pre-admission render reserves before computing presence, then releases and restores visibility.
	eng.BeginPromptTurn(coordSess.ID)
	failRender = true
	if _, err := eng.BuildCompletionMessages(t.Context(), coordSess, history, nil); err == nil {
		t.Fatal("expected simulated render failure")
	}
	if presenceDuringRender {
		t.Fatal("expected one-shot covered by reservation to be excluded from actionable presence during render")
	}
	restored, err := adapter.HasActionableWatches(t.Context(), coordSess)
	testutil.FailErr(t, "actionable after failed render", err)
	if !restored {
		t.Fatal("failed render did not release reservation and restore visibility")
	}

	// Successful render reserves, retry before commit replays identical receipt, and commit marks delivered.
	failRender = false
	msgs1, err := eng.BuildCompletionMessages(t.Context(), coordSess, history, nil)
	testutil.FailErr(t, "first render", err)
	if presenceDuringRender {
		t.Fatal("expected covered terminal one-shot excluded during successful render")
	}
	msgsRetry, err := eng.BuildCompletionMessages(t.Context(), coordSess, history, nil)
	testutil.FailErr(t, "retry render", err)
	block1, blockRetry := findWatchBlock(msgs1), findWatchBlock(msgsRetry)
	if block1 == "" || block1 != blockRetry {
		t.Fatalf("expected identical replayed watch block:\nfirst=%q\nretry=%q", block1, blockRetry)
	}
	testutil.FailErr(t, "commit turn context", eng.CommitTurnContext(t.Context(), coordSess.ID, "resp-1"))
	eng.EndPromptTurn(coordSess.ID)

	view, err := store.Inspect(t.Context(), owner, rec.Handle)
	testutil.FailErr(t, "inspect committed watch", err)
	if view.PendingMatches != 0 {
		t.Fatalf("pending matches after commit = %d, want 0", view.PendingMatches)
	}

	// Worker session collects only its own job's watches through the worker request path.
	workerOwner := Owner{Kind: OwnerJob, ProjectID: owner.ProjectID, SessionID: workerSessID, WorkerJobID: workerJobID}
	wRec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      workerOwner,
		Delivery:   DeliveryCollect,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"w-cmd"}}},
	})
	testutil.FailErr(t, "register worker watch", err)
	_, _, err = store.RecordMatch(t.Context(), wRec.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "w-occ-1",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "w-cmd"},
	}, nil)
	testutil.FailErr(t, "record worker match", err)

	workerSess := &api.Session{ID: workerSessID, ProjectID: owner.ProjectID, ParentSessionID: owner.SessionID, AgentType: "implement"}
	eng.BeginPromptTurn(workerSess.ID)
	wMsgs, err := eng.BuildCompletionMessages(t.Context(), workerSess, history, nil)
	testutil.FailErr(t, "worker render", err)
	if got := findWatchBlock(wMsgs); !strings.Contains(got, wRec.Handle) {
		t.Fatalf("worker prompt missing collected watch block: %q", got)
	}
	testutil.FailErr(t, "commit worker turn context", eng.CommitTurnContext(t.Context(), workerSess.ID, "w-resp-1"))
	eng.EndPromptTurn(workerSess.ID)
}

func TestAmbientWakeNoWorkflowActiveWorkflowHoldsAndWorkerExclusion(t *testing.T) {
	store, _, database, owner := openTestWatchStore(t)
	workerSessID := "worker-sess-2"
	testdbseed.InsertChildSession(t, database, workerSessID, owner.SessionID)

	var (
		sessionIdle     = true
		pendingInput    = false
		approvalHeld    = false
		spendHeld       = false
		hasWorkflow     = false
		workflowRunning = true
		admittedTurns   []string
	)
	store.SetAmbientWakeGate(AmbientWakeGate{
		SessionEligible: func(_ context.Context, sessionID string) (bool, bool) {
			if sessionID == workerSessID || !sessionIdle || pendingInput || spendHeld {
				return false, true
			}
			return true, false
		},
		WorkflowAdmit: func(_ context.Context, _ string) (bool, bool) {
			if approvalHeld {
				return false, hasWorkflow
			}
			if !hasWorkflow {
				return true, false
			}
			return workflowRunning, true
		},
		DispatchTurn: func(_ context.Context, _, submissionID, text string, onAdmitted func() error) error {
			if err := onAdmitted(); err != nil {
				return err
			}
			admittedTurns = append(admittedTurns, submissionID+":"+text)
			return nil
		},
	})

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
	})
	testutil.FailErr(t, "register wake watch", err)

	// 1. Idle chat with NO workflow wakes immediately.
	_, _, err = store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "occ-no-wf", Outcome: "satisfied",
		Evidence: map[string]any{"step": "no_workflow"},
	}, nil)
	testutil.FailErr(t, "match idle no workflow", err)
	if len(admittedTurns) != 1 || !strings.Contains(admittedTurns[0], "no_workflow") {
		t.Fatalf("expected 1 ambient wake with no workflow, got %v", admittedTurns)
	}

	// 2. Pending input, approval hold, and spend hold each retain evidence without admitting a turn.
	pendingInput = true
	_, _, err = store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "occ-held", Outcome: "satisfied",
		Evidence: map[string]any{"step": "held"},
	}, nil)
	testutil.FailErr(t, "match during pending input", err)
	if len(admittedTurns) != 1 {
		t.Fatalf("pending input failed to block ambient wake: %v", admittedTurns)
	}
	pendingInput = false
	approvalHeld = true
	if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke {
		t.Fatalf("approval hold failed to block ambient wake: woke=%v err=%v", woke, err)
	}
	approvalHeld = false
	spendHeld = true
	if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke {
		t.Fatalf("spend hold failed to block ambient wake: woke=%v err=%v", woke, err)
	}

	// 3. Clearing holds within an active workflow admits the retained match.
	spendHeld = false
	hasWorkflow = true
	workflowRunning = true
	woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID)
	testutil.FailErr(t, "wake in active workflow", err)
	if !woke || len(admittedTurns) != 2 || !strings.Contains(admittedTurns[1], "held") {
		t.Fatalf("active workflow wake failed: woke=%v turns=%v", woke, admittedTurns)
	}

	// 4. Changing wake -> collect withdraws unadmitted queued demand.
	sessionIdle = false
	_, _, err = store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
		SourceKind: ConditionProcessDone, OccurrenceID: "occ-withdraw", Outcome: "satisfied",
		Evidence: map[string]any{"step": "withdraw"},
	}, nil)
	testutil.FailErr(t, "record match while busy", err)
	_, err = store.SetDelivery(t.Context(), owner, rec.Handle, DeliveryCollect)
	testutil.FailErr(t, "switch wake to collect", err)
	sessionIdle = true
	if woke, err := store.TriggerAmbientWake(t.Context(), owner.SessionID); err != nil || woke {
		t.Fatalf("expected no ambient wake after switching to collect: woke=%v err=%v", woke, err)
	}

	// 5. Workers cannot register wake watches and never trigger ambient wakes.
	workerOwner := Owner{Kind: OwnerJob, ProjectID: owner.ProjectID, SessionID: workerSessID, WorkerJobID: "job-2"}
	if _, err := store.Register(t.Context(), RegisterRequest{
		Owner:      workerOwner,
		Delivery:   DeliveryWake,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"w"}}},
	}); err == nil {
		t.Fatal("expected worker wake registration to be rejected")
	}
}

func TestAmbientWakeRollingHourlyBudgetAndRestartedCoalescedRetry(t *testing.T) {
	store, clock, database, owner := openTestWatchStore(t)
	var admittedBatches []string
	gate := AmbientWakeGate{
		DispatchTurn: func(_ context.Context, _, _, text string, onAdmitted func() error) error {
			if err := onAdmitted(); err != nil {
				return err
			}
			admittedBatches = append(admittedBatches, text)
			return nil
		},
	}
	store.SetAmbientWakeGate(gate)

	rec, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: ConditionProcessDone}},
	})
	testutil.FailErr(t, "register repeating wake watch", err)

	for i := 1; i <= DefaultHourlyWakeBudget+2; i++ {
		clock.Advance(time.Minute)
		_, _, err := store.RecordMatch(t.Context(), rec.WatchID, MatchInput{
			SourceKind:   ConditionProcessDone,
			OccurrenceID: fmt.Sprintf("tick-%d", i),
			Outcome:      "satisfied",
			Evidence:     map[string]any{"tick": i},
		}, nil)
		testutil.FailErr(t, "record budget tick", err)
	}
	if len(admittedBatches) != DefaultHourlyWakeBudget {
		t.Fatalf("admitted wakes in 1h = %d, want %d", len(admittedBatches), DefaultHourlyWakeBudget)
	}

	// Simulate restart: a fresh Store recovers the persisted wake_retry_due_at into its heap.
	restarted := NewStore(database, clock)
	restarted.SetAmbientWakeGate(gate)
	testutil.FailErr(t, "recover before due", restarted.Recover(t.Context()))
	if len(admittedBatches) != DefaultHourlyWakeBudget {
		t.Fatalf("recovered retry fired early: %d", len(admittedBatches))
	}

	clock.Advance(time.Hour)
	testutil.FailErr(t, "advance due after budget window", restarted.AdvanceDue(t.Context()))
	if len(admittedBatches) != DefaultHourlyWakeBudget+1 {
		t.Fatalf("coalesced retry did not admit exactly once: got %d", len(admittedBatches))
	}
	coalesced := admittedBatches[len(admittedBatches)-1]
	if !strings.Contains(coalesced, `"tick":13`) || !strings.Contains(coalesced, `"tick":14`) {
		t.Fatalf("expected coalesced batch to carry both retained matches: %s", coalesced)
	}
}

func findWatchBlock(msgs []api.Message) string {
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "Watch observations (receipt ") {
			return m.Content
		}
	}
	return ""
}
