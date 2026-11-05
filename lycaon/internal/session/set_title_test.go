package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSetTitleOverwritesAndPublishes(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}})

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	updated, err := mgr.Chats.Naming.SetTitle(ctx, sess.ID, "  Ship readiness checklist  ")
	testutil.FailErr(t, "set title", err)
	if updated.Title != "Ship readiness checklist" {
		t.Fatalf("title = %q", updated.Title)
	}

	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "Ship readiness checklist" {
		t.Fatalf("stored title = %q", got.Title)
	}

	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != api.EventTopicSession {
				continue
			}
			var ev api.SessionEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "decode", err)
			}
			if ev.ID == sess.ID && ev.Title == "Ship readiness checklist" {
				return
			}
		case <-t.Context().Done():
			t.Fatal("timed out waiting for SessionEvent title")
		}
	}
}

func TestUpdateTitleIfUnsetNoOpsAfterManualSet(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", Text: "ok"},
	}})
	mgr := NewHost(mem, Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Chats.Naming.SetTitle(ctx, sess.ID, "Manual name")
	testutil.FailErr(t, "manual set", err)

	// The auto-title path does not overwrite a manual title.
	mgr.Chats.Naming.SessionFromPrompt(ctx, sess, "Build a completely different thing")
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "Manual name" {
		t.Fatalf("title overwritten to %q", got.Title)
	}

	updated, err := mem.UpdateTitleIfUnset(ctx, sess.ID, "Race loser")
	testutil.FailErr(t, "ifunset", err)
	if updated {
		t.Fatal("UpdateTitleIfUnset reported update after manual set")
	}
}

func TestSetTitleWinsRaceVsIfUnset(t *testing.T) {
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	mem := store.NewMemory()
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	// Concurrent-style: Set always writes; subsequent IfUnset no-ops.
	_, err = mgr.Chats.Naming.SetTitle(ctx, sess.ID, "User wins")
	testutil.FailErr(t, "set", err)
	ok, err := mem.UpdateTitleIfUnset(ctx, sess.ID, "Auto late")
	testutil.FailErr(t, "ifunset", err)
	if ok {
		t.Fatal("IfUnset should no-op when title already set")
	}
	got, err := mem.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.Title != "User wins" {
		t.Fatalf("title = %q", got.Title)
	}
}

func TestSourceAnalysisFailureAnnotatesPartialResult(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-source-analysis"}
	failure := &tsparse.Failure{Reason: "timeout", Language: "swift", TimeoutMS: 5000, ElapsedMS: 5001, SourceBytes: 6236, ParsedBytes: 4000}
	data := failure.Facts("source")
	data["path"], data["parse_failure_count"], data["parse_paths"] = "Level.swift", 1, []string{"Level.swift"}
	raised := guidance.ToolResultFacts{}.WithFeedback(toolrejection.SourceAnalysisUnavailableCode, data, &api.FeedbackSubject{Kind: "path", ID: "Level.swift"})
	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "grep", map[string]any{"pattern": "spawn($A)", "structural": true}, "partial matches", 1, raised)
	for _, want := range []string{"partial matches", "Code: SOURCE_ANALYSIS_UNAVAILABLE", "Level.swift", "timeout", "5000", "structural=false"} {
		if !strings.Contains(out, want) {
			t.Fatalf("feedback missing %q: %s", want, out)
		}
	}
	if !facts.HasCode(toolrejection.SourceAnalysisUnavailableCode) {
		t.Fatalf("feedback code missing: %+v", facts)
	}
	plain, _ := mgr.ToolPolicy.AfterTool(context.Background(), sess, "grep", map[string]any{"pattern": "spawn"}, "no matches", 1, guidance.ToolResultFacts{})
	if strings.Contains(plain, toolrejection.SourceAnalysisUnavailableCode) {
		t.Fatalf("invented parser failure: %s", plain)
	}
}

func TestSyntaxOverrideFeedbackRecordsExplicitChoice(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	sess := &api.Session{ID: "sess-syntax-override"}
	data := map[string]any{
		"path": "a.swift", "paths": []string{"a.swift"},
		"syntax_override_reason": "the language compiler accepts this file",
	}
	raised := guidance.ToolResultFacts{}.WithFeedback(tools.SyntaxCheckOverriddenCode, data, &api.FeedbackSubject{Kind: "path", ID: "a.swift"})
	raised = raised.WithFeedback(tools.SyntaxCheckOverriddenCode, map[string]any{
		"paths": []string{"b.swift"}, "syntax_override_reason": data["syntax_override_reason"],
	}, &api.FeedbackSubject{Kind: "path", ID: "b.swift"})
	out, facts := mgr.ToolPolicy.AfterTool(context.Background(), sess, "write", map[string]any{"path": "a.swift"}, "write completed", 1, raised)
	for _, want := range []string{"write completed", "Code: SYNTAX_CHECK_OVERRIDDEN", "a.swift", "b.swift", "the language compiler accepts this file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("override feedback missing %q: %s", want, out)
		}
	}
	if !facts.HasCode(tools.SyntaxCheckOverriddenCode) {
		t.Fatalf("override code missing: %+v", facts)
	}
}

// ordStoreFixture is one Store backend wired up with a fresh session, ready for
// the ord contract checks. The SQL and memory stores behave identically.
type ordStoreFixture struct {
	store     Store
	sessionID string
}

func newSQLOrdFixture(t *testing.T) ordStoreFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "ord-parity.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	store := store.NewSQL(sqlDB)
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return ordStoreFixture{store: store, sessionID: sess.ID}
}

func newMemoryOrdFixture(t *testing.T) ordStoreFixture {
	t.Helper()
	store := store.NewMemory()
	sess, err := store.Create(context.Background(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	return ordStoreFixture{store: store, sessionID: sess.ID}
}

// AppendMessages mints a strictly increasing per-session creation ordinal in both
// stores; ord is the integer Den sorts by.
func TestStoreOrdMonotonicAppend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(t *testing.T) ordStoreFixture
	}{
		{"sql", newSQLOrdFixture},
		{"memory", newMemoryOrdFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			ctx := context.Background()
			batch := []api.Message{
				{Role: api.MessageRoleUser, Content: "first", CreatedAt: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)},
				{Role: api.MessageRoleAssistant, Content: "second", CreatedAt: time.Date(2026, 7, 8, 12, 0, 1, 0, time.UTC)},
				{Role: api.MessageRoleTool, Content: "third", CreatedAt: time.Date(2026, 7, 8, 12, 0, 2, 0, time.UTC)},
			}
			testutil.FailErr(t, "append batch", f.store.AppendMessages(ctx, f.sessionID, batch...))

			got, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages", err)
			if len(got) != len(batch) {
				t.Fatalf("got %d messages, want %d", len(got), len(batch))
			}
			for i, m := range got {
				if m.Ord != int64(i+1) {
					t.Fatalf("message %d ord = %d, want %d (strictly increasing from 1)", i, m.Ord, i+1)
				}
			}
			// A second append continues the ordinal; it does not reset.
			testutil.FailErr(t, "append second batch", f.store.AppendMessages(ctx, f.sessionID, api.Message{
				Role: api.MessageRoleUser, Content: "fourth", CreatedAt: time.Now().UTC(),
			}))
			got2, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages after second append", err)
			if got2[len(got2)-1].Ord != int64(len(batch)+1) {
				t.Fatalf("fourth message ord = %d, want %d (ordinal continues across appends)", got2[len(got2)-1].Ord, len(batch)+1)
			}
		})
	}
}

// Patches preserve creation time and ordinal while advancing the mutation sequence.
func TestStoreOrdAndTSSurvivePatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(t *testing.T) ordStoreFixture
	}{
		{"sql", newSQLOrdFixture},
		{"memory", newMemoryOrdFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			ctx := context.Background()
			created := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
			testutil.FailErr(t, "append", f.store.AppendMessages(ctx, f.sessionID, api.Message{
				ID:        "draft-1",
				Role:      api.MessageRoleAssistant,
				Content:   "streaming...",
				CreatedAt: created,
			}))

			before, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages before patch", err)
			if len(before) != 1 {
				t.Fatalf("got %d messages, want 1", len(before))
			}
			wantOrd := before[0].Ord
			wantTS := before[0].CreatedAt
			wantSeq := before[0].Seq
			if wantOrd == 0 {
				t.Fatalf("ord not minted at append: %d", wantOrd)
			}

			// A patch with a zero ts and a mutated body: the caller does not know
			// the creation time and cannot overwrite it.
			_, err = f.store.UpdateMessage(ctx, f.sessionID, "draft-1", api.Message{
				ID:        "draft-1",
				Role:      api.MessageRoleAssistant,
				Content:   "committed body",
				CreatedAt: time.Time{},
			})
			testutil.FailErr(t, "patch", err)

			after, err := f.store.GetMessages(ctx, f.sessionID)
			testutil.FailErr(t, "get messages after patch", err)
			if len(after) != 1 {
				t.Fatalf("got %d messages after patch, want 1", len(after))
			}
			if after[0].Ord != wantOrd {
				t.Fatalf("ord changed across patch: was %d, now %d — ord is immutable", wantOrd, after[0].Ord)
			}
			if !after[0].CreatedAt.Equal(wantTS) {
				t.Fatalf("ts changed across patch: was %v, now %v — ts is the creation timestamp, immutable on patch", wantTS, after[0].CreatedAt)
			}
			if after[0].Seq <= wantSeq {
				t.Fatalf("seq did not advance across patch: was %d, now %d — seq is the mutation clock and must advance", wantSeq, after[0].Seq)
			}
			if after[0].Content != "committed body" {
				t.Fatalf("patched content = %q, want %q", after[0].Content, "committed body")
			}
		})
	}
}

func TestSurveyStreakNudgeNamesTheStreakForTheCoordinatorOnly(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	nudge := mgr.Coordinator.Nudges.SurveyStreak(t.Context(), &api.Session{ID: "root"}, 6, []string{"git_diff", "read"})
	if nudge.Empty() {
		t.Fatal("coordinator survey streak rendered empty")
	}
	for _, want := range []string{"6 consecutive", "`git_diff`", "`read`", "required paging and canonical recovery"} {
		if !strings.Contains(nudge.Content, want) {
			t.Fatalf("nudge missing %q:\n%s", want, nudge.Content)
		}
	}
	worker := mgr.Coordinator.Nudges.SurveyStreak(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"}, 6, []string{"read"})
	if !worker.Empty() {
		t.Fatalf("a worker's reads are its job, not a streak: %q", worker.Content)
	}
}

func TestSurveyStreakNudgeStaysQuietOnInspectTurns(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	ledger := turnload.NewLedger()
	mgr.Coordinator.Loading.SetLedger(ledger)
	mgr.Coordinator.Nudges.SetLedger(ledger)
	sess := &api.Session{ID: "root"}

	ledger.BeginTurn(sess.ID, turnload.TurnOpening{}, turnload.Decision{Kind: turnload.KindInspect})
	if nudge := mgr.Coordinator.Nudges.SurveyStreak(t.Context(), sess, 6, []string{"grep"}); !nudge.Empty() {
		t.Fatalf("an inspect turn's reads are its work, not a streak: %q", nudge.Content)
	}
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{}, turnload.Decision{Kind: turnload.KindChange})
	if nudge := mgr.Coordinator.Nudges.SurveyStreak(t.Context(), sess, 6, []string{"grep"}); nudge.Empty() {
		t.Fatal("a change turn's read-only streak rendered empty")
	}
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{}, turnload.Decision{Kind: turnload.KindInspect})
	ledger.BeginUndecidedTurn(sess.ID, turnload.TurnOpening{})
	if nudge := mgr.Coordinator.Nudges.SurveyStreak(t.Context(), sess, 6, []string{"grep"}); nudge.Empty() {
		t.Fatal("an undecided turn kept the previous turn's kind")
	}
}
