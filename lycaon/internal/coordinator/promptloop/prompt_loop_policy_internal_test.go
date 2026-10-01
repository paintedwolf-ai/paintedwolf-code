package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunLoadsOneCoordinatorFramePerModelTurn(t *testing.T) {
	memory := store.NewMemory()
	source := &countingCoordinatorFrameSource{}
	buildCalls := 0
	deps := StoreDeps(memory)
	deps.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "done"}}})
	deps.CoordinatorFrame = source
	deps.BuildMessages = func(_ context.Context, _ *api.Session, history []api.Message, frame *inject.CoordinatorTurnFrame) ([]api.Message, error) {
		buildCalls++
		if frame == nil || frame.WorkflowRevision != int64(buildCalls) {
			t.Fatalf("frame = %+v at model turn %d", frame, buildCalls)
		}
		return history, nil
	}
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	_, err = NewPromptLoopForTest(deps).Run(t.Context(), PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "run prompt loop", err)
	if buildCalls == 0 || source.calls != buildCalls {
		t.Fatalf("frame loads = %d, model turns = %d", source.calls, buildCalls)
	}
}

func TestRunBindsCoordinatorPolicySnapshotToPromptToolSchema(t *testing.T) {
	memory := store.NewMemory()
	policy := &frameCheckingToolPolicy{}
	deps := StoreDeps(memory)
	deps.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "done"}}})
	deps.CoordinatorFrame = staticCoordinatorFrameSource{frame: inject.CoordinatorTurnFrame{
		RunContext:    api.CoordinatorRunContext{WorkflowID: "implement", RunID: "run-1", CurrentPhase: "verify"},
		ManifestRules: []string{"workflow-rules.yaml"},
		ScaffoldVars:  map[string]any{"dispatch": "sealed"},
	}}
	deps.CoordinatorPostureRules = func(context.Context, *api.Session) ([]string, error) {
		return []string{"postures/build.yaml"}, nil
	}
	deps.Policy = policy
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
	testutil.FailErr(t, "create session", err)
	_, err = NewPromptLoopForTest(deps).Run(t.Context(), PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "run prompt loop", err)
	if policy.eval.Phase != "verify" || policy.eval.WorkflowRunID != "run-1" {
		t.Fatalf("tool-schema workflow state = %+v", policy.eval)
	}
	if len(policy.eval.ManifestRules) != 1 || policy.eval.ManifestRules[0] != "workflow-rules.yaml" {
		t.Fatalf("tool-schema manifest rules = %v", policy.eval.ManifestRules)
	}
	if len(policy.eval.PostureRules) != 1 || policy.eval.PostureRules[0] != "postures/build.yaml" {
		t.Fatalf("tool-schema posture rules = %v", policy.eval.PostureRules)
	}
	if policy.eval.Vars["dispatch"] != "sealed" {
		t.Fatalf("tool-schema vars = %v", policy.eval.Vars)
	}
}

func TestCompleteStreamUsesPolicyList(t *testing.T) {
	policy := &recordingToolPolicy{}
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LLM:    client,
		Policy: policy,
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
	}}
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec}
	if _, _, err := loop.completeStream(context.Background(), sess, "s1", []api.Message{{Role: api.MessageRoleUser, Content: "go"}}, "coordinator", "go", 0, 8, false, nil, nil); err != nil {
		testutil.FailErr(t, "loop.completeStream failed", err)
	}
	if len(policy.listCalls) != 1 || policy.listCalls[0] != "coordinator" {
		t.Fatalf("list calls = %v", policy.listCalls)
	}
}

func TestSurfaceGatedInvokeAllowsOnSurfaceDeniesOff(t *testing.T) {
	cases := []struct {
		surface, tool string
		want          bool
	}{
		{"implement_investigate", "read", true},
		{"implement_investigate", "command", true}, // command is on-surface on investigate
		{"implement_investigate", "write", true},   // product write is on-surface on investigate
		{"implement_dispatch", "task", true},
		{"implement_dispatch", "command", false}, // off-surface command denied on orchestrate
		{"implement_dispatch", "write", false},   // off-surface write denied on orchestrate
	}
	for _, c := range cases {
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: c.surface}, 1)
		testutil.FailErr(t, "compile tool plan", err)
		if got := surfaceAllowsTool(plan, c.surface, c.tool); got != c.want {
			t.Errorf("surfaceAllowsTool(%s, %s) = %v, want %v", c.surface, c.tool, got, c.want)
		}
	}
	if !surfaceAllowsTool(toolsurface.Plan{}, "", "command") {
		t.Error("empty surface must not be gated")
	}
	if surfaceAllowsTool(toolsurface.Plan{}, "implement_dispatch", "command") {
		t.Error("named surface without a compile must fail closed")
	}
}

func TestCoordinatorToolsForTurnTrimsEveryDiscoveredProfile(t *testing.T) {
	dirty := tools.ToolMeta{
		Name: "probe_rewrite",
		ArgsSchema: map[string]any{
			"type":  "object",
			"anyOf": []any{map[string]any{"required": []any{"path"}}},
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
		},
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Policy: registryTestPolicy{metas: []tools.ToolMeta{dirty}},
	})
	ids := discoveredPromptProfileIDs(t)
	offered := 0
	for _, id := range ids {
		got, _, _, err := loop.coordinatorToolsForTurn(
			context.Background(),
			&api.Session{ID: "child-1", ParentSessionID: "parent-1", AgentType: id},
			id,
			nil,
			"do work",
			0,
			8,
			inject.CoordinatorTurnFrame{},
			nil,
		)
		testutil.FailErr(t, "compile coordinator tools", err)
		if len(got) == 0 {
			continue
		}
		offered++
		for _, meta := range got {
			if err := tools.ValidateFunctionParametersRoot(meta.ArgsSchema); err != nil {
				t.Fatalf("profile %q tool %q: %v", id, meta.Name, err)
			}
		}
	}
	if offered == 0 {
		t.Fatalf("no discovered profile offered tools; ids=%v", ids)
	}
}
