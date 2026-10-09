package contract

import (
	"context"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestInjectBuildersDoNotImportBlueprints(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	targets := []string{
		filepath.Join(root, "lycaon", "internal", "coordinator", "inject", "inject_dto.go"),
		filepath.Join(root, "lycaon", "internal", "coordinator", "inject", "active_workflow_inject.go"),
		filepath.Join(root, "lycaon", "internal", "workflow", "runtime", "coordinator_frames.go"),
		filepath.Join(root, "lycaon", "internal", "workflow", "runtime", "snapshots.go"),
	}
	for _, path := range targets {
		imports, err := goFileImports(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, imp := range imports {
			if strings.Contains(imp, "/internal/blueprint") {
				t.Fatalf("%s imports blueprint package %q", path, imp)
			}
		}
	}
}

func TestDefaultPipelineActiveWorkflowInjectVisible(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "pipeline-inject.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(ctx, agents)
	mgr.Profiles.SetAgentRegistry(agents)
	mgr.SetPromptEngine(contractcheck.BundledPromptEngineForRoot(t))
	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfigStock", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	mgr.SetWorkflowHints(hintCfg, gateCfg)

	sessionWF := workflowdrafts.NewSQL(sqlDB)
	manifestReg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfMgr := workflow.NewManager(workflowpersistence.New(sqlDB), store, manifestReg, nil)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	wfMgr.SetConditionRegistry(reg)
	wfMgr.Resolver.SessionStore = sessionWF
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	frameLoader := &workflowruntime.CoordinatorFrames{Runs: wfMgr.Store.Runs, Resolver: &wfMgr.Resolver, Snapshots: wfMgr.Snapshots, Policy: wfMgr.Policy, Obligations: wfMgr.Obligations, SessionStore: sessionWF}
	mgr.SetCoordinatorTurnFrameSource(frameLoader)

	dir := t.TempDir()
	blueprintMgr := blueprint.NewManager(blueprint.NewFileStoreForTest(dir))
	wfMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	wfMgr.Blueprints.Getter = blueprintMgr
	wfMgr.Presentation.BlueprintGetter = blueprintMgr
	wfMgr.Approvals.Getter = blueprintMgr

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "store.Create failed", err)
	if _, err := wfMgr.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "bugbash", WorkflowVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	run, err := wfMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing active run")
	}
	if _, err := wfMgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected advance blocked before parallel hunt stages complete")
	}

	runCtx, err := mgr.Coordinator.Context.RunContext(ctx, sess.ID)
	contractcheck.FailErr(t, "mgr.CoordinatorRunContext failed", err)
	block := renderWorkflowFrame(t, ctx, frameLoader, sess.ID, surface.StaticWorkflowHintCodes(runCtx, false), hintCfg, gateCfg)
	for _, want := range []string{
		inject.ActiveWorkflowInjectSentinel,
		"bugbash",
		"hunt",
		"failed_leaves",
		"parallel_stages_complete",
		"completes on",
		"Workflow phases",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

// Unsatisfied gate feedback appears in the workflow inject.
func TestPlanResearchObligationsInInject(t *testing.T) {
	ctx, wfMgr, _, blueprintMgr := newBundledPlanWorkflowManager(t)

	hintCfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "LoadHintConfig", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	frameLoader := &workflowruntime.CoordinatorFrames{Runs: wfMgr.Store.Runs, Resolver: &wfMgr.Resolver, Snapshots: wfMgr.Snapshots, Policy: wfMgr.Policy, Obligations: wfMgr.Obligations}

	run, err := wfMgr.Starts.StartHuman(ctx, "sess-posture", wire.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
	})
	contractcheck.FailErr(t, "StartHuman plan", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	seed, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	contractcheck.FailErr(t, "blueprintMgr.Get", err)
	researchPlan := strings.Replace(conditions.TestPlanContentWithTasks, "research_depth: none", "research_depth: light", 1)
	_, err = blueprintMgr.Store.UpdateContent(
		ctx, run.ProjectID, run.BlueprintPath, researchPlan, blueprint.ContentDigest(seed.Content),
	)
	contractcheck.FailErr(t, "seed research plan", err)
	if _, err := wfMgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected research gate to block advance")
	}

	block := renderWorkflowFrame(t, ctx, frameLoader, "sess-posture", nil, hintCfg, gateCfg)
	for _, want := range []string{
		"Phase obligations",
		"research_satisfied",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in inject:\n%s", want, block)
		}
	}

	vars, err := wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	contractcheck.FailErr(t, "GetScaffoldVars", err)
	vars["research_satisfied"] = true
	run, err = wfMgr.Store.Runs.Get(ctx, run.ID)
	contractcheck.FailErr(t, "reload run after rejected advance", err)
	run.CurrentPhase = "research"
	contractcheck.FailErr(t, "CommitState", wfMgr.Store.State.CommitState(ctx, run, "", vars))

	block = renderWorkflowFrame(t, ctx, frameLoader, "sess-posture", nil, hintCfg, gateCfg)
	if idx := strings.Index(block, "### Phase obligations"); idx >= 0 {
		if strings.Contains(block[idx:], "research_satisfied") {
			t.Fatalf("satisfied research_satisfied must leave Phase obligations:\n%s", block[idx:])
		}
	}
}

func renderWorkflowFrame(
	t *testing.T,
	ctx context.Context,
	loader *workflowruntime.CoordinatorFrames,
	sessionID string,
	codes []string,
	hints *guidance.HintConfig,
	gateFeedback *feedback.GateFeedbackCatalog,
) string {
	t.Helper()
	frame, err := loader.BuildCoordinatorTurnFrame(ctx, sessionID, nil)
	contractcheck.FailErr(t, "BuildCoordinatorTurnFrame", err)
	block, err := inject.RenderActiveWorkflowInject(
		ctx,
		prompts.NewInjectRenderer(contractcheck.BundledPromptEngineForRoot(t)),
		"sess-inject-test",
		frame,
		hints,
		codes,
		gateFeedback,
	)
	contractcheck.FailErr(t, "RenderActiveWorkflowInject", err)
	return block
}

func goFileImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out, nil
}
