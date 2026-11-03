package privategit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestTrunkBranchDeleteAndPruneDoesNotBreakWorkerCheckpointOrImport(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)

	// Create an ephemeral branch in trunk with a unique commit and blob, then switch trunk back to main.
	gittest.Run(t, fix.trunkDir, "checkout", "-b", "ephemeral-branch")
	testutil.FailErr(t, "write ephemeral.txt", os.WriteFile(filepath.Join(fix.trunkDir, "ephemeral.txt"), []byte("ephemeral-history-payload\n"), 0o644))
	gittest.CommitAll(t, fix.trunkDir, "ephemeral commit to be pruned from trunk")
	ephemeralOID := strings.TrimSpace(gittest.Run(t, fix.trunkDir, "rev-parse", "HEAD"))
	gittest.Run(t, fix.trunkDir, "checkout", "main")

	mgr := newTestManager(t, fix.tmpDir)
	core := newMountedViewWithManager(t, mgr, "view-prune", fix.trunkDir)
	exec := NewGitExecutor(mgr, "view-prune", RemotePolicy{})

	// Initialize worker and switch worker onto ephemeral-branch.
	_, err := exec.Checkout(t.Context(), "ephemeral-branch")
	testutil.FailErr(t, "worker checkout ephemeral-branch", err)

	// Force-delete ephemeral-branch in trunk and prune all unreachable objects immediately!
	gittest.Run(t, fix.trunkDir, "branch", "-D", "ephemeral-branch")
	gittest.Run(t, fix.trunkDir, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	gittest.Run(t, fix.trunkDir, "gc", "--prune=now")

	// Confirm ephemeralOID is genuinely gone from trunk!
	if _, code, _ := gitexec.Run(t.Context(), fix.trunkDir, []string{"cat-file", "-e", ephemeralOID}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); code == 0 {
		t.Fatalf("expected %s to be pruned from trunk", ephemeralOID)
	}

	// Prove worker history, fsck, and new commit on top of pruned history still succeed!
	logOut, err := exec.Log(t.Context(), "--oneline", "-n", "2")
	testutil.FailErr(t, "worker git log after trunk prune", err)
	if !strings.Contains(logOut, "ephemeral commit") {
		t.Fatalf("expected worker to retain pruned commit, got:\n%s", logOut)
	}
	_, err = exec.Run(t.Context(), ".", "fsck", "--full")
	testutil.FailErr(t, "worker git fsck after trunk prune", err)

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "worker_child.txt"}, []byte("built-on-pruned-base\n"), 0o644)
	testutil.FailErr(t, "write worker_child.txt", err)
	_, err = exec.Run(t.Context(), ".", "add", "worker_child.txt")
	testutil.FailErr(t, "git add worker_child.txt", err)
	_, err = exec.Commit(t.Context(), "worker commit on top of pruned trunk branch")
	testutil.FailErr(t, "worker commit on ephemeral-branch", err)

	// Capture checkpoint before releasing view.
	cp, err := mgr.CaptureCheckpointGitState(t.Context(), "view-prune", "cp-prune-1")
	testutil.FailErr(t, "CaptureCheckpointGitState", err)
	if len(cp.Artifact.IndexBytes) == 0 || cp.HeadOID == "" {
		t.Fatalf("expected non-empty checkpoint index and HeadOID, got %+v", cp)
	}

	// Release the worker view and run GC on the retained store: checkpoint lease must protect history!
	testutil.FailErr(t, "core.ReleaseView", core.ReleaseView())
	testutil.FailErr(t, "mgr.ReleaseView", mgr.ReleaseView("view-prune"))
	_, err = mgr.Store().CollectGarbage(t.Context())
	testutil.FailErr(t, "CollectGarbage with active checkpoint lease", err)
	testutil.FailErr(t, "VerifyCheckpointHistory after view release & GC", mgr.VerifyCheckpointHistory(t.Context(), cp))

	// Import checkpoint refs back into trunk and verify the pruned commit and its child are now reachable in trunk!
	trunkWTBefore := gittest.Run(t, fix.trunkDir, "status", "--porcelain")
	impRes, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID:        "job-prune",
		WorkerID:     "worker-view-prune",
		CheckpointID: "cp-prune-1",
		TrunkDir:     fix.trunkDir,
		Checkpoint:   cp,
	})
	testutil.FailErr(t, "ImportRefsToTrunk after trunk prune", err)
	wantDest := "refs/paintedwolf/workers/job-prune/cp-prune-1/heads/ephemeral-branch"
	if impRes.ImportedRefs[wantDest] != cp.HeadOID {
		t.Fatalf("expected %s -> %s, got %+v", wantDest, cp.HeadOID, impRes.ImportedRefs)
	}
	trunkLog := gittest.Run(t, fix.trunkDir, "log", "--oneline", wantDest)
	if !strings.Contains(trunkLog, "ephemeral commit") || !strings.Contains(trunkLog, "worker commit on top") {
		t.Fatalf("expected imported ref in trunk to contain full history, got:\n%s", trunkLog)
	}
	trunkWTAfter := gittest.Run(t, fix.trunkDir, "status", "--porcelain")
	if trunkWTBefore != trunkWTAfter {
		t.Fatalf("ref import must not alter trunk working tree: before=%q after=%q", trunkWTBefore, trunkWTAfter)
	}
}

func TestTwoWorkersReuseObjectsConcurrentInitFailedImportAndGC(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)

	// Worker 1 initializes and creates a private commit.
	core1 := newMountedViewWithManager(t, mgr, "view-w1", fix.trunkDir)
	exec1 := NewGitExecutor(mgr, "view-w1", RemotePolicy{})
	_, err := exec1.Status(t.Context())
	testutil.FailErr(t, "w1 status", err)
	m1, _ := mgr.RepoMetrics("view-w1", ".")
	if m1.BytesImported == 0 {
		t.Fatalf("expected w1 to import bytes, got %+v", m1)
	}

	// Worker 2 initializes over the same repo and reuses objects!
	core2 := newMountedViewWithManager(t, mgr, "view-w2", fix.trunkDir)
	exec2 := NewGitExecutor(mgr, "view-w2", RemotePolicy{})
	_, err = exec2.Status(t.Context())
	testutil.FailErr(t, "w2 status", err)
	m2, _ := mgr.RepoMetrics("view-w2", ".")
	if m2.BytesReused == 0 || m2.ObjectsReused == 0 {
		t.Fatalf("expected w2 to reuse retained objects, got %+v", m2)
	}

	// Worker 1 and Worker 2 keep independent HEAD, refs, index, and private objects.
	_, err = exec1.Switch(t.Context(), "-c", "only-in-w1")
	testutil.FailErr(t, "w1 switch -c only-in-w1", err)
	_, err = core1.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "w1.txt"}, []byte("w1-only\n"), 0o644)
	testutil.FailErr(t, "w1 write w1.txt", err)
	_, _ = exec1.Run(t.Context(), ".", "add", "w1.txt")
	_, err = exec1.Commit(t.Context(), "w1 private commit")
	testutil.FailErr(t, "w1 commit", err)

	w2Branches, err := exec2.Run(t.Context(), ".", "branch", "--list")
	testutil.FailErr(t, "w2 branch --list", err)
	if strings.Contains(w2Branches, "only-in-w1") {
		t.Fatalf("w2 must not see w1's private branch: %s", w2Branches)
	}
	if _, err := core2.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: "w1.txt"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("w2 must not see w1's private file, got %v", err)
	}

	// Failed import leaves retryable ErrGitPending state, never a partially usable repo; concurrent lookups succeed on retry.
	var failOnce atomic.Bool
	failOnce.Store(true)
	mgr.cfg.FailObjectImportHook = func(vID, _ string) error {
		if vID == "view-retry" && failOnce.CompareAndSwap(true, false) {
			return errors.New("injected object import failure")
		}
		return nil
	}
	coreRetry := newMountedViewWithManager(t, mgr, "view-retry", fix.trunkDir)
	if _, err := coreRetry.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}); !errors.Is(err, layeredfs.ErrGitPending) {
		t.Fatalf("expected ErrGitPending on injected import failure, got %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, sErr := coreRetry.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}); sErr != nil {
				t.Errorf("concurrent retry Stat(.git/HEAD) failed: %v", sErr)
			}
		}()
	}
	wg.Wait()

	// Stacked child lease and GC verification.
	stackedLease, err := mgr.Store().RetainForStackedChild("view-child", "cp-parent", nil)
	testutil.FailErr(t, "RetainForStackedChild", err)
	testutil.FailErr(t, "ReleaseLease stacked", mgr.Store().ReleaseLease(stackedLease))
}

func TestFetchRepackNoBorrowedPackCopyAndPushEgressPolicy(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)

	// Create a bare remote fixture initialized from trunk and add a new commit to it via a clone.
	remoteBare := filepath.Join(fix.tmpDir, "remote.git")
	gittest.Run(t, fix.tmpDir, "clone", "--bare", fix.trunkDir, remoteBare)
	gittest.Run(t, fix.trunkDir, "remote", "add", "origin", remoteBare)

	pusherClone := filepath.Join(fix.tmpDir, "pusher-clone")
	gittest.Run(t, fix.tmpDir, "-c", "protocol.file.allow=always", "clone", remoteBare, pusherClone)
	testutil.FailErr(t, "write remote_new.txt", os.WriteFile(filepath.Join(pusherClone, "remote_new.txt"), []byte("from-remote\n"), 0o644))
	gittest.CommitAll(t, pusherClone, "upstream commit on remote")
	gittest.Run(t, pusherClone, "push", "origin", "HEAD:refs/heads/upstream-branch")

	mgr := newTestManager(t, fix.tmpDir)
	_ = newMountedViewWithManager(t, mgr, "view-net", fix.trunkDir)

	// 1. Disallowed fetch and push must be refused by RemotePolicy.
	deniedExec := NewGitExecutor(mgr, "view-net", RemotePolicy{AllowFetch: false, AllowPush: false})
	if _, err := deniedExec.Fetch(t.Context(), "origin"); !errors.Is(err, ErrRemoteOperationRefused) {
		t.Fatalf("expected ErrRemoteOperationRefused when AllowFetch=false, got %v", err)
	}
	if _, err := deniedExec.Push(t.Context(), "origin", "main"); !errors.Is(err, ErrRemoteOperationRefused) {
		t.Fatalf("expected ErrRemoteOperationRefused when AllowPush=false, got %v", err)
	}
	// Pushing directly to trunk must be refused even if AllowPush=true!
	trunkPushExec := NewGitExecutor(mgr, "view-net", RemotePolicy{
		AllowPush:       true,
		ApprovedRemotes: map[string]bool{"origin": true},
	})
	if _, err := trunkPushExec.Push(t.Context(), fix.trunkDir, "main"); !errors.Is(err, ErrRemoteOperationRefused) {
		t.Fatalf("expected ErrRemoteOperationRefused pushing to unapproved trunk, got %v", err)
	}

	// 2. Approved fetch + repack creates private pack without copying borrowed packs, and approved push succeeds.
	credChecked := 0
	approvedExec := NewGitExecutor(mgr, "view-net", RemotePolicy{
		AllowFetch:      true,
		AllowPush:       true,
		ApprovedRemotes: map[string]bool{"origin": true, remoteBare: true},
		CredentialCheck: func(_ context.Context, _, _, _ string) error {
			credChecked++
			return nil
		},
	})
	_, err := approvedExec.Fetch(t.Context(), "origin")
	testutil.FailErr(t, "approved Fetch", err)
	_, err = approvedExec.Repack(t.Context(), "-a", "-d")
	testutil.FailErr(t, "approved Repack", err)

	privGitDir, err := mgr.PrivateGitDir("view-net", ".")
	testutil.FailErr(t, "PrivateGitDir", err)
	storePacks, _ := os.ReadDir(filepath.Join(mgr.Store().ObjectsDir(), "pack"))
	privPacks, _ := os.ReadDir(filepath.Join(privGitDir, "objects", "pack"))
	storePackNames := make(map[string]bool)
	for _, sp := range storePacks {
		storePackNames[sp.Name()] = true
	}
	for _, pp := range privPacks {
		if storePackNames[pp.Name()] {
			t.Fatalf("borrowed pack %s was copied into worker private objects/pack", pp.Name())
		}
	}
	if mgr.Store().Metrics().BytesNewlyWritten == 0 {
		t.Fatalf("expected BytesNewlyWritten > 0 after fetch/repack, got %+v", mgr.Store().Metrics())
	}

	_, err = approvedExec.Commit(t.Context(), "worker commit for push")
	testutil.FailErr(t, "commit before push", err)
	_, err = approvedExec.Push(t.Context(), "origin", "HEAD:refs/heads/worker-pushed")
	testutil.FailErr(t, "approved Push", err)
	if credChecked < 2 {
		t.Fatalf("expected CredentialCheck to run for fetch and push, got %d", credChecked)
	}
}

func TestCrashRecoveryRefImportIdempotencyConflictAndDeletedRefSafety(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)
	core := newMountedViewWithManager(t, mgr, "view-import", fix.trunkDir)
	exec := NewGitExecutor(mgr, "view-import", RemotePolicy{})

	// Worker deletes `side-branch` privately, creates a new branch `worker-br`, a tag `v1-worker`, and a stash.
	_, err := exec.Run(t.Context(), ".", "branch", "-D", "side-branch")
	testutil.FailErr(t, "delete side-branch privately", err)
	_, err = exec.Switch(t.Context(), "-c", "worker-br")
	testutil.FailErr(t, "switch -c worker-br", err)
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "imported.txt"}, []byte("import-me\n"), 0o644)
	testutil.FailErr(t, "write imported.txt", err)
	_, _ = exec.Run(t.Context(), ".", "add", "imported.txt")
	_, err = exec.Commit(t.Context(), "worker branch commit")
	testutil.FailErr(t, "commit worker-br", err)
	_, err = exec.Run(t.Context(), ".", "tag", "v1-worker")
	testutil.FailErr(t, "tag v1-worker", err)
	_, err = exec.Stash(t.Context(), "push", "-m", "checkpointed-stash")
	testutil.FailErr(t, "stash push", err)

	cp, err := mgr.CaptureCheckpointGitState(t.Context(), "view-import", "cp-crash-1")
	testutil.FailErr(t, "CaptureCheckpointGitState", err)

	// Inject crashes at StageObjectsImported, StageMidRefPublish, and StageRefsPublished, then retry cleanly!
	allOpts := RefImportOptionFlags{IncludeDetachedHead: true, IncludeTags: true, IncludeStashes: true}
	for _, crashAt := range []ImportStage{StageObjectsImported, StageMidRefPublish, StageRefsPublished} {
		simErr := errors.New("simulated crash at " + string(crashAt))
		_, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
			JobID: "job-crash", WorkerID: "worker-view-import", CheckpointID: "cp-crash-1",
			TrunkDir: fix.trunkDir, Checkpoint: cp, Options: allOpts,
			CrashHook: func(stage ImportStage) error {
				if stage == crashAt {
					return simErr
				}
				return nil
			},
		})
		if !errors.Is(err, simErr) {
			t.Fatalf("expected simulated crash at %s, got %v", crashAt, err)
		}
	}

	// Idempotent retry without crash hook must succeed and produce the exact namespaced refs.
	res, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID: "job-crash", WorkerID: "worker-view-import", CheckpointID: "cp-crash-1",
		TrunkDir: fix.trunkDir, Checkpoint: cp, Options: allOpts,
	})
	testutil.FailErr(t, "idempotent ImportRefsToTrunk retry", err)

	for _, wantRef := range []string{
		"refs/paintedwolf/workers/job-crash/cp-crash-1/heads/worker-br",
		"refs/paintedwolf/workers/job-crash/cp-crash-1/tags/v1-worker",
		"refs/paintedwolf/workers/job-crash/cp-crash-1/stash/0",
	} {
		if res.ImportedRefs[wantRef] == "" {
			t.Fatalf("missing expected imported ref %s in %+v", wantRef, res.ImportedRefs)
		}
	}

	// Prove private deletion of `side-branch` NEVER deleted `refs/heads/side-branch` in trunk!
	trunkBranches := gittest.Run(t, fix.trunkDir, "branch", "--list", "side-branch")
	if !strings.Contains(trunkBranches, "side-branch") {
		t.Fatalf("private branch deletion must never delete trunk branch; got %q", trunkBranches)
	}
	// Prove importing commits did not apply `imported.txt` to trunk's working tree!
	if _, err := os.Stat(filepath.Join(fix.trunkDir, "imported.txt")); !os.IsNotExist(err) {
		t.Fatalf("ref import must not apply files to trunk working tree, got err=%v", err)
	}

	// Identity mismatch and destination ref conflict checks.
	if _, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID:        "job-crash",
		WorkerID:     "wrong-worker-id",
		CheckpointID: "cp-crash-1",
		TrunkDir:     fix.trunkDir,
		Checkpoint:   cp,
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch, got %v", err)
	}

	mainOID := strings.TrimSpace(gittest.Run(t, fix.trunkDir, "rev-parse", "refs/heads/main"))
	gittest.Run(t, fix.trunkDir, "update-ref", "refs/paintedwolf/workers/job-conflict/cp-crash-1/heads/worker-br", mainOID)
	if _, err := mgr.ImportRefsToTrunk(t.Context(), RefImportRequest{
		JobID:        "job-conflict",
		WorkerID:     "worker-view-import",
		CheckpointID: "cp-crash-1",
		TrunkDir:     fix.trunkDir,
		Checkpoint:   cp,
	}); !errors.Is(err, ErrRefImportConflict) {
		t.Fatalf("expected ErrRefImportConflict when destination ref has different OID, got %v", err)
	}
}
