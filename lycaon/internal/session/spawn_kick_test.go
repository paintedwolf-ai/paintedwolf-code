package session_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
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
	mgr := session.NewManager(store.NewMemory(), rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetWorkerContextBuilder(&stubKickWorkerContext{ctx: inject.WorkerLegContext{
		LegID:     "leg-99",
		PhaseID:   "implement",
		AgentType: orchestration.ProfileImplementer,
	}})

	ctx := context.Background()
	parent, err := mgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "start leg",
	})
	testutil.FailErr(t, "mgr.SpawnChild failed", err)
	if _, err := mgr.Prompt(ctx, child.ID, ""); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
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
	mgr := session.NewManager(store.NewMemory(), rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	// No worker context builder — implement-mode task() spawn has empty leg_id.

	ctx := context.Background()
	parent, err := mgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfilePathExplorer,
		Prompt:    "survey foo.html structure",
	})
	testutil.FailErr(t, "mgr.SpawnChild failed", err)
	if _, err := mgr.Prompt(ctx, child.ID, ""); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
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
	mgr := session.NewManager(store.NewMemory(), llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	// No SetPromptEngine — worker kick queue is a no-op.
	ctx := context.Background()
	parent, err := mgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "hi"})
	testutil.FailErr(t, "mgr.SpawnChild failed", err)
	if _, err := mgr.Prompt(ctx, child.ID, "hi"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
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
