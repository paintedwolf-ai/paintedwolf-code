package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type teardownStopStub struct {
	err         error
	workers     int
	runningOnly int
	held        int
	delegations int
}

func (s *teardownStopStub) CancelWorkersByRunID(context.Context, string, string) error {
	s.workers++
	return s.err
}

func (s *teardownStopStub) SettleWorkerCancellationsByRunID(context.Context, string, string) error {
	s.runningOnly++
	return s.err
}

func (s *teardownStopStub) HoldPendingWorkersByRunID(context.Context, string) error {
	s.held++
	return s.err
}

func (s *teardownStopStub) CancelDelegationsByRunID(context.Context, string) error {
	s.delegations++
	return s.err
}

// overrideTestManifest replaces one registered manifest in a fixed registry.
func overrideTestManifest(mgr *workflow.RunManager, manifest workflowdef.Manifest) {
	entries := mgr.Resolver.Overlay.All()
	entries[workflowdef.ManifestKey(manifest.ID, manifest.Version)] = manifest
	mgr.Resolver.Overlay = workflowdef.NewRegistry(entries)
}

func TestPauseHonorsManifestWorkerControls(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	manifest, err := mgr.Resolver.Overlay.Get("options", "1.0.0")
	testutil.FailErr(t, "Get options manifest", err)
	manifest.Controls.OnPause = &workflowdef.PauseControls{HoldPending: false, CancelRunning: true}
	overrideTestManifest(mgr, manifest)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "options", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	stop := &teardownStopStub{}
	mgr.Controls.Cleanup.Workers = stop
	_, err = mgr.Controls.Pause(ctx, run.ID, "operator pause")
	testutil.FailErr(t, "Pause", err)
	if stop.runningOnly != 1 || stop.held != 0 {
		t.Fatalf("pause calls running=%d held=%d, want running-only cancellation", stop.runningOnly, stop.held)
	}
}

func TestExitPersistsAndRecoversTeardown(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	failing := &teardownStopStub{err: errors.New("teardown unavailable")}
	mgr.Controls.Cleanup.Workers = failing
	_, err = mgr.Controls.Exit(ctx, sessionID, run.ID, run.Revision, "user_exit")
	testutil.FailErr(t, "Exit", err)
	pending, err := mgr.Store.Teardowns.PendingTeardowns(ctx)
	testutil.FailErr(t, "PendingTeardowns", err)
	if len(pending) != 1 || pending[0].RunID != run.ID {
		t.Fatalf("pending = %+v, want durable teardown for %s", pending, run.ID)
	}

	recovered := &teardownStopStub{}
	mgr.Controls.Cleanup.Workers = recovered
	testutil.FailErr(t, "RecoverTeardownOperations", mgr.Controls.Cleanup.Recover(ctx))
	pending, err = mgr.Store.Teardowns.PendingTeardowns(ctx)
	testutil.FailErr(t, "PendingTeardowns after recovery", err)
	if len(pending) != 0 {
		t.Fatalf("pending after recovery = %+v", pending)
	}
	if recovered.workers != 1 || recovered.delegations != 1 {
		t.Fatalf("recovered calls workers=%d delegations=%d, want one each", recovered.workers, recovered.delegations)
	}
}

func TestExitHonorsNoTeardownControls(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	manifest, err := mgr.Resolver.Overlay.Get("options", "1.0.0")
	testutil.FailErr(t, "Get options manifest", err)
	manifest.Controls.OnStop = &workflowdef.StopControls{}
	overrideTestManifest(mgr, manifest)

	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	stop := &teardownStopStub{}
	mgr.Controls.Cleanup.Workers = stop
	_, err = mgr.Controls.Exit(ctx, sessionID, run.ID, run.Revision, "user_exit")
	testutil.FailErr(t, "Exit", err)

	pending, err := mgr.Store.Teardowns.PendingTeardowns(ctx)
	testutil.FailErr(t, "PendingTeardowns", err)
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want no teardown", pending)
	}
	if stop.workers != 0 || stop.delegations != 0 {
		t.Fatalf("stop calls workers=%d delegations=%d, want none", stop.workers, stop.delegations)
	}
}

func TestTeardownStaysPendingWithoutService(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	mgr.Controls.Cleanup.Workers = nil
	_, err = mgr.Controls.Exit(ctx, sessionID, run.ID, run.Revision, "user_exit")
	testutil.FailErr(t, "Exit", err)

	if err := mgr.Controls.Cleanup.Recover(ctx); err == nil {
		t.Fatal("RecoverTeardownOperations succeeded without a teardown service")
	}
	pending, err := mgr.Store.Teardowns.PendingTeardowns(ctx)
	testutil.FailErr(t, "PendingTeardowns", err)
	if len(pending) != 1 || pending[0].RunID != run.ID {
		t.Fatalf("pending = %+v, want teardown for %s", pending, run.ID)
	}
}
