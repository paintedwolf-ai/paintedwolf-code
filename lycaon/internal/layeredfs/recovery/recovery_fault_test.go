package recovery_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMain(m *testing.M) {
	if runtime.GOMAXPROCS(0) < 2 {
		runtime.GOMAXPROCS(2)
	}
	gittestsetup.Enable()
	os.Exit(m.Run())
}

type recoveryFixture struct {
	t          *testing.T
	baseDir    string
	trunkDir   string
	cpStore    *checkpoint.Store
	gitMgr     *privategit.Manager
	coord      *recovery.Coordinator
	mountedAdp *adapter.MountedAdapter
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	baseDir := t.TempDir()
	trunkDir := filepath.Join(baseDir, "trunk")
	if err := os.MkdirAll(filepath.Join(trunkDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Init(t, trunkDir)
	writeFile(t, filepath.Join(trunkDir, "app.go"), "package main // v0\n")
	writeFile(t, filepath.Join(trunkDir, "keep.txt"), "keep-v0\n")
	writeFile(t, filepath.Join(trunkDir, "sub", "dir.txt"), "dir-v0\n")
	gittest.CommitAll(t, trunkDir, "initial trunk")

	cpStore, err := checkpoint.NewStore(filepath.Join(baseDir, "cpstore"))
	if err != nil {
		t.Fatal(err)
	}
	gitMgr, err := privategit.NewManager(privategit.ManagerConfig{
		BackingDir: filepath.Join(baseDir, "privategit"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gitMgr.Store().Close() })

	coord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir:         filepath.Join(baseDir, "recovery"),
		CheckpointStore: cpStore,
		GitStore:        gitMgr.Store(),
		GitManager:      gitMgr,
		CurrentBootID:   "boot-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	mountedAdp := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID:     adapter.AdapterFUSELinux,
		TargetOS:      runtime.GOOS,
		MountViewFn:   func(_ context.Context, _ string, _ []adapter.PreparedRoot) error { return nil },
		UnmountViewFn: func(_ context.Context, _ string, _ string) error { return nil },
	})

	return &recoveryFixture{
		t:          t,
		baseDir:    baseDir,
		trunkDir:   trunkDir,
		cpStore:    cpStore,
		gitMgr:     gitMgr,
		coord:      coord,
		mountedAdp: mountedAdp,
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fx *recoveryFixture) prepareRequest(viewID string) adapter.PrepareRequest {
	return adapter.PrepareRequest{
		ViewID:     viewID,
		BackingDir: filepath.Join(fx.baseDir, "backing-"+viewID),
		RuntimeDir: filepath.Join(fx.baseDir, "runtime"),
		Grants: []adapter.RootGrant{
			{RootID: "primary", Label: "primary", SourcePath: fx.trunkDir, IsPrimary: true},
		},
	}
}

func TestFaultInjectionAllSevenPhasesAndQuarantine(t *testing.T) {
	fx := newRecoveryFixture(t)
	ctx := t.Context()
	const workerID = "worker-faults"

	if err := fx.coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion: recovery.FormatVersion,
		WorkerID:      workerID,
		ViewID:        "view-init",
		TaskID:        "task-1",
		ProjectID:     "proj-1",
		AdapterID:     fx.mountedAdp.ID(),
	}); err != nil {
		t.Fatal(err)
	}

	view1, err := fx.mountedAdp.Prepare(ctx, fx.prepareRequest("view-init"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, view1.ViewID) }()

	qpApp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	if _, err := view1.Core.WriteFile(qpApp, []byte("package main // cpA\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             view1.Core,
		Store:            fx.cpStore,
		WorkerID:         workerID,
		TaskID:           "task-1",
		ProjectID:        "proj-1",
		SourceGeneration: "gen-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			CommitRetention: func(cCtx context.Context, cpID string, blobs, gitObjs []string) error {
				return fx.coord.CommitCheckpointRetention(cCtx, workerID, cpID, blobs, gitObjs, nil)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Publish valid baseline Checkpoint A.
	resA, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-A"})
	if err != nil || resA.Manifest.CheckpointID != "cp-A" {
		t.Fatalf("publish cp-A: %v", err)
	}
	if err := fx.coord.RecordTransition(workerID, view1.ViewID, recovery.PhasePublication, recovery.StateCommitted, "cp-A", "publish", nil, false); err != nil {
		t.Fatal(err)
	}

	// Inject fault during barrier_flush and publication (leaving a half-written manifest.json.tmp for cp-B).
	if _, err := view1.Core.WriteFile(qpApp, []byte("package main // unsealed-B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = pub.Publish(ctx, checkpoint.PublishRequest{
		CheckpointID: "cp-B-half",
		FaultInjector: func(stage checkpoint.FaultStage) error {
			if stage == checkpoint.FaultBeforeManifestSync {
				return errors.New("injected crash before manifest sync")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatalf("expected fault during cp-B-half publication")
	}
	// Stage a half-written manifest.json.tmp to simulate crash before rename.
	halfDir := filepath.Join(fx.cpStore.RootDir(), "checkpoints", "cp-B-half")
	_ = os.MkdirAll(halfDir, 0o700)
	writeFile(t, filepath.Join(halfDir, "manifest.json.tmp"), `{"checkpoint_id":"cp-B-half"}`)

	if _, err := fx.coord.VerifyCheckpointReconstructible(ctx, "cp-B-half"); !errors.Is(err, recovery.ErrIncompleteCheckpoint) {
		t.Fatalf("expected ErrIncompleteCheckpoint for half-written manifest, got %v", err)
	}

	// Exercise all 7 phases with transition begin + fault injection.
	phases := []recovery.TransitionPhase{
		recovery.PhasePreparation,
		recovery.PhaseActiveMutation,
		recovery.PhaseBarrierFlush,
		recovery.PhasePublication,
		recovery.PhaseRefImport,
		recovery.PhaseFilePromotion,
		recovery.PhaseRelease,
	}
	for _, ph := range phases {
		unknown := ph == recovery.PhaseActiveMutation
		if err := fx.coord.RecordTransition(workerID, view1.ViewID, ph, recovery.StateBegin, "cp-A", "op-"+string(ph), []layeredfs.QualifiedPath{qpApp}, unknown); err != nil {
			t.Fatalf("record begin %s: %v", ph, err)
		}
	}

	// Recover worker view: must expose only cp-A and preserve interrupted work in BucketInterruptedWorkingData.
	recRes, err := fx.coord.RecoverWorkerView(ctx, recovery.RecoverRequest{
		WorkerID:           workerID,
		ExpectedTaskID:     "task-1",
		ExpectedProjectID:  "proj-1",
		NewViewID:          "view-recovered-1",
		Adapter:            fx.mountedAdp,
		PrepareReq:         fx.prepareRequest("view-recovered-1"),
		PreviousViewID:     view1.ViewID,
		PreviousBackingDir: view1.BackingDir,
	})
	if err != nil {
		t.Fatalf("RecoverWorkerView: %v", err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, recRes.ViewID) }()

	if recRes.RestoredCheckpointID != "cp-A" || recRes.QuarantinedWork == nil {
		t.Fatalf("expected cp-A and quarantined work, got cp=%q quarantined=%+v", recRes.RestoredCheckpointID, recRes.QuarantinedWork)
	}
	if len(recRes.QuarantinedWork.AffectedPaths) == 0 {
		t.Fatalf("expected affected paths in quarantined record, got %+v", recRes.QuarantinedWork)
	}

	inv, err := fx.coord.LoadInventory()
	if err != nil {
		t.Fatal(err)
	}
	var hasRetained, hasInterrupted, hasRuntime bool
	for _, e := range inv.Entries {
		switch e.Bucket {
		case recovery.BucketRetainedWorkerResults:
			hasRetained = true
		case recovery.BucketInterruptedWorkingData:
			hasInterrupted = true
		case recovery.BucketDisposableRuntimeCache:
			hasRuntime = true
		}
	}
	if !hasRetained || !hasInterrupted || !hasRuntime {
		t.Fatalf("expected all 3 inventory buckets populated, got retained=%t interrupted=%t runtime=%t", hasRetained, hasInterrupted, hasRuntime)
	}
}

func TestOverwriteAndDeleteAfterCheckpointACrashReconstructsA(t *testing.T) {
	// [process-crash evidence, not physical power-loss proof]
	fx := newRecoveryFixture(t)
	ctx := t.Context()
	const workerID = "worker-overwrite-delete"

	_ = fx.coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion: recovery.FormatVersion,
		WorkerID:      workerID,
		ViewID:        "view-a",
		TaskID:        "task-a",
		ProjectID:     "proj-a",
		AdapterID:     fx.mountedAdp.ID(),
	})

	viewA, err := fx.mountedAdp.Prepare(ctx, fx.prepareRequest("view-a"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, viewA.ViewID) }()

	qpApp := layeredfs.QualifiedPath{RootID: "primary", Rel: "app.go"}
	qpKeep := layeredfs.QualifiedPath{RootID: "primary", Rel: "keep.txt"}
	qpSub := layeredfs.QualifiedPath{RootID: "primary", Rel: "sub/dir.txt"}
	qpAdded := layeredfs.QualifiedPath{RootID: "primary", Rel: "added_in_a.txt"}

	_, _ = viewA.Core.WriteFile(qpApp, []byte("package main // checkpoint-A\n"), 0o644)
	_, _ = viewA.Core.WriteFile(qpAdded, []byte("created-in-A\n"), 0o644)
	_ = viewA.Core.Unlink(qpSub, false)

	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             viewA.Core,
		Store:            fx.cpStore,
		WorkerID:         workerID,
		TaskID:           "task-a",
		ProjectID:        "proj-a",
		SourceGeneration: "gen-a",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: fx.trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.Publish(ctx, checkpoint.PublishRequest{CheckpointID: "cp-A"}); err != nil {
		t.Fatal(err)
	}
	_ = fx.coord.RecordTransition(workerID, viewA.ViewID, recovery.PhasePublication, recovery.StateCommitted, "cp-A", "publish", nil, false)

	// Later unsealed work: overwrite app.go, delete keep.txt and added_in_a.txt, then simulate crash.
	_, _ = viewA.Core.WriteFile(qpApp, []byte("CORRUPTED AFTER A\n"), 0o644)
	_ = viewA.Core.Unlink(qpKeep, false)
	_ = viewA.Core.Unlink(qpAdded, false)
	_ = fx.coord.RecordTransition(workerID, viewA.ViewID, recovery.PhaseActiveMutation, recovery.StateBegin, "cp-A", "post_a_edits", nil, true)

	rec1, err := fx.coord.RecoverWorkerView(ctx, recovery.RecoverRequest{
		WorkerID:           workerID,
		ExpectedTaskID:     "task-a",
		ExpectedProjectID:  "proj-a",
		NewViewID:          "view-rec-1",
		Adapter:            fx.mountedAdp,
		PrepareReq:         fx.prepareRequest("view-rec-1"),
		PreviousViewID:     viewA.ViewID,
		PreviousBackingDir: viewA.BackingDir,
	})
	if err != nil {
		t.Fatalf("RecoverWorkerView 1: %v", err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, rec1.ViewID) }()

	// Verify idempotent retry of RecoverWorkerView.
	rec2, err := fx.coord.RecoverWorkerView(ctx, recovery.RecoverRequest{
		WorkerID:          workerID,
		ExpectedTaskID:    "task-a",
		ExpectedProjectID: "proj-a",
		NewViewID:         "view-rec-2",
		Adapter:           fx.mountedAdp,
		PrepareReq:        fx.prepareRequest("view-rec-2"),
	})
	if err != nil {
		t.Fatalf("RecoverWorkerView idempotent retry: %v", err)
	}
	defer func() { _, _ = fx.mountedAdp.Release(ctx, rec2.ViewID) }()

	gotApp, err := rec2.PreparedView.Core.ReadFile(qpApp)
	if err != nil || !bytes.Equal(gotApp, []byte("package main // checkpoint-A\n")) {
		t.Fatalf("expected app.go from cp-A, got %q err=%v", string(gotApp), err)
	}
	gotKeep, err := rec2.PreparedView.Core.ReadFile(qpKeep)
	if err != nil || !bytes.Equal(gotKeep, []byte("keep-v0\n")) {
		t.Fatalf("expected keep.txt intact from cp-A, got %q err=%v", string(gotKeep), err)
	}
	gotAdded, err := rec2.PreparedView.Core.ReadFile(qpAdded)
	if err != nil || !bytes.Equal(gotAdded, []byte("created-in-A\n")) {
		t.Fatalf("expected added_in_a.txt restored from cp-A, got %q err=%v", string(gotAdded), err)
	}
	if _, err := rec2.PreparedView.Core.Lstat(qpSub); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected sub/dir.txt deleted in cp-A, got err=%v", err)
	}
}
