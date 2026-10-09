package phases_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"testing"
)

func choiceTransitionsTestMgr(t *testing.T) (*workflow.RunManager, *store.SQL, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "choice-tr.db")

	path := filepath.Join("..", "..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	m = workflowdef.FinalizeManifest(m)

	sessStore := store.NewSQL(sqlDB)
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(m.ID, m.Version): m})
	wfStore := workflowpersistence.New(sqlDB)
	mgr := workflow.NewManager(wfStore, sessStore, reg, nil)
	dir := t.TempDir()
	workflow.WireBlueprintDepsForTest(mgr, dir)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	return mgr, sessStore, dir
}

func TestFireTransitionTwoArms(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)
	if run.CurrentPhase != "decide" {
		t.Fatalf("phase = %q want decide", run.CurrentPhase)
	}

	out, err := mgr.Phases.FireTransition(ctx, run.ID, "deepen", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition deepen", err)
	if out.CurrentPhase != "research_more" {
		t.Fatalf("phase = %q want research_more", out.CurrentPhase)
	}

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetGateSatisfied(vars, "research_satisfied", true)
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, out, sess.WorkspacePath, vars))
	out, err = mgr.Phases.Advance(ctx, run.ID)
	testutil.FailErr(t, "Advance back", err)
	if out.CurrentPhase != "decide" {
		t.Fatalf("phase = %q want decide", out.CurrentPhase)
	}

	out, err = mgr.Phases.FireTransition(ctx, run.ID, "critique", workflowdef.TransitionActorCoordinator)
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
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetHostVar(vars, "params.review_depth", "none")
	vars = runstate.SetGateSatisfied(vars, "evidence_passed:fixture_review", true)
	vars = runstate.SetHostVar(vars, "phase_skipped.review", true)
	vars = runstate.SetHostVar(vars, runstate.ReviewLoopAttemptPath("review"), "2")
	vars = runstate.SetGateSatisfied(vars, "human_approval", true)
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, sess.WorkspacePath, vars))

	out, err := mgr.Phases.FireTransition(ctx, run.ID, "critique", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition", err)
	if out.CurrentPhase != "review" {
		t.Fatalf("phase = %q want review", out.CurrentPhase)
	}
	vars, err = mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after", err)
	if runstate.ReviewLoopAttempt(vars, "review") != 0 {
		t.Fatalf("attempt = %d want 0", runstate.ReviewLoopAttempt(vars, "review"))
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

func TestFireTransitionActorACL(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	_, err = mgr.Phases.FireTransition(ctx, run.ID, "side_quest", workflowdef.TransitionActorCoordinator)
	if !errors.Is(err, runstate.ErrTransitionActorDenied) {
		t.Fatalf("err = %v want runstate.ErrTransitionActorDenied", err)
	}
}

func TestFireTransitionPromptCoordinatorStillRuns(t *testing.T) {
	mgr, sessStore, _ := choiceTransitionsTestMgr(t)
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := mgr.Phases.FireTransition(ctx, run.ID, "side_quest", workflowdef.TransitionActorHuman)
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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest})
	run, err := startRun(context.Background(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "startRun", err)

	completedCount := 0
	mgr.Children.OnRunCompleted = func(_ context.Context, completed *api.WorkflowRun) {
		completedCount++
		if completed.ID != run.ID || completed.Status != api.WorkflowRunStatusComplete {
			t.Fatalf("unexpected completion: %+v", completed)
		}
	}
	phaseWakeCount := 0
	phaseEnterCount := 0
	mgr.Publication.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) { phaseWakeCount++ }
	mgr.Phases.PhaseEnterHook = func(context.Context, *workflowphases.RunContext, workflowdef.PhaseDef) { phaseEnterCount++ }
	completed, err := mgr.Phases.FireTransition(context.Background(), run.ID, "finish", workflowdef.TransitionActorHuman)
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
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "choice-transitions-fixture", WorkflowVersion: "1.0.0",
		Request: "Review the fixture and choose the next step.",
	})
	testutil.FailErr(t, "StartHuman", err)

	var hookPhases []string
	mgr.Phases.PhaseEnterHook = func(ctx context.Context, rc *workflowphases.RunContext, _ workflowdef.PhaseDef) {
		stored, err := mgr.Store.Runs.Get(ctx, rc.RunID)
		testutil.FailErr(t, "Get run inside PhaseEnterHook", err)
		hookPhases = append(hookPhases, stored.CurrentPhase)
	}

	out, err := mgr.Phases.FireTransition(ctx, run.ID, "deepen", workflowdef.TransitionActorHuman)
	testutil.FailErr(t, "FireTransition deepen", err)
	if out.CurrentPhase != "research_more" {
		t.Fatalf("phase = %q want research_more", out.CurrentPhase)
	}
	if len(hookPhases) != 1 || hookPhases[0] != "research_more" {
		t.Fatalf("stored phase at PhaseEnterHook = %v want [research_more] — target phase must be persisted before the wake fires", hookPhases)
	}
}
