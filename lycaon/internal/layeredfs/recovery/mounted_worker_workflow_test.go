//go:build darwin || linux

package recovery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// TestMountedWorkerWorkflowCheckpointCrashRecoveryAndStackedPromotion runs on the host's real mounted
// adapter. Its views live under the test temp directory, so a killed run leaves them where the test
// runner reclaims them.
func TestMountedWorkerWorkflowCheckpointCrashRecoveryAndStackedPromotion(t *testing.T) {
	baseDir := t.TempDir()
	host := newMountedWorkflowHost()
	adp := host.adapter

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

	views := watchMountedViews(t, adp)
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
	prepParent, err := adp.Prepare(t.Context(), host.prepareRequest(baseDir, trunkDir, parentViewID, gitMgr))
	if err != nil {
		t.Fatalf("adp.Prepare parent: %v", err)
	}
	parentConnections := views.track(t, prepParent)

	if err := adp.BindPrivateGit(parentViewID, parentWorkerID, gitMgr); err != nil {
		t.Fatalf("BindPrivateGit: %v", err)
	}

	// 1. Mutate files and commit over the live mount, then publish Checkpoint A.
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
	if _, err := gitExec.Commit(t.Context(), "parent commit A over the mount"); err != nil {
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
	testutil.FailErr(t, "save parent journal", coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion:             recovery.FormatVersion,
		WorkerID:                  parentWorkerID,
		ViewID:                    parentViewID,
		TaskID:                    "task-nfs",
		ProjectID:                 "proj-nfs",
		AdapterID:                 adp.ID(),
		LastPublishedCheckpointID: "cp-nfs-A",
	}))

	// 2. Overwrite and delete cp-nfs-A files over the mount, then crash before Checkpoint B seals.
	writeFile(t, filepath.Join(mountPath, "README.md"), "corrupted unsealed edit after A\n")
	testutil.FailErr(t, "remove parent feature", os.Remove(filepath.Join(mountPath, "parent_feature.txt")))
	testutil.FailErr(t, "flush parent", adp.FlushClientData(t.Context(), parentViewID))
	if receipt, err := adp.Release(t.Context(), parentViewID); err != nil || !receipt.Settled {
		t.Fatalf("release parent: %+v err=%v", receipt, err)
	}
	views.requireAbsent(t, parentConnections)

	// 3. RecoverWorkerView onto a fresh mount and verify cp-nfs-A is reconstructed.
	recRes, err := coord.RecoverWorkerView(t.Context(), recovery.RecoverRequest{
		WorkerID:           parentWorkerID,
		ExpectedTaskID:     "task-nfs",
		ExpectedProjectID:  "proj-nfs",
		NewViewID:          "view-nfs-parent-rec",
		Adapter:            adp,
		PrepareReq:         host.prepareRequest(baseDir, trunkDir, "view-nfs-parent-rec", gitMgr),
		PreviousViewID:     parentViewID,
		PreviousBackingDir: prepParent.BackingDir,
	})
	if err != nil {
		t.Fatalf("RecoverWorkerView over the mounted adapter: %v", err)
	}
	recConnections := views.track(t, recRes.PreparedView)

	childConnections := verifyRecoveredMountAndStackedChild(t, baseDir, trunkDir, cpStore, gitMgr, host, views, coord, recRes, headOIDA)
	views.requireAbsent(t, childConnections)
	views.requireAbsent(t, recConnections)
}

func verifyRecoveredMountAndStackedChild(
	t *testing.T,
	baseDir, trunkDir string,
	cpStore *checkpoint.Store,
	gitMgr *privategit.Manager,
	host mountedWorkflowHost,
	views *mountedViews,
	coord *recovery.Coordinator,
	recRes *recovery.RecoveryResult,
	headOIDA string,
) []adapter.RecordedConnection {
	t.Helper()
	adp := host.adapter
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

	// 4. Prepare a stacked child worker over cp-nfs-A on a real mount.
	stacked, err := coord.PrepareStackedWorkerView(t.Context(), recovery.StackedViewRequest{
		ChildWorkerID:      "worker-nfs-child",
		ChildViewID:        "view-nfs-child",
		TaskID:             "task-nfs",
		ProjectID:          "proj-nfs",
		ParentCheckpointID: "cp-nfs-A",
		Adapter:            adp,
		PrepareReq:         host.prepareRequest(baseDir, trunkDir, "view-nfs-child", gitMgr),
	})
	if err != nil {
		t.Fatalf("PrepareStackedWorkerView over the mounted adapter: %v", err)
	}
	childConnections := views.track(t, stacked.PreparedView)

	childMount := stacked.PreparedView.PrimaryWorkPath
	if got, rErr := os.ReadFile(filepath.Join(childMount, "parent_feature.txt")); rErr != nil || string(got) != "parent-feature-v1\n" {
		t.Fatalf("stacked child mount missing parent_feature.txt: %q err=%v", string(got), rErr)
	}
	writeFile(t, filepath.Join(childMount, "child_nfs.txt"), "child-over-nfs\n")
	writeFile(t, filepath.Join(childMount, "README.md"), "updated by stacked child over nfs\n")
	testutil.FailErr(t, "flush child", adp.FlushClientData(t.Context(), stacked.ChildViewID))

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

	// Release both mounted views; the caller verifies no connection remains.
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
		t.Fatalf("expected 0 active views after CancelAndRelease")
	}
	if _, err := os.Stat(filepath.Join(trunkDir, ".git")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return childConnections
}

// mountedWorkflowAdapter is what the workflow needs from a host's real mounted adapter.
type mountedWorkflowAdapter interface {
	adapter.Adapter
	adapter.ConnectionRecovery
	BindPrivateGit(viewID, workerID string, mgr *privategit.Manager, authorizedGitDirs ...string) error
	FlushClientData(ctx context.Context, viewID string) error
	IsViewActive(viewID string) bool
}

type mountedWorkflowHost struct {
	adapter      mountedWorkflowAdapter
	quiesce      func(context.Context, string) error
	flushClients func(context.Context, string) error
}

func (h mountedWorkflowHost) prepareRequest(baseDir, trunkDir, viewID string, gitMgr *privategit.Manager) adapter.PrepareRequest {
	return adapter.PrepareRequest{
		ViewID:          viewID,
		BackingDir:      filepath.Join(baseDir, "backing-"+viewID),
		RuntimeDir:      filepath.Join(baseDir, "runtime"),
		GitInterceptor:  gitMgr,
		QuiesceHook:     h.quiesce,
		FlushClientHook: h.flushClients,
		Grants: []adapter.RootGrant{
			{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true},
		},
	}
}

// mountedViews remembers each view's recorded connections, because a settled release deletes the
// records. On every exit from the test it releases what remains and requires the kernel to hold none.
type mountedViews struct {
	adapter     mountedWorkflowAdapter
	viewIDs     []string
	connections []adapter.RecordedConnection
}

func watchMountedViews(t *testing.T, adp mountedWorkflowAdapter) *mountedViews {
	t.Helper()
	views := &mountedViews{adapter: adp}
	t.Cleanup(func() {
		for _, viewID := range views.viewIDs {
			receipt, err := adp.Release(context.Background(), viewID)
			if err != nil && receipt.ReasonCode != adapter.ReasonViewNotFound {
				t.Errorf("release %s: %+v err=%v", viewID, receipt, err)
			}
		}
		views.requireAbsent(t, views.connections)
	})
	return views
}

func (v *mountedViews) track(t *testing.T, view *adapter.PreparedView) []adapter.RecordedConnection {
	t.Helper()
	connections, err := adapter.LoadRecordedConnections(view.RuntimeViewDir)
	testutil.FailErr(t, "load recorded connections", err)
	if len(connections) == 0 {
		t.Fatalf("view %s recorded no connection", view.ViewID)
	}
	v.viewIDs = append(v.viewIDs, view.ViewID)
	v.connections = append(v.connections, connections...)
	return connections
}

// requireAbsent reads the kernel mount table through the adapter. This process serves these mounts,
// so settling one that remains reports it and never removes it.
func (v *mountedViews) requireAbsent(t *testing.T, connections []adapter.RecordedConnection) {
	t.Helper()
	for _, connection := range connections {
		settlement, err := v.adapter.SettleRecordedConnection(context.Background(), connection)
		if err != nil || settlement.Disposition != adapter.ConnectionAbsent {
			t.Errorf("connection at %s: %+v err=%v", connection.MountPath, settlement, err)
		}
	}
}
