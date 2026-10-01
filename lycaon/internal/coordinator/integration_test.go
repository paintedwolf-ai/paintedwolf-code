package coordinator_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/pkg/api"
)

func kickTestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func stubWorkspaceRootsOne() assembly.WorkspaceRootsLoader {
	return func(_ context.Context, sess *api.Session) ([]map[string]any, int, string) {
		if sess == nil || strings.TrimSpace(sess.WorkspacePath) == "" {
			return nil, 0, ""
		}
		path := sess.WorkspacePath
		return []map[string]any{{
			"label":      "main",
			"path":       path,
			"is_primary": true,
		}}, 1, path
	}
}

func kickEngineWithPrompts(t *testing.T) *kick.KickEngine {
	t.Helper()
	ensureAnchorRegistry(t)
	engine := &kick.KickEngine{}
	engine.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)}))
	return engine
}

func ensureAnchorRegistry(t *testing.T) {
	t.Helper()
	if anchor.DefaultRegistry() != nil {
		return
	}
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "LoadRegistryFromConfigRoot", err)
	anchor.SetDefaultRegistry(reg)
}

func TestKickEngineQueueAndTake(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	engine.QueueDeferred("sess-1", anchor.InformRender(anchor.ComposeDone))
	if id := engine.TakePendingKickID("sess-1"); id != anchor.InformRender(anchor.ComposeDone) {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-1", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Compose") {
		t.Fatalf("nudge = %q ok=%v", text, ok)
	}
}

func TestKickEngineWorkerTaskFinishedIncludesDigest(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	engine.QueueDeferred("sess-1", anchor.InformRender(anchor.WorkerTaskFinished), kick.WithWorkerDigest("Host worker digest — agent=implementer status=complete"))
	if id := engine.TakePendingKickID("sess-1"); id != anchor.InformRender(anchor.WorkerTaskFinished) {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-1", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "Host worker digest") {
		t.Fatalf("nudge = %q ok=%v", text, ok)
	}
}

func TestKickEngineWorkerTaskFinishedNudge(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	engine.QueueDeferred("sess-1", anchor.InformRender(anchor.WorkerTaskFinished))
	if id := engine.TakePendingKickID("sess-1"); id != anchor.InformRender(anchor.WorkerTaskFinished) {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-1", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "host digest") {
		t.Fatalf("nudge = %q ok=%v", text, ok)
	}
}

func TestKickEngineClearPending(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	engine.QueueDeferred("sess-1", anchor.InformRender(anchor.WorkerTaskFinished))
	engine.ClearPending("sess-1")
	if id := engine.TakePendingKickID("sess-1"); id != "" {
		t.Fatalf("kick id = %q want empty", id)
	}
	if text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-1", kick.CoordinatorKickRenderContext{}); ok || text != "" {
		t.Fatalf("nudge = %q ok=%v want cleared", text, ok)
	}
}

func TestKickEngineQueuesMultipleCoordinatorKicksFIFO(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	engine.QueueDeferred("sess-race", anchor.InformRender(anchor.WorkerTaskFinished))
	engine.QueueDeferred("sess-race", anchor.InformRender(anchor.LegFinished))
	if id := engine.TakePendingKickID("sess-race"); id != anchor.InformRender(anchor.WorkerTaskFinished) {
		t.Fatalf("first kick id = %q want %q", id, anchor.InformRender(anchor.WorkerTaskFinished))
	}
	text, lease, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-race", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "host digest") {
		t.Fatalf("first nudge = %q ok=%v want worker-task-finished", text, ok)
	}
	engine.AckPendingNudge("sess-race", lease)
	if id := engine.TakePendingKickID("sess-race"); id != anchor.InformRender(anchor.LegFinished) {
		t.Fatalf("second kick id = %q want %q", id, anchor.InformRender(anchor.LegFinished))
	}
}

func TestKickEngineLegFinishedRelativeAgo(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	done := time.Now().UTC().Add(-2 * time.Hour)
	engine.QueueDeferred("sess-2", anchor.InformRender(anchor.LegFinished), kick.WithLegCompletedAt(done))
	if id := engine.TakePendingKickID("sess-2"); id != anchor.InformRender(anchor.LegFinished) {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-2", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "finished") {
		t.Fatalf("nudge = %q", text)
	}
}

func TestKickEngineRawNudge(t *testing.T) {
	engine := &kick.KickEngine{}
	engine.QueuePendingText("sess-3", "custom nudge", anchor.InformRender(anchor.GateBlocked))
	if id := engine.TakePendingKickID("sess-3"); id != anchor.InformRender(anchor.GateBlocked) {
		t.Fatalf("kick id = %q", id)
	}
	text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "sess-3", kick.CoordinatorKickRenderContext{})
	if !ok || text != "custom nudge" {
		t.Fatalf("nudge = %q", text)
	}
}

func TestRenderKickMissingEngine(t *testing.T) {
	engine := &kick.KickEngine{}
	engine.QueueDeferred("sess-x", anchor.InformRender(anchor.GateBlocked))
	if id := engine.TakePendingKickID("sess-x"); id != anchor.InformRender(anchor.GateBlocked) {
		t.Fatalf("kick id = %q want %s", id, anchor.InformRender(anchor.GateBlocked))
	}
	if text, _, ok, err := engine.RenderPendingNudge(t.Context(), "sess-x", kick.CoordinatorKickRenderContext{}); ok || text != "" || err == nil {
		t.Fatalf("missing engine = %q, ok %v, err %v", text, ok, err)
	}
}

type stubBoardBuilder struct {
	hash string
}

func (s stubBoardBuilder) BuildBoardSnapshot(_ context.Context, _, _, _ string, _ api.BoardDetailLevel, _ []projectroot.RootRef) (*api.BoardSnapshot, error) {
	return &api.BoardSnapshot{PackContentHash: s.hash}, nil
}

type stubBoardFormatter struct{}

func (stubBoardFormatter) FormatBoardInject(_ api.BoardSnapshot, _ bool, _ time.Time) (string, bool) {
	return "pack-board:v1\nbody", true
}

func TestBoardEnginePrependOnFirstTurn(t *testing.T) {
	engine := assembly.NewBoardEngine(stubBoardBuilder{hash: "abc"}, stubBoardFormatter{}, nil)
	ctx := context.Background()
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	if err := os.WriteFile(filepath.Join(sess.WorkspacePath, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	block, ok := engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if !ok || !strings.Contains(block, "Pack board") {
		t.Fatalf("block = %q ok=%v", block, ok)
	}
	block2, ok2 := engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if ok2 {
		t.Fatalf("expected no reinject on stable hash/phase, got %q", block2)
	}
}

func TestBoardWillForceInjectOnPhaseChange(t *testing.T) {
	engine := assembly.NewBoardEngine(stubBoardBuilder{hash: "abc"}, stubBoardFormatter{}, nil)
	ctx := context.Background()
	sess := &api.Session{ID: "s2", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	if err := os.WriteFile(filepath.Join(sess.WorkspacePath, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	_, _ = engine.PrependBoardIfChanged(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if !engine.BoardWillForceInject(ctx, sess, api.CoordinatorRunContext{CurrentPhase: "implement"}) {
		t.Fatal("phase change should force inject")
	}
}

func TestLoopEvaluateDeniesWhenDisabled(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	disabled := settings.DefaultSessionLimits()
	f := false
	disabled.CoordinatorLoop = &f
	engine.SetDeps(loopwake.LoopDeps{
		GetSession: func(_ context.Context, _ string) (*api.Session, error) {
			return &api.Session{Status: api.SessionStatusIdle}, nil
		},
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return disabled },
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
	})
	allow, busy := engine.EvaluateForTest(context.Background(), "s1", anchor.LegFinished)
	if allow || busy {
		t.Fatalf("allow=%v busy=%v", allow, busy)
	}
}

func TestLoopBudgetConsumption(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	limits := settings.DefaultSessionLimits()
	limits.MaxCoordinatorLoopCycles = 1
	sess := &api.Session{Status: api.SessionStatusIdle}
	engine.SetDeps(loopwake.LoopDeps{
		GetSession: func(_ context.Context, _ string) (*api.Session, error) { return sess, nil },
		Limits:     func(context.Context, *api.Session) settings.SessionLimits { return limits },
		WorkflowSource: stubLoopWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "implement"},
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
	})
	ctx := context.Background()
	if !engine.TryConsumeBudgetForTest(ctx, "s1", "run-1") {
		t.Fatal("first consume should succeed")
	}
	if engine.TryConsumeBudgetForTest(ctx, "s1", "run-1") {
		t.Fatal("second consume should fail at max=1")
	}
}

type stubLoopWF struct {
	run  *api.WorkflowRun
	vars map[string]any
}

func (s stubLoopWF) ActiveRun(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (s stubLoopWF) ScaffoldVars(context.Context, string) (map[string]any, error) {
	if s.vars != nil {
		return s.vars, nil
	}
	return map[string]any{}, nil
}

func (s stubLoopWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return scaffoldvars.HumanApprovalAwaiting(s.vars), nil
}

func (stubLoopWF) HostObligationHeld(context.Context, string) (bool, error) { return false, nil }

func (stubLoopWF) HostObligationHoldKinds(context.Context, string) []string { return nil }

func TestLoopScheduleAndDrain(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	limits := settings.DefaultSessionLimits()
	sess := &api.Session{ID: "s1", Status: api.SessionStatusBusy}
	engine.SetDeps(loopwake.LoopDeps{
		GetSession: func(_ context.Context, id string) (*api.Session, error) {
			return sess, nil
		},
		Limits: func(context.Context, *api.Session) settings.SessionLimits { return limits },
		WorkflowSource: stubLoopWF{
			run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "implement"},
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
	})
	ctx := context.Background()
	finishExecution := engine.BeginPromptExecution(t.Context(), "s1")
	engine.Nudge(ctx, "s1", anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	if _, ok := engine.PendingForTest("s1"); !ok {
		t.Fatal("expected pending loop wake while prompt execution is active")
	}
	finishExecution()
	if !engine.TryConsumeBudgetForTest(ctx, "s1", "run-1") {
		t.Fatal("expected budget consume on drain path")
	}
	engine.DrainPending(ctx, "s1")
	if _, ok := engine.PendingForTest("s1"); ok {
		t.Fatal("pending should be cleared")
	}
}

func TestLoopShouldLoopWakeHumanApprovalAwaiting(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{Status: api.SessionStatusIdle}, nil
	}
	deps.WorkflowSource = stubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "decide"},
		vars: map[string]any{"human_approval": map[string]any{
			"active": true, "ready": true, "blueprint_path": "review.md",
		}},
	}
	engine.SetDeps(deps)
	allow, reason, err := engine.ShouldLoopWake(context.Background(), "s1", anchor.LegFinished)
	if err != nil || allow || reason != "human_approval_awaiting" {
		t.Fatalf("allow=%v reason=%q err=%v", allow, reason, err)
	}
}

func TestLoopShouldLoopWakeIgnoresApprovePhaseName(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{Status: api.SessionStatusIdle}, nil
	}
	deps.WorkflowSource = stubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "approve"},
	}
	engine.SetDeps(deps)
	allow, reason, err := engine.ShouldLoopWake(context.Background(), "s1", anchor.LegFinished)
	if err != nil {
		t.Fatalf("ShouldLoopWake: %v", err)
	}
	if reason == "approve_phase" || reason == "human_approval_awaiting" {
		t.Fatalf("phase name must not deny, reason=%q", reason)
	}
	if !allow {
		t.Fatalf("allow=%v reason=%q want allow without awaiting vars", allow, reason)
	}
}

func TestRuntimeSetKickAndBoard(t *testing.T) {
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	rt.SetBoardInject(stubBoardBuilder{hash: "h1"}, stubBoardFormatter{}, func() bool { return true })

	// No kick was queued — the kick surface stays empty.
	if rt.Kicks().TakePendingKickID("x") != "" {
		t.Fatal("unexpected kick")
	}

	// The first turn prepends the wired board.
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	if err := os.WriteFile(filepath.Join(sess.WorkspacePath, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	block, ok := rt.Board().PrependBoardIfChanged(context.Background(), sess, api.CoordinatorRunContext{CurrentPhase: "plan"})
	if !ok || !strings.Contains(block, "Pack board") {
		t.Fatalf("board block = %q ok=%v want injected board", block, ok)
	}
}

func TestSessionPromptCacheTurnLifecycle(t *testing.T) {
	var cache assembly.SessionPromptCache
	cache.BeginTurn("s1", anchor.InformRender(anchor.FeedbackPending))
	turn := cache.LoadTurn("s1")
	if len(turn.PendingKickIDs) != 1 || turn.PendingKickIDs[0] != anchor.InformRender(anchor.FeedbackPending) {
		t.Fatalf("kicks = %v", turn.PendingKickIDs)
	}
	cache.EndTurn("s1")
	turn2 := cache.LoadTurn("s1")
	if len(turn2.PendingKickIDs) != 0 {
		t.Fatal("expected fresh turn after end")
	}
}

func TestScaffoldPendingFeedbackPhase(t *testing.T) {
	vars := map[string]any{
		"user_feedback": map[string]any{
			"plan": map[string]any{"pending": true},
		},
	}
	if phase, ok := scaffoldvars.PendingFeedbackPhase(vars); !ok || phase != "plan" {
		t.Fatalf("phase=%q ok=%v", phase, ok)
	}
}

func TestResolveSystemPromptTemplateDefault(t *testing.T) {
	ref := assembly.ResolveSystemPromptTemplate(&api.Session{}, nil, "")
	if ref != "agents/coordinator-core.md" {
		t.Fatalf("ref = %q", ref)
	}
}

func TestBuildCompletionMessagesWorkerLeg(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts:       pe,
		Injects:       prompts.NewInjectRenderer(pe),
		Limits:        func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerContext: stubWorkerCtx{leg: inject.WorkerLegContext{LegID: "leg-1", Checklist: []string{"step"}}},
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	msgs, err := rt.BuildCompletionMessages(context.Background(), &api.Session{
		ID: "child", ParentSessionID: "parent", WorkspacePath: t.TempDir(),
	}, []api.Message{{Role: api.MessageRoleUser, Content: "work"}}, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	foundLeg := false
	for _, m := range msgs {
		if strings.Contains(m.Content, inject.WorkerLegInjectSentinel) {
			foundLeg = true
		}
	}
	if !foundLeg {
		t.Fatal("expected worker leg block")
	}
}

type stubWorkerCtx struct {
	leg inject.WorkerLegContext
}

func (s stubWorkerCtx) BuildWorkerPromptContext(_ string, _ *api.Session) (inject.WorkerLegContext, error) {
	return s.leg, nil
}

func TestScaffoldAnyDecisionPending(t *testing.T) {
	vars := map[string]any{
		"user_decision": map[string]any{
			"risk": map[string]any{"pending": true},
		},
	}
	if !scaffoldvars.AnyDecisionPending(vars) {
		t.Fatal("expected decision pending")
	}
}

func TestRenderCoordinatorTripartiteComposeDraft(t *testing.T) {
	root := kickTestRoot(t)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars("implement_synthesis", nil, vars, prompts.SurfaceTurn{}); err != nil {
		testutil.FailErr(t, "MergeCoordinatorSurfacePathVars", err)
	}
	profile := surface.ResolveTurnProfile(api.CoordinatorRunContext{HasComposeDraft: true}, nil, nil)
	var parts []string
	for _, ref := range append([]string{"agents/coordinator-core.md"}, surface.ModeTemplateRef(profile.ModeRefs[0])) {
		chunk, err := engine.Render(context.Background(), ref, vars)
		testutil.FailErr(t, "render", err)
		parts = append(parts, strings.TrimSpace(chunk))
	}
	out := strings.Join(parts, "\n\n")
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected tripartite compose content")
	}
	if profile.SurfaceID != "workflow_compose" {
		t.Fatalf("surface = %q", profile.SurfaceID)
	}
}

func TestSessionPromptCacheSecondIterationOmitsRunContext(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts:          pe,
		Injects:          prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: stubCoordCtx{ctx: api.CoordinatorRunContext{WorkflowID: "wf-1", CoordinatorBrief: "b"}},
		PromptToolLister: prompttest.CoordinatorTools,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureSpec, WorkspacePath: t.TempDir()}
	ctx := context.Background()
	rt.BeginPromptTurn("s1", "")
	msgs1, err := rt.BuildCompletionMessages(ctx, sess, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	runCtxCount1 := countRunContextBlocks(msgs1)
	turn := rt.Assembly().Cache().LoadTurn("s1")
	turn.Iteration = 1
	msgs2, err := rt.BuildCompletionMessages(ctx, sess, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	runCtxCount2 := countRunContextBlocks(msgs2)
	if runCtxCount2 >= runCtxCount1 && runCtxCount1 > 0 {
		t.Fatalf("iteration 2 run context blocks = %d want fewer than %d", runCtxCount2, runCtxCount1)
	}
}

func TestAssemblyImplementSpawnInjectWithoutWorkflow(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: stubCoordCtx{ctx: api.CoordinatorRunContext{
			AllowedAgents: spawn.AmbientAllowedAgents(),
		}},
		PromptToolLister: prompttest.CoordinatorTools,
		WorkspaceRoots:   stubWorkspaceRootsOne(),
		LoadedTools:      workerToolsLoaded,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	workspace := t.TempDir()
	testutil.FailErr(t, "write project fixture", os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# fixture\n"), 0o600))
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: workspace}
	msgs, err := rt.BuildCompletionMessages(context.Background(), sess, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Content, inject.ImplementSpawnInjectSentinel) {
			found = true
			if !strings.Contains(m.Content, "`implementer`") {
				t.Fatalf("spawn inject missing implementer: %q", m.Content)
			}
		}
		if strings.Contains(m.Content, inject.ActiveWorkflowInjectSentinel) {
			t.Fatal("implement-default must not render active-workflow inject")
		}
	}
	if !found {
		t.Fatalf("messages missing implement-spawn inject: %+v", msgs)
	}
}

func TestAssemblyWorkflowSessionIncludesSpawnRoster(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: stubCoordCtx{ctx: api.CoordinatorRunContext{
			WorkflowID:    "implement",
			CurrentPhase:  "work",
			AllowedAgents: spawn.AmbientAllowedAgents(),
		}},
		PromptToolLister: prompttest.CoordinatorTools,
		WorkspaceRoots:   stubWorkspaceRootsOne(),
		LoadedTools:      workerToolsLoaded,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	workspace := t.TempDir()
	testutil.FailErr(t, "write project fixture", os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# fixture\n"), 0o600))
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: workspace}
	msgs, err := rt.BuildCompletionMessages(context.Background(), sess, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	var spawnFound, workflowFound bool
	for _, m := range msgs {
		if strings.Contains(m.Content, inject.ImplementSpawnInjectSentinel) {
			spawnFound = true
			for _, want := range []string{"Spawn roster", "Pool:", "DISALLOWED_AGENT"} {
				if !strings.Contains(m.Content, want) {
					t.Fatalf("spawn roster missing %q on workflow session: %q", want, m.Content)
				}
			}
		}
		if strings.Contains(m.Content, inject.ActiveWorkflowInjectSentinel) {
			workflowFound = true
		}
	}
	if !workflowFound {
		t.Fatal("workflow session missing active-workflow inject")
	}
	if !spawnFound {
		t.Fatalf("workflow session missing spawn roster (the dead-context bug): %+v", msgs)
	}
}

func TestAssemblyImplementSpawnInjectCacheSecondIteration(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: stubCoordCtx{ctx: api.CoordinatorRunContext{
			AllowedAgents: spawn.AmbientAllowedAgents(),
		}},
		PromptToolLister: prompttest.CoordinatorTools,
		WorkspaceRoots:   stubWorkspaceRootsOne(),
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	ctx := context.Background()
	rt.BeginPromptTurn("s1", "")
	msgs1, err := rt.BuildCompletionMessages(ctx, sess, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	count1 := countImplementSpawnBlocks(msgs1)
	turn := rt.Assembly().Cache().LoadTurn("s1")
	turn.Iteration = 1
	msgs2, err := rt.BuildCompletionMessages(ctx, sess, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	count2 := countImplementSpawnBlocks(msgs2)
	if count2 >= count1 && count1 > 0 {
		t.Fatalf("iteration 2 implement-spawn blocks = %d want fewer than %d", count2, count1)
	}
}

func countImplementSpawnBlocks(msgs []api.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, inject.ImplementSpawnInjectSentinel) {
			n++
		}
	}
	return n
}

type stubCoordCtx struct{ ctx api.CoordinatorRunContext }

func (s stubCoordCtx) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: s.ctx}, nil
}

func countRunContextBlocks(msgs []api.Message) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, inject.ActiveWorkflowInjectSentinel) {
			n++
		}
	}
	return n
}

func TestAssemblyWorkerLegInjectUsesRenderer(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	boardEng := assembly.NewBoardEngine(stubBoardBuilder{hash: "abc"}, stubBoardFormatter{}, nil)
	boardEng.SetInjectRenderer(prompts.NewInjectRenderer(pe))
	deps := assembly.AssemblyDeps{
		Prompts:       pe,
		Injects:       prompts.NewInjectRenderer(pe),
		Limits:        func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerContext: stubWorkerCtx{leg: inject.WorkerLegContext{LegID: "leg-1", PhaseID: "implement", Checklist: []string{"step"}}},
		Board:         boardEng,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	child := &api.Session{ID: "child-1", ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, Posture: api.SessionPostureBuild, WorkspacePath: dir}
	msgs, err := rt.BuildCompletionMessages(context.Background(), child, nil, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	foundLeg := false
	foundBoard := false
	boardIdx, legIdx := -1, -1
	for i, m := range msgs {
		if strings.Contains(m.Content, inject.WorkerLegInjectSentinel) {
			foundLeg = true
			legIdx = i
		}
		if strings.Contains(m.Content, "pack-board:v1") {
			foundBoard = true
			boardIdx = i
		}
	}
	if !foundLeg {
		t.Fatalf("messages missing worker-leg inject: %+v", msgs)
	}
	if !foundBoard {
		t.Fatalf("messages missing pack board inject: %+v", msgs)
	}
	if boardIdx < 0 || legIdx < 0 || boardIdx >= legIdx {
		t.Fatalf("pack board must precede leg context: boardIdx=%d legIdx=%d", boardIdx, legIdx)
	}
}

func TestLoopScheduleLegFinished(t *testing.T) {
	engine := loopwake.NewLoopEngine()
	kicked := false
	deps := loopwake.LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{Status: api.SessionStatusBusy}, nil
	}
	deps.WorkflowSource = stubLoopWF{
		run: &api.WorkflowRun{ID: "run-1", Status: api.WorkflowRunStatusRunning, CurrentPhase: "implement"},
	}
	var informed anchor.Envelope
	deps.QueueInform = func(_ context.Context, _ string, _ anchor.ID, env anchor.Envelope) {
		kicked = true
		informed = env
	}
	engine.SetDeps(deps)
	engine.NudgeLegFinished(context.Background(), "s1", time.Now(), "leg-1")
	if !kicked {
		t.Fatal("expected leg finished kick while session busy")
	}
	// The kick names the leg so a recall for omitted detail can address it.
	if got, _ := informed.Vars["leg_id"].(string); got != "leg-1" {
		t.Fatalf("leg finished envelope leg_id = %q, want leg-1", got)
	}
}

func TestQueueEagerDelivery(t *testing.T) {
	engine := kickEngineWithPrompts(t)
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "LoadRegistryFromConfigRoot", err)
	bus := anchor.NewBus(engine)
	bus.SetRegistry(reg)
	bus.EmitEager(t.Context(), "child-1", anchor.WorkerLegStarted, map[string]string{"leg_id": "leg-a"})
	text, _, ok, _ := engine.RenderPendingNudge(t.Context(), "child-1", kick.CoordinatorKickRenderContext{})
	if !ok || !strings.Contains(text, "leg-a") {
		t.Fatalf("nudge=%q ok=%v", text, ok)
	}
}

func TestBoardInjectHashViaAssembly(t *testing.T) {
	root := kickTestRoot(t)
	boardEng := assembly.NewBoardEngine(stubBoardBuilder{hash: "xyz"}, stubBoardFormatter{}, nil)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	deps := assembly.AssemblyDeps{
		Prompts:          pe,
		Injects:          prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: stubCoordCtx{ctx: api.CoordinatorRunContext{WorkflowID: "plan", CurrentPhase: "stub"}},
		Board:            boardEng,
		PromptToolLister: prompttest.CoordinatorTools,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	sess := &api.Session{ID: "s1", ProjectID: testdbseed.DefaultProjectID, Posture: api.SessionPostureSpec, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	if err := os.WriteFile(filepath.Join(sess.WorkspacePath, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	ctx := context.Background()
	rt.BeginPromptTurn("s1", "")
	if _, err := rt.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	}
	if hash := boardEng.BoardInjectHash("s1"); hash == "" {
		t.Fatal("expected inject cache key after first assembly")
	}
	firstKey := boardEng.BoardInjectHash("s1")
	rt.BeginPromptTurn("s1", "")
	if _, err := rt.BuildCompletionMessages(ctx, sess, nil, nil); err != nil {
		testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	}
	if boardEng.BoardInjectHash("s1") != firstKey {
		t.Fatalf("cache key = %q want stable %q", boardEng.BoardInjectHash("s1"), firstKey)
	}
}

type stubAgentProfiles map[string]agentdef.Profile

func (s stubAgentProfiles) Get(id string) (agentdef.Profile, error) {
	p, ok := s[id]
	if !ok {
		return agentdef.Profile{}, fmt.Errorf("agent profile %q not found", id)
	}
	return p, nil
}

func TestResolveSystemPromptTemplateAgentPrecedence(t *testing.T) {
	agents := stubAgentProfiles{
		"custom":      {SystemPromptTemplate: "agents/custom.md"},
		"coordinator": {SystemPromptTemplate: "agents/coordinator-core.md"},
	}
	ref := assembly.ResolveSystemPromptTemplate(&api.Session{AgentType: "custom"}, agents, "")
	if ref != "agents/custom.md" {
		t.Fatalf("ref = %q", ref)
	}
	ref = assembly.ResolveSystemPromptTemplate(&api.Session{}, agents, "custom")
	if ref != "agents/custom.md" {
		t.Fatalf("coordinator profile ref = %q", ref)
	}
}

func TestIsCoordinatorSessionUsesAgentType(t *testing.T) {
	worker := &api.Session{AgentType: "implementer"}
	if surface.IsCoordinatorSession(worker) {
		t.Fatal("worker agent type must not classify as coordinator")
	}
	if !surface.IsCoordinatorSession(&api.Session{AgentType: "coordinator"}) {
		t.Fatal("coordinator agent type must classify as coordinator")
	}
	if surface.IsCoordinatorSession(nil) {
		t.Fatal("nil session")
	}
}

func TestRuntimeNilSafeAccessors(t *testing.T) {
	var rt *coordinator.Runtime
	if rt.Kicks() == nil {
		t.Fatal("expected kick engine")
	}
	if rt.Board() == nil {
		t.Fatal("expected board engine")
	}
	if rt.CoordinatorLoop() == nil {
		t.Fatal("expected coordinator loop engine")
	}
	var eng *assembly.AssemblyEngine
	if eng.Cache() == nil {
		t.Fatal("expected assembly cache")
	}
}

func TestAnchorDedupLegFinishedOmitsBoard(t *testing.T) {
	if !anchor.OmitInformWhenBoardReinjected(anchor.LegFinished) {
		t.Fatal("leg.finished must omit inform when board reinjected")
	}
}

func TestFormatKickRelativeAgoUnits(t *testing.T) {
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	if got := kick.FormatKickRelativeAgo(now, now.Add(-30*time.Second)); got != "just now" {
		t.Fatalf("got %q", got)
	}
	if got := kick.FormatKickRelativeAgo(now, now.Add(-2*time.Hour)); got != "2h ago" {
		t.Fatalf("got %q", got)
	}
	if got := kick.FormatKickRelativeAgo(now, now.Add(-48*time.Hour)); got != "2d ago" {
		t.Fatalf("got %q", got)
	}
}

func TestTransitionInjectOncePerPromptTurn(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	store := surface.NewExecutionModeStateStore()
	store.Save("s1", surface.ExecutionModeFamilyInvestigate)
	deps := assembly.AssemblyDeps{
		Prompts: pe,
		Injects: prompts.NewInjectRenderer(pe),
		Limits:  func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		LoadExecutionModeState: func(_ context.Context, sessionID string) surface.ExecutionModeState {
			return store.Load(sessionID)
		},
		SaveExecutionModeState: func(_ context.Context, sessionID, family string) {
			store.Save(sessionID, family)
		},
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{WorkersInFlight: 1, PendingOverlayIDs: []string{}}
		},
		PromptToolLister: prompttest.CoordinatorTools,
	}
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{AssemblyDeps: func() assembly.AssemblyDeps { return deps }})
	// AgentType enables coordinator tail injects.
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir(), AgentType: "coordinator"}
	ctx := context.Background()
	rt.BeginPromptTurn("s1", "")
	rt.PushModeTransitionCause("s1", surface.ModeTransitionCause{
		Kind: surface.ModeTransitionCauseWorkflowDefault,
		Mode: surface.ExecutionModeFamilyOrchestrate,
	})
	history := []api.Message{{Role: api.MessageRoleUser, Content: "fix auth in src/auth.go"}}
	msgs1, err := rt.BuildCompletionMessages(ctx, sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}
	transitionBlocks1 := countTransitionBlocks(msgs1)
	if transitionBlocks1 != 1 {
		t.Fatalf("first assembly transition blocks = %d, want 1", transitionBlocks1)
	}
	turn := rt.Assembly().Cache().LoadTurn("s1")
	turn.Iteration = 1
	msgs2, err := rt.BuildCompletionMessages(ctx, sess, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages retry: %v", err)
	}
	transitionBlocks2 := countTransitionBlocks(msgs2)
	if transitionBlocks2 != 1 {
		t.Fatalf("retry transition blocks = %d, want 1 (dedup)", transitionBlocks2)
	}
}

func countTransitionBlocks(msgs []api.Message) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleSystem {
			continue
		}
		if strings.Contains(msg.Content, "## Entered orchestrate") {
			n++
		}
	}
	return n
}

// workerToolsLoaded models a turn whose load decision offered the worker
// tools on the investigate surface, so the spawn roster has a task schema
// to accompany.
func workerToolsLoaded(string) map[string]bool {
	return map[string]bool{"task": true, "pack_board": true, "worker_cancel": true, "answer_decision": true, "extend_worker_budget": true}
}
