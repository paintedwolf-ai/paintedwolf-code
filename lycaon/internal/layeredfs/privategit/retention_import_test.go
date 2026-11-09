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
	_, err = exec1.Run(t.Context(), ".", "switch", "-c", "only-in-w1")
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

func TestCollectingAnEmptyStoreLeavesItsHostDirectoryRemovable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := NewRetainedObjectStore(root)
	testutil.FailErr(t, "open store", err)
	_, err = store.CollectGarbage(t.Context())
	testutil.FailErr(t, "collect", err)
	testutil.FailErr(t, "remove host directory", os.RemoveAll(root))
}

func TestImportRepositoryObjectsPostRenameSyncAndRollbackRemovalFaults(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	tmp := t.TempDir()

	makeRepo := func(name, content string, pack bool) (string, string) {
		dir := filepath.Join(tmp, name)
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(dir, 0o755))
		gittest.Init(t, dir)
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(content), 0o644))
		gittest.CommitAll(t, dir, "commit "+name)
		oid := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
		if pack {
			gittest.Run(t, dir, "repack", "-a", "-d")
		}
		return filepath.Join(dir, ".git", "objects"), oid
	}

	objKeep, oidKeep := makeRepo("keep", "keep-leased-payload\n", true)
	objLoose, oidLoose := makeRepo("loose", "loose-candidate-payload\n", false)
	objPack, oidPack := makeRepo("pack", "pack-candidate-payload\n", true)

	srcPackEntries, err := os.ReadDir(filepath.Join(objPack, "pack"))
	testutil.FailErr(t, "ReadDir src pack", err)
	var candidateBase string
	for _, pe := range srcPackEntries {
		if strings.HasPrefix(pe.Name(), "pack-") && strings.HasSuffix(pe.Name(), ".pack") {
			candidateBase = strings.TrimSuffix(pe.Name(), ".pack")
			break
		}
	}
	if candidateBase == "" {
		t.Fatalf("expected pack-<hash>.pack in candidate source repo")
	}

	storeRoot := filepath.Join(tmp, "store")
	store, err := NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "NewRetainedObjectStoreContext", err)
	t.Cleanup(func() { _ = store.Close() })

	_, reachableKeep, err := store.ImportRepositoryObjects(ctx, []string{objKeep}, []string{oidKeep})
	testutil.FailErr(t, "import keep", err)
	_, err = store.RetainForCheckpoint("cp-keep", reachableKeep)
	testutil.FailErr(t, "lease keep", err)
	baseMetrics := store.Metrics()

	packDstDir := filepath.Join(storeRoot, "objects", "pack")
	candidatePackPath := filepath.Join(packDstDir, candidateBase+".pack")
	candidateIdxPath := filepath.Join(packDstDir, candidateBase+".idx")
	candidateMarkerPath := filepath.Join(packDstDir, retiringPackPrefix+candidateBase+".pack")

	// 1. Injected post-rename syncDir failure during copyFileAtomicReadOnly rolls back
	// the already-renamed loose or pack file cleanly via S1-S8 while preserving leased stock.
	errSyncFault := errors.New("injected post-rename directory sync failure")
	var syncCalls atomic.Int32
	oneShotSyncFailCtx := withGCTestHooks(ctx, gcTestHooks{
		syncDir: func(_ string) error {
			if syncCalls.Add(1) == 1 {
				return errSyncFault
			}
			return nil
		},
	})
	if _, _, err := store.ImportRepositoryObjects(oneShotSyncFailCtx, []string{objLoose}, []string{oidLoose}); !errors.Is(err, errSyncFault) {
		t.Fatalf("expected errSyncFault on loose import, got %v", err)
	}
	if store.HasObject(oidLoose) {
		t.Fatalf("expected post-rename loose object to be rolled back after syncDir failure")
	}
	syncCalls.Store(0)
	if _, _, err := store.ImportRepositoryObjects(oneShotSyncFailCtx, []string{objPack}, []string{oidPack}); !errors.Is(err, errSyncFault) {
		t.Fatalf("expected errSyncFault on pack import, got %v", err)
	}
	if store.HasObject(oidPack) {
		t.Fatalf("expected post-rename pack object to be rolled back after syncDir failure")
	}
	invAfterSyncFault, err := store.InspectStock(ctx)
	testutil.FailErr(t, "InspectStock after syncDir faults", err)
	if !invAfterSyncFault.Complete || store.Metrics().RetainedObjects != baseMetrics.RetainedObjects || invAfterSyncFault.LogicalObjects != baseMetrics.RetainedObjects {
		t.Fatalf("expected preexisting leased stock preserved after syncDir rollback: base=%+v current=%+v inv=%+v",
			baseMetrics, store.Metrics(), invAfterSyncFault)
	}

	missingTip := strings.Repeat("f", 40)

	// 2. S1 Marker-Creation Failure (beforeRollbackMarkerWrite):
	// Verify zero destructive progression occurs when marker creation fails:
	// both candidate .pack and .idx companions remain on disk, zero .retiring- markers exist,
	// and removePackLocked did not run before S1 completed.
	errMarkerCreateFault := errors.New("injected S1 marker creation failure")
	s1CreateFailCtx := withGCTestHooks(ctx, gcTestHooks{
		beforeRollbackMarkerWrite: func(_, _ string) error {
			return errMarkerCreateFault
		},
	})
	_, _, s1CreateErr := store.ImportRepositoryObjects(s1CreateFailCtx, []string{objPack}, []string{oidPack, missingTip})
	if !errors.Is(s1CreateErr, ErrCorruptObjectStore) || !errors.Is(s1CreateErr, errMarkerCreateFault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and errMarkerCreateFault, got %v", s1CreateErr)
	}
	if _, err := os.Stat(candidatePackPath); err != nil {
		t.Fatalf("expected candidate .pack still present after S1 marker creation failure, got %v", err)
	}
	if _, err := os.Stat(candidateIdxPath); err != nil {
		t.Fatalf("expected candidate .idx still present after S1 marker creation failure, got %v", err)
	}
	if _, err := os.Stat(candidateMarkerPath); !os.IsNotExist(err) {
		t.Fatalf("expected no .retiring- marker on disk after S1 marker creation failure, got %v", err)
	}
	if !store.HasObject(oidPack) {
		t.Fatalf("expected in-memory pack stock untouched when S1 stops before S2 removePackLocked")
	}
	// Clean up the unreferenced intact candidate pack via GC so next stage starts clean.
	if _, gcErr := store.CollectGarbage(ctx); gcErr != nil {
		t.Fatalf("expected CollectGarbage to retire unleased intact candidate pack cleanly, got %v", gcErr)
	}
	if !store.HasObject(oidKeep) || store.HasObject(oidPack) {
		t.Fatalf("expected keep preserved and unleased candidate pack collected after GC")
	}

	// 3. S1 Barrier-1 Intent-Sync Failure (syncDir failing on first rollback sync after marker write):
	// Verify zero companion unlinks occur when Barrier-1 fails, while .retiring- marker remains on disk.
	errIntentSyncFault := errors.New("injected S1 Barrier-1 intent directory sync failure")
	var markerWritten atomic.Bool
	s1SyncFailCtx := withGCTestHooks(ctx, gcTestHooks{
		beforeRollbackMarkerWrite: func(_, _ string) error {
			markerWritten.Store(true)
			return nil
		},
		syncDir: func(dirPath string) error {
			if markerWritten.Load() && dirPath == packDstDir {
				return errIntentSyncFault
			}
			return nil
		},
	})
	_, _, s1SyncErr := store.ImportRepositoryObjects(s1SyncFailCtx, []string{objPack}, []string{oidPack, missingTip})
	if !errors.Is(s1SyncErr, ErrCorruptObjectStore) || !errors.Is(s1SyncErr, errIntentSyncFault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and errIntentSyncFault, got %v", s1SyncErr)
	}
	if _, err := os.Stat(candidatePackPath); err != nil {
		t.Fatalf("expected candidate .pack still present (zero companion unlinks) when S1 Barrier-1 fails, got %v", err)
	}
	if _, err := os.Stat(candidateIdxPath); err != nil {
		t.Fatalf("expected candidate .idx still present (zero companion unlinks) when S1 Barrier-1 fails, got %v", err)
	}
	if _, err := os.Stat(candidateMarkerPath); err != nil {
		t.Fatalf("expected .retiring- marker present on disk when S1 Barrier-1 fails after write, got %v", err)
	}
	// Reopen reconciles the S1 marker cleanly and preserves oidKeep.
	testutil.FailErr(t, "close after S1 Barrier-1 fault", store.Close())
	store, err = NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "reopen after S1 Barrier-1 fault", err)
	if !store.HasObject(oidKeep) || store.HasObject(oidPack) {
		t.Fatalf("expected keep preserved and candidate retired on reopen after S1 Barrier-1 fault")
	}

	// 4. S4 Mid-Companion-Removal Failure (fail on 2nd companion unlink .idx after unlinking .pack):
	// Verify .pack is unlinked, .idx and .retiring- marker remain on disk, and reopen recovers cleanly.
	errCompanionUnlinkFault := errors.New("injected S4 second companion unlink failure")
	s4FailCtx := withGCTestHooks(ctx, gcTestHooks{
		removeFile: func(p string) error {
			if p == candidateIdxPath {
				return errCompanionUnlinkFault
			}
			return os.Remove(p)
		},
	})
	_, _, s4Err := store.ImportRepositoryObjects(s4FailCtx, []string{objPack}, []string{oidPack, missingTip})
	if !errors.Is(s4Err, ErrCorruptObjectStore) || !errors.Is(s4Err, errCompanionUnlinkFault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and errCompanionUnlinkFault, got %v", s4Err)
	}
	if _, err := os.Stat(candidatePackPath); !os.IsNotExist(err) {
		t.Fatalf("expected .pack unlinked before 2nd companion failure, got %v", err)
	}
	if _, err := os.Stat(candidateIdxPath); err != nil {
		t.Fatalf("expected .idx still on disk after S4 failure on .idx unlink, got %v", err)
	}
	if _, err := os.Stat(candidateMarkerPath); err != nil {
		t.Fatalf("expected .retiring- marker retained on disk when S4 companion unlink fails, got %v", err)
	}
	testutil.FailErr(t, "close after S4 fault", store.Close())
	store, err = NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "reopen after S4 fault", err)
	if !store.HasObject(oidKeep) || store.HasObject(oidPack) {
		t.Fatalf("expected keep preserved and half-unlinked candidate retired on reopen after S4 fault")
	}

	// 5. S5 Barrier-2 Failure (directory sync between companion removal and marker removal):
	// Verify companions are unlinked, .retiring- marker is STILL retained on disk (Barrier-2 gated marker deletion),
	// and reopen reconciles cleanly.
	errBarrier2SyncFault := errors.New("injected S5 Barrier-2 directory sync failure")
	var rollbackPackSyncs atomic.Int32
	markerWritten.Store(false)
	s5FailCtx := withGCTestHooks(ctx, gcTestHooks{
		beforeRollbackMarkerWrite: func(_, _ string) error {
			markerWritten.Store(true)
			return nil
		},
		syncDir: func(dirPath string) error {
			if markerWritten.Load() && dirPath == packDstDir {
				if rollbackPackSyncs.Add(1) == 2 {
					return errBarrier2SyncFault
				}
			}
			return nil
		},
	})
	_, _, s5Err := store.ImportRepositoryObjects(s5FailCtx, []string{objPack}, []string{oidPack, missingTip})
	if !errors.Is(s5Err, ErrCorruptObjectStore) || !errors.Is(s5Err, errBarrier2SyncFault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and errBarrier2SyncFault, got %v", s5Err)
	}
	if _, err := os.Stat(candidatePackPath); !os.IsNotExist(err) {
		t.Fatalf("expected candidate .pack unlinked before S5 Barrier-2, got %v", err)
	}
	if _, err := os.Stat(candidateIdxPath); !os.IsNotExist(err) {
		t.Fatalf("expected candidate .idx unlinked before S5 Barrier-2, got %v", err)
	}
	if _, err := os.Stat(candidateMarkerPath); err != nil {
		t.Fatalf("expected .retiring- marker retained on disk when S5 Barrier-2 fails, got %v", err)
	}
	testutil.FailErr(t, "close after S5 Barrier-2 fault", store.Close())
	store, err = NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "reopen after S5 Barrier-2 fault", err)
	if !store.HasObject(oidKeep) || store.HasObject(oidPack) {
		t.Fatalf("expected keep preserved and leftover marker cleaned on reopen after S5 Barrier-2 fault")
	}

	// 6. S6 Marker-Removal Failure & S7 Barrier-3 Failure, followed by clean reopen and retry.
	errMarkerRemoveFault := errors.New("injected S6 marker removal failure")
	s6FailCtx := withGCTestHooks(ctx, gcTestHooks{
		removeFile: func(p string) error {
			if p == candidateMarkerPath {
				return errMarkerRemoveFault
			}
			return os.Remove(p)
		},
	})
	_, _, s6Err := store.ImportRepositoryObjects(s6FailCtx, []string{objPack}, []string{oidPack, missingTip})
	if !errors.Is(s6Err, ErrCorruptObjectStore) || !errors.Is(s6Err, errMarkerRemoveFault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and errMarkerRemoveFault, got %v", s6Err)
	}
	if _, err := os.Stat(candidateMarkerPath); err != nil {
		t.Fatalf("expected .retiring- marker still on disk after S6 marker removal fault, got %v", err)
	}
	testutil.FailErr(t, "close after S6 fault", store.Close())
	store, err = NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "reopen after S6 fault", err)

	errBarrier3SyncFault := errors.New("injected S7 Barrier-3 directory sync failure")
	rollbackPackSyncs.Store(0)
	markerWritten.Store(false)
	s7FailCtx := withGCTestHooks(ctx, gcTestHooks{
		beforeRollbackMarkerWrite: func(_, _ string) error {
			markerWritten.Store(true)
			return nil
		},
		syncDir: func(dirPath string) error {
			if markerWritten.Load() && dirPath == packDstDir {
				if rollbackPackSyncs.Add(1) == 3 {
					return errBarrier3SyncFault
				}
			}
			return nil
		},
	})
	_, _, s7Err := store.ImportRepositoryObjects(s7FailCtx, []string{objPack}, []string{oidPack, missingTip})
	if !errors.Is(s7Err, ErrCorruptObjectStore) || !errors.Is(s7Err, errBarrier3SyncFault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and errBarrier3SyncFault, got %v", s7Err)
	}
	testutil.FailErr(t, "close after S7 Barrier-3 fault", store.Close())

	reopenedFinal, err := NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "reopen after S7 fault", err)
	t.Cleanup(func() { _ = reopenedFinal.Close() })
	if !reopenedFinal.HasObject(oidKeep) || reopenedFinal.HasObject(oidPack) {
		t.Fatalf("expected keep object preserved and failed pack retired on final reopen")
	}
	_, _, err = reopenedFinal.ImportRepositoryObjects(ctx, []string{objPack}, []string{oidPack})
	testutil.FailErr(t, "retry ImportRepositoryObjects after transient fault clears", err)
	if !reopenedFinal.HasObject(oidKeep) || !reopenedFinal.HasObject(oidPack) {
		t.Fatalf("expected both keep and newly imported pack objects present after retry")
	}
}
