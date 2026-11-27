package watch

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
	"github.com/lycaon/lycaon/pkg/api"
)

type ww07NoticeEvent struct {
	WatchID string
	Code    string
	Reason  string
	Clear   bool
}

type ww07DispatchedRun struct {
	WatchID   string
	RunID     string
	SessionID string
	Prompt    string
}

type ww07StandingHarness struct {
	t         *testing.T
	store     *Store
	engine    *StandingEngine
	db        db.Handle
	clock     *ww02ManualClock
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
	notices       []ww07NoticeEvent
	dispatched    []ww07DispatchedRun
	onDispatch    func(sw StandingWatch, run WatchRun, prompt string) (StandingRunDispatchOutcome, error)
}

func newWW07StandingHarness(t *testing.T, start time.Time) *ww07StandingHarness {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ww07-standing.db")
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
	clock := newWW02ManualClock(start)

	h := &ww07StandingHarness{
		t:             t,
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
	h.attachStore(NewStore(sqlDB, clock))
	return h
}

func (h *ww07StandingHarness) attachStore(store *Store) {
	h.t.Helper()
	h.store = store
	h.engine = store.Standing()
	h.engine.SetCallbacks(StandingCallbacks{
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
			if missing || pID != h.projectID {
				return false, false, nil
			}
			return true, busy, nil
		},
		ProjectDefaults: func(_ context.Context, _ string) (string, string, string, Authority) {
			return "default", "openai:gpt-5", "America/New_York", Authority{
				ProjectDir: h.rootDir,
				RootPaths:  []string{h.rootDir},
				Roots:      []WatchRootGrant{{Label: "repo", RootID: h.rootID, Path: h.rootDir}},
			}
		},
		NoticeEmitter: func(_ context.Context, _, watchID, _, code, reason string, clear bool) {
			h.mu.Lock()
			h.notices = append(h.notices, ww07NoticeEvent{WatchID: watchID, Code: code, Reason: reason, Clear: clear})
			h.mu.Unlock()
		},
		RunDispatcher: func(ctx context.Context, sw StandingWatch, run WatchRun, prompt string) (StandingRunDispatchOutcome, error) {
			h.mu.Lock()
			custom := h.onDispatch
			h.mu.Unlock()
			if custom != nil {
				return custom(sw, run, prompt)
			}
			sid := run.SessionID
			if sw.Target.Kind == "new_chat" && sid == "" {
				sid = "run-chat-" + run.RunID[:8]
				testdbseed.InsertSession(h.t, h.db, sid, sw.ProjectID)
				_, _ = h.db.ExecContext(ctx, `UPDATE sessions SET seen_at = ? WHERE id = ?`, h.clock.Now().Format(time.RFC3339Nano), sid)
				_ = h.engine.RecordChatProvenance(ctx, StandingChatProvenance{
					SessionID: sid, ProjectID: sw.ProjectID, WatchID: sw.WatchID, WatchName: sw.Name, RunID: run.RunID,
				})
			} else if sw.Target.Kind == "existing_chat" {
				sid = sw.Target.SessionID
			}
			h.mu.Lock()
			h.dispatched = append(h.dispatched, ww07DispatchedRun{WatchID: sw.WatchID, RunID: run.RunID, SessionID: sid, Prompt: prompt})
			h.mu.Unlock()
			return StandingRunDispatchOutcome{SessionID: sid}, nil
		},
	})

	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(h.t, "load schemas", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	testutil.FailErr(h.t, "new registry", err)
	testutil.FailErr(h.t, "register watch tools", RegisterWatchTools(reg, ToolDeps{
		Store: store,
		ProfileConditions: map[string]map[string]bool{
			"coordinator": {
				"process_done": true, "process_output": true, "http_ready": true, "port_ready": true,
				"file_change": true, "git_ref": true, "timer": true,
			},
		},
		ProcessKnown:        func(_, s string) bool { return strings.HasPrefix(s, "cmd-") },
		ProjectChat:         func(_ context.Context, p string) bool { return p == h.projectID },
		StandingWatchExists: func(p, id string) bool { return h.engine.Exists(context.Background(), p, id) },
		OverlappingStanding: h.engine.Overlapping,
	}))
	h.toolsReg = reg
}

func (h *ww07StandingHarness) restartStore(ctx context.Context) {
	h.t.Helper()
	h.attachStore(NewStore(h.db, h.clock))
	testutil.FailErr(h.t, "recover restarted store", h.store.Recover(ctx))
}

func (h *ww07StandingHarness) execTool(ctx context.Context, name string, args map[string]any, callID string) (string, error) {
	def, ok := h.toolsReg.Definition(name)
	if !ok {
		return "", fmt.Errorf("missing tool %s", name)
	}
	return def.Handler(ctx, args, tools.ToolContext{
		Identity: tools.InvocationIdentity{
			ProjectID: h.projectID, SessionID: "chat-1", ToolCallID: callID, Agent: "coordinator",
		},
		Source: tools.InvocationSource{
			ActiveRootID: "repo",
			Roots:        []projectroot.RootRef{{ID: h.rootID, Label: "repo", Path: h.rootDir, IsPrimary: true}},
		},
	})
}

func ww07PtrStr(s string) *string { return &s }

func emitWW07Evidence(t *testing.T, caseID string, facts map[string]any) {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("WW07_EVIDENCE_DIR"))
	if dir == "" {
		dir = t.TempDir()
	}
	writeWW02Evidence(t, filepath.Join(dir, caseID+".json"), ww02EvidenceBundle{
		SchemaVersion: 1,
		RunID:         "ww07-inprocess-" + caseID,
		Lane:          "in-process",
		ClockDomain:   "manual",
		CaseID:        caseID,
		Expectations:  map[string]any{"expected_outcome": "pass"},
		Observers: map[string]any{
			"source":         map[string]any{"observer": "source", "clock_domain": "manual", "facts": facts},
			"storage":        map[string]any{"observer": "storage", "clock_domain": "manual", "available": true},
			"admission":      map[string]any{"observer": "admission", "clock_domain": "manual", "facts": facts},
			"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual", "presentations": []any{}},
			"den":            map[string]any{"observer": "den", "status": "not_run"},
		},
	})
}

// TestWW07C23DraftKeepDismissRefusals covers ledger slot C23-draft-keep-dismiss-refusals:
// proposals, dismissal suppression (WATCH_DRAFT_DISMISSED), changed draft acceptance,
// Keep with and without draft, edit-and-keep replacement with replaces conflict,
// draft expiry, stale revision edit race, and typed refusals (WATCH_DRAFT_INVALID,
// WATCH_CONDITION_INVALID, WATCH_NAME_TAKEN, WATCH_SPEND_UNENFORCEABLE).
func TestWW07C23DraftKeepDismissRefusals(t *testing.T) {
	start := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	h := newWW07StandingHarness(t, start)
	ctx := t.Context()

	// 1. Create and dismiss a proposal, suppress identical dismissed draft, accept changed draft.
	disRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"09:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "Morning standup", "prompt": "Summarize yesterday."},
	}, "ww07-c23-draft-dismiss")
	testutil.FailErr(t, "create proposal to dismiss", err)
	var disRec RegisterReceipt
	testutil.FailErr(t, "decode dismiss draft receipt", json.Unmarshal([]byte(disRaw), &disRec))
	if !disRec.StandingDraft {
		t.Fatalf("expected standing_draft=true, got %+v", disRec)
	}

	cancelRec, err := h.engine.CancelByPerson(ctx, "chat-1", disRec.Handle)
	testutil.FailErr(t, "dismiss draft", err)
	if cancelRec.Outcome != OutcomeDismissed {
		t.Fatalf("dismissed draft outcome = %s, want dismissed", cancelRec.Outcome)
	}

	// Identical condition in same chat must be refused with WATCH_DRAFT_DISMISSED.
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"09:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "Renamed duplicate", "prompt": "Different prompt wording"},
	}, "ww07-c23-draft-dup"); err == nil || !strings.Contains(err.Error(), "WATCH_DRAFT_DISMISSED") {
		t.Fatalf("expected WATCH_DRAFT_DISMISSED, got %v", err)
	}

	// Changed draft (different time) must be accepted and Keep with draft persists owner/trigger/prompt/revision.
	changedRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{
			"kind": "timer", "calendar": map[string]any{"days": []any{"weekdays"}, "times": []any{"10:00"}}, "time_zone": "UTC",
		}},
		"repeat":   true,
		"standing": map[string]any{"name": "10am standup", "prompt": "Summarize overnight PRs."},
	}, "ww07-c23-draft-changed")
	testutil.FailErr(t, "changed draft accepted", err)
	var changedRec RegisterReceipt
	testutil.FailErr(t, "decode changed draft", json.Unmarshal([]byte(changedRaw), &changedRec))

	keptWithDraft, recWithDraft, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", changedRec.Handle, StandingWatchInput{}, 0)
	testutil.FailErr(t, "keep with draft", err)
	if recWithDraft.Outcome != OutcomeKept || recWithDraft.StandingWatchID != keptWithDraft.WatchID {
		t.Fatalf("unexpected keep-with-draft receipt: %+v", recWithDraft)
	}
	if keptWithDraft.OwnerPersonID != h.ownerID || keptWithDraft.Name != "10am standup" || keptWithDraft.Prompt != "Summarize overnight PRs." || keptWithDraft.Revision != 1 {
		t.Fatalf("unexpected persisted standing watch: %+v", keptWithDraft)
	}
	if keptWithDraft.KeptFrom == nil || keptWithDraft.KeptFrom.SessionID != "chat-1" || keptWithDraft.KeptFrom.Handle != changedRec.Handle {
		t.Fatalf("expected kept_from provenance, got %+v", keptWithDraft.KeptFrom)
	}

	// Double Keep on already-settled draft must fail with KeepUnavailableError (one settled outcome across windows).
	var keepUnavail *KeepUnavailableError
	if _, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", changedRec.Handle, StandingWatchInput{}, 0); !errors.As(err, &keepUnavail) {
		t.Fatalf("expected KeepUnavailableError on second Keep, got %v", err)
	}

	// 2. Keep WITHOUT a draft: repeating delay_ms clamps to every_minutes=15; one-shot delay_ms converts to at.
	plainRepeatRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 10 * 60 * 1000}},
		"repeat":     true,
	}, "ww07-c23-plain-repeat")
	testutil.FailErr(t, "register plain repeat watch", err)
	var plainRepeat RegisterReceipt
	testutil.FailErr(t, "decode plain repeat", json.Unmarshal([]byte(plainRepeatRaw), &plainRepeat))

	keptNoDraft, recNoDraft, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", plainRepeat.Handle, StandingWatchInput{
		Name:   "Kept without draft",
		Prompt: "Run periodic status check.",
	}, 0)
	testutil.FailErr(t, "keep without draft", err)
	if recNoDraft.Outcome != OutcomeKept || keptNoDraft.Conditions[0].EveryMinutes != 15 || keptNoDraft.OwnerPersonID != h.ownerID {
		t.Fatalf("unexpected keep-without-draft: sw=%+v rec=%+v", keptNoDraft, recNoDraft)
	}

	// 3. Edit-and-keep replacement (`replaces`) with conflict detection & stale revision edit race.
	existing, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Main branch monitor",
		Prompt:     "Check main v1.",
		Conditions: []awaitstore.Condition{{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}},
	})
	testutil.FailErr(t, "create existing watch for replaces", err)

	repRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "git_ref", "root": "repo", "ref": "refs/heads/main"}},
		"standing": map[string]any{
			"name":     "Main branch monitor v2",
			"prompt":   "Check main v2.",
			"replaces": existing.WatchID,
		},
	}, "ww07-c23-replaces-draft")
	testutil.FailErr(t, "create replaces draft", err)
	var repRec RegisterReceipt
	testutil.FailErr(t, "decode replaces draft", json.Unmarshal([]byte(repRaw), &repRec))
	if !repRec.StandingDraft || len(repRec.OverlappingStanding) != 1 || repRec.OverlappingStanding[0].WatchID != existing.WatchID {
		t.Fatalf("expected overlapping_standing=[%s], got %+v", existing.WatchID, repRec)
	}

	// Concurrent edit bumps existing watch from rev 1 -> rev 2.
	updatedExisting, err := h.engine.Update(ctx, h.ownerID, h.projectID, existing.WatchID, existing.Revision, StandingWatchPatch{
		Prompt: ww07PtrStr("Intermediate edit"),
	})
	testutil.FailErr(t, "intermediate edit", err)
	if updatedExisting.Revision != 2 {
		t.Fatalf("expected revision 2, got %d", updatedExisting.Revision)
	}

	// Stale revision edit race on direct Update returns RevisionConflictError with CurrentRevision=2.
	var revConflict *RevisionConflictError
	if _, err := h.engine.Update(ctx, h.ownerID, h.projectID, existing.WatchID, 1, StandingWatchPatch{
		Prompt: ww07PtrStr("Stale edit"),
	}); !errors.As(err, &revConflict) || revConflict.CurrentRevision != 2 {
		t.Fatalf("expected RevisionConflictError(current=2), got %v", err)
	}

	// Keep with stale base_revision fails with RevisionConflictError (Review changes offer) without consuming draft.
	revConflict = nil
	if _, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", repRec.Handle, StandingWatchInput{}, 1); !errors.As(err, &revConflict) || revConflict.CurrentRevision != 2 {
		t.Fatalf("expected RevisionConflictError on stale replaces Keep, got %v", err)
	}

	// Retrying Keep with current revision=2 succeeds, persists new prompt & bumps revision to 3.
	replaced, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", repRec.Handle, StandingWatchInput{}, 2)
	testutil.FailErr(t, "replaces keep with current revision", err)
	if replaced.WatchID != existing.WatchID || replaced.Prompt != "Check main v2." || replaced.Revision != 3 {
		t.Fatalf("unexpected replaced watch: %+v", replaced)
	}

	// 4. Expire a standing draft via manual clock advance -> KeepUnavailableError.
	expRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "file_change", "root": "repo", "path": "watched.txt"}},
		"expires_at": h.clock.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
		"standing":   map[string]any{"name": "Expiring draft", "prompt": "Watch file."},
	}, "ww07-c23-expiring-draft")
	testutil.FailErr(t, "create expiring draft", err)
	var expRec RegisterReceipt
	testutil.FailErr(t, "decode expiring draft", json.Unmarshal([]byte(expRaw), &expRec))

	h.clock.Advance(3 * time.Minute)
	testutil.FailErr(t, "advance past draft expiry", h.store.AdvanceDue(ctx))
	keepUnavail = nil
	if _, _, err := h.engine.KeepAgentWatch(ctx, h.ownerID, "chat-1", expRec.Handle, StandingWatchInput{}, 0); !errors.As(err, &keepUnavail) {
		t.Fatalf("expected KeepUnavailableError on expired draft, got %v", err)
	}

	// 5. Typed refusals: process draft, unknown replaces, person-owned process, duplicate name, unpriced model.
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		"standing":   map[string]any{"name": "Bad process draft", "prompt": "nope"},
	}, "ww07-c23-bad-proc-draft"); err == nil || !strings.Contains(err.Error(), "WATCH_DRAFT_INVALID") {
		t.Fatalf("expected WATCH_DRAFT_INVALID for process draft, got %v", err)
	}
	if _, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "git_ref", "root": "repo", "ref": "refs/heads/main"}},
		"standing":   map[string]any{"name": "Bad replaces", "prompt": "nope", "replaces": "00000000-0000-4000-8000-000000000099"},
	}, "ww07-c23-bad-replaces"); err == nil || !strings.Contains(err.Error(), "WATCH_DRAFT_INVALID") {
		t.Fatalf("expected WATCH_DRAFT_INVALID for unknown replaces, got %v", err)
	}
	var valErr *ValidationError
	if _, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Person process watch",
		Prompt:     "nope",
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-1"}}},
	}); !errors.As(err, &valErr) || valErr.Code != "WATCH_CONDITION_INVALID" {
		t.Fatalf("expected WATCH_CONDITION_INVALID for person-owned process condition, got %v", err)
	}
	var nameErr *NameTakenError
	if _, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "10AM STANDUP",
		Prompt:     "dup",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	}); !errors.As(err, &nameErr) {
		t.Fatalf("expected NameTakenError for case-insensitive duplicate, got %v", err)
	}
	var spendErr *SpendUnenforceableError
	if _, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Unpriced model watch",
		Prompt:     "run",
		ModelID:    "custom:unpriced",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	}); !errors.As(err, &spendErr) {
		t.Fatalf("expected SpendUnenforceableError for unpriced model without allow_unpriced, got %v", err)
	}

	// Verify persisted state via Get.
	persisted, err := h.engine.Get(ctx, h.projectID, replaced.WatchID)
	testutil.FailErr(t, "get replaced watch", err)
	if persisted.OwnerPersonID != h.ownerID || persisted.Prompt != "Check main v2." || persisted.Revision != 3 || len(persisted.Conditions) != 1 || persisted.Conditions[0].Kind != "git_ref" {
		t.Fatalf("persisted standing watch mismatch: %+v", persisted)
	}

	emitWW07Evidence(t, "C23-draft-keep-dismiss-refusals", map[string]any{
		"dismissed_code":     "WATCH_DRAFT_DISMISSED",
		"invalid_draft_code": "WATCH_DRAFT_INVALID",
		"replaces_revision":  persisted.Revision,
		"owner_person_id":    persisted.OwnerPersonID,
	})
}

// TestWW07C24EveryMinutesWording covers ledger slot C24-every-minutes-wording across all 6 required intervals.
func TestWW07C24EveryMinutesWording(t *testing.T) {
	cases := []struct {
		minutes int64
		want    string
	}{
		{15, "Every 15 minutes"},
		{90, "Every 90 minutes"},
		{120, "Every 2 hours"},
		{1440, "Every day"},
		{2880, "Every 2 days"},
		{10080, "Every week"},
	}
	for _, tc := range cases {
		if got := FormatEveryMinutesSummary(tc.minutes); got != tc.want {
			t.Fatalf("FormatEveryMinutesSummary(%d) = %q, want %q", tc.minutes, got, tc.want)
		}
	}
	emitWW07Evidence(t, "C24-every-minutes-wording", map[string]any{"verified_intervals": 6})
}

// TestWW07C24CalendarDSTCatchup covers ledger slot C24-calendar-dst-catchup:
// independent virtual calendar (ww02ManualClock) across at, every_minutes, calendar,
// file_change, git_ref, weekday gate, both DST transitions (spring-forward skip hour
// and fall-back repeat hour), and outage across multiple eligible ticks.
func TestWW07C24CalendarDSTCatchup(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	testutil.FailErr(t, "load America/New_York", err)

	// Phase 1: Spring-forward DST (2026-03-08 02:00 EST -> 03:00 EDT).
	springStart := time.Date(2026, 3, 8, 1, 0, 0, 0, ny).UTC()
	h := newWW07StandingHarness(t, springStart)
	ctx := t.Context()

	springWatch, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Spring DST 02:30",
		Prompt:     "Run at spring-forward transition.",
		TimeZone:   "America/New_York",
		Conditions: []awaitstore.Condition{{Kind: "timer", Calendar: &awaitstore.CalendarSpec{Days: []string{"sunday"}, Times: []string{"02:30"}}}},
	})
	testutil.FailErr(t, "create spring DST watch", err)

	wantSpringInstant := time.Date(2026, 3, 8, 3, 0, 0, 0, ny).UTC()
	if len(springWatch.NextRuns) == 0 || springWatch.NextRuns[0] != wantSpringInstant.In(ny).Format(time.RFC3339) {
		t.Fatalf("spring-forward 02:30 next_runs = %v, want %s", springWatch.NextRuns, wantSpringInstant.In(ny).Format(time.RFC3339))
	}
	h.clock.Set(wantSpringInstant)
	testutil.FailErr(t, "fire spring-forward tick", h.store.AdvanceDue(ctx))
	springRuns, _, err := h.engine.ListRuns(ctx, h.projectID, springWatch.WatchID, "", 5)
	testutil.FailErr(t, "list spring runs", err)
	if len(springRuns) != 1 || springRuns[0].Status != RunRunning || !springRuns[0].ScheduledFor.Equal(wantSpringInstant) {
		t.Fatalf("expected 1 running spring-forward run at %s, got %+v", wantSpringInstant, springRuns)
	}
	testutil.FailErr(t, "settle spring run", h.engine.SettleRun(ctx, springRuns[0].SessionID, RunCompleted))

	// Phase 2: Fall-back DST (2026-11-01 02:00 EDT -> 01:00 EST): 01:30 must fire ONCE at -04:00, not twice.
	fallStart := time.Date(2026, 11, 1, 0, 30, 0, 0, ny).UTC()
	hFall := newWW07StandingHarness(t, fallStart)
	fallWatch, err := hFall.engine.Create(ctx, hFall.ownerID, StandingWatchInput{
		ProjectID:  hFall.projectID,
		Name:       "Fall DST 01:30",
		Prompt:     "Run once at fall-back transition.",
		TimeZone:   "America/New_York",
		Conditions: []awaitstore.Condition{{Kind: "timer", Calendar: &awaitstore.CalendarSpec{Days: []string{"sunday"}, Times: []string{"01:30"}}}},
	})
	testutil.FailErr(t, "create fall DST watch", err)

	// First 01:30 is EDT (-04:00) = 05:30 UTC; repeated 01:30 is EST (-05:00) = 06:30 UTC.
	first0130UTC := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	second0130UTC := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	if len(fallWatch.NextRuns) < 2 || fallWatch.NextRuns[0] != "2026-11-01T01:30:00-04:00" || strings.HasPrefix(fallWatch.NextRuns[1], "2026-11-01") {
		t.Fatalf("fall-back next_runs must fire once on 2026-11-01 at -04:00, got %v", fallWatch.NextRuns)
	}
	hFall.clock.Set(first0130UTC)
	testutil.FailErr(t, "fire first 01:30 EDT", hFall.store.AdvanceDue(ctx))
	fallRuns1, _, _ := hFall.engine.ListRuns(ctx, hFall.projectID, fallWatch.WatchID, "", 5)
	if len(fallRuns1) != 1 || fallRuns1[0].Status != RunRunning {
		t.Fatalf("expected 1 run at first 01:30 EDT, got %+v", fallRuns1)
	}
	testutil.FailErr(t, "complete first 01:30 run", hFall.engine.SettleRun(ctx, fallRuns1[0].SessionID, RunCompleted))

	hFall.clock.Set(second0130UTC)
	testutil.FailErr(t, "advance across repeated 01:30 EST", hFall.store.AdvanceDue(ctx))
	fallRuns2, _, _ := hFall.engine.ListRuns(ctx, hFall.projectID, fallWatch.WatchID, "", 5)
	if len(fallRuns2) != 1 {
		t.Fatalf("fall-back repeated 01:30 EST must NOT fire a second time, got %d runs: %+v", len(fallRuns2), fallRuns2)
	}

	// Phase 3: at, every_minutes, file_change, git_ref, weekday gate, and multi-tick outage catch-up.
	monStart := time.Date(2026, 11, 30, 8, 30, 0, 0, time.UTC) // Monday
	hCal := newWW07StandingHarness(t, monStart)

	// 3a. One-shot `at` watch.
	atDue := monStart.Add(10 * time.Minute)
	atSW, err := hCal.engine.Create(ctx, hCal.ownerID, StandingWatchInput{
		ProjectID:  hCal.projectID,
		Name:       "One-shot at",
		Prompt:     "Run once at exact instant.",
		TimeZone:   "UTC",
		Conditions: []awaitstore.Condition{{Kind: "timer", At: atDue.Format("2006-01-02T15:04"), TimeZone: "UTC"}},
	})
	testutil.FailErr(t, "create at watch", err)

	// 3b. `every_minutes` watch (15m).
	everySW, err := hCal.engine.Create(ctx, hCal.ownerID, StandingWatchInput{
		ProjectID:  hCal.projectID,
		Name:       "Every 15m",
		Prompt:     "Periodic check.",
		TimeZone:   "UTC",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 15}},
	})
	testutil.FailErr(t, "create every_minutes watch", err)

	// 3c. `calendar` (weekdays 09:00 UTC) with `git_ref` gate.
	gate := awaitstore.Condition{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}
	weekdaySW, err := hCal.engine.Create(ctx, hCal.ownerID, StandingWatchInput{
		ProjectID:  hCal.projectID,
		Name:       "Weekday gated review",
		Prompt:     "Review commits on main.",
		TimeZone:   "UTC",
		Gate:       &gate,
		Conditions: []awaitstore.Condition{{Kind: "timer", Calendar: &awaitstore.CalendarSpec{Days: []string{"weekdays"}, Times: []string{"09:00"}}, TimeZone: "UTC"}},
	})
	testutil.FailErr(t, "create weekday gated watch", err)

	// 3d. `file_change` watch and `git_ref` watch.
	fileSW, err := hCal.engine.Create(ctx, hCal.ownerID, StandingWatchInput{
		ProjectID:  hCal.projectID,
		Name:       "File change trigger",
		Prompt:     "Check watched.txt.",
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "repo", Path: "watched.txt"}},
	})
	testutil.FailErr(t, "create file_change watch", err)

	gitSW, err := hCal.engine.Create(ctx, hCal.ownerID, StandingWatchInput{
		ProjectID:  hCal.projectID,
		Name:       "Git ref trigger",
		Prompt:     "Check refs/heads/main.",
		Conditions: []awaitstore.Condition{{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}},
	})
	testutil.FailErr(t, "create git_ref watch", err)

	// Advance to 08:40 (at due) and 08:45 (every_minutes due).
	hCal.clock.Set(atDue)
	testutil.FailErr(t, "advance to at due", hCal.store.AdvanceDue(ctx))
	atRuns, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, atSW.WatchID, "", 5)
	if len(atRuns) != 1 || atRuns[0].Status != RunRunning {
		t.Fatalf("expected running at watch, got %+v", atRuns)
	}
	testutil.FailErr(t, "complete at run", hCal.engine.SettleRun(ctx, atRuns[0].SessionID, RunCompleted))
	atAfter, _ := hCal.engine.Get(ctx, hCal.projectID, atSW.WatchID)
	if atAfter.State != StateCompleted {
		t.Fatalf("at-only watch should transition to completed, got %s", atAfter.State)
	}

	hCal.clock.Set(monStart.Add(15 * time.Minute))
	testutil.FailErr(t, "advance to every_minutes due", hCal.store.AdvanceDue(ctx))
	evRuns, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, everySW.WatchID, "", 5)
	if len(evRuns) != 1 || evRuns[0].Status != RunRunning {
		t.Fatalf("expected every_minutes run at +15m, got %+v", evRuns)
	}
	testutil.FailErr(t, "complete every_minutes run", hCal.engine.SettleRun(ctx, evRuns[0].SessionID, RunCompleted))
	// Pause everySW so it doesn't crowd concurrency during the multi-day outage below.
	_, err = hCal.engine.Pause(ctx, hCal.projectID, everySW.WatchID, everySW.Revision)
	testutil.FailErr(t, "pause every_minutes after check", err)

	// External file edit triggers fileSW.
	ledger := sourceledger.New(hCal.db, t.TempDir(), sourceledger.Timing{})
	testutil.FailErr(t, "write file v2", os.WriteFile(filepath.Join(hCal.rootDir, "watched.txt"), []byte("v2\n"), 0o644))
	testutil.FailErr(t, "record file v2", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: hCal.projectID, RecordLocation: sourceledger.RecordLocation{RootID: hCal.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, PersonID: hCal.ownerID, Cause: "user_save", CaptureQuality: "exact", Before: []byte("v1\n"), After: []byte("v2\n"),
	}))
	testutil.FailErr(t, "notify file change", hCal.store.NotifyWorkspaceChange(ctx, hCal.projectID, hCal.rootDir))
	fRuns, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, fileSW.WatchID, "", 5)
	if len(fRuns) != 1 || fRuns[0].Status != RunRunning {
		t.Fatalf("expected file_change run, got %+v", fRuns)
	}
	testutil.FailErr(t, "complete file_change run", hCal.engine.SettleRun(ctx, fRuns[0].SessionID, RunCompleted))

	// Monday 09:00 UTC with no commits -> weekdaySW skipped_unchanged.
	hCal.clock.Set(time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "advance Monday 09:00", hCal.store.AdvanceDue(ctx))
	wRunsMon, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, weekdaySW.WatchID, "", 5)
	if len(wRunsMon) != 1 || wRunsMon[0].Status != RunSkippedUnchanged {
		t.Fatalf("expected skipped_unchanged on Monday, got %+v", wRunsMon)
	}

	// Push commit 2 -> triggers gitSW immediately AND unblocks weekdaySW on Tuesday 09:00.
	testutil.FailErr(t, "push commit 2", os.WriteFile(filepath.Join(hCal.rootDir, ".git", "refs", "heads", "main"), []byte("2222222222222222222222222222222222222222\n"), 0o644))
	testutil.FailErr(t, "notify git ref change", hCal.store.NotifyWorkspaceChange(ctx, hCal.projectID, hCal.rootDir))
	gRuns, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, gitSW.WatchID, "", 5)
	if len(gRuns) != 1 || gRuns[0].Status != RunRunning {
		t.Fatalf("expected git_ref run, got %+v", gRuns)
	}
	testutil.FailErr(t, "complete git_ref run", hCal.engine.SettleRun(ctx, gRuns[0].SessionID, RunCompleted))

	hCal.clock.Set(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "advance Tuesday 09:00", hCal.store.AdvanceDue(ctx))
	wRunsTue, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, weekdaySW.WatchID, "", 5)
	if len(wRunsTue) < 2 || wRunsTue[0].Status != RunRunning {
		t.Fatalf("expected Tuesday running run, got %+v", wRunsTue)
	}
	tueRun := wRunsTue[0]
	prov, found, exists, err := hCal.engine.LookupChatProvenance(ctx, tueRun.SessionID)
	testutil.FailErr(t, "lookup Tuesday chat provenance", err)
	if !found || !exists || prov.WatchID != weekdaySW.WatchID || prov.RunID != tueRun.RunID || prov.WatchName != "Weekday gated review" {
		t.Fatalf("unexpected run chat provenance: found=%v exists=%v prov=%+v", found, exists, prov)
	}
	testutil.FailErr(t, "complete Tuesday run", hCal.engine.SettleRun(ctx, tueRun.SessionID, RunCompleted))

	// Outage across Wed Dec 2, Thu Dec 3, Fri Dec 4 09:00 -> recover at Fri Dec 4 10:00 -> 1 catch-up run (MissedCount=2) + 2 RunMissed rows.
	testutil.FailErr(t, "push commit 3", os.WriteFile(filepath.Join(hCal.rootDir, ".git", "refs", "heads", "main"), []byte("3333333333333333333333333333333333333333\n"), 0o644))
	hCal.clock.Set(time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC))
	hCal.restartStore(ctx)
	wRunsCatch, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, weekdaySW.WatchID, "", 10)
	if len(wRunsCatch) == 0 || wRunsCatch[0].Status != RunRunning || wRunsCatch[0].MissedCount != 2 {
		t.Fatalf("expected 1 catch-up run with MissedCount=2, got %+v", wRunsCatch)
	}
	missedCount := 0
	for _, r := range wRunsCatch {
		if r.Status == RunMissed {
			missedCount++
		}
	}
	if missedCount != 2 {
		t.Fatalf("expected 2 RunMissed rows, got %d (%+v)", missedCount, wRunsCatch)
	}
	testutil.FailErr(t, "complete catch-up run", hCal.engine.SettleRun(ctx, wRunsCatch[0].SessionID, RunCompleted))

	// Saturday Dec 5 & Sunday Dec 6 09:00 -> weekday gate must produce zero new runs.
	beforeWeekend := len(wRunsCatch)
	hCal.clock.Set(time.Date(2026, 12, 6, 12, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "advance across weekend", hCal.store.AdvanceDue(ctx))
	wRunsWeekend, _, _ := hCal.engine.ListRuns(ctx, hCal.projectID, weekdaySW.WatchID, "", 20)
	if len(wRunsWeekend) != beforeWeekend {
		t.Fatalf("weekday calendar watch must not fire on Sat/Sun: before=%d after=%d", beforeWeekend, len(wRunsWeekend))
	}

	emitWW07Evidence(t, "C24-calendar-dst-catchup", map[string]any{
		"spring_forward_instant": wantSpringInstant.Format(time.RFC3339),
		"fall_back_runs":         len(fallRuns2),
		"catchup_missed_count":   wRunsCatch[0].MissedCount,
		"missed_rows":            missedCount,
	})
}

// TestWW07C25DispatchConcurrencyOverlap covers ledger slot C25-dispatch-concurrency-overlap:
// 100 standing watches in 1 project (no refusal), 5 due -> 4 active + 1 queued -> release 1 -> 5th dispatches,
// timer overlap skip, coalesced change run, existing-chat busy (skipped_busy) & hourly budget,
// and idempotent dispatch across store restart.
func TestWW07C25DispatchConcurrencyOverlap(t *testing.T) {
	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	h := newWW07StandingHarness(t, start)
	ctx := t.Context()

	// 1. 100 standing watches in one project without refusal (first 5 due in 30m, rest in 120m).
	var fiveIDs []string
	for i := 1; i <= 5; i++ {
		sw, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
			ProjectID:  h.projectID,
			Name:       fmt.Sprintf("Concurrent #%02d", i),
			Prompt:     fmt.Sprintf("Prompt #%02d", i),
			TimeZone:   "UTC",
			Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
		})
		testutil.FailErr(t, "create due watch", err)
		fiveIDs = append(fiveIDs, sw.WatchID)
		h.clock.Advance(time.Millisecond)
	}
	allowUnpriced := true
	for i := 6; i <= 100; i++ {
		_, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
			ProjectID:     h.projectID,
			Name:          fmt.Sprintf("Standing #%03d", i),
			Prompt:        "Background standing check.",
			TimeZone:      "UTC",
			ModelID:       "custom:unpriced",
			AllowUnpriced: &allowUnpriced,
			Conditions:    []awaitstore.Condition{{Kind: "timer", EveryMinutes: 1440}},
		})
		testutil.FailErr(t, "create watch to 100", err)
	}
	listed, err := h.engine.List(ctx, h.projectID, false)
	testutil.FailErr(t, "list 100 watches", err)
	if len(listed) != 100 {
		t.Fatalf("expected 100 standing watches, got %d", len(listed))
	}

	// 2. Make 5 due -> 4 running + 1 queued -> release 1 -> 5th dispatches.
	h.clock.Advance(30 * time.Minute)
	testutil.FailErr(t, "advance 5 due watches", h.store.AdvanceDue(ctx))
	for i := 0; i < 4; i++ {
		runs, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[i], "", 5)
		if len(runs) != 1 || runs[0].Status != RunRunning {
			t.Fatalf("watch %d expected running, got %+v", i+1, runs)
		}
	}
	runs5, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[4], "", 5)
	if len(runs5) != 1 || runs5[0].Status != RunQueued {
		t.Fatalf("5th due watch expected queued, got %+v", runs5)
	}

	// Idempotent dispatch across store restart while 4 are active and 1 is queued.
	h.restartStore(ctx)
	for i := 0; i < 4; i++ {
		runs, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[i], "", 5)
		if len(runs) != 1 || runs[0].Status != RunRunning {
			t.Fatalf("after restart watch %d should still have exactly 1 running run, got %+v", i+1, runs)
		}
	}
	runs5Restart, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[4], "", 5)
	if len(runs5Restart) != 1 || runs5Restart[0].Status != RunQueued {
		t.Fatalf("after restart 5th watch should remain queued without duplicate, got %+v", runs5Restart)
	}

	// Release watch 1 -> 5th queued watch immediately dispatches.
	runs1, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[0], "", 5)
	testutil.FailErr(t, "complete watch 1", h.engine.SettleRun(ctx, runs1[0].SessionID, RunCompleted))
	runs5After, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[4], "", 5)
	if len(runs5After) != 1 || runs5After[0].Status != RunRunning {
		t.Fatalf("expected 5th watch to dispatch on release, got %+v", runs5After)
	}

	// 3. Timer overlap skip while watch 2 is still running.
	h.clock.Advance(30 * time.Minute)
	testutil.FailErr(t, "advance for timer overlap", h.store.AdvanceDue(ctx))
	runs2, _, _ := h.engine.ListRuns(ctx, h.projectID, fiveIDs[1], "", 5)
	if len(runs2) < 2 || runs2[0].Status != RunSkippedOverlap {
		t.Fatalf("expected skipped_overlap for still-running watch 2, got %+v", runs2)
	}

	// Complete remaining active runs to free all 4 project slots.
	for _, wid := range fiveIDs {
		runs, _, _ := h.engine.ListRuns(ctx, h.projectID, wid, "", 5)
		for _, r := range runs {
			if r.Status == RunRunning {
				testutil.FailErr(t, "settle active batch run", h.engine.SettleRun(ctx, r.SessionID, RunCompleted))
			}
		}
		sw, _ := h.engine.Get(ctx, h.projectID, wid)
		_, _ = h.engine.Pause(ctx, h.projectID, wid, sw.Revision)
	}

	// 4. Coalesced change run within min_gap_minutes=15.
	minGap := 15
	gitCoalesce, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:     h.projectID,
		Name:          "Coalesced git watch",
		Prompt:        "Check coalesced commits.",
		MinGapMinutes: &minGap,
		Conditions:    []awaitstore.Condition{{Kind: "git_ref", Root: "repo", Ref: "refs/heads/main"}},
	})
	testutil.FailErr(t, "create coalesced git watch", err)

	// First ref update dispatches immediately.
	testutil.FailErr(t, "commit A", os.WriteFile(filepath.Join(h.rootDir, ".git", "refs", "heads", "main"), []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o644))
	testutil.FailErr(t, "notify commit A", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	cRuns1, _, _ := h.engine.ListRuns(ctx, h.projectID, gitCoalesce.WatchID, "", 10)
	if len(cRuns1) != 1 || cRuns1[0].Status != RunRunning {
		t.Fatalf("expected commit A to start run 1, got %+v", cRuns1)
	}
	testutil.FailErr(t, "complete commit A run", h.engine.SettleRun(ctx, cRuns1[0].SessionID, RunCompleted))

	// Two rapid updates within min_gap_minutes coalesce into 1 pending change, then flush as 1 run after 15m.
	h.clock.Advance(3 * time.Minute)
	testutil.FailErr(t, "commit B", os.WriteFile(filepath.Join(h.rootDir, ".git", "refs", "heads", "main"), []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"), 0o644))
	testutil.FailErr(t, "notify commit B", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	h.clock.Advance(3 * time.Minute)
	testutil.FailErr(t, "commit C", os.WriteFile(filepath.Join(h.rootDir, ".git", "refs", "heads", "main"), []byte("cccccccccccccccccccccccccccccccccccccccc\n"), 0o644))
	testutil.FailErr(t, "notify commit C", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))

	cRunsMid, _, _ := h.engine.ListRuns(ctx, h.projectID, gitCoalesce.WatchID, "", 10)
	if len(cRunsMid) != 1 {
		t.Fatalf("expected commits B & C to coalesce during min_gap, got %d runs", len(cRunsMid))
	}
	h.clock.Advance(10 * time.Minute) // total 16m since commit A
	testutil.FailErr(t, "flush coalesced change after min_gap", h.store.AdvanceDue(ctx))
	cRunsAfter, _, _ := h.engine.ListRuns(ctx, h.projectID, gitCoalesce.WatchID, "", 10)
	if len(cRunsAfter) != 2 || cRunsAfter[0].Status != RunRunning {
		t.Fatalf("expected exactly 1 coalesced run after min_gap (2 total), got %+v", cRunsAfter)
	}
	testutil.FailErr(t, "complete coalesced run", h.engine.SettleRun(ctx, cRunsAfter[0].SessionID, RunCompleted))

	// 5. Existing-chat busy (skipped_busy) and existing-chat hourly budget enforcement.
	swExisting, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Existing chat watch",
		Prompt:     "Nudge chat-1.",
		TimeZone:   "UTC",
		Target:     &StandingTarget{Kind: "existing_chat", SessionID: "chat-1"},
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 15}},
	})
	testutil.FailErr(t, "create existing_chat watch", err)

	h.mu.Lock()
	h.targetBusy["chat-1"] = true
	h.mu.Unlock()
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "first tick while chat-1 busy", h.store.AdvanceDue(ctx))
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "second tick marks earlier pending skipped_busy", h.store.AdvanceDue(ctx))
	exRuns, _, _ := h.engine.ListRuns(ctx, h.projectID, swExisting.WatchID, "", 10)
	foundSkippedBusy := false
	for _, r := range exRuns {
		if r.Status == RunSkippedBusy {
			foundSkippedBusy = true
		}
	}
	if !foundSkippedBusy {
		t.Fatalf("expected skipped_busy on earlier pending existing_chat run, got %+v", exRuns)
	}

	// Exhaust existing_chat hourly budget (DefaultHourlyWakeBudget) -> next admission defers until +1h.
	h.mu.Lock()
	h.targetBusy["chat-1"] = false
	h.mu.Unlock()
	// Clear any pending run on swExisting by pausing and resuming.
	swExCur, _ := h.engine.Get(ctx, h.projectID, swExisting.WatchID)
	swExPaused, _ := h.engine.Pause(ctx, h.projectID, swExCur.WatchID, swExCur.Revision)
	swExResumed, _ := h.engine.Resume(ctx, h.ownerID, h.projectID, swExPaused.WatchID, swExPaused.Revision)
	_ = swExResumed

	capHigh := 96
	swBudget, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:   h.projectID,
		Name:        "Existing chat budget watch",
		Prompt:      "Budget check.",
		TimeZone:    "UTC",
		DailyRunCap: &capHigh,
		Target:      &StandingTarget{Kind: "existing_chat", SessionID: "chat-1"},
		Conditions:  []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
	})
	testutil.FailErr(t, "create existing_chat budget watch", err)

	// Pre-fill watch_session_state wake_admissions_json with DefaultHourlyWakeBudget recent timestamps.
	now := h.clock.Now()
	recent := make([]string, DefaultHourlyWakeBudget)
	for i := range recent {
		recent[i] = now.Add(-time.Duration(10+i) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	rawRecent, _ := json.Marshal(recent)
	_, err = h.db.ExecContext(ctx,
		`INSERT INTO watch_session_state (session_id, wake_admissions_json) VALUES ('chat-1', ?)
		 ON CONFLICT(session_id) DO UPDATE SET wake_admissions_json = excluded.wake_admissions_json`,
		string(rawRecent))
	testutil.FailErr(t, "seed hourly wake budget", err)

	deferredRun, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swBudget.WatchID)
	testutil.FailErr(t, "run now when hourly budget exhausted", err)
	if deferredRun.Status != RunPending {
		t.Fatalf("expected existing_chat run to stay pending when hourly budget exhausted, got %+v", deferredRun)
	}

	// Advance 61 minutes so the oldest wake admission expires -> deferred run dispatches.
	h.clock.Advance(61 * time.Minute)
	testutil.FailErr(t, "advance past hourly budget window", h.store.AdvanceDue(ctx))
	afterBudget, _, _ := h.engine.ListRuns(ctx, h.projectID, swBudget.WatchID, "", 5)
	foundDispatched := false
	for _, r := range afterBudget {
		if r.RunID == deferredRun.RunID && r.Status == RunRunning {
			foundDispatched = true
		}
	}
	if !foundDispatched {
		t.Fatalf("expected deferred existing_chat run %s to dispatch after hourly budget window, got %+v", deferredRun.RunID, afterBudget)
	}

	emitWW07Evidence(t, "C25-dispatch-concurrency-overlap", map[string]any{
		"registered_watches":   len(listed),
		"concurrency_cap":      MaxConcurrentProjectRuns,
		"coalesced_total_runs": len(cRunsAfter),
		"skipped_busy":         foundSkippedBusy,
		"hourly_budget_enforced": foundDispatched,
	})
}

// TestWW07C26SpendOwnerLifecycle covers ledger slot C26-spend-owner-lifecycle:
// unpriced-model policy, per-run spend fence, daily cap, 3 consecutive failures auto-pause
// (standing_watch_paused), owner revocation (standing_watch_disabled) & takeover,
// pause with queued work + resume no catch-up, edit during run (immutable in-flight prompt),
// delete during run (chat intact), and Run now.
func TestWW07C26SpendOwnerLifecycle(t *testing.T) {
	start := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	h := newWW07StandingHarness(t, start)
	ctx := t.Context()

	// 1. Unpriced model refused without allow_unpriced, allowed with allow_unpriced=true.
	var spendErr *SpendUnenforceableError
	if _, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Unpriced refused",
		Prompt:     "check",
		ModelID:    "custom:unpriced",
		Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 30}},
	}); !errors.As(err, &spendErr) {
		t.Fatalf("expected SpendUnenforceableError, got %v", err)
	}
	allowTrue := true
	unpricedSW, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:     h.projectID,
		Name:          "Unpriced allowed",
		Prompt:        "check",
		ModelID:       "custom:unpriced",
		AllowUnpriced: &allowTrue,
		Conditions:    []awaitstore.Condition{{Kind: "timer", EveryMinutes: 60}},
	})
	testutil.FailErr(t, "create allow_unpriced watch", err)
	unpricedRun, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, unpricedSW.WatchID)
	testutil.FailErr(t, "run unpriced watch", err)
	if limit, ok := h.engine.ActiveRunSpendLimit(ctx, unpricedRun.SessionID); ok || limit != 0 {
		t.Fatalf("allow_unpriced run should have no enforced spend limit, got limit=%v ok=%v", limit, ok)
	}
	testutil.FailErr(t, "settle unpriced run", h.engine.SettleRun(ctx, unpricedRun.SessionID, RunCompleted))
	_, _ = h.engine.Pause(ctx, h.projectID, unpricedSW.WatchID, unpricedSW.Revision)

	// 2. Priced watch with spend fence (1.50 USD), daily cap=2, 3 consecutive failures auto-pause.
	limit150 := 1.50
	cap2 := 2
	swGov, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:        h.projectID,
		Name:             "Governed watch",
		Prompt:           "Original prompt v1.",
		TimeZone:         "UTC",
		RunSpendLimitUSD: &limit150,
		DailyRunCap:      &cap2,
		Conditions:       []awaitstore.Condition{{Kind: "timer", EveryMinutes: 15}},
	})
	testutil.FailErr(t, "create governed watch", err)

	r1, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID)
	testutil.FailErr(t, "run now 1", err)
	if limit, ok := h.engine.ActiveRunSpendLimit(ctx, r1.SessionID); !ok || limit != 1.50 {
		t.Fatalf("expected ActiveRunSpendLimit=(1.50, true), got (%v, %v)", limit, ok)
	}
	// RunNow while active must return RunActiveError.
	var activeErr *RunActiveError
	if _, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID); !errors.As(err, &activeErr) {
		t.Fatalf("expected RunActiveError, got %v", err)
	}
	testutil.FailErr(t, "settle run 1 fenced", h.engine.SettleRun(ctx, r1.SessionID, RunFenced))

	r2, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID)
	testutil.FailErr(t, "run now 2", err)
	testutil.FailErr(t, "settle run 2 failed", h.engine.SettleRun(ctx, r2.SessionID, RunFailed))

	// Daily cap reached -> RunNow returns DailyCapReachedError, scheduled tick records skipped_daily_cap.
	var capErr *DailyCapReachedError
	if _, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swGov.WatchID); !errors.As(err, &capErr) {
		t.Fatalf("expected DailyCapReachedError, got %v", err)
	}
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "scheduled tick under daily cap", h.store.AdvanceDue(ctx))
	runsCap, _, _ := h.engine.ListRuns(ctx, h.projectID, swGov.WatchID, "", 10)
	if runsCap[0].Status != RunSkippedDailyCap {
		t.Fatalf("expected skipped_daily_cap on scheduled tick, got %+v", runsCap)
	}

	// Next day -> 3rd failure auto-pauses with consecutive_failures and emits standing_watch_paused notice.
	h.clock.Advance(24 * time.Hour)
	testutil.FailErr(t, "recover next day for 3rd failure", h.store.Recover(ctx))
	runsNextDay, _, _ := h.engine.ListRuns(ctx, h.projectID, swGov.WatchID, "", 10)
	var r3 WatchRun
	for _, r := range runsNextDay {
		if r.Status == RunRunning {
			r3 = r
			break
		}
	}
	if r3.RunID == "" {
		t.Fatalf("expected running 3rd run next day, got %+v", runsNextDay)
	}
	testutil.FailErr(t, "settle 3rd run fenced", h.engine.SettleRun(ctx, r3.SessionID, RunFenced))

	swPaused, err := h.engine.Get(ctx, h.projectID, swGov.WatchID)
	testutil.FailErr(t, "get auto-paused watch", err)
	if swPaused.State != StatePaused || swPaused.StateReason != "consecutive_failures" {
		t.Fatalf("expected paused(consecutive_failures), got %+v", swPaused)
	}
	foundPausedNotice := false
	h.mu.Lock()
	for _, n := range h.notices {
		if n.WatchID == swGov.WatchID && n.Code == NoticeStandingWatchPaused && !n.Clear {
			foundPausedNotice = true
		}
	}
	h.mu.Unlock()
	if !foundPausedNotice {
		t.Fatalf("expected %s notice emitted on 3 consecutive failures", NoticeStandingWatchPaused)
	}

	// 3. Resume after outage while paused -> ZERO catch-up runs.
	runsBeforeResume, _, _ := h.engine.ListRuns(ctx, h.projectID, swGov.WatchID, "", 20)
	h.clock.Advance(3 * time.Hour) // 12 missed 15m ticks while paused
	swResumed, err := h.engine.Resume(ctx, h.ownerID, h.projectID, swPaused.WatchID, swPaused.Revision)
	testutil.FailErr(t, "resume paused watch", err)
	testutil.FailErr(t, "advance immediately after resume", h.store.AdvanceDue(ctx))
	runsAfterResume, _, _ := h.engine.ListRuns(ctx, h.projectID, swGov.WatchID, "", 20)
	if len(runsAfterResume) != len(runsBeforeResume) {
		t.Fatalf("resume must not catch up missed ticks while paused: before=%d after=%d", len(runsBeforeResume), len(runsAfterResume))
	}

	// 4. Owner revocation -> disabled(owner_access_revoked) + standing_watch_disabled notice + takeover.
	h.mu.Lock()
	h.revokedOwner[h.ownerID] = true
	h.mu.Unlock()
	h.clock.Advance(15 * time.Minute)
	testutil.FailErr(t, "advance after owner revocation", h.store.AdvanceDue(ctx))
	swDisabled, _ := h.engine.Get(ctx, h.projectID, swResumed.WatchID)
	if swDisabled.State != StateDisabled || swDisabled.StateReason != "owner_access_revoked" {
		t.Fatalf("expected disabled(owner_access_revoked), got %+v", swDisabled)
	}
	foundDisabledNotice := false
	h.mu.Lock()
	for _, n := range h.notices {
		if n.WatchID == swGov.WatchID && n.Code == NoticeStandingWatchDisabled && !n.Clear {
			foundDisabledNotice = true
		}
	}
	h.mu.Unlock()
	if !foundDisabledNotice {
		t.Fatalf("expected %s notice emitted on owner revocation", NoticeStandingWatchDisabled)
	}

	var notOwnerErr *NotOwnerError
	if _, err := h.engine.Update(ctx, "other-member", h.projectID, swDisabled.WatchID, swDisabled.Revision, StandingWatchPatch{
		Prompt: ww07PtrStr("unauthorized edit"),
	}); !errors.As(err, &notOwnerErr) {
		t.Fatalf("expected NotOwnerError without Enable takeover, got %v", err)
	}
	_, _ = h.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	_, err = h.db.ExecContext(ctx, `UPDATE watch_registrations SET owner_person_id = 'revoked-former-owner' WHERE watch_id = ?`, swDisabled.WatchID)
	_, _ = h.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	testutil.FailErr(t, "seed revoked former owner", err)
	h.mu.Lock()
	delete(h.revokedOwner, h.ownerID)
	h.mu.Unlock()

	swTakenOver, err := h.engine.Update(ctx, h.ownerID, h.projectID, swDisabled.WatchID, swDisabled.Revision, StandingWatchPatch{Enable: true})
	testutil.FailErr(t, "takeover disabled watch", err)
	if swTakenOver.State != StateEnabled || swTakenOver.OwnerPersonID != h.ownerID {
		t.Fatalf("expected takeover to re-enable under %s, got %+v", h.ownerID, swTakenOver)
	}

	// 5. Pause with queued work cancels queued runs.
	h.clock.Advance(24 * time.Hour) // fresh day for daily cap
	var blockers []WatchRun
	for i := 1; i <= 4; i++ {
		bw, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
			ProjectID:  h.projectID,
			Name:       fmt.Sprintf("Blocker %d", i),
			Prompt:     "Hold slot.",
			TimeZone:   "UTC",
			Conditions: []awaitstore.Condition{{Kind: "timer", EveryMinutes: 1440}},
		})
		testutil.FailErr(t, "create blocker", err)
		br, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, bw.WatchID)
		testutil.FailErr(t, "start blocker", err)
		blockers = append(blockers, br)
	}
	queuedRun, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swTakenOver.WatchID)
	testutil.FailErr(t, "enqueue 5th run", err)
	if queuedRun.Status != RunQueued {
		t.Fatalf("expected queued run while 4 blockers active, got %+v", queuedRun)
	}
	swPausedQueued, err := h.engine.Pause(ctx, h.projectID, swTakenOver.WatchID, swTakenOver.Revision)
	testutil.FailErr(t, "pause with queued run", err)
	qAfterPause, err := getWatchRunByID(ctx, h.store, queuedRun.RunID)
	testutil.FailErr(t, "get canceled queued run", err)
	if qAfterPause.Status != RunCanceled {
		t.Fatalf("pause must cancel queued run, got %s", qAfterPause.Status)
	}
	for _, br := range blockers {
		testutil.FailErr(t, "settle blocker", h.engine.SettleRun(ctx, br.SessionID, RunCompleted))
	}

	// 6. Edit during active run preserves immutable in-flight prompt; Delete during active run retains chat.
	swActive, err := h.engine.Resume(ctx, h.ownerID, h.projectID, swPausedQueued.WatchID, swPausedQueued.Revision)
	testutil.FailErr(t, "resume for edit/delete during run", err)
	inFlightRun, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, swActive.WatchID)
	testutil.FailErr(t, "start in-flight run", err)
	if inFlightRun.PromptSnapshot != "Original prompt v1." {
		t.Fatalf("expected in-flight PromptSnapshot='Original prompt v1.', got %q", inFlightRun.PromptSnapshot)
	}

	swEdited, err := h.engine.Update(ctx, h.ownerID, h.projectID, swActive.WatchID, swActive.Revision, StandingWatchPatch{
		Prompt: ww07PtrStr("Updated prompt v2."),
	})
	testutil.FailErr(t, "edit during active run", err)
	inFlightReloaded, err := getWatchRunByID(ctx, h.store, inFlightRun.RunID)
	testutil.FailErr(t, "reload in-flight run", err)
	if inFlightReloaded.PromptSnapshot != "Original prompt v1." {
		t.Fatalf("in-flight prompt must remain immutable after edit, got %q", inFlightReloaded.PromptSnapshot)
	}
	if !strings.Contains(BuildStandingRunPrompt(swEdited, inFlightReloaded), "Original prompt v1.") {
		t.Fatalf("BuildStandingRunPrompt must render immutable snapshot")
	}

	// Delete while run is active -> watch removed, run chat intact, provenance reports watchExists=false.
	testutil.FailErr(t, "delete during active run", h.engine.Delete(ctx, h.projectID, swEdited.WatchID))
	if h.engine.Exists(ctx, h.projectID, swEdited.WatchID) {
		t.Fatal("deleted standing watch should no longer exist")
	}
	var sessCount int
	testutil.FailErr(t, "check surviving run chat", h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = ?`, inFlightRun.SessionID).Scan(&sessCount))
	if sessCount != 1 {
		t.Fatalf("run chat %s must survive standing watch deletion", inFlightRun.SessionID)
	}
	prov, found, exists, err := h.engine.LookupChatProvenance(ctx, inFlightRun.SessionID)
	testutil.FailErr(t, "lookup provenance after delete", err)
	if !found || exists || prov.WatchName != "Governed watch" {
		t.Fatalf("expected surviving provenance with watchExists=false, got found=%v exists=%v prov=%+v", found, exists, prov)
	}

	emitWW07Evidence(t, "C26-spend-owner-lifecycle", map[string]any{
		"paused_notice":           foundPausedNotice,
		"disabled_notice":         foundDisabledNotice,
		"in_flight_prompt":        inFlightReloaded.PromptSnapshot,
		"surviving_chat_session":  inFlightRun.SessionID,
		"provenance_watch_exists": exists,
	})
}

// TestWW07C27OwnWritesUnattendedRuns covers ledger slot C27-own-writes-unattended-runs:
// run edits watched file directly and via formatter -> zero self-trigger;
// external edit -> 1 eligible run; approvals wait for owner (waiting_for_owner);
// agent cannot create, enable, edit, pause, resume, run, or delete a standing watch.
func TestWW07C27OwnWritesUnattendedRuns(t *testing.T) {
	start := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	h := newWW07StandingHarness(t, start)
	ctx := t.Context()

	ledger := sourceledger.New(h.db, t.TempDir(), sourceledger.Timing{})
	testutil.FailErr(t, "seed ledger v1", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginUser, PersonID: h.ownerID, Cause: "user_save", CaptureQuality: "exact", After: []byte("v1\n"),
	}))

	fileSW, err := h.engine.Create(ctx, h.ownerID, StandingWatchInput{
		ProjectID:  h.projectID,
		Name:       "Auto-format watched.txt",
		Prompt:     "Format watched.txt and summarize changes.",
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "repo", Path: "watched.txt"}},
	})
	testutil.FailErr(t, "create file standing watch", err)

	// 1. External user edit triggers Run 1.
	testutil.FailErr(t, "external write v2", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v2\n"), 0o644))
	testutil.FailErr(t, "ledger external v2", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, PersonID: h.ownerID, Cause: "user_save", CaptureQuality: "exact", Before: []byte("v1\n"), After: []byte("v2\n"),
	}))
	testutil.FailErr(t, "notify external edit", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))

	runs1, _, _ := h.engine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 10)
	if len(runs1) != 1 || runs1[0].Status != RunRunning {
		t.Fatalf("expected external edit to trigger Run 1, got %+v", runs1)
	}
	runChat1 := runs1[0].SessionID

	// 2. Approvals wait for owner: MarkWaitingForOwner blocks RunNow until owner resumes.
	testutil.FailErr(t, "mark waiting for owner", h.engine.MarkWaitingForOwner(ctx, runChat1))
	runsWaiting, _, _ := h.engine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 10)
	if runsWaiting[0].Status != RunWaitingForOwner {
		t.Fatalf("expected waiting_for_owner, got %+v", runsWaiting)
	}
	var activeErr *RunActiveError
	if _, err := h.engine.RunNow(ctx, h.ownerID, h.projectID, fileSW.WatchID); !errors.As(err, &activeErr) {
		t.Fatalf("expected RunActiveError while waiting_for_owner, got %v", err)
	}
	testutil.FailErr(t, "owner approves and resumes run", h.engine.MarkResumed(ctx, runChat1))

	// 3. Run 1 writes watched.txt directly (agent write-door) AND via formatter (command window), then completes.
	testutil.FailErr(t, "direct agent write v3", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v3\n"), 0o644))
	testutil.FailErr(t, "ledger own direct write", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: runChat1, Turn: 1, Cause: "agent_edit", CaptureQuality: "exact", Before: []byte("v2\n"), After: []byte("v3\n"),
	}))
	testutil.FailErr(t, "formatter write v4", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v4-fmt\n"), 0o644))
	testutil.FailErr(t, "ledger own formatter write", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginExternal, SessionID: runChat1, Cause: sourceledger.CauseCommandWindow, CaptureQuality: "observed", Before: []byte("v3\n"), After: []byte("v4-fmt\n"),
	}))
	testutil.FailErr(t, "settle run 1", h.engine.SettleRun(ctx, runChat1, RunCompleted))

	h.clock.Advance(20 * time.Minute)
	testutil.FailErr(t, "notify after own writes", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	testutil.FailErr(t, "advance past min_gap", h.store.AdvanceDue(ctx))
	runsAfterOwn, _, _ := h.engine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 10)
	if len(runsAfterOwn) != 1 {
		t.Fatalf("standing watch must NOT self-trigger on direct or formatter own writes, got %+v", runsAfterOwn)
	}

	// 4. Subsequent external edit triggers exactly 1 new eligible run (2 total).
	testutil.FailErr(t, "external write v5", os.WriteFile(filepath.Join(h.rootDir, "watched.txt"), []byte("v5-external\n"), 0o644))
	testutil.FailErr(t, "ledger external v5", ledger.Record(ctx, sourceledger.RecordInput{
		ProjectID: h.projectID, RecordLocation: sourceledger.RecordLocation{RootID: h.rootID, Path: "watched.txt"},
		Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginUser, PersonID: h.ownerID, Cause: "user_save", CaptureQuality: "exact", Before: []byte("v4-fmt\n"), After: []byte("v5-external\n"),
	}))
	testutil.FailErr(t, "notify external v5", h.store.NotifyWorkspaceChange(ctx, h.projectID, h.rootDir))
	runsAfterExt, _, _ := h.engine.ListRuns(ctx, h.projectID, fileSW.WatchID, "", 10)
	if len(runsAfterExt) != 2 || runsAfterExt[0].Status != RunRunning {
		t.Fatalf("expected external edit to trigger exactly 1 new run (2 total), got %+v", runsAfterExt)
	}
	testutil.FailErr(t, "settle run 2", h.engine.SettleRun(ctx, runsAfterExt[0].SessionID, RunCompleted))

	// 5. Agent isolation: agent tools cannot create enabled standing watches or inspect/cancel/pause/resume/run/delete them.
	beforePersonWatches, _ := h.engine.List(ctx, h.projectID, true)
	draftRaw, err := h.execTool(ctx, "watch", map[string]any{
		"conditions": []any{map[string]any{"kind": "timer", "delay_ms": 30 * 60 * 1000}},
		"repeat":     true,
		"standing":   map[string]any{"name": "Agent proposed only", "prompt": "Should remain draft."},
	}, "ww07-c27-agent-proposal")
	testutil.FailErr(t, "agent watch creates draft only", err)
	var draftRec RegisterReceipt
	testutil.FailErr(t, "decode agent draft", json.Unmarshal([]byte(draftRaw), &draftRec))
	if !draftRec.StandingDraft {
		t.Fatalf("expected standing_draft=true, got %+v", draftRec)
	}
	afterPersonWatches, _ := h.engine.List(ctx, h.projectID, true)
	if len(afterPersonWatches) != len(beforePersonWatches) {
		t.Fatalf("agent watch tool must not create a person-owned standing watch: before=%d after=%d", len(beforePersonWatches), len(afterPersonWatches))
	}

	for _, op := range []string{"inspect", "cancel", "pause", "resume", "run", "delete", "edit", "enable"} {
		if _, err := h.execTool(ctx, "watch_control", map[string]any{
			"handle":    fileSW.WatchID,
			"operation": op,
		}, "ww07-c27-ctrl-"+op); err == nil {
			t.Fatalf("agent watch_control(operation=%q) must fail on person-owned standing watch %s", op, fileSW.WatchID)
		}
	}

	emitWW07Evidence(t, "C27-own-writes-unattended-runs", map[string]any{
		"runs_after_own_writes": len(runsAfterOwn),
		"runs_after_external":   len(runsAfterExt),
		"agent_isolation":       true,
	})
}
