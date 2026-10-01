//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func countRunContextBlocks(msgs []api.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == api.MessageRoleSystem && strings.Contains(m.Content, inject.ActiveWorkflowInjectSentinel) {
			n++
		}
	}
	return n
}

func TestRunContextInjectsOnceAcrossIterations(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "multi-iter",
		ToolCalls: []llm.MockToolCall{{
			Name: "list_dir",
			Args: map[string]any{"path": "."},
		}},
		FollowUpText: "done",
	}}}))
	store := store.NewMemory()
	reg := tools.NewStubRegistry()
	if err := reg.Register("list_dir", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "[]", nil
	}); err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager(store, rec, reg, settings.DefaultSessionLimits())
	projects := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(ctx, projects, t.TempDir())
	testutil.FailErr(t, "create project", err)
	mgr.SetProjectRegistry(projects)
	// Reserve one iteration for prose closeout.
	mgr.SetMaxIterations(3)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetCoordinatorTurnFrameSource(&phaseStubCoordinator{
		phase:   "expand",
		brief:   "Coordinate the hotfix workflow.",
		surface: "implement_investigate",
	})

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session in store", err)
	resp, err := mgr.Prompt(ctx, sess.ID, "multi-iter start")
	if err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}

	reqs := rec.AllRequests()
	if len(reqs) < 2 {
		msgs, _ := store.GetMessages(ctx, sess.ID)
		var offered []string
		if len(reqs) > 0 {
			offered = toolNames(reqs[0].Tools)
		}
		t.Fatalf("expected at least 2 LLM requests, got %d; offered=%v response=%+v messages=%+v", len(reqs), offered, resp, msgs)
	}
	// Count the one-shot block directly.
	if countRunContextBlocks(reqs[0].Messages) != 1 {
		t.Fatal("expected run context on first iteration")
	}
	if countRunContextBlocks(reqs[1].Messages) != 0 {
		t.Fatal("expected run context omitted on second iteration")
	}
}

func TestSecondPromptStillGetsRunContextAfterAdvance(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".", Text: "ok"},
		{Pattern: ".", Text: "ok"},
	}}))
	store := store.NewMemory()
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	coord := &phaseStubCoordinator{phase: "expand", brief: "phase expand brief"}
	mgr.SetCoordinatorTurnFrameSource(coord)

	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureSpec}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := mgr.Prompt(ctx, sess.ID, "turn one"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	coord.phase = "build"
	coord.brief = "phase build brief"
	if _, err := mgr.Prompt(ctx, sess.ID, "turn two"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	if countRunContextBlocks(rec.LastRequest().Messages) != 1 {
		t.Fatal("expected fresh run context on second user Prompt after phase change")
	}
	foundBrief := false
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && strings.Contains(msg.Content, "phase build brief") {
			foundBrief = true
		}
	}
	if !foundBrief {
		t.Fatal("expected updated coordinator brief in run context")
	}
}

func TestWorkerLegOmittedOnSecondIteration(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "worker-multi",
		ToolCalls: []llm.MockToolCall{{
			Name: "read",
			Args: map[string]any{"path": "go.mod"},
		}},
		FollowUpText: "done",
	}}}))
	store := store.NewMemory()
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetAgentRegistry(loadTestAgentRegistry(t))
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetWorkerContextBuilder(&stubWorkerContext{ctx: inject.WorkerLegContext{
		LegID:     "leg-1",
		PhaseID:   "implement",
		Checklist: []string{"Run tests"},
	}})

	parent, err := mgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "mgr.Create failed", err)
	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: "implementer",
		Prompt:    "worker-multi go",
	})
	testutil.FailErr(t, "mgr.SpawnChild failed", err)
	if _, err := mgr.Prompt(ctx, child.ID, "worker-multi go"); err != nil {
		testutil.FailErr(t, "mgr.Prompt failed", err)
	}
	reqs := rec.AllRequests()
	if len(reqs) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqs))
	}
	legBlocks := func(msgs []api.Message) int {
		n := 0
		for _, m := range msgs {
			if m.Role == api.MessageRoleSystem && strings.Contains(m.Content, inject.WorkerLegInjectSentinel) {
				n++
			}
		}
		return n
	}
	if legBlocks(reqs[0].Messages) != 1 {
		t.Fatal("expected leg context on first iteration")
	}
	if legBlocks(reqs[1].Messages) != 0 {
		t.Fatal("expected leg context omitted on second iteration")
	}
}
