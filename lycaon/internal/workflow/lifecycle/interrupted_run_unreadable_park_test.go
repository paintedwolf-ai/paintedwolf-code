package lifecycle_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowlifecycle "github.com/lycaon/lycaon/internal/workflow/lifecycle"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// scaffoldVarsFailingStore fails exactly the read hasDurableHumanWait depends
// on, leaving every other store operation intact.
type scaffoldVarsFailingStore struct {
	workflowlifecycle.RunReader
	err error
}

func (s scaffoldVarsFailingStore) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return nil, s.err
}

// Unreadable park state leaves a run intact because interruption is irreversible.
func TestReconcileLeavesRunIntactWhenParkIsUnreadable(t *testing.T) {
	mgr, store, _, projectDir := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Recovery.Busy = func(context.Context, string) bool { return false }

	run := &api.WorkflowRun{
		ID: "run-unreadable-park", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "approve",
	}
	testutil.FailErr(t, "create run", mgr.Store.State.CreateState(ctx, run, projectDir, nil))

	// Everything else keeps working; only the park read fails.
	mgr.Recovery.Runs = scaffoldVarsFailingStore{RunReader: mgr.Recovery.Runs, err: errors.New("scaffold vars read failed")}
	testutil.FailErr(t, "reconcile", mgr.Recovery.ReconcileOrphanedRuns(ctx, sess.ID))

	got, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("status = %q want running: an unreadable park must not be interrupted", got.Status)
	}
}

// A readable, absent park state interrupts the run.
func TestReconcileStillInterruptsWhenParkIsReadableAndAbsent(t *testing.T) {
	mgr, store, _, projectDir := testManager(t)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Recovery.Busy = func(context.Context, string) bool { return false }

	run := &api.WorkflowRun{
		ID: "run-readable-absent", SessionID: sess.ID, WorkflowID: "plan",
		WorkflowVersion: "1.0.0", Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot",
	}
	testutil.FailErr(t, "create run", mgr.Store.State.CreateState(ctx, run, projectDir, nil))
	testutil.FailErr(t, "reconcile", mgr.Recovery.ReconcileOrphanedRuns(ctx, sess.ID))

	got, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if got.Status != api.WorkflowRunStatusInterrupted {
		t.Fatalf("status = %q want interrupted for a readable, unparked run", got.Status)
	}
}
