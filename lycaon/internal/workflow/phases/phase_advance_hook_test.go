package phases_test

import (
	workflow "github.com/lycaon/lycaon/internal/workflow"

	"context"
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

func TestTryAutoAdvanceInvokesPhaseAdvancedHook(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "auto-advance-hook.db")

	store := store.NewSQL(sqlDB)
	hookManifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "hook-test",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "stub", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"plan_stub_valid"}, Next: "done"},
			{ID: "done"},
		},
	})
	manifestReg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey(hookManifest.ID, hookManifest.Version): hookManifest,
	})
	wfStore := workflowpersistence.New(sqlDB)
	mgr := workflow.NewManager(wfStore, store, manifestReg, nil)
	dir := t.TempDir()
	workflow.WireBlueprintDepsForTest(mgr, dir)

	ctx := context.Background()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	run, err := mgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "hook-test", WorkflowVersion: "1.0.0"})
	testutil.FailErr(t, "mgr.StartHuman failed", err)
	if run.CurrentPhase != "stub" {
		t.Fatalf("phase = %q want stub", run.CurrentPhase)
	}

	var hooked bool
	mgr.Publication.OnPhaseAutoAdvanced = func(_ context.Context, sessionID, runID, previousPhase, newPhase string) {
		if sessionID != sess.ID || runID != run.ID {
			t.Fatalf("hook session=%q run=%q", sessionID, runID)
		}
		if previousPhase != "stub" || newPhase != "done" {
			t.Fatalf("phases %q -> %q", previousPhase, newPhase)
		}
		hooked = true
	}

	vars, err := wfStore.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "wfStore.Runs.GetScaffoldVars failed", err)
	vars = runstate.SetGateSatisfied(vars, "plan_stub_valid", true)
	if err := wfStore.State.UpdateVars(ctx, run, sess.WorkspacePath, vars); err != nil {
		testutil.FailErr(t, "wfStore.State.UpdateVars failed", err)
	}

	if _, err := mgr.Phases.TryAutoAdvance(ctx, run.ID); err != nil {
		testutil.FailErr(t, "mgr.Phases.TryAutoAdvance failed", err)
	}
	if !hooked {
		t.Fatal("expected OnPhaseAutoAdvanced hook")
	}
	updated, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "mgr.Get failed", err)
	if updated.CurrentPhase != "done" {
		t.Fatalf("phase = %q", updated.CurrentPhase)
	}
}
