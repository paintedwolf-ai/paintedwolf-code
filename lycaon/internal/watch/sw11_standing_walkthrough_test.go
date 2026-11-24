package watch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSW11StandingWatchesEndToEndWalkthrough(t *testing.T) {
	// Monday 2026-04-20 12:00 UTC = Monday 08:00 EDT (America/New_York).
	start := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	h := newStandingHarness(t, start)
	ctx := t.Context()

	// 1. Agent creates weekday 09:00 watch with standing draft; person chooses Edit and keep adding git_ref gate.
	rawOut, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{
			map[string]any{
				"kind": "timer",
				"calendar": map[string]any{
					"days":  []any{"weekdays"},
					"times": []any{"09:00"},
				},
				"time_zone": "America/New_York",
			},
		},
		"standing": map[string]any{
			"name":   "Main commit review",
			"prompt": "Review new commits on main and summarize risks.",
		},
	}, "call-sw11-draft-1")
	testutil.FailErr(t, "agent watch with standing draft", err)

	var draftReceipt struct {
		Handle        string `json:"handle"`
		StandingDraft bool   `json:"standing_draft"`
	}
	testutil.FailErr(t, "decode draft receipt", json.Unmarshal([]byte(rawOut), &draftReceipt))
	if !draftReceipt.StandingDraft || draftReceipt.Handle == "" {
		t.Fatalf("expected standing_draft receipt, got %s", rawOut)
	}

	gateRef := awaitstore.Condition{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}
	kept, keepRec, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", draftReceipt.Handle, watch.StandingWatchInput{
		Gate:     &gateRef,
		TimeZone: "America/New_York",
	}, 0)
	testutil.FailErr(t, "edit and keep standing watch", err)
	if keepRec.Outcome != watch.OutcomeKept || kept.State != watch.StateEnabled || kept.Gate == nil {
		t.Fatalf("unexpected kept standing watch: watch=%+v rec=%+v", kept, keepRec)
	}

	// Agent collects the kept terminal receipt on its session owner.
	batchKept, hasKept, err := watch.ReserveCollectBatch(ctx, h.store, watch.Owner{
		ProjectID: h.projectID,
		SessionID: "chat-1",
	}, "req:sw11:kept")
	testutil.FailErr(t, "reserve kept receipt", err)
	if !hasKept || len(batchKept.Matches) != 1 || batchKept.Matches[0].Outcome != string(watch.OutcomeKept) {
		t.Fatalf("expected kept receipt for agent collection, got %+v", batchKept)
	}
	testutil.FailErr(t, "commit kept receipt", watch.CommitCollectBatch(ctx, h.store, "chat-1", batchKept.DeliveryID))

	// 2. Cross Mon 09:00 EDT (13:00 UTC) with no new commits -> skipped_unchanged.
	h.clock.Set(time.Date(2026, 4, 20, 13, 1, 0, 0, time.UTC))
	testutil.FailErr(t, "tick unchanged Mon 09:00", h.store.AdvanceDue(ctx))
	runs1, _, err := h.engine.ListRuns(ctx, h.projectID, kept.WatchID, "", 10)
	testutil.FailErr(t, "list runs after unchanged day", err)
	if len(runs1) != 1 || runs1[0].Status != watch.RunSkippedUnchanged {
		t.Fatalf("expected skipped_unchanged run on unchanged main, got %+v", runs1)
	}

	// 3. Push a commit to refs/heads/main and cross Tue 09:00 EDT (2026-04-21 13:01 UTC) -> 1 run in new chat.
	testutil.FailErr(t, "advance main oid", os.WriteFile(
		filepath.Join(h.rootDir, ".git", "refs", "heads", "main"),
		[]byte("2222222222222222222222222222222222222222\n"),
		0o644,
	))
	h.clock.Set(time.Date(2026, 4, 21, 13, 1, 0, 0, time.UTC))
	testutil.FailErr(t, "tick Tue 09:00 after commit", h.store.AdvanceDue(ctx))
	testutil.FailErr(t, "dispatch Tue run", h.engine.DispatchProjectRuns(ctx, h.projectID))

	h.mu.Lock()
	if len(h.dispatched) != 1 {
		h.mu.Unlock()
		t.Fatalf("expected 1 dispatched run, got %d", len(h.dispatched))
	}
	firstRun := h.dispatched[0]
	h.mu.Unlock()
	if !strings.Contains(firstRun.Prompt, "watch_id="+kept.WatchID) {
		t.Fatalf("standing run prompt missing watch_id header: %s", firstRun.Prompt)
	}
	testutil.FailErr(t, "settle Tue run completed", h.engine.SettleRun(ctx, firstRun.SessionID, watch.RunCompleted))
	var seenNull int
	testutil.FailErr(t, "check unread mark", h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE id = ? AND seen_at IS NULL`, firstRun.SessionID).Scan(&seenNull))
	if seenNull != 1 {
		t.Fatalf("completed new_chat run was not marked unread")
	}

	// 4. Close host across 3 weekday 09:00 runs (Wed, Thu, Fri) -> 1 catch-up run with missed_count == 2.
	testutil.FailErr(t, "advance main oid for catch-up", os.WriteFile(
		filepath.Join(h.rootDir, ".git", "refs", "heads", "main"),
		[]byte("3333333333333333333333333333333333333333\n"),
		0o644,
	))
	h.clock.Set(time.Date(2026, 4, 24, 15, 0, 0, 0, time.UTC)) // Friday 11:00 EDT
	restartedStore := watch.NewStore(h.db, h.clock)
	restartedEngine := restartedStore.Standing()
	auth := watch.Authority{
		ProjectDir: h.rootDir,
		RootPaths:  []string{h.rootDir},
		Roots:      []watch.WatchRootGrant{{Label: "repo", RootID: h.rootID, Path: h.rootDir}},
	}
	restartedEngine.SetCallbacks(watch.StandingCallbacks{
		ModelPriceCheck:   func(_ context.Context, _, _ string) (bool, error) { return true, nil },
		PersonAccessCheck: func(_ context.Context, _, _ string) (bool, error) { return true, nil },
		ProjectDefaults: func(_ context.Context, _ string) (string, string, string, watch.Authority) {
			return "default", "openai:gpt-5", "America/New_York", auth
		},
		RunDispatcher: func(rCtx context.Context, sw watch.StandingWatch, run watch.WatchRun, prompt string) (watch.StandingRunDispatchOutcome, error) {
			sid := "catchup-chat-" + run.RunID[:8]
			testdbseed.InsertSession(t, h.db, sid, sw.ProjectID)
			_ = restartedEngine.RecordChatProvenance(rCtx, watch.StandingChatProvenance{
				SessionID: sid, ProjectID: sw.ProjectID, WatchID: sw.WatchID, WatchName: sw.Name, RunID: run.RunID,
			})
			h.mu.Lock()
			h.dispatched = append(h.dispatched, dispatchedRunRecord{WatchID: sw.WatchID, RunID: run.RunID, SessionID: sid, Prompt: prompt})
			h.mu.Unlock()
			return watch.StandingRunDispatchOutcome{SessionID: sid}, nil
		},
	})
	testutil.FailErr(t, "recover startup after 3 missed runs", restartedStore.Recover(ctx))
	testutil.FailErr(t, "dispatch catch-up run", restartedEngine.DispatchProjectRuns(ctx, h.projectID))

	runsCatchup, _, err := restartedEngine.ListRuns(ctx, h.projectID, kept.WatchID, "", 10)
	testutil.FailErr(t, "list runs after catch-up", err)
	if len(runsCatchup) == 0 || runsCatchup[0].MissedCount != 2 || runsCatchup[0].Status != watch.RunRunning {
		t.Fatalf("expected running catch-up run with missed_count=2, got %+v", runsCatchup)
	}
	catchupSessionID := runsCatchup[0].SessionID

	// 5. Hold catch-up run on an approval (waiting_for_owner) while Mon 09:00 arrives -> skipped_overlap.
	testutil.FailErr(t, "mark waiting_for_owner", restartedEngine.MarkWaitingForOwner(ctx, catchupSessionID))
	testutil.FailErr(t, "advance main oid during approval hold", os.WriteFile(
		filepath.Join(h.rootDir, ".git", "refs", "heads", "main"),
		[]byte("4444444444444444444444444444444444444444\n"),
		0o644,
	))
	h.clock.Set(time.Date(2026, 4, 27, 13, 1, 0, 0, time.UTC)) // Mon 09:01 EDT
	testutil.FailErr(t, "tick Mon 09:00 while waiting_for_owner", restartedStore.AdvanceDue(ctx))
	runsOverlap, _, err := restartedEngine.ListRuns(ctx, h.projectID, kept.WatchID, "", 10)
	testutil.FailErr(t, "list runs after overlap", err)
	if runsOverlap[0].Status != watch.RunSkippedOverlap {
		t.Fatalf("expected skipped_overlap while prior run is waiting_for_owner, got %+v", runsOverlap[0])
	}
	testutil.FailErr(t, "settle held run", restartedEngine.SettleRun(ctx, catchupSessionID, watch.RunCompleted))

	// 6. File-change standing watch whose own run edits the watched file does NOT retrigger itself.
	ledger := sourceledger.New(h.db, t.TempDir(), sourceledger.Timing{})
	fileSW, err := restartedEngine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Format watcher",
		Prompt:     "Format watched.txt",
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "repo", Path: "watched.txt"}},
		TimeZone:   "UTC",
		Authority:  auth,
	})
	testutil.FailErr(t, "create file_change standing watch", err)

	// External edit triggers run 1.
	testutil.FailErr(t, "external edit watched.txt", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v2-ext\n"), 0o644))
	testutil.FailErr(t, "record external ledger", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID:      h.projectID,
		RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       h.ownerID,
		Cause:          "user_save",
		CaptureQuality: "exact",
		Before:         []byte("v1\n"),
		After:          []byte("v2-ext\n"),
	}))
	testutil.FailErr(t, "notify external file change", restartedStore.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	testutil.FailErr(t, "dispatch file run 1", restartedEngine.DispatchProjectRuns(ctx, h.projectID))

	fileRuns1, _, err := restartedEngine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 10)
	testutil.FailErr(t, "list file runs", err)
	if len(fileRuns1) != 1 || fileRuns1[0].Status != watch.RunRunning {
		t.Fatalf("expected 1 running file_change run, got %+v", fileRuns1)
	}
	runSessionID := fileRuns1[0].SessionID

	// Run's own write-door edit + formatter command-window edit in runSessionID must NOT schedule a second run.
	h.clock.Advance(20 * time.Minute)
	testutil.FailErr(t, "own agent edit watched.txt", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v3-own-edit\n"), 0o644))
	testutil.FailErr(t, "record own agent edit", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID:      h.projectID,
		RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      runSessionID,
		Cause:          "agent_edit",
		CaptureQuality: "exact",
		Before:         []byte("v2-ext\n"),
		After:          []byte("v3-own-edit\n"),
	}))
	testutil.FailErr(t, "notify own agent edit", restartedStore.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))

	testutil.FailErr(t, "own formatter edit watched.txt", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v4-own-fmt\n"), 0o644))
	testutil.FailErr(t, "record own formatter edit", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID:      h.projectID,
		RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginExternal,
		SessionID:      runSessionID,
		Cause:          sourceledger.CauseCommandWindow,
		CaptureQuality: "observed",
		Before:         []byte("v3-own-edit\n"),
		After:          []byte("v4-own-fmt\n"),
	}))
	testutil.FailErr(t, "notify own formatter edit", restartedStore.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	testutil.FailErr(t, "settle file run 1", restartedEngine.SettleRun(ctx, runSessionID, watch.RunCompleted))

	fileRunsAfterOwn, _, err := restartedEngine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 10)
	testutil.FailErr(t, "list file runs after own edits", err)
	if len(fileRunsAfterOwn) != 1 {
		t.Fatalf("standing watch run retriggered itself on own edits: %+v", fileRunsAfterOwn)
	}

	// 7. Dismissing a draft sets outcome dismissed, refuses identical draft, and accepts a changed timer.
	rawDismissDraft, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 1800000}},
		"repeat":     true,
		"standing":   map[string]any{"name": "Dismiss me", "prompt": "Check logs"},
	}, "call-sw11-dismiss-1")
	testutil.FailErr(t, "register draft to dismiss", err)
	var dismissRec struct {
		Handle string `json:"handle"`
	}
	testutil.FailErr(t, "decode dismiss draft", json.Unmarshal([]byte(rawDismissDraft), &dismissRec))
	dismissedReceipt, err := h.engine.CancelByPerson(ctx, "chat-1", dismissRec.Handle)
	testutil.FailErr(t, "dismiss draft", err)
	if dismissedReceipt.Outcome != watch.OutcomeDismissed {
		t.Fatalf("expected dismissed outcome, got %+v", dismissedReceipt)
	}
	if _, dupErr := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 1800000}},
		"repeat":     true,
		"standing":   map[string]any{"name": "Reworded title", "prompt": "Reworded prompt"},
	}, "call-sw11-dismiss-dup"); dupErr == nil || !strings.Contains(dupErr.Error(), "WATCH_DRAFT_DISMISSED") {
		t.Fatalf("expected WATCH_DRAFT_DISMISSED, got %v", dupErr)
	}
	if _, diffErr := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 3600000}},
		"repeat":     true,
		"standing":   map[string]any{"name": "Hourly logs", "prompt": "Check logs"},
	}, "call-sw11-dismiss-diff"); diffErr != nil {
		t.Fatalf("expected different timer draft to succeed, got %v", diffErr)
	}

	// 8. Create up to 100 standing watches in one project without artificial cap refusal.
	existingList, err := restartedEngine.List(ctx, h.projectID, false)
	testutil.FailErr(t, "list existing standing watches", err)
	for i := len(existingList); i < 100; i++ {
		_, cErr := restartedEngine.Create(ctx, h.ownerID, watch.StandingWatchInput{
			ProjectID:  h.projectID,
			Name:       fmt.Sprintf("Scale watch %03d", i),
			Prompt:     "Verify health",
			Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
			TimeZone:   "UTC",
		})
		testutil.FailErr(t, "create standing watch to 100", cErr)
	}
	hundredList, err := restartedEngine.List(ctx, h.projectID, false)
	testutil.FailErr(t, "list 100 standing watches", err)
	if len(hundredList) < 100 {
		t.Fatalf("expected >= 100 standing watches, got %d", len(hundredList))
	}
}
