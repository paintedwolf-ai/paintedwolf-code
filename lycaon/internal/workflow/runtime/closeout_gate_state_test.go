package runtime_test

import (
	workflow "github.com/lycaon/lycaon/internal/workflow"

	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func newCloseoutGateHarness(t *testing.T) (*workflow.RunManager, *api.Session, *api.WorkflowRun) {
	t.Helper()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "closeout-gate.db")

	sessionStore := store.NewSQL(sqlDB)
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := sessionStore.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessionStore.Create", err)

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	mgr := workflow.NewManager(workflowpersistence.New(sqlDB), sessionStore, manifestRegistry, nil)
	workflow.WireBlueprintDepsForTest(mgr, dir)

	// plan@ research declares gated closeout with research_satisfied.
	run, err := startRun(ctx, mgr, sess.ID, "plan", "1.0.0")
	testutil.FailErr(t, "start plan run", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research", run.CurrentPhase)
	}
	return mgr, sess, run
}

func TestActiveCloseoutGateStateReportsOpenLeaves(t *testing.T) {
	mgr, sess, run := newCloseoutGateHarness(t)
	ctx := context.Background()

	state, stateErr := mgr.Policy.ActiveCloseoutGateState(ctx, sess.ID)
	testutil.FailErr(t, "read closeout gates", stateErr)
	if !state.Gated || state.Phase != "research" {
		t.Fatalf("state = %+v want gated research", state)
	}
	if len(state.OpenLeaves) != 1 || state.OpenLeaves[0] != "research_satisfied" {
		t.Fatalf("open leaves = %v want [research_satisfied]", state.OpenLeaves)
	}

	// A satisfied gate clears the state without leaving the phase.
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetGateSatisfied(vars, "research_satisfied", true)
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, "", vars))
	if state, err := mgr.Policy.ActiveCloseoutGateState(ctx, sess.ID); err != nil || state.Gated {
		t.Fatalf("state = %+v want cleared after the gate stamped", state)
	}
}

func TestActiveCloseoutGateStateClearedByPendingUserInput(t *testing.T) {
	mgr, sess, run := newCloseoutGateHarness(t)
	ctx := context.Background()

	// A pending coordinator→human wait means the run is parked, not stalled.
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.SetFeedbackPending(vars, "ask-test", "Which direction?")
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, "", vars))
	if state, err := mgr.Policy.ActiveCloseoutGateState(ctx, sess.ID); err != nil || state.Gated {
		t.Fatalf("state = %+v want cleared while an ask is pending", state)
	}
}

func TestReviewPhaseRequiresStructuredCompletion(t *testing.T) {
	mgr, sess, run := newCloseoutGateHarness(t)
	run.CurrentPhase = "review"
	testutil.FailErr(t, "enter review fixture", mgr.Store.State.Update(t.Context(), run))
	var incomplete *workflowfacts.IncompleteCloseoutError
	if err := mgr.Policy.AssertSessionCompletion(t.Context(), sess.ID); !errors.As(err, &incomplete) {
		t.Fatalf("review completion=%v", err)
	}
	if incomplete.State.Phase != "review" {
		t.Fatalf("phase=%s", incomplete.State.Phase)
	}
}

type unavailableCloseoutRuns struct {
	runstate.RunsRepository
	failure error
	atVars  bool
}

func (r unavailableCloseoutRuns) ActiveBySession(ctx context.Context, id string) (*api.WorkflowRun, error) {
	if !r.atVars {
		return nil, r.failure
	}
	return r.RunsRepository.ActiveBySession(ctx, id)
}
func (r unavailableCloseoutRuns) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return nil, r.failure
}

type unavailableCloseoutGates struct{ failure error }

func (g unavailableCloseoutGates) PhaseGateMet(context.Context, workflowdef.Manifest, *api.WorkflowRun, map[string]any) (bool, workflowgates.GateCheckResult, error) {
	return false, workflowgates.GateCheckResult{}, g.failure
}

func TestCompletionStateErrorsNeverAuthorizeCloseout(t *testing.T) {
	for _, at := range []string{"run", "vars", "gates", "phase"} {
		t.Run(at, func(t *testing.T) {
			mgr, sess, run := newCloseoutGateHarness(t)
			failure := errors.New("completion ledger unavailable")
			switch at {
			case "run", "vars":
				mgr.Policy.Runs = unavailableCloseoutRuns{RunsRepository: mgr.Store.Runs, failure: failure, atVars: at == "vars"}
			case "gates":
				mgr.Policy.Gates = unavailableCloseoutGates{failure: failure}
			case "phase":
				run.CurrentPhase = "missing"
				testutil.FailErr(t, "set unavailable phase", mgr.Store.State.Update(t.Context(), run))
			}
			err := mgr.Policy.AssertSessionCompletion(t.Context(), sess.ID)
			if err == nil || at != "phase" && !errors.Is(err, failure) {
				t.Fatalf("completion error=%v", err)
			}
		})
	}
}
