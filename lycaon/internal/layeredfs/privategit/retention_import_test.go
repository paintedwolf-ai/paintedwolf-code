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

	privGitDir, err := readyPrivateGitDir(mgr, "view-net", ".")
	testutil.FailErr(t, "readyPrivateGitDir", err)
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

func TestInjectedCaptureAndObjectRetentionFailuresPreserveRecovery(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	mgr := newTestManager(t, fix.tmpDir)
	idxPath := filepath.Join(fix.trunkDir, ".git", "index")
	origIdx, err := os.ReadFile(idxPath)
	testutil.FailErr(t, "read trunk index", err)
	testutil.FailErr(t, "corrupt trunk index", os.WriteFile(idxPath, []byte("corrupt-index-bytes"), 0o644))

	core := newMountedViewWithManager(t, mgr, "view-fail-recover", fix.trunkDir)
	if _, _, _, pErr := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-fail-recover"); !errors.Is(pErr, ErrCorruptObjectStore) {
		t.Fatalf("expected ErrCorruptObjectStore on corrupt source index capture, got %v", pErr)
	}
	if _, sErr := core.Stat(layeredfs.QualifiedPath{RootID: "primary", Rel: ".git/HEAD"}); !errors.Is(sErr, layeredfs.ErrGitPending) {
		t.Fatalf("expected ErrGitPending on Stat(.git/HEAD) while source index is corrupt, got %v", sErr)
	}

	testutil.FailErr(t, "restore trunk index", os.WriteFile(idxPath, origIdx, 0o644))
	wtDir, finish, managed, err := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-fail-recover")
	testutil.FailErr(t, "PrepareViewGitOperation after index repair", err)
	if !managed || wtDir == "" {
		t.Fatalf("expected managed worktree after repair, got managed=%v wtDir=%q", managed, wtDir)
	}
	testutil.FailErr(t, "finish after repair", finish())

	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "recover_me.txt"}, []byte("precious-worker-work\n"), 0o644)
	testutil.FailErr(t, "write recover_me.txt", err)
	injectedRetainErr := errors.New("injected retention failure")
	mgr.cfg.FailObjectRetentionHook = func(vID, _ string) error {
		if vID == "view-fail-recover" {
			return injectedRetainErr
		}
		return nil
	}

	_, finFail, _, err := PrepareViewGitOperation(t.Context(), fix.trunkDir, "worker-view-fail-recover")
	testutil.FailErr(t, "PrepareViewGitOperation before finish failure", err)
	if fErr := finFail(); !errors.Is(fErr, ErrCorruptObjectStore) {
		t.Fatalf("expected finish() to propagate ErrCorruptObjectStore on retention failure, got %v", fErr)
	}
	mgr.cfg.FailObjectRetentionHook = nil
	cp, err := mgr.CaptureCheckpointGitState(t.Context(), "view-fail-recover", "cp-recovered")
	testutil.FailErr(t, "CaptureCheckpointGitState after clearing hook", err)
	testutil.FailErr(t, "VerifyCheckpointHistory after recovery", mgr.VerifyCheckpointHistory(t.Context(), cp))
}
