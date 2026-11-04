package recovery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMountedMacOSWorkerWorkflowCheckpointCrashRecoveryAndStackedPromotion(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real /sbin/mount_nfs loopback workflow test requires darwin")
	}
	baseDir, err := os.MkdirTemp("/tmp", "lycaon-rec-nfs-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(baseDir) })

	trunkDir := filepath.Join(baseDir, "trunk")
	if err := os.MkdirAll(filepath.Join(trunkDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Init(t, trunkDir)
	writeFile(t, filepath.Join(trunkDir, "README.md"), "trunk readme v0\n")
	writeFile(t, filepath.Join(trunkDir, "src", "app.go"), "package src // v0\n")
	gittest.CommitAll(t, trunkDir, "initial trunk commit")

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

	adp := nfsdarwin.New(nfsdarwin.Config{})
	coord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir:         filepath.Join(baseDir, "recovery"),
		CheckpointStore: cpStore,
		GitStore:        gitMgr.Store(),
		GitManager:      gitMgr,
		CurrentBootID:   "boot-macos-1",
		MountReconciler: recovery.ConnectionReconciler{},
	})
	if err != nil {
		t.Fatal(err)
	}

	const parentWorkerID = "worker-nfs-parent"
	const parentViewID = "view-nfs-parent-1"
	prepParent, err := adp.Prepare(t.Context(), nfsPrepareReq(baseDir, trunkDir, parentViewID, gitMgr))
	if err != nil {
		t.Fatalf("adp.Prepare parent: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), parentViewID) })

	if err := adp.BindPrivateGit(parentViewID, parentWorkerID, gitMgr); err != nil {
		t.Fatalf("BindPrivateGit: %v", err)
	}

	// 1. Mutate files and commit over the live /sbin/mount_nfs mount, then publish Checkpoint A.
	mountPath := prepParent.PrimaryWorkPath
	writeFile(t, filepath.Join(mountPath, "README.md"), "updated over nfs in cp-A\n")
	writeFile(t, filepath.Join(mountPath, "parent_feature.txt"), "parent-feature-v1\n")

	wtDir, finishPrep, managed, err := privategit.PrepareViewGitOperation(t.Context(), trunkDir, parentWorkerID)
	if err != nil || !managed || wtDir != mountPath {
		t.Fatalf("PrepareViewGitOperation: wt=%q managed=%t err=%v", wtDir, managed, err)
	}
	_ = finishPrep()

	gitExec := privategit.NewGitExecutor(gitMgr, parentViewID, privategit.RemotePolicy{})
	if _, err := gitExec.Run(t.Context(), ".", "add", "README.md", "parent_feature.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := gitExec.Commit(t.Context(), "parent commit A over nfs"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	headOIDA, err := gitExec.Run(t.Context(), ".", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	headOIDA = strings.TrimSpace(headOIDA)

	cpGitA, err := gitMgr.CaptureCheckpointGitState(t.Context(), parentViewID, "cp-nfs-A")
	if err != nil {
		t.Fatalf("CaptureCheckpointGitState: %v", err)
	}
	parentPub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             prepParent.Core,
		Store:            cpStore,
		WorkerID:         parentWorkerID,
		TaskID:           "task-nfs",
		ProjectID:        "proj-nfs",
		SourceGeneration: "gen-nfs-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			FlushClientDirtyData: func(ctx context.Context) error { return adp.FlushClientData(ctx, parentViewID) },
			CommitRetention: func(ctx context.Context, cpID string, blobs, gitObjs []string) error {
				return coord.CommitCheckpointRetention(ctx, parentWorkerID, cpID, blobs, gitObjs, nil)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parentPub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: "cp-nfs-A",
		PrivateGit: checkpoint.PrivateGitSnapshot{
			HeadRef:         cpGitA.HeadSymbolic,
			Refs:            cpGitA.Refs,
			RetainedObjects: cpGitA.Artifact.ReachableOIDs,
		},
	}); err != nil {
		t.Fatalf("Publish cp-nfs-A: %v", err)
	}
	_ = coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion:             recovery.FormatVersion,
		WorkerID:                  parentWorkerID,
		ViewID:                    parentViewID,
		TaskID:                    "task-nfs",
		ProjectID:                 "proj-nfs",
		AdapterID:                 adp.ID(),
		LastPublishedCheckpointID: "cp-nfs-A",
	})

	// 2. Overwrite and delete cp-nfs-A files over NFS, then crash before Checkpoint B seals.
	writeFile(t, filepath.Join(mountPath, "README.md"), "corrupted unsealed edit after A\n")
	_ = os.Remove(filepath.Join(mountPath, "parent_feature.txt"))
	_ = adp.FlushClientData(t.Context(), parentViewID)
	_, _ = adp.Release(t.Context(), parentViewID)

	// 3. RecoverWorkerView onto a fresh /sbin/mount_nfs mount and verify cp-nfs-A is reconstructed.
	recRes, err := coord.RecoverWorkerView(t.Context(), recovery.RecoverRequest{
		WorkerID:           parentWorkerID,
		ExpectedTaskID:     "task-nfs",
		ExpectedProjectID:  "proj-nfs",
		NewViewID:          "view-nfs-parent-rec",
		Adapter:            adp,
		PrepareReq:         nfsPrepareReq(baseDir, trunkDir, "view-nfs-parent-rec", gitMgr),
		PreviousViewID:     parentViewID,
		PreviousBackingDir: prepParent.BackingDir,
	})
	if err != nil {
		t.Fatalf("RecoverWorkerView over nfsdarwin: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), recRes.ViewID) })

	verifyRecoveredMountAndStackedChild(t, baseDir, trunkDir, cpStore, gitMgr, adp, coord, recRes, headOIDA)
}

func verifyRecoveredMountAndStackedChild(
	t *testing.T,
	baseDir, trunkDir string,
	cpStore *checkpoint.Store,
	gitMgr *privategit.Manager,
	adp *nfsdarwin.Adapter,
	coord *recovery.Coordinator,
	recRes *recovery.RecoveryResult,
	headOIDA string,
) {
	t.Helper()
	recMount := recRes.PreparedView.PrimaryWorkPath
	gotReadme, err := os.ReadFile(filepath.Join(recMount, "README.md"))
	if err != nil || string(gotReadme) != "updated over nfs in cp-A\n" {
		t.Fatalf("expected README.md from cp-nfs-A on recovered mount, got %q err=%v", string(gotReadme), err)
	}
	gotFeat, err := os.ReadFile(filepath.Join(recMount, "parent_feature.txt"))
	if err != nil || string(gotFeat) != "parent-feature-v1\n" {
		t.Fatalf("expected parent_feature.txt restored on recovered mount, got %q err=%v", string(gotFeat), err)
	}
	recGit := privategit.NewGitExecutor(gitMgr, recRes.ViewID, privategit.RemotePolicy{})
	gotHead, err := recGit.Run(t.Context(), ".", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(gotHead) != headOIDA {
		t.Fatalf("expected recovered HEAD %s, got %q err=%v", headOIDA, strings.TrimSpace(gotHead), err)
	}

	// 4. Prepare a stacked child worker over cp-nfs-A on a real /sbin/mount_nfs mount.
	stacked, err := coord.PrepareStackedWorkerView(t.Context(), recovery.StackedViewRequest{
		ChildWorkerID:      "worker-nfs-child",
		ChildViewID:        "view-nfs-child",
		TaskID:             "task-nfs",
		ProjectID:          "proj-nfs",
		ParentCheckpointID: "cp-nfs-A",
		Adapter:            adp,
		PrepareReq:         nfsPrepareReq(baseDir, trunkDir, "view-nfs-child", gitMgr),
	})
	if err != nil {
		t.Fatalf("PrepareStackedWorkerView over nfsdarwin: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(context.Background(), stacked.ChildViewID) })

	childMount := stacked.PreparedView.PrimaryWorkPath
	if got, rErr := os.ReadFile(filepath.Join(childMount, "parent_feature.txt")); rErr != nil || string(got) != "parent-feature-v1\n" {
		t.Fatalf("stacked child mount missing parent_feature.txt: %q err=%v", string(got), rErr)
	}
	writeFile(t, filepath.Join(childMount, "child_nfs.txt"), "child-over-nfs\n")
	writeFile(t, filepath.Join(childMount, "README.md"), "updated by stacked child over nfs\n")
	_ = adp.FlushClientData(t.Context(), stacked.ChildViewID)

	childPub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:              stacked.PreparedView.Core,
		Store:             cpStore,
		WorkerID:          "worker-nfs-child",
		TaskID:            "task-nfs",
		ProjectID:         "proj-nfs",
		SourceGeneration:  "gen-nfs-1",
		SelectedRoots:     []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		ParentCheckpoints: stacked.ParentCheckpointIDs,
		Callbacks: checkpoint.BarrierCallbacks{
			FlushClientDirtyData: func(ctx context.Context) error { return adp.FlushClientData(ctx, stacked.ChildViewID) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := childPub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-nfs-child-C"}); err != nil {
		t.Fatalf("Publish cp-nfs-child-C: %v", err)
	}

	promoter, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{
		Store:       cpStore,
		TargetRoots: map[string]string{"primary": trunkDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coord.PromoteStackedCheckpoint(t.Context(), recovery.StackedPromotionRequest{
		ChildCheckpointID: "cp-nfs-child-C",
		TargetRoots:       map[string]string{"primary": trunkDir},
		Promoter:          promoter,
	}); err != nil {
		t.Fatalf("PromoteStackedCheckpoint: %v", err)
	}

	if got, rErr := os.ReadFile(filepath.Join(trunkDir, "parent_feature.txt")); rErr != nil || string(got) != "parent-feature-v1\n" {
		t.Fatalf("expected parent_feature.txt promoted to trunk, got %q err=%v", string(got), rErr)
	}
	if got, rErr := os.ReadFile(filepath.Join(trunkDir, "child_nfs.txt")); rErr != nil || string(got) != "child-over-nfs\n" {
		t.Fatalf("expected child_nfs.txt promoted to trunk, got %q err=%v", string(got), rErr)
	}

	// Release both mounted views and verify 0 active mounts remain.
	if _, err := coord.CancelAndRelease(t.Context(), recovery.CancelReleaseRequest{
		WorkerID: "worker-nfs-child",
		ViewID:   stacked.ChildViewID,
		View:     stacked.PreparedView,
		Adapter:  adp,
	}); err != nil {
		t.Fatalf("CancelAndRelease child: %v", err)
	}
	if _, err := coord.CancelAndRelease(t.Context(), recovery.CancelReleaseRequest{
		WorkerID: "worker-nfs-parent",
		ViewID:   recRes.ViewID,
		View:     recRes.PreparedView,
		Adapter:  adp,
	}); err != nil {
		t.Fatalf("CancelAndRelease parent: %v", err)
	}
	if adp.IsViewActive(stacked.ChildViewID) || adp.IsViewActive(recRes.ViewID) {
		t.Fatalf("expected 0 active nfsdarwin views after CancelAndRelease")
	}
	if _, err := os.Stat(filepath.Join(trunkDir, ".git")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func nfsPrepareReq(baseDir, trunkDir, viewID string, gitMgr *privategit.Manager) adapter.PrepareRequest {
	return adapter.PrepareRequest{
		ViewID:          viewID,
		BackingDir:      filepath.Join(baseDir, "backing-"+viewID),
		RuntimeDir:      filepath.Join(baseDir, "runtime"),
		GitInterceptor:  gitMgr,
		QuiesceHook:     nfsdarwin.NoopQuiesceHook,
		FlushClientHook: nfsdarwin.NoWritableMappingsFlushHook,
		Grants: []adapter.RootGrant{
			{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true},
		},
	}
}
