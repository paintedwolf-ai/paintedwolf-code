package watch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

type standingHarness struct {
	store     *watch.Store
	engine    *watch.StandingEngine
	db        db.Handle
	clock     *testutil.FakeClock
	toolsReg  *tools.DefaultRegistry
	rootDir   string
	rootID    string
	ownerID   string
	projectID string

	mu            sync.Mutex
	unpricedModel map[string]bool
	revokedOwner  map[string]bool
	targetBusy    map[string]bool
	targetMissing map[string]bool
	notices       []standingNoticeEvent
	dispatched    []dispatchedRunRecord
	onDispatch    func(sw watch.StandingWatch, run watch.WatchRun, prompt string) (watch.StandingRunDispatchOutcome, error)
}

type standingNoticeEvent struct {
	WatchID string
	Code    string
	Reason  string
	Clear   bool
}

type dispatchedRunRecord struct {
	WatchID   string
	RunID     string
	SessionID string
	Prompt    string
}

func newStandingHarness(t *testing.T, start time.Time) *standingHarness {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "standing-watches.db")
	projectID := testdbseed.DefaultProjectID
	testdbseed.InsertSession(t, sqlDB, "chat-1", projectID)
	ownerID := testdbseed.OwnerID(t, sqlDB)

	rootDir := t.TempDir()
	gitDir := filepath.Join(rootDir, ".git")
	testutil.FailErr(t, "mkdir refs/heads", os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755))
	testutil.FailErr(t, "write HEAD", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	testutil.FailErr(t, "write main oid", os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte("1111111111111111111111111111111111111111\n"), 0o644))
	testutil.FailErr(t, "write watched file", os.WriteFile(filepath.Join(rootDir, "watched.txt"), []byte("v1\n"), 0o644))

	rootID := testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, "root-repo", rootDir)
	clock := testutil.NewFakeClock(start)
	store := watch.NewStore(sqlDB, clock)

	h := &standingHarness{
		store:         store,
		engine:        store.Standing(),
		db:            sqlDB,
		clock:         clock,
		rootDir:       rootDir,
		rootID:        rootID,
		ownerID:       ownerID,
		projectID:     projectID,
		unpricedModel: map[string]bool{"custom:unpriced": true},
		revokedOwner:  map[string]bool{},
		targetBusy:    map[string]bool{},
		targetMissing: map[string]bool{},
	}

	h.engine.SetCallbacks(watch.StandingCallbacks{
		ModelPriceCheck: func(_ context.Context, _, modelID string) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return !h.unpricedModel[modelID], nil
		},
		PersonAccessCheck: func(_ context.Context, _, personID string) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return !h.revokedOwner[personID], nil
		},
		SessionTargetCheck: func(_ context.Context, pID, sessionID string) (bool, bool, error) {
			h.mu.Lock()
			missing, busy := h.targetMissing[sessionID], h.targetBusy[sessionID]
			h.mu.Unlock()
			if missing || pID != projectID {
				return false, false, nil
			}
			return true, busy, nil
		},
		ProjectDefaults: func(_ context.Context, _ string) (string, string, string, watch.Authority) {
			return "default", "openai:gpt-5", "America/New_York", watch.Authority{
				ProjectDir: rootDir,
				RootPaths:  []string{rootDir},
				Roots:      []watch.WatchRootGrant{{Label: "repo", RootID: rootID, Path: rootDir}},
			}
		},
		NoticeEmitter: func(_ context.Context, _, watchID, _, code, reason string, clear bool) {
			h.mu.Lock()
			h.notices = append(h.notices, standingNoticeEvent{WatchID: watchID, Code: code, Reason: reason, Clear: clear})
			h.mu.Unlock()
		},
		RunDispatcher: func(ctx context.Context, sw watch.StandingWatch, run watch.WatchRun, prompt string) (watch.StandingRunDispatchOutcome, error) {
			h.mu.Lock()
			custom := h.onDispatch
			h.mu.Unlock()
			if custom != nil {
				return custom(sw, run, prompt)
			}
			sid := run.SessionID
			if sw.Target.Kind == "new_chat" && sid == "" {
				sid = "run-chat-" + run.RunID[:8]
				testdbseed.InsertSession(t, sqlDB, sid, sw.ProjectID)
				_, _ = sqlDB.ExecContext(ctx, `UPDATE sessions SET seen_at = ? WHERE id = ?`, start.Format(time.RFC3339Nano), sid)
				_ = h.engine.RecordChatProvenance(ctx, watch.StandingChatProvenance{
					SessionID: sid, ProjectID: sw.ProjectID, WatchID: sw.WatchID, WatchName: sw.Name, RunID: run.RunID,
				})
			} else if sw.Target.Kind == "existing_chat" {
				sid = sw.Target.SessionID
			}
			h.mu.Lock()
			h.dispatched = append(h.dispatched, dispatchedRunRecord{WatchID: sw.WatchID, RunID: run.RunID, SessionID: sid, Prompt: prompt})
			h.mu.Unlock()
			return watch.StandingRunDispatchOutcome{SessionID: sid}, nil
		},
	})

	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load schemas", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	testutil.FailErr(t, "new registry", err)
	testutil.FailErr(t, "register watch tools", watch.RegisterWatchTools(reg, watch.ToolDeps{
		Store: store,
		ProfileConditions: map[string]map[string]bool{
			"coordinator": {
				"process_done": true, "process_output": true, "http_ready": true, "port_ready": true,
				"file_change": true, "git_ref": true, "timer": true,
			},
		},
		ProcessKnown:        func(_, s string) bool { return strings.HasPrefix(s, "cmd-") },
		ProjectChat:         func(_ context.Context, p string) bool { return p == projectID },
		StandingWatchExists: func(p, id string) bool { return h.engine.Exists(context.Background(), p, id) },
		OverlappingStanding: h.engine.Overlapping,
	}))
	h.toolsReg = reg
	return h
}

func (h *standingHarness) execTool(ctx context.Context, name string, args map[string]any, callID string) (string, error) {
	def, ok := h.toolsReg.Definition(name)
	if !ok {
		return "", fmt.Errorf("missing tool %s", name)
	}
	return def.Handler(ctx, args, h.toolCtx(callID))
}

func (h *standingHarness) toolCtx(callID string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{
			ProjectID: h.projectID, SessionID: "chat-1", ToolCallID: callID, Agent: "coordinator",
		},
		Source: tools.InvocationSource{
			ActiveRootID: "repo",
			Roots:        []projectroot.RootRef{{ID: h.rootID, Label: "repo", Path: h.rootDir, IsPrimary: true}},
		},
	}
}

func TestSW08ConditionsDSTHundredWatchesAndAgentIsolation(t *testing.T) {
	// Start before 2026 US spring-forward (2026-03-08 02:00 EST -> 03:00 EDT).
	ny, err := time.LoadLocation("America/New_York")
	testutil.FailErr(t, "load NY", err)
	start := time.Date(2026, 3, 8, 1, 0, 0, 0, ny).UTC()
	h := newStandingHarness(t, start)
	ctx := t.Context()

	// Verify every_minutes summary formatting across required intervals.
	wantSummaries := map[int64]string{
		15: "Every 15 minutes", 90: "Every 90 minutes", 120: "Every 2 hours",
		1440: "Every day", 2880: "Every 2 days", 10080: "Every week",
	}
	for mins, want := range wantSummaries {
		if got := watch.FormatEveryMinutesSummary(mins); got != want {
			t.Fatalf("FormatEveryMinutesSummary(%d) = %q, want %q", mins, got, want)
		}
	}

	// Spring-forward 02:30 resolves to 03:00 EDT; fall-back 01:30 fires once at -04:00.
	calWatch, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "DST check",
		Prompt:     "Check schedule.",
		TimeZone:   "America/New_York",
		Conditions: []awaitstore.Condition{{Kind: "timer", Calendar: &awaitstore.CalendarSpec{Days: []string{"sunday"}, Times: []string{"02:30"}}}},
	})
	testutil.FailErr(t, "create DST calendar watch", err)
	if len(calWatch.NextRuns) == 0 || !strings.HasPrefix(calWatch.NextRuns[0], "2026-03-08T03:00:00-04:00") {
		t.Fatalf("spring-forward skipped local hour should resolve to 03:00 EDT, got %v", calWatch.NextRuns)
	}

	fallStart := time.Date(2026, 11, 1, 0, 30, 0, 0, ny).UTC()
	fallPreview, err := watch.PreviewStandingSchedule([]awaitstore.Condition{{
		Kind: "timer", Calendar: &awaitstore.CalendarSpec{Days: []string{"sunday"}, Times: []string{"01:30"}},
	}}, "America/New_York", fallStart, fallStart, 2)
	testutil.FailErr(t, "preview fall-back", err)
	if len(fallPreview) < 2 || fallPreview[0].In(ny).Format(time.RFC3339) != "2026-11-01T01:30:00-04:00" ||
		fallPreview[1].In(ny).Day() == 1 {
		t.Fatalf("fall-back repeated local hour must fire once at first instance (-04:00), got %v", fallPreview)
	}

	// Refuse process condition on standing watch, unpriced model without allow_unpriced, and duplicate name.
	if _, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "Bad kind", Prompt: "run",
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-1"}}},
	}); err == nil {
		t.Fatal("expected WATCH_CONDITION_INVALID for process_done on standing watch")
	}
	var spendErr *watch.SpendUnenforceableError
	if _, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "Unpriced", Prompt: "run", ModelID: "custom:unpriced",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	}); !errors.As(err, &spendErr) {
		t.Fatalf("expected SpendUnenforceableError, got %v", err)
	}
	var nameErr *watch.NameTakenError
	if _, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "dst CHECK", Prompt: "run",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	}); !errors.As(err, &nameErr) {
		t.Fatalf("expected case-insensitive NameTakenError, got %v", err)
	}

	// Create 100 standing watches in one project without refusal across all condition kinds.
	atWatch, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "One-shot at", Prompt: "Run once.", TimeZone: "UTC",
		Conditions: []awaitstore.Condition{{Kind: "timer", At: start.Add(10 * time.Minute).UTC().Format("2006-01-02T15:04"), TimeZone: "UTC"}},
	})
	testutil.FailErr(t, "create at watch", err)
	_, err = h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "Git ref watch", Prompt: "Check main.",
		Conditions: []awaitstore.Condition{{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}},
	})
	testutil.FailErr(t, "create git_ref watch", err)
	_, err = h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "File watch", Prompt: "Check watched.txt.",
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "repo", Path: "watched.txt"}},
	})
	testutil.FailErr(t, "create file_change watch", err)
	allowUnpriced := true
	for i := 5; i <= 100; i++ {
		_, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
			ProjectID: h.projectID, Name: fmt.Sprintf("Standing #%03d", i), Prompt: "Periodic check.",
			ModelID: "custom:unpriced", AllowUnpriced: &allowUnpriced,
			Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
		})
		testutil.FailErr(t, "create standing watch to 100", err)
	}
	listed, err := h.engine.List(ctx, h.projectID, false)
	testutil.FailErr(t, "list 100 watches", err)
	if len(listed) != 100 {
		t.Fatalf("expected 100 standing watches, got %d", len(listed))
	}

	// Agent tools cannot inspect/cancel/edit standing watches and cannot create enabled standing watches.
	if _, cErr := h.execTool(ctx, "watch_control", map[string]any{"handle": calWatch.WatchID, "operation": "cancel"}, "c-1"); cErr == nil {
		t.Fatal("agent watch_control must not address person-owned standing watch")
	}

	// One-shot `at` watch completes after its run settles and prunes after 30 days.
	h.clock.Advance(10 * time.Minute)
	testutil.FailErr(t, "advance for at watch", h.store.AdvanceDue(ctx))
	runs, _, err := h.engine.ListRuns(ctx, h.projectID, atWatch.WatchID, "", 10)
	testutil.FailErr(t, "list at runs", err)
	if len(runs) != 1 || runs[0].Status != watch.RunRunning {
		t.Fatalf("expected running one-shot run, got %+v", runs)
	}
	testutil.FailErr(t, "settle at run", h.engine.SettleRun(ctx, runs[0].SessionID, watch.RunCompleted))
	var seenAt *string
	_ = h.db.QueryRowContext(ctx, `SELECT seen_at FROM sessions WHERE id = ?`, runs[0].SessionID).Scan(&seenAt)
	if seenAt != nil {
		t.Fatalf("completed new_chat run must mark session unread (seen_at IS NULL), got %v", *seenAt)
	}
	afterAt, err := h.engine.Get(ctx, h.projectID, atWatch.WatchID)
	testutil.FailErr(t, "get completed at watch", err)
	if afterAt.State != watch.StateCompleted {
		t.Fatalf("at-only standing watch state = %s, want completed", afterAt.State)
	}
	h.clock.Advance(31 * 24 * time.Hour)
	testutil.FailErr(t, "recover prunes 30d completed watch", h.store.Recover(ctx))
	if h.engine.Exists(ctx, h.projectID, atWatch.WatchID) {
		t.Fatal("expected completed standing watch older than 30 days to be pruned")
	}
}

func TestSW08DraftKeepTimerConversionReplacesDismissAndOneOff(t *testing.T) {
	start := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	h := newStandingHarness(t, start)
	ctx := t.Context()

	// Existing standing watch makes overlapping_standing populate on matching draft.
	existing, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Main branch monitor",
		Prompt:     "Check main.",
		Conditions: []awaitstore.Condition{{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}},
	})
	testutil.FailErr(t, "create existing watch", err)

	// One-off follow-up ("check CI in 20 minutes"): plain watch with delivery=wake and standing_draft omitted.
	oneOffRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 20 * 60 * 1000}},
		"delivery":   "wake",
	}, "one-off")
	testutil.FailErr(t, "register one-off watch", err)
	var oneOff map[string]any
	_ = json.Unmarshal([]byte(oneOffRaw), &oneOff)
	if oneOff["standing_draft"] != nil || oneOff["delivery"] != "wake" {
		t.Fatalf("one-off watch should have no standing_draft and delivery=wake: %+v", oneOff)
	}

	// Keep an agent watch WITHOUT a draft: repeating delay_ms=10m clamps to every_minutes=15.
	plainRepeatRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 10 * 60 * 1000}},
		"repeat":     true,
	}, "plain-repeat")
	testutil.FailErr(t, "register plain repeating timer", err)
	var plainRepeat map[string]any
	_ = json.Unmarshal([]byte(plainRepeatRaw), &plainRepeat)
	keptPlain, recPlain, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", plainRepeat["handle"].(string), watch.StandingWatchInput{
		Name:   "Kept without draft",
		Prompt: "Run periodic check.",
	}, 0)
	testutil.FailErr(t, "keep plain watch", err)
	if recPlain.Outcome != watch.OutcomeKept || recPlain.StandingWatchID != keptPlain.WatchID || keptPlain.Conditions[0].EveryMinutes != 15 {
		t.Fatalf("unexpected Keep conversion: watch=%+v receipt=%+v", keptPlain, recPlain)
	}

	// Refuse process condition on draft (WATCH_DRAFT_INVALID) and unknown replaces UUID.
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		"standing":   map[string]any{"name": "Bad", "prompt": "Bad"},
	}, "bad-draft"); err == nil || !strings.Contains(err.Error(), "WATCH_DRAFT_INVALID") {
		t.Fatalf("expected WATCH_DRAFT_INVALID for process condition, got %v", err)
	}
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "git_ref", "root": "repo", "ref": "refs/heads/main"}},
		"standing":   map[string]any{"name": "Bad rep", "prompt": "Bad", "replaces": "00000000-0000-4000-8000-000000000099"},
	}, "bad-rep"); err == nil || !strings.Contains(err.Error(), "WATCH_DRAFT_INVALID") {
		t.Fatalf("expected WATCH_DRAFT_INVALID for unknown replaces, got %v", err)
	}

	// Overlapping draft reports overlapping_standing, and Keep with replaces edits the target watch.
	repRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "git_ref", "root": "repo", "ref": "refs/heads/main"}},
		"standing": map[string]any{
			"name":     "Main branch monitor v2",
			"prompt":   "Summarize commits on main.",
			"replaces": existing.WatchID,
		},
	}, "draft-replaces")
	testutil.FailErr(t, "register replaces draft", err)
	var repReceipt watch.RegisterReceipt
	_ = json.Unmarshal([]byte(repRaw), &repReceipt)
	if !repReceipt.StandingDraft || len(repReceipt.OverlappingStanding) != 1 || repReceipt.OverlappingStanding[0].WatchID != existing.WatchID {
		t.Fatalf("expected overlapping_standing=[%s], got %+v", existing.WatchID, repReceipt)
	}

	// Stale base_revision on Keep with replaces fails with RevisionConflictError without consuming the draft.
	_, err = h.engine.Update(ctx, h.ownerID, h.projectID, existing.WatchID, existing.Revision, watch.StandingWatchPatch{
		Prompt: ptrStr("Intermediate edit"),
	})
	testutil.FailErr(t, "intermediate edit", err)
	var revConflict *watch.RevisionConflictError
	if _, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", repReceipt.Handle, watch.StandingWatchInput{}, 1); !errors.As(err, &revConflict) {
		t.Fatalf("expected RevisionConflictError on stale replaces Keep, got %v", err)
	}
	editedWatch, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", repReceipt.Handle, watch.StandingWatchInput{}, 2)
	testutil.FailErr(t, "keep replaces with current revision", err)
	if editedWatch.WatchID != existing.WatchID || editedWatch.Name != "Main branch monitor v2" || editedWatch.Revision != 3 {
		t.Fatalf("expected replaces Keep to update %s to rev 3, got %+v", existing.WatchID, editedWatch)
	}

	// Dismissed draft blocks identical conditions in the same chat while allowing a different timer.
	disRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"09:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "Daily standup", "prompt": "Summarize yesterday."},
	}, "draft-dismiss")
	testutil.FailErr(t, "register draft to dismiss", err)
	var disRec watch.RegisterReceipt
	_ = json.Unmarshal([]byte(disRaw), &disRec)
	cancelRec, err := h.engine.CancelByPerson(ctx, "chat-1", disRec.Handle)
	testutil.FailErr(t, "dismiss draft", err)
	if cancelRec.Outcome != watch.OutcomeDismissed {
		t.Fatalf("dismissed draft outcome = %s, want dismissed", cancelRec.Outcome)
	}
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"09:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "Renamed duplicate", "prompt": "Different wording"},
	}, "draft-dup"); err == nil || !strings.Contains(err.Error(), "WATCH_DRAFT_DISMISSED") {
		t.Fatalf("expected WATCH_DRAFT_DISMISSED on identical conditions, got %v", err)
	}
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"10:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "10am standup", "prompt": "Different timer"},
	}, "draft-diff-timer"); err != nil {
		t.Fatalf("draft with different timer must be accepted, got %v", err)
	}
}

func TestSW08ConcurrencyApprovalHoldAndOverlap(t *testing.T) {
	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	h := newStandingHarness(t, start)
	ctx := t.Context()

	var fiveIDs []string
	for i := 1; i <= 5; i++ {
		sw, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
			ProjectID:  h.projectID,
			Name:       fmt.Sprintf("Batch %d", i),
			Prompt:     fmt.Sprintf("Prompt %d", i),
			TimeZone:   "UTC",
			Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
		})
		testutil.FailErr(t, "create batch watch", err)
		fiveIDs = append(fiveIDs, sw.WatchID)
		h.clock.Advance(time.Millisecond)
	}
	h.clock.Advance(30 * time.Minute)
	testutil.FailErr(t, "fire 5 due watches", h.store.AdvanceDue(ctx))

	for i := 0; i < 4; i++ {
		runs, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[i], "", 5)
		if len(runs) != 1 || runs[0].Status != watch.RunRunning {
			t.Fatalf("watch %d expected running, got %+v", i+1, runs)
		}
	}
	runs5, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[4], "", 5)
	if len(runs5) != 1 || runs5[0].Status != watch.RunQueued {
		t.Fatalf("5th due watch expected queued, got %+v", runs5)
	}

	sw1, _ := h.engine.Get(ctx, h.projectID, fiveIDs[0])
	_, err := h.engine.Update(ctx, h.ownerID, h.projectID, sw1.WatchID, sw1.Revision, watch.StandingWatchPatch{
		Prompt: ptrStr("Updated Prompt 1"),
	})
	testutil.FailErr(t, "edit during active run", err)
	r1Before, _, _ := h.engine.ListRuns(ctx, h.projectID, sw1.WatchID, "", 5)

	testutil.FailErr(t, "mark run 1 waiting_for_owner", h.engine.MarkWaitingForOwner(ctx, r1Before[0].SessionID))
	runs5After, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[4], "", 5)
	if runs5After[0].Status != watch.RunRunning {
		t.Fatalf("expected 5th watch to dispatch when run 1 entered waiting_for_owner, got %+v", runs5After)
	}

	var activeErr *watch.RunActiveError
	if _, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, sw1.WatchID); !errors.As(err, &activeErr) {
		t.Fatalf("expected RunActiveError while waiting_for_owner, got %v", err)
	}
	h.clock.Advance(30 * time.Minute)
	testutil.FailErr(t, "advance for overlap", h.store.AdvanceDue(ctx))
	r1List, _, _ := h.engine.ListRuns(ctx, h.projectID, sw1.WatchID, "", 10)
	if r1List[0].Status != watch.RunSkippedOverlap {
		t.Fatalf("expected skipped_overlap while prior run is waiting_for_owner, got %+v", r1List)
	}
}

func TestSW08DailyCapFailuresRevocationAndBusyChat(t *testing.T) {
	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	h := newStandingHarness(t, start)
	ctx := t.Context()

	cap2 := 2
	swGov, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID: h.projectID, Name: "Governed watch", Prompt: "Run governed.",
		TimeZone: "UTC", DailyRunCap: &cap2,
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 15}},
	})
	testutil.FailErr(t, "create governed watch", err)

	rManual1, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID)
	testutil.FailErr(t, "run now 1", err)
	testutil.FailErr(t, "fence run 1", h.engine.SettleRun(ctx, rManual1.SessionID, watch.RunFenced))

	rManual2, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID)
	testutil.FailErr(t, "run now 2", err)
	testutil.FailErr(t, "fail run 2", h.engine.SettleRun(ctx, rManual2.SessionID, watch.RunFailed))

	var capErr *watch.DailyCapReachedError
	if _, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID); !errors.As(err, &capErr) {
		t.Fatalf("expected DailyCapReachedError after 2 runs, got %v", err)
	}
	h.clock.Advance(24 * time.Hour)
	testutil.FailErr(t, "advance across day for 3rd run", h.store.Recover(ctx))
	govRuns, _, _ := h.engine.ListRuns(ctx, h.projectID, swGov.WatchID, "", 10)
	for _, r := range govRuns {
		if r.Status == watch.RunRunning {
			testutil.FailErr(t, "fence 3rd run", h.engine.SettleRun(ctx, r.SessionID, watch.RunFenced))
		}
	}
	swGovPaused, _ := h.engine.Get(ctx, h.projectID, swGov.WatchID)
	if swGovPaused.State != watch.StatePaused || swGovPaused.StateReason != "consecutive_failures" {
		t.Fatalf("expected paused with consecutive_failures after 3 failures, got %+v", swGovPaused)
	}

	swGovResumed, err := h.engine.Resume(ctx, h.ownerID, h.projectID, swGovPaused.WatchID, swGovPaused.Revision)
	testutil.FailErr(t, "resume governed watch", err)
	h.mu.Lock()
	h.revokedOwner[h.ownerID] = true
	h.mu.Unlock()
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "advance after owner revocation", h.store.AdvanceDue(ctx))
	swDisabled, _ := h.engine.Get(ctx, h.projectID, swGovResumed.WatchID)
	if swDisabled.State != watch.StateDisabled || swDisabled.StateReason != "owner_access_revoked" {
		t.Fatalf("expected disabled with owner_access_revoked, got %+v", swDisabled)
	}
	var notOwnerErr *watch.NotOwnerError
	if _, err := h.engine.Update(ctx, "other-person", h.projectID, swDisabled.WatchID, swDisabled.Revision, watch.StandingWatchPatch{Prompt: ptrStr("nope")}); !errors.As(err, &notOwnerErr) {
		t.Fatalf("expected NotOwnerError without Enable takeover, got %v", err)
	}
	_, _ = h.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	_, err = h.db.ExecContext(ctx, `UPDATE watch_registrations SET owner_person_id = 'revoked-member-1' WHERE watch_id = ?`, swDisabled.WatchID)
	_, _ = h.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	testutil.FailErr(t, "seed revoked former owner id", err)
	h.mu.Lock()
	delete(h.revokedOwner, h.ownerID)
	h.mu.Unlock()

	swTakenOver, err := h.engine.Update(ctx, h.ownerID, h.projectID, swDisabled.WatchID, swDisabled.Revision, watch.StandingWatchPatch{Enable: true})
	testutil.FailErr(t, "take over disabled watch", err)
	if swTakenOver.State != watch.StateEnabled || swTakenOver.OwnerPersonID != h.ownerID {
		t.Fatalf("expected takeover to enable watch under %s, got %+v", h.ownerID, swTakenOver)
	}
	_, _ = h.engine.Pause(ctx, h.projectID, swTakenOver.WatchID, swTakenOver.Revision)

	swExisting, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Existing chat watch",
		Prompt:     "Nudge chat-1.",
		TimeZone:   "UTC",
		Target:     &watch.StandingTarget{Kind: "existing_chat", SessionID: "chat-1"},
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 15}},
	})
	testutil.FailErr(t, "create existing_chat watch", err)
	h.mu.Lock()
	h.targetBusy["chat-1"] = true
	h.mu.Unlock()
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "first tick while chat-1 busy", h.store.AdvanceDue(ctx))
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "second tick marks earlier pending run skipped_busy", h.store.AdvanceDue(ctx))
	exRuns, _, _ := h.engine.ListRuns(ctx, h.projectID, swExisting.WatchID, "", 10)
	foundSkippedBusy := false
	for _, r := range exRuns {
		if r.Status == watch.RunSkippedBusy {
			foundSkippedBusy = true
		}
	}
	if !foundSkippedBusy {
		t.Fatalf("expected skipped_busy on earlier pending existing_chat run, got %+v", exRuns)
	}
}

func TestSW08WalkthroughKeepGateCatchupDeleteAndOwnRunExclusion(t *testing.T) {
	walkStart := time.Date(2026, 11, 30, 8, 30, 0, 0, time.UTC)
	h := newStandingHarness(t, walkStart)
	ctx := t.Context()

	walkDraftRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"09:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "Weekday main review", "prompt": "Review new commits on main."},
	}, "walk-draft")
	testutil.FailErr(t, "create walkthrough draft", err)
	var walkDraft watch.RegisterReceipt
	_ = json.Unmarshal([]byte(walkDraftRaw), &walkDraft)

	gate := awaitstore.Condition{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}
	walkSW, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", walkDraft.Handle, watch.StandingWatchInput{
		Gate:     &gate,
		TimeZone: "UTC",
	}, 0)
	testutil.FailErr(t, "edit and keep with git_ref gate", err)

	// Agent collects the kept receipt!
	batchKept, hasKept, err := watch.ReserveCollectBatch(ctx, h.store, watch.SessionOwner("chat-1"), "req:walk:kept")
	testutil.FailErr(t, "collect kept receipt", err)
	if !hasKept || len(batchKept.Matches) != 1 || batchKept.Matches[0].Outcome != string(watch.OutcomeKept) {
		t.Fatalf("agent should collect kept receipt, got %+v", batchKept)
	}

	// Monday 09:00 UTC with no commits -> skipped_unchanged!
	h.clock.Set(time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "advance Monday 09:00", h.store.AdvanceDue(ctx))
	wRuns1, _, _ := h.engine.ListRuns(ctx, h.projectID, walkSW.WatchID, "", 5)
	if len(wRuns1) != 1 || wRuns1[0].Status != watch.RunSkippedUnchanged {
		t.Fatalf("expected skipped_unchanged with no commits, got %+v", wRuns1)
	}

	// Push commit -> Tuesday 09:00 UTC starts one new-chat run and advances gate baseline on completion.
	testutil.FailErr(t, "push commit 2", os.WriteFile(filepath.Join(h.rootDir, ".git", "refs", "heads", "main"), []byte("2222222222222222222222222222222222222222\n"), 0o644))
	h.clock.Set(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "advance Tuesday 09:00", h.store.AdvanceDue(ctx))
	wRuns2, _, _ := h.engine.ListRuns(ctx, h.projectID, walkSW.WatchID, "", 5)
	if wRuns2[0].Status != watch.RunRunning {
		t.Fatalf("expected running after commit, got %+v", wRuns2)
	}
	tueSession := wRuns2[0].SessionID
	testutil.FailErr(t, "complete Tuesday run", h.engine.SettleRun(ctx, tueSession, watch.RunCompleted))

	// Stop across Wed/Thu/Fri 09:00 runs -> restart Fri 10:00 dispatches one catch-up with missed_count=2.
	testutil.FailErr(t, "push commit 3", os.WriteFile(filepath.Join(h.rootDir, ".git", "refs", "heads", "main"), []byte("3333333333333333333333333333333333333333\n"), 0o644))
	h.clock.Set(time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "recover across 3 missed weekday runs", h.store.Recover(ctx))
	wRunsCatch, _, _ := h.engine.ListRuns(ctx, h.projectID, walkSW.WatchID, "", 10)
	if wRunsCatch[0].Status != watch.RunRunning || wRunsCatch[0].MissedCount != 2 {
		t.Fatalf("expected 1 catch-up run with MissedCount=2, got %+v", wRunsCatch)
	}
	missedRows := 0
	for _, r := range wRunsCatch {
		if r.Status == watch.RunMissed {
			missedRows++
		}
	}
	if missedRows != 2 {
		t.Fatalf("expected 2 missed run rows recorded, got %d: %+v", missedRows, wRunsCatch)
	}
	testutil.FailErr(t, "complete catch-up run", h.engine.SettleRun(ctx, wRunsCatch[0].SessionID, watch.RunCompleted))

	// Delete standing watch -> run chat survives and provenance reports watchExists=false.
	testutil.FailErr(t, "delete walkSW", h.engine.Delete(ctx, h.projectID, walkSW.WatchID))
	prov, foundProv, watchStillExists, err := h.engine.LookupChatProvenance(ctx, tueSession)
	testutil.FailErr(t, "lookup provenance after delete", err)
	if !foundProv || watchStillExists || prov.WatchName != "Weekday main review" {
		t.Fatalf("expected surviving chat provenance with watchExists=false, got found=%v exists=%v prov=%+v", foundProv, watchStillExists, prov)
	}

	// Own-run exclusion: write-door edits and command-window formatter drift do not retrigger.
	ledger := sourceledger.New(h.db, t.TempDir(), sourceledger.Timing{})
	testutil.FailErr(t, "seed ledger v1", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, PersonID: h.ownerID, Cause: "user_save", CaptureQuality: "exact", After: []byte("v1\n"),
	}))
	fileSW, err := h.engine.Create(ctx, h.ownerID, watch.StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Format watcher",
		Prompt:     "Format watched.txt.",
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "repo", Path: "watched.txt"}},
	})
	testutil.FailErr(t, "create file standing watch", err)

	// External user edit triggers Run 1:
	testutil.FailErr(t, "user write v2", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v2\n"), 0o644))
	testutil.FailErr(t, "ledger user v2", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, PersonID: h.ownerID, Cause: "user_save", CaptureQuality: "exact", Before: []byte("v1\n"), After: []byte("v2\n"),
	}))
	testutil.FailErr(t, "notify workspace user edit", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))

	fRuns1, _, _ := h.engine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 5)
	if len(fRuns1) != 1 || fRuns1[0].Status != watch.RunRunning {
		t.Fatalf("expected user edit to start fileSW run 1, got %+v", fRuns1)
	}
	runChatID := fRuns1[0].SessionID

	// Run 1 writes via write-door and formatter, then completes; advancing past min_gap does not retrigger.
	testutil.FailErr(t, "run write-door v3", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v3\n"), 0o644))
	testutil.FailErr(t, "ledger own agent edit", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: runChatID, Turn: 1, Cause: "agent_edit", CaptureQuality: "exact", Before: []byte("v2\n"), After: []byte("v3\n"),
	}))
	testutil.FailErr(t, "run formatter v4", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v4-formatted\n"), 0o644))
	testutil.FailErr(t, "ledger own formatter drift", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, SessionID: runChatID, Cause: sourceledger.CauseCommandWindow, CaptureQuality: "observed", Before: []byte("v3\n"), After: []byte("v4-formatted\n"),
	}))
	testutil.FailErr(t, "settle fileSW run 1", h.engine.SettleRun(ctx, runChatID, watch.RunCompleted))
	h.clock.Advance(20 * time.Minute)
	testutil.FailErr(t, "notify workspace after own-run edits", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	testutil.FailErr(t, "advance past min_gap_minutes", h.store.AdvanceDue(ctx))

	fRunsAfterOwn, _, _ := h.engine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 5)
	if len(fRunsAfterOwn) != 1 {
		t.Fatalf("standing watch retriggered on its own run's write-door or formatter edits: %+v", fRunsAfterOwn)
	}
}

func ptrStr(s string) *string { return &s }
