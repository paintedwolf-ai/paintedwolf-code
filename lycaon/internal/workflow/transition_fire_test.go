package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func choiceTransitionsTestMgr(t *testing.T) (*RunManager, *store.SQL, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "choice-tr.db")

	path := filepath.Join("..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	m = workflowdef.FinalizeManifest(m)

	sessStore := store.NewSQL(sqlDB)
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(m.ID, m.Version): m})
	wfStore := NewSQLStore(sqlDB)
	mgr := NewManager(wfStore, sessStore, reg, nil)
	dir := t.TempDir()
	WireBlueprintDepsForTest(mgr, dir)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	return mgr, sessStore, dir
}

func TestFireTransitionTwoArms(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "decide" {
		t.Fatalf("phase = %q want decide", run.CurrentPhase)
	}

	out, err := mgr.FireTransition(ctx, run.ID, "deepen", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition deepen", err)
	if out.CurrentPhase != "research_more" {
		t.Fatalf("phase = %q want research_more", out.CurrentPhase)
	}

	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = SetGateSatisfied(vars, "research_satisfied", true)
	testutil.FailErr(t, "UpdateVars", mgr.Store.UpdateVars(ctx, out, sess.WorkspacePath, vars))
	out, err = mgr.Advance(ctx, run.ID)
	testutil.FailErr(t, "Advance back", err)
	if out.CurrentPhase != "decide" {
		t.Fatalf("phase = %q want decide", out.CurrentPhase)
	}

	out, err = mgr.FireTransition(ctx, run.ID, "critique", workflowdef.TransitionActorCoordinator)
	testutil.FailErr(t, "FireTransition critique", err)
	if out.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", out.CurrentPhase)
	}
}

func TestFireTransitionClearSetAndDepthSkipSuppress(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = SetHostVar(vars, "params.review_depth", "none")
	vars = SetGateSatisfied(vars, "evidence_passed:fixture_review", true)
	vars = SetHostVar(vars, "phase_skipped.review", true)
	vars = SetHostVar(vars, reviewLoopAttemptPath("review"), "2")
	vars = SetGateSatisfied(vars, "human_approval", true)
	testutil.FailErr(t, "UpdateVars", mgr.Store.UpdateVars(ctx, run, sess.WorkspacePath, vars))

	out, err := mgr.FireTransition(ctx, run.ID, "critique", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition", err)
	if out.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", out.CurrentPhase)
	}
	vars, err = mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after", err)
	if ReviewLoopAttempt(vars, "review") != 0 {
		t.Fatalf("attempt = %d want 0", ReviewLoopAttempt(vars, "review"))
	}
	if conditions.DotPathTruthy(vars, "phase_skipped.review") {
		t.Fatal("phase_skipped.review still set after choice entry")
	}
	gates, _ := vars["gates"].(map[string]any)
	if ok, _ := gates["evidence_passed:fixture_review"].(bool); ok {
		t.Fatal("evidence_passed:fixture_review still satisfied — depth-skip must not auto-satisfy on choice entry")
	}
	if ok, _ := gates["human_approval"].(bool); !ok {
		t.Fatal("source human_approval gate must remain")
	}
}

func TestClearChoiceEntryProofsRequiresFreshFanoutPlan(t *testing.T) {
	vars := stampFanoutPlan(nil, FanoutPlan{
		Legs: []FanoutPlanLeg{{AgentType: "path-explorer", Prompt: "Map the initial scope"}},
	})
	vars = SetGateSatisfied(vars, "fanout_planned", true)

	cleared := clearChoiceEntryProofs(vars, workflowdef.PhaseDef{
		ID:    "drill_plan",
		Gates: []string{"fanout_planned"},
	})
	gates, _ := cleared["gates"].(map[string]any)
	if planned, _ := gates["fanout_planned"].(bool); planned {
		t.Fatal("choice entry retained fanout_planned satisfaction")
	}
}

func TestFireTransitionActorACL(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	_, err = mgr.FireTransition(ctx, run.ID, "side_quest", workflowdef.TransitionActorCoordinator)
	if !errors.Is(err, ErrTransitionActorDenied) {
		t.Fatalf("err = %v want ErrTransitionActorDenied", err)
	}
}

func TestFireTransitionPromptCoordinatorStillRuns(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := mgr.FireTransition(ctx, run.ID, "side_quest", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition side_quest", err)
	if out.CurrentPhase != "side" {
		t.Fatalf("phase = %q want side", out.CurrentPhase)
	}
}

func TestFireTransitionIntoTerminalCompletesWithoutCoordinatorHook(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "terminal-choice",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "choose", Transitions: []workflowdef.PhaseTransitionDef{{
				ID: "finish", To: "done", Actors: []string{workflowdef.TransitionActorHuman},
			}}},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest})
	run, err := startRun(context.Background(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "startRun", err)

	completedCount := 0
	mgr.OnRunCompleted = func(_ context.Context, completed *api.WorkflowRun) {
		completedCount++
		if completed.ID != run.ID || completed.Status != api.WorkflowRunStatusComplete {
			t.Fatalf("unexpected completion: %+v", completed)
		}
	}
	phaseWakeCount := 0
	phaseEnterCount := 0
	mgr.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) { phaseWakeCount++ }
	mgr.PhaseEnterHook = func(context.Context, *RunContext, workflowdef.PhaseDef) { phaseEnterCount++ }
	completed, err := mgr.FireTransition(context.Background(), run.ID, "finish", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition finish", err)
	if completed.Status != api.WorkflowRunStatusComplete || completed.CurrentPhase != "done" {
		t.Fatalf("terminal transition = status %q phase %q", completed.Status, completed.CurrentPhase)
	}
	if completed.CompletedAt == nil || completed.EndMessageID == "" {
		t.Fatalf("terminal transition missing completion metadata: %+v", completed)
	}
	if completedCount != 1 {
		t.Fatalf("completion notifications = %d, want 1", completedCount)
	}
	if phaseWakeCount != 0 || phaseEnterCount != 0 {
		t.Fatalf("terminal transition callbacks: phase=%d enter=%d", phaseWakeCount, phaseEnterCount)
	}
}

// Phase entry hooks observe the persisted target phase.
func TestFireTransitionPersistsPhaseBeforeEnterHook(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	var hookPhases []string
	mgr.PhaseEnterHook = func(ctx context.Context, rc *RunContext, _ workflowdef.PhaseDef) {
		stored, err := mgr.Get(ctx, rc.RunID)
		testutil.FailErr(t, "Get run inside PhaseEnterHook", err)
		hookPhases = append(hookPhases, stored.CurrentPhase)
	}

	out, err := mgr.FireTransition(ctx, run.ID, "deepen", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition deepen", err)
	if out.CurrentPhase != "research_more" {
		t.Fatalf("phase = %q want research_more", out.CurrentPhase)
	}
	if len(hookPhases) != 1 || hookPhases[0] != "research_more" {
		t.Fatalf("stored phase at PhaseEnterHook = %v want [research_more] — target phase must be persisted before the wake fires", hookPhases)
	}
}
