package session_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func loadTestAgentRegistry(t *testing.T) *orchestration.MemoryAgentRegistry {
	t.Helper()
	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry", err)
	}
	return reg
}

func newPromptTestManager(t *testing.T, llmClient modelcall.LLMClient) (*session.Manager, *store.Memory) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	toolReg := tools.NewExecutorRegistry(nil, tools.NewDefaultRegistry())
	mgr := session.NewManager(store, llmClient, toolReg, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetAgentRegistry(loadTestAgentRegistry(t))
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	return mgr, store
}

func firstSystemMessage(msgs []api.Message) (api.Message, bool) {
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleSystem {
			return msg, true
		}
	}
	return api.Message{}, false
}

func TestCompleteStreamInjectsAgentSystemPrompt(t *testing.T) {
	ctx := context.Background()
	inner := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "ok",
	}}})
	rec := llm.NewRecordingClient(inner)
	mgr, store := newPromptTestManager(t, rec)

	coord, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := mgr.Prompt(ctx, coord.ID, "hello coordinator"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	coordReq := rec.LastRequest()
	coordSys, ok := firstSystemMessage(coordReq.Messages)
	if !ok {
		t.Fatal("expected system message for coordinator")
	}
	if !strings.Contains(coordSys.Content, "## Investigate") {
		t.Fatalf("coordinator system = %q want investigate default", coordSys.Content)
	}

	child, err := mgr.SpawnChild(ctx, coord.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement feature",
	})
	testutil.FailErr(t, "mgr.SpawnChild failed", err)
	if _, err := mgr.Prompt(ctx, child.ID, "implement feature"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	implReq := rec.LastRequest()
	implSys, ok := firstSystemMessage(implReq.Messages)
	if !ok {
		t.Fatal("expected system message for implementer")
	}
	if !strings.Contains(implSys.Content, "Implementer") {
		t.Fatalf("implementer system = %q want persona render with contract delta", implSys.Content)
	}
	if coordSys.Content == implSys.Content {
		t.Fatal("coordinator and implementer system prompts must differ")
	}
}

func TestCoordinatorPromptIncludesPackBoardInject(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module coordboard\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	store := store.NewMemory()
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetBoardInject(
		&board.InjectBuilder{SnapshotBuilder: &board.SnapshotBuilder{Repo: repotest.NewProvider(t)}},
		board.DefaultInjectFormatter(),
	)
	mgr.SetCoordinatorTurnFrameSource(planBoardContextStub{
		ctx: api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "stub"},
	})

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, dir)
	if _, err := mgr.Prompt(ctx, sess.ID, "plan"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	found := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, "pack-board:v1") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected pack board sentinel in coordinator messages")
	}
}

func TestCompleteStreamCoordinatorDefault(t *testing.T) {
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "ok",
	}}}))
	mgr, store := newPromptTestManager(t, rec)

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := mgr.Prompt(ctx, sess.ID, "plan something"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	sys, ok := firstSystemMessage(rec.LastRequest().Messages)
	if !ok {
		t.Fatal("expected default coordinator system message")
	}
	if !strings.Contains(sys.Content, "## Investigate") {
		t.Fatalf("system = %q want investigate default", sys.Content)
	}
}

func TestCompleteStreamMissingTemplateFails(t *testing.T) {
	ctx := context.Background()
	mgr, store := newPromptTestManager(t, llm.NewMockProvider(nil))
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	mgr.SetPromptEngine(engine)
	mgr.SetAgentRegistry(&stubAgentResolver{profile: agentdef.Profile{
		ID:                   "broken",
		SystemPromptTemplate: "agents/missing.md",
	}})

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	_ = mgr.SetAgentType(ctx, sess.ID, "broken")
	if _, err := mgr.Prompt(ctx, sess.ID, "hi"); err == nil {
		t.Fatal("expected prompt error for missing template")
	} else if !strings.Contains(err.Error(), "system prompt") {
		t.Fatalf("error = %v", err)
	}
}

type stubAgentResolver struct {
	profile agentdef.Profile
}

func (s *stubAgentResolver) Get(id string) (agentdef.Profile, error) {
	if id == s.profile.ID {
		return s.profile, nil
	}
	return agentdef.Profile{}, fmt.Errorf("agent profile %q not found", id)
}

type planBoardContextStub struct {
	ctx api.CoordinatorRunContext
}

func (s planBoardContextStub) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: s.ctx}, nil
}
