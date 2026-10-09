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
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecordWorkerTerminalProofReentersBuildPhase(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "worker-proof.db")

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
	run.CurrentPhase = "work"
	testutil.FailErr(t, "Update", wfStore.State.Update(ctx, run))
	vars, err := wfStore.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	testutil.FailErr(t, "UpdateVars", wfStore.State.UpdateVars(ctx, run, dir, vars))

	if err := mgr.Fanout.RecordWorkerTerminalProof(ctx, sess.ID, "job-1", "complete"); err != nil {
		testutil.FailErr(t, "RecordWorkerTerminalProof", err)
	}
	updated, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if updated.CurrentPhase != "work" {
		t.Fatalf("phase = %q want work (re-enter)", updated.CurrentPhase)
	}
}

func TestRecordWorkerTerminalProofAdvancesBootViaReconWorker(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "orient-proof.db")

	store := store.NewSQL(sqlDB)
	bundledDir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfStore := workflowpersistence.New(sqlDB)
	mgr := NewManager(wfStore, store, reg, nil)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
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
		t.Fatalf("phase = %q want boot", run.CurrentPhase)
	}

	if err := mgr.Fanout.RecordWorkerTerminalProof(ctx, sess.ID, "job-recon-1", "complete"); err != nil {
		testutil.FailErr(t, "RecordWorkerTerminalProof", err)
	}
	updated, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if updated.CurrentPhase != "work" {
		t.Fatalf("phase = %q want work after boot recon worker", updated.CurrentPhase)
	}
}

func TestRecordBoardOrientReadyAdvancesBootToWork(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "board-orient.db")

	store := store.NewSQL(sqlDB)
	bundledDir := filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "workflows")
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfStore := workflowpersistence.New(sqlDB)
	mgr := NewManager(wfStore, store, reg, nil)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
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

	testutil.FailErr(t, "RecordBoardOrientReady", mgr.Fanout.RecordBoardOrientReady(ctx, sess.ID, "fp-orient-1"))
	updated, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if updated.CurrentPhase != "work" {
		t.Fatalf("phase = %q want work after board orient ready", updated.CurrentPhase)
	}
}

func TestReenterLegForAdvanceFromManifestOnReenter(t *testing.T) {
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{
		ID:        "work",
		OnReenter: workflowdef.PhaseOnReenter{ReenterLeg: "implement-work:{session_id}"},
	}}}
	leg, ok := workflowphases.ReenterLegForAdvance(manifest, "work", "work", "sess-1")
	if !ok || leg != "implement-work:sess-1" {
		t.Fatalf("leg = %q ok=%v", leg, ok)
	}
	leg, ok = workflowphases.ReenterLegForAdvance(workflowdef.Manifest{ID: "other"}, "work", "work", "sess-1")
	if ok {
		t.Fatalf("expected no leg without on_reenter.reenter_leg, got %q", leg)
	}
}
