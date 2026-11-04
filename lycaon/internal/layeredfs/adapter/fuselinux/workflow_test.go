//go:build linux

package fuselinux_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

func TestNarrowRealMountedWorkerWorkflowEndToEnd(t *testing.T) {

	tmp := t.TempDir()
	trunkDir := filepath.Join(tmp, "trunk")
	backingDir := filepath.Join(tmp, "backing")
	runtimeDir := filepath.Join(tmp, "runtime")
	pgitDir := filepath.Join(tmp, "privategit")
	cpStoreDir := filepath.Join(tmp, "checkpoints")
	for _, d := range []string{trunkDir, backingDir, runtimeDir, pgitDir, cpStoreDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// 1. Seed canonical TrunkRoot Git repository.
	trunkInitialHead := seedWorkflowTrunk(t, trunkDir)
	trunkBefore := snapshotTreeDigests(t, trunkDir)

	// 2. Construct privategit.Manager and fuselinux.Adapter; prepare and mount the real fusermount3 view.
	gitMgr, err := privategit.NewManager(privategit.ManagerConfig{BackingDir: pgitDir})
	if err != nil {
		t.Fatalf("privategit.NewManager: %v", err)
	}
	t.Cleanup(func() { _ = gitMgr.Store().Close() })

	adp := fuselinux.New(fuselinux.Config{})
	const viewID = "view-mounted-fuse-e2e"
	const workerID = "worker-mounted-fuse-e2e"

	prep, err := adp.Prepare(t.Context(), adapter.PrepareRequest{
		ViewID:          viewID,
		BackingDir:      backingDir,
		RuntimeDir:      runtimeDir,
		GitInterceptor:  gitMgr,
		QuiesceHook:     fuselinux.NoopQuiesceHook,
		FlushClientHook: fuselinux.NoWritableMappingsFlushHook,
		Grants:          []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: trunkDir, IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("adp.Prepare: %v", err)
	}
	t.Cleanup(func() { _, _ = adp.Release(t.Context(), viewID) })

	if err := adp.BindPrivateGit(viewID, workerID, gitMgr); err != nil {
		t.Fatalf("adp.BindPrivateGit: %v", err)
	}
	if !adp.IsViewActive(viewID) {
		t.Fatalf("expected IsViewActive(%q)=true while mounted", viewID)
	}

	// 3. Perform both native Go and shell file mutations through the real FUSE mount.
	mountPath := prep.PrimaryWorkPath
	if err := os.WriteFile(filepath.Join(mountPath, "README.md"), []byte("updated by worker over fuse\n"), 0o644); err != nil {
		t.Fatalf("write mounted README.md: %v", err)
	}
	shCmd := exec.CommandContext(t.Context(), "/bin/sh", "-c",
		"mkdir -p pkg && printf 'package pkg\n' > pkg/feature.go && cat README.md")
	shCmd.Dir = mountPath
	fuselinux.DetachMountEntry(shCmd)
	shOut, err := shCmd.CombinedOutput()
	if err != nil || string(shOut) != "updated by worker over fuse\n" {
		t.Fatalf("shell command in mount failed: err=%v out=%q", err, string(shOut))
	}
	if gotPkg, err := os.ReadFile(filepath.Join(mountPath, "pkg", "feature.go")); err != nil || string(gotPkg) != "package pkg\n" {
		t.Fatalf("expected pkg/feature.go readable from Go immediately, got %q err=%v", string(gotPkg), err)
	}

	journalSnap, err := prep.Core.SnapshotJournal()
	if err != nil || len(journalSnap.Operations) < 2 || len(journalSnap.Changes) < 2 {
		t.Fatalf("expected Core mutation journal to record mounted writes, got ops=%d changes=%d err=%v",
			len(journalSnap.Operations), len(journalSnap.Changes), err)
	}

	// 4. Exercise private Git initialization and commit against the live FUSE mount without re-entry deadlock.
	wtDir, finishPrep, managed, err := privategit.PrepareViewGitOperation(t.Context(), trunkDir, workerID)
	if err != nil || !managed || wtDir != mountPath {
		t.Fatalf("PrepareViewGitOperation failed: wtDir=%q (want %q) managed=%v err=%v", wtDir, mountPath, managed, err)
	}
	if err := finishPrep(); err != nil {
		t.Fatalf("finishPrep: %v", err)
	}

	mountedHeadRaw, err := os.ReadFile(filepath.Join(mountPath, ".git", "HEAD"))
	if err != nil || !strings.Contains(string(mountedHeadRaw), "refs/heads/main") {
		t.Fatalf("expected .git/HEAD readable through live FUSE mount, got %q err=%v", string(mountedHeadRaw), err)
	}

	gitExec := privategit.NewGitExecutor(gitMgr, viewID, privategit.RemotePolicy{})
	workerHeadOID := commitThroughPrivateGit(t, gitExec, trunkInitialHead)

	// Exercise unrestricted shell `git` subprocesses directly inside mountPath (with cwd = mountPath).
	shellGitCmd := exec.CommandContext(t.Context(), "/bin/sh", "-c",
		"git --no-optional-locks status --porcelain=v1 && git rev-parse HEAD && git branch --list")
	shellGitCmd.Dir = mountPath
	fuselinux.DetachMountEntry(shellGitCmd)
	shellGitOut, err := shellGitCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unrestricted shell git in mountPath failed: %v\n%s", err, string(shellGitOut))
	}
	if !strings.Contains(string(shellGitOut), workerHeadOID) || !strings.Contains(string(shellGitOut), "worker/feature") {
		t.Fatalf("expected unrestricted shell git in mountPath to see worker commit %s and branch worker/feature, got:\n%s",
			workerHeadOID, string(shellGitOut))
	}

	trunkDuring := snapshotTreeDigests(t, trunkDir)
	assertTreesIdentical(t, "trunk untouched during mounted worker edits and private git commit", trunkBefore, trunkDuring)

	// 5. Execute CheckpointBarrier, capture private Git state, and publish an immutable checkpoint (#1663).
	barrierReceipt, releaseBarrier, err := adp.CheckpointBarrier(t.Context(), viewID)
	if err != nil || !barrierReceipt.DirtyDataFlushed || !barrierReceipt.WritersQuiesced {
		t.Fatalf("CheckpointBarrier failed: receipt=%+v err=%v", barrierReceipt, err)
	}
	if err := releaseBarrier(); err != nil {
		t.Fatalf("releaseBarrier: %v", err)
	}

	const checkpointID = "cp-mounted-fuse-e2e-1"
	cpGitState, err := gitMgr.CaptureCheckpointGitState(t.Context(), viewID, checkpointID)
	if err != nil || cpGitState.HeadOID != workerHeadOID {
		t.Fatalf("CaptureCheckpointGitState: state=%+v err=%v", cpGitState, err)
	}

	cpStore := publishWorkflowCheckpoint(t, adp, prep.Core, cpStoreDir, trunkDir, viewID, workerID, checkpointID, cpGitState)

	// 6. Release the mounted view and verify subsequent privategit operations fail closed with ErrMountedWorktreeUnavailable.
	relReceipt, err := adp.Release(t.Context(), viewID)
	if err != nil || !relReceipt.Settled {
		t.Fatalf("Release failed: receipt=%+v err=%v", relReceipt, err)
	}
	if _, err := gitExec.Status(t.Context()); !errors.Is(err, privategit.ErrMountedWorktreeUnavailable) {
		t.Fatalf("expected ErrMountedWorktreeUnavailable from Status after Release, got %v", err)
	}

	// 7. Import checkpoint refs and promote the checkpoint into TrunkRoot (#1671 + #1663).
	promoteWorkflowCheckpoint(t, gitMgr, cpStore, trunkDir, workerID, checkpointID, cpGitState)

	promotedReadme, err := os.ReadFile(filepath.Join(trunkDir, "README.md"))
	if err != nil || string(promotedReadme) != "updated by worker over fuse\n" {
		t.Fatalf("expected promoted README.md in trunk, got %q err=%v", string(promotedReadme), err)
	}
	promotedFeature, err := os.ReadFile(filepath.Join(trunkDir, "pkg", "feature.go"))
	if err != nil || string(promotedFeature) != "package pkg\n" {
		t.Fatalf("expected promoted pkg/feature.go in trunk, got %q err=%v", string(promotedFeature), err)
	}
	importedMainRef, err := privategit.NamespacedRef(workerID, checkpointID, "refs/heads/main")
	if err != nil {
		t.Fatalf("NamespacedRef: %v", err)
	}
	resolvedImportedOID := strings.TrimSpace(gittest.Run(t, trunkDir, "rev-parse", importedMainRef))
	if resolvedImportedOID != workerHeadOID {
		t.Fatalf("expected imported ref %s in trunk to resolve to %s, got %s", importedMainRef, workerHeadOID, resolvedImportedOID)
	}
}

func commitThroughPrivateGit(t *testing.T, gitExec *privategit.GitExecutor, trunkInitialHead string) string {
	t.Helper()
	statusOut, err := gitExec.Status(t.Context(), "--porcelain=v1")
	if err != nil || !strings.Contains(statusOut, "README.md") || !strings.Contains(statusOut, "pkg/") {
		t.Fatalf("unexpected git status over mounted view: err=%v\n%s", err, statusOut)
	}
	if _, err := gitExec.Run(t.Context(), ".", "add", "README.md", "pkg/feature.go"); err != nil {
		t.Fatalf("git add over mounted view: %v", err)
	}
	if _, err := gitExec.Commit(t.Context(), "worker commit over real fuselinux mount"); err != nil {
		t.Fatalf("git commit over mounted view: %v", err)
	}
	if _, err := gitExec.Run(t.Context(), ".", "branch", "worker/feature"); err != nil {
		t.Fatalf("git branch over mounted view: %v", err)
	}
	workerHeadOID, err := gitExec.Run(t.Context(), ".", "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD in worker view: %v", err)
	}
	workerHeadOID = strings.TrimSpace(workerHeadOID)
	if workerHeadOID == "" || workerHeadOID == trunkInitialHead {
		t.Fatalf("expected new worker commit OID distinct from trunk (%s), got %q", trunkInitialHead, workerHeadOID)
	}
	return workerHeadOID
}

func seedWorkflowTrunk(t *testing.T, trunkDir string) string {
	t.Helper()
	gittest.Init(t, trunkDir)
	gittest.Run(t, trunkDir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(trunkDir, "README.md"), []byte("initial readme\n"), 0o644); err != nil {
		t.Fatalf("write trunk README.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(trunkDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir trunk src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "src", "app.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write trunk src/app.go: %v", err)
	}
	gittest.CommitAll(t, trunkDir, "initial trunk commit")
	return strings.TrimSpace(gittest.Run(t, trunkDir, "rev-parse", "HEAD"))
}

func publishWorkflowCheckpoint(
	t *testing.T,
	adp *fuselinux.Adapter,
	core *layeredfs.Core,
	cpStoreDir, trunkDir, viewID, workerID, checkpointID string,
	cpGitState *privategit.CheckpointGitState,
) *checkpoint.Store {
	t.Helper()
	cpStore, err := checkpoint.NewStore(cpStoreDir)
	if err != nil {
		t.Fatalf("checkpoint.NewStore: %v", err)
	}
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		Core:             core,
		Store:            cpStore,
		WorkerID:         workerID,
		TaskID:           "task-e2e",
		ProjectID:        "proj-e2e",
		SourceGeneration: "gen-e2e-1",
		SelectedRoots:    []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunkDir, IsPrimary: true}},
		Callbacks: checkpoint.BarrierCallbacks{
			FlushClientDirtyData: func(ctx context.Context) error {
				return adp.FlushClientData(ctx, viewID)
			},
		},
	})
	if err != nil {
		t.Fatalf("checkpoint.NewPublisher: %v", err)
	}
	pubRes, err := pub.Publish(t.Context(), checkpoint.PublishRequest{
		CheckpointID: checkpointID,
		Trigger:      checkpoint.TriggerExplicitCapture,
		PrivateGit: checkpoint.PrivateGitSnapshot{
			HeadRef:         cpGitState.HeadSymbolic,
			Refs:            cpGitState.Refs,
			RetainedObjects: cpGitState.Artifact.ReachableOIDs,
		},
	})
	if err != nil || pubRes.Manifest.CheckpointID != checkpointID {
		t.Fatalf("Publish checkpoint failed: %v", err)
	}
	return cpStore
}

func promoteWorkflowCheckpoint(
	t *testing.T,
	gitMgr *privategit.Manager,
	cpStore *checkpoint.Store,
	trunkDir, workerID, checkpointID string,
	cpGitState *privategit.CheckpointGitState,
) {
	t.Helper()
	promoter, err := checkpoint.NewPromoter(checkpoint.PromoterConfig{
		Store:       cpStore,
		TargetRoots: map[string]string{"primary": trunkDir},
		RefImporter: func(ctx context.Context, _ string, wID string, _ checkpoint.PrivateGitSnapshot) (map[string]string, error) {
			impRes, iErr := gitMgr.ImportRefsToTrunk(ctx, privategit.RefImportRequest{
				JobID: wID, WorkerID: wID, CheckpointID: checkpointID, TrunkDir: trunkDir, Checkpoint: cpGitState,
			})
			if iErr != nil {
				return nil, iErr
			}
			return impRes.ImportedRefs, nil
		},
	})
	if err != nil {
		t.Fatalf("checkpoint.NewPromoter: %v", err)
	}
	preview, err := promoter.Preview(t.Context(), checkpoint.PreviewRequest{
		CheckpointID: checkpointID, ExpectedWorkerID: workerID, ExpectedTaskID: "task-e2e",
		ExpectedProjectID: "proj-e2e", ExpectedSourceGeneration: "gen-e2e-1",
	})
	if err != nil || len(preview.Conflicts) > 0 {
		t.Fatalf("promoter.Preview failed: conflicts=%+v err=%v", preview, err)
	}
	applyRes, err := promoter.Apply(t.Context(), checkpoint.ApplyRequest{Preview: preview, ImportGitRefs: true})
	if err != nil || len(applyRes.ImportedRefs) == 0 {
		t.Fatalf("promoter.Apply failed: res=%+v err=%v", applyRes, err)
	}
}

func snapshotTreeDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(root, path)
		if rErr != nil || rel == "." {
			return rErr
		}
		info, iErr := d.Info()
		if iErr != nil {
			return iErr
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			out[slash] = fmt.Sprintf("dir:%04o", info.Mode().Perm())
			return nil
		}
		raw, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		sum := sha256.Sum256(raw)
		out[slash] = fmt.Sprintf("file:%04o:%s", info.Mode().Perm(), hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotTreeDigests(%s): %v", root, err)
	}
	return out
}

func assertTreesIdentical(t *testing.T, label string, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("%s: entry count changed: before=%d after=%d", label, len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s: path %q changed: before=%s after=%s", label, k, v, after[k])
		}
	}
}
