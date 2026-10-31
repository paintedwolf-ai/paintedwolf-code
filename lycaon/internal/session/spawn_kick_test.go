package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubKickWorkerContext struct {
	ctx inject.WorkerLegContext
}

func (s *stubKickWorkerContext) BuildWorkerPromptContext(_ string, _ *api.Session) (inject.WorkerLegContext, error) {
	return s.ctx, nil
}

func TestSpawnChildInjectsWorkerKick(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store.NewMemory(), session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetWorkerContextBuilder(&stubKickWorkerContext{ctx: inject.WorkerLegContext{
		LegID:     "leg-99",
		PhaseID:   "implement",
		AgentType: orchestration.ProfileImplementer,
	}})

	ctx := context.Background()
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "start leg",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, ""); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	found := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && msg.Origin == api.MessageOriginHost && strings.Contains(msg.Content, "Worker leg started") &&
			strings.Contains(msg.Content, "leg-99") && strings.Contains(msg.Content, "implement") {
			found = true
		}
	}
	if !found {
		t.Fatalf("messages = %+v", rec.LastRequest().Messages)
	}
}

func TestSpawnChildInjectsImplementModeWorkerKick(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store.NewMemory(), session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	// No worker context builder — implement-mode task() spawn has empty leg_id.

	ctx := context.Background()
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfilePathExplorer,
		Prompt:    "survey foo.html structure",
	})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, ""); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	var kickText, promptText string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && msg.Origin == api.MessageOriginHost && strings.Contains(msg.Content, "Worker task started") {
			kickText = msg.Content
		}
		if msg.Role == api.MessageRoleUser && strings.Contains(msg.Content, "survey foo.html structure") {
			promptText = msg.Content
		}
	}
	if kickText == "" || promptText == "" {
		t.Fatalf("messages = %+v", rec.LastRequest().Messages)
	}
	if strings.Contains(kickText, "CompletionCriteria") || strings.Contains(kickText, "leg ``") {
		t.Fatalf("implement-mode kick must not reference workflow leg metadata: %q", kickText)
	}
}

func TestSpawnChildMissingKickTemplateSoftFails(t *testing.T) {
	mgr := session.NewHost(store.NewMemory(), session.Models{Client: llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	// No SetPromptEngine — worker kick queue is a no-op.
	ctx := context.Background()
	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "hi"})
	testutil.FailErr(t, "mgr.Workers.SpawnChild failed", err)
	if _, err := mgr.Submissions.Prompt(ctx, child.ID, "hi"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
}

func TestPlanWriterRenderIncludesToolProfileStub(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "plan-writer", nil)
	testutil.FailErr(t, "prompts.RenderPersona failed", err)
	if !strings.Contains(got, "Tool schema") || !strings.Contains(got, "WRITE_SCOPE_DENIED") {
		t.Fatalf("plan-writer render missing agent tool surface: %q", got)
	}
}

func TestListToolsForPromptSpecHidesDelegation(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureSpec)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	if hasTool(listed, "delegate_dispatch") {
		t.Fatalf("spec posture must hide delegate_dispatch, got %v", toolNames(listed))
	}
	if !hasTool(listed, "read") {
		t.Fatal("expected read visible in spec posture")
	}
	if !hasTool(listed, "workflow_compose") {
		t.Fatal("expected workflow_compose visible in spec posture")
	}
}

func TestListToolsForPromptBuildShowsDelegation(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	if !hasTool(listed, "delegate_dispatch") {
		t.Fatalf("build posture must show delegate_dispatch, got %v", toolNames(listed))
	}
	if !hasTool(listed, "task") {
		t.Fatal("expected task visible in build posture")
	}
}

func TestListToolsForPromptCoordinatorCeiling(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.Sess, fix.ProfileID)
	// Profile grant; the turn surface gates invoke.
	for _, name := range []string{"grep", "read", "command"} {
		if !hasTool(listed, name) {
			t.Fatalf("coordinator profile should list %q", name)
		}
	}
	if !hasTool(listed, "write") {
		t.Fatal("coordinator profile should list write (plan-scoped)")
	}
}

func TestTranscriptPageProjectsToolStepWithoutChangingModelHistory(t *testing.T) {
	st := store.NewMemory()
	mgr := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	step := api.Message{ID: "step", Role: api.MessageRoleAssistant, Content: "internal tool explanation",
		ToolCalls: []api.ToolCall{{ID: "call", Name: "wait"}}, Visibility: api.MessageVisibilityTranscript}
	note := api.Message{ID: "note", Role: api.MessageRoleAssistant, Content: "The CLI implementation is ready.", Visibility: api.MessageVisibilityTranscript}
	testutil.FailErr(t, "append step and progress note", st.AppendMessages(t.Context(), sess.ID, step, note))
	page, err := mgr.Runner.Transcript.GetTranscriptPage(t.Context(), sess.ID, api.TranscriptPageQuery{})
	testutil.FailErr(t, "read transcript", err)
	if len(page.Messages) != 2 || page.Messages[0].Content != "" || len(page.Messages[0].ToolCalls) != 1 || page.Messages[1].Content != note.Content {
		t.Fatalf("transcript lost cards or progress note: %+v", page.Messages)
	}
	history, err := mgr.Runner.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read model history", err)
	if len(history) != 2 || history[0].Content != step.Content {
		t.Fatalf("model history changed: %+v", history)
	}
}

func TestSessionUntrustedContentFreshIsClean(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)
	if store.SessionUntrustedContent(sess.ID) {
		t.Fatal("fresh session must not be untrusted")
	}
	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.UntrustedContent {
		t.Fatal("Get wire field must be false for fresh session")
	}
}

func TestSessionUntrustedContentWebAndMCP(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	if err := store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "read#1", Kind: "file", Shape: evidence.ShapeFileRegion, SourceTool: "read",
	}); err != nil {
		testutil.FailErr(t, "upsert read", err)
	}
	if store.SessionUntrustedContent(sess.ID) {
		t.Fatal("read must not mark untrusted")
	}

	if err := store.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: "https://example.com/a",
	}); err != nil {
		testutil.FailErr(t, "upsert fetch_url", err)
	}
	if !store.SessionUntrustedContent(sess.ID) {
		t.Fatal("fetch_url must mark untrusted")
	}

	sess2, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create 2", err)
	if err := store.UpsertEvidenceRecord(ctx, sess2.ID, evidence.Record{
		Handle: "mcp#1", Kind: "mcp", Shape: evidence.ShapeOpaque, SourceTool: "mcp_docs_get",
	}); err != nil {
		testutil.FailErr(t, "upsert mcp", err)
	}
	if !store.SessionUntrustedContent(sess2.ID) {
		t.Fatal("any MCP tool must mark untrusted")
	}

	sess3, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create 3", err)
	if err := store.UpsertEvidenceRecord(ctx, sess3.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "web_search",
	}); err != nil {
		testutil.FailErr(t, "upsert web_search", err)
	}
	if !store.SessionUntrustedContent(sess3.ID) {
		t.Fatal("web_search must mark untrusted")
	}
}

func TestSessionUntrustedContentSpawnInheritance(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	if err := store.UpsertEvidenceRecord(ctx, parent.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: "https://example.com",
	}); err != nil {
		testutil.FailErr(t, "upsert parent", err)
	}

	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "do work"})
	testutil.FailErr(t, "SpawnChild", err)
	if !child.UntrustedContent {
		t.Fatal("spawned child must be born untrusted when parent is")
	}
	if !store.SessionUntrustedContent(child.ID) {
		t.Fatal("child SessionUntrustedContent must be true after inherit seed")
	}

	clean, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create clean parent", err)
	cleanChild, err := mgr.Workers.SpawnChild(ctx, clean.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "do work"})
	testutil.FailErr(t, "SpawnChild clean", err)
	if cleanChild.UntrustedContent || store.SessionUntrustedContent(cleanChild.ID) {
		t.Fatal("child of trusted parent must stay clean until its own untrusted ingest")
	}
}

func TestMergeWorkerUntrustedIntoParent(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: orchestration.ProfileWebResearcher})
	testutil.FailErr(t, "create child", err)

	childURL := "https://docs.example.com/page"
	if err := store.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: childURL, Fidelity: evidence.FidelityStructured,
	}); err != nil {
		testutil.FailErr(t, "upsert child url", err)
	}

	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        "research done",
		JobID:          "job-web-1",
		ChildSessionID: child.ID,
		AgentType:      orchestration.ProfileWebResearcher,
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	if !store.SessionUntrustedContent(parent.ID) {
		t.Fatal("web-researcher summary ingest must mark parent untrusted")
	}
	parentEv, err := store.LoadLedger(ctx, parent.ID)
	testutil.FailErr(t, "LoadLedger parent", err)
	urls := evidence.ObservedURLsSorted(parentEv)
	found := false
	for _, u := range urls {
		if u == childURL {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("parent ledger must include merged child URL; got %v", urls)
	}
}

func TestMergeWorkerUntrustedIntoParentUsesChildEvidenceNotAgentName(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "custom-all-tools"})
	testutil.FailErr(t, "create child", err)
	if err := store.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "web#custom", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "fetch_url",
		URL: "https://example.com/custom", Fidelity: evidence.FidelityStructured,
	}); err != nil {
		testutil.FailErr(t, "upsert child url", err)
	}

	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary: "done", JobID: "job-custom", ChildSessionID: child.ID, AgentType: "custom-all-tools",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	if !store.SessionUntrustedContent(parent.ID) {
		t.Fatal("custom worker with untrusted evidence must mark parent untrusted")
	}

	cleanParent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create clean parent", err)
	cleanChild, err := store.CreateChild(ctx, cleanParent, api.SpawnChildRequest{AgentType: orchestration.ProfileWebResearcher})
	testutil.FailErr(t, "create clean child", err)
	if _, err := mgr.Workers.Summaries.Append(ctx, cleanParent.ID, workeroutcomes.SummaryInput{
		Summary: "done", JobID: "job-clean", ChildSessionID: cleanChild.ID, AgentType: orchestration.ProfileWebResearcher,
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary clean", err)
	}
	if store.SessionUntrustedContent(cleanParent.ID) {
		t.Fatal("agent name without child evidence must not mark parent untrusted")
	}
}

func TestSessionUntrustedContentSQLRebuild(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "untrusted.db")
	hot := store.NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := hot.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	if err := hot.UpsertEvidenceRecord(ctx, sess.ID, evidence.Record{
		Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, SourceTool: "web_search",
	}); err != nil {
		testutil.FailErr(t, "upsert", err)
	}
	// Cold cache path: new store handle over same DB rebuilds from rows.
	cold := store.NewSQL(sqlDB)
	if !cold.SessionUntrustedContent(sess.ID) {
		t.Fatal("SQL rebuild-from-records must report untrusted")
	}
	got, err := cold.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if !got.UntrustedContent {
		t.Fatal("Get must hydrate untrusted_content after rebuild")
	}
}
