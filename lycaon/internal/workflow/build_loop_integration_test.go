//go:build integration

package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestImplementBuildLoopClosure exercises boot→build and build→build re-enter after workers.
func TestImplementBuildLoopClosure(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "build-loop.db")

	store := store.NewSQL(sqlDB)
	bundledDir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfStore := workflowpersistence.New(sqlDB)
	mgr := NewManager(wfStore, store, reg, nil)
	q := worker.NewInMemoryQueue(4)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		WorkerCycleIdle: func(projectID, sessionID, completingJobID string) (bool, error) {
			return workeroutcomes.ParentSessionWorkerCycleIdle(ctx, q, testdbseed.DefaultProjectID, sessionID, completingJobID)
		},
	})
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(condReg)

	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	run, err := mgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	if run.CurrentPhase != "boot" {
		t.Fatalf("start phase = %q want boot", run.CurrentPhase)
	}

	testutil.FailErr(t, "RecordBoardOrientReady", mgr.Fanout.RecordBoardOrientReady(ctx, sess.ID, "fp-1"))
	run, err = mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if run.CurrentPhase != "work" {
		t.Fatalf("after boot: phase = %q want work", run.CurrentPhase)
	}
	frame, err := (&workflowruntime.CoordinatorFrames{Runs: mgr.Store.Runs, Resolver: &mgr.Resolver, Snapshots: mgr.Snapshots, Policy: mgr.Policy, Obligations: mgr.Obligations}).BuildCoordinatorTurnFrame(ctx, sess.ID, sess)
	testutil.FailErr(t, "BuildCoordinatorTurnFrame", err)
	for _, phase := range frame.Runtime.Phases {
		if phase.ID != "work" || len(phase.Gates) != 1 {
			continue
		}
		if !phase.Gates[0].Dormant {
			t.Fatalf("idle worker-cycle gate must be dormant: %+v", phase.Gates[0])
		}
	}
	if len(frame.RunContext.FailedLeaves) != 0 {
		t.Fatalf("idle worker cycle projected as failed gate: %+v", frame.RunContext.FailedLeaves)
	}

	if err := mgr.Fanout.RecordWorkerTerminalProof(ctx, sess.ID, "job-1", "complete"); err != nil {
		testutil.FailErr(t, "RecordWorkerTerminalProof", err)
	}
	run, err = mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if run.CurrentPhase != "work" {
		t.Fatalf("after worker: phase = %q want work (re-enter)", run.CurrentPhase)
	}

	vars, err := wfStore.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	wc := vars["worker_cycle"]
	if wc != nil {
		t.Fatalf("worker_cycle vars should be cleared after build re-enter, got %v", wc)
	}
}

// TestBuildPhaseSamePhaseReenterFiresReenterLeg pins build→build scheduling reenter_leg.
func TestBuildPhaseSamePhaseReenterFiresReenterLeg(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{
		ID:        "work",
		OnReenter: workflowdef.PhaseOnReenter{ReenterLeg: "implement-work:{session_id}"},
	}}}
	leg, ok := workflowphases.ReenterLegForAdvance(manifest, "work", "work", "sess-1")
	if !ok || leg != "implement-work:sess-1" {
		t.Fatalf("workflowphases.ReenterLegForAdvance = %q ok=%v", leg, ok)
	}
	if leg, ok := workflowphases.ReenterLegForAdvance(manifest, "boot", "work", "sess-1"); ok {
		t.Fatalf("boot→work must not schedule leg loop wake, got %q", leg)
	}
}
