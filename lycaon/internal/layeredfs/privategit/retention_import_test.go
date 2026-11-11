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

	exported := filepath.Join(t.TempDir(), "exported")
	testutil.FailErr(t, "export checkpoint objects", mgr.ExportCheckpointObjects(t.Context(), "view-prune", cp, exported))

	// Release the worker view and run GC on the retained store: checkpoint lease must protect history!
	testutil.FailErr(t, "core.ReleaseView", core.ReleaseView())
	testutil.FailErr(t, "mgr.ReleaseView", mgr.ReleaseView("view-prune"))
	_, err = mgr.Store().CollectGarbage(t.Context())
	testutil.FailErr(t, "CollectGarbage with active checkpoint lease", err)
	testutil.FailErr(t, "VerifyCheckpointHistory after view release & GC", mgr.VerifyCheckpointHistory(t.Context(), cp))

	// The export carries the history the trunk pruned, so the trunk can take the worker's commits back.
	packs, err := filepath.Glob(filepath.Join(exported, "pack", "*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("exported packs = %v, %v", packs, err)
	}
	trunkWTBefore := gittest.Run(t, fix.trunkDir, "status", "--porcelain")
	raw, err := os.ReadFile(packs[0])
	testutil.FailErr(t, "read exported pack", err)
	admitted := filepath.Join(fix.trunkDir, ".git", "objects", "pack", filepath.Base(packs[0]))
	testutil.FailErr(t, "place exported pack", os.WriteFile(admitted, raw, 0o444))
	gittest.Run(t, fix.trunkDir, "index-pack", "--strict", admitted)
	trunkLog := gittest.Run(t, fix.trunkDir, "log", "--oneline", cp.HeadOID)
	if !strings.Contains(trunkLog, "ephemeral commit") || !strings.Contains(trunkLog, "worker commit on top") {
		t.Fatalf("expected the exported history in trunk, got:\n%s", trunkLog)
	}
	if trunkWTAfter := gittest.Run(t, fix.trunkDir, "status", "--porcelain"); trunkWTBefore != trunkWTAfter {
		t.Fatalf("admitting objects altered the trunk working tree: before=%q after=%q", trunkWTBefore, trunkWTAfter)
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

	// Failed import leaves retryable ErrGitPending state, never a partially usable repo; concurrent
	// lookups succeed on retry.
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

	_, reachableKeep, err := store.ImportRepositoryObjects(ctx, []string{objKeep}, []string{oidKeep})
	testutil.FailErr(t, "import keep", err)
	_, err = store.RetainForCheckpoint("cp-keep", reachableKeep)
	testutil.FailErr(t, "lease keep", err)
	baseMetrics := store.Metrics()

	packDstDir := filepath.Join(storeRoot, "objects", "pack")
	candidatePackPath := filepath.Join(packDstDir, candidateBase+".pack")
	candidateIdxPath := filepath.Join(packDstDir, candidateBase+".idx")
	candidateMarkerPath := filepath.Join(packDstDir, retiringPackPrefix+candidateBase+".pack")

	sc := importFaultScenario{ctx: ctx, tmp: tmp, objKeep: objKeep, oidKeep: oidKeep, objLoose: objLoose, oidLoose: oidLoose, objPack: objPack, oidPack: oidPack, candidateBase: candidateBase, storeRoot: storeRoot, store: store, reachableKeep: reachableKeep, baseMetrics: baseMetrics, packDstDir: packDstDir, candidatePackPath: candidatePackPath, candidateIdxPath: candidateIdxPath, candidateMarkerPath: candidateMarkerPath, makeRepo: makeRepo}
	sc.importFaultRenameSync(t)
	sc.importFaultRollbackSteps(t)
}

// missingTip names a commit no store holds, so connectivity fails and the import rolls back.
var missingTip = strings.Repeat("f", 40)

// importFaultScenario is one store with a leased pack and the candidates later imports roll back.
type importFaultScenario struct {
	ctx                                                                                   context.Context
	tmp, objKeep, oidKeep, objLoose, oidLoose, objPack, oidPack, candidateBase, storeRoot string
	store                                                                                 *RetainedObjectStore
	reachableKeep                                                                         []string
	baseMetrics                                                                           StoreMetrics
	packDstDir, candidatePackPath, candidateIdxPath, candidateMarkerPath                  string
	makeRepo                                                                              func(name, content string, pack bool) (string, string)
}

func (sc importFaultScenario) importFaultRenameSync(t *testing.T) {
	t.Helper()
	ctx := sc.ctx
	objKeep := sc.objKeep
	oidKeep := sc.oidKeep
	objLoose := sc.objLoose
	oidLoose := sc.oidLoose
	objPack := sc.objPack
	oidPack := sc.oidPack
	store := sc.store
	baseMetrics := sc.baseMetrics
	packDstDir := sc.packDstDir
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

	// 1b. Preexisting leased pack+idx without .rev: when a subsequent import adds a .rev for the same
	// preexisting base, reuses the existing .pack/.idx, and then rolls back on connectivity failure,
	// rollback must unlink only the newly added .rev and MUST NOT retire the preexisting leased .pack/.idx pair.
	keepPackEntries, err := os.ReadDir(filepath.Join(objKeep, "pack"))
	testutil.FailErr(t, "ReadDir src keep pack", err)
	var keepBase string
	for _, pe := range keepPackEntries {
		if strings.HasPrefix(pe.Name(), "pack-") && strings.HasSuffix(pe.Name(), ".pack") {
			keepBase = strings.TrimSuffix(pe.Name(), ".pack")
			break
		}
	}
	if keepBase == "" {
		t.Fatalf("expected pack-<hash>.pack in keep source repo")
	}
	// Remove any preexisting .rev in the store so keepBase starts with only .pack and .idx in the store,
	// while ensuring the source directory has a .rev companion to copy.
	storeKeepRevPath := filepath.Join(packDstDir, keepBase+".rev")
	_ = os.Remove(storeKeepRevPath)
	srcKeepRevPath := filepath.Join(objKeep, "pack", keepBase+".rev")
	if _, err := os.Stat(srcKeepRevPath); os.IsNotExist(err) {
		testutil.FailErr(t, "write synthetic src .rev for keep", os.WriteFile(srcKeepRevPath, []byte("RIDX\x00\x00\x00\x01"), 0o644))
	}
	_, _, revRbErr := store.ImportRepositoryObjects(ctx, []string{objKeep}, []string{oidKeep, missingTip})
	if !errors.Is(revRbErr, ErrCorruptObjectStore) {
		t.Fatalf("expected ErrCorruptObjectStore when rollback triggers after copying .rev for preexisting base, got %v", revRbErr)
	}
	if !store.HasObject(oidKeep) || !store.HasObject("pack:"+keepBase) {
		t.Fatalf("expected preexisting leased pack and object preserved when only auxiliary .rev was added and rolled back")
	}
	if _, err := os.Stat(filepath.Join(packDstDir, keepBase+".pack")); err != nil {
		t.Fatalf("expected preexisting leased .pack still on disk after .rev rollback, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(packDstDir, keepBase+".idx")); err != nil {
		t.Fatalf("expected preexisting leased .idx still on disk after .rev rollback, got %v", err)
	}
	if _, err := os.Stat(storeKeepRevPath); !os.IsNotExist(err) {
		t.Fatalf("expected newly added .rev for preexisting base unlinked on rollback, got %v", err)
	}

}

// importFaultRollbackSteps fails each step of a candidate pack's rollback in turn, then retries cleanly.
func (sc importFaultScenario) importFaultRollbackSteps(t *testing.T) {
	t.Helper()
	sc.rollbackMarkerCreateFault(t)
	sc.store = sc.rollbackIntentSyncFault(t)
	sc.store = sc.rollbackCompanionUnlinkFault(t)
	sc.store = sc.rollbackSecondBarrierFault(t)
	sc.store = sc.rollbackMarkerRemovalFault(t)
	sc.rollbackThirdBarrierFaultThenRetry(t)
}

// candidateOnDisk checks which of the candidate pack, its index and the rollback marker exist.
func (sc importFaultScenario) candidateOnDisk(t *testing.T, when string, pack, idx, marker bool) {
	t.Helper()
	for path, want := range map[string]bool{sc.candidatePackPath: pack, sc.candidateIdxPath: idx, sc.candidateMarkerPath: marker} {
		_, err := os.Stat(path)
		if present := err == nil; present != want || (err != nil && !os.IsNotExist(err)) {
			t.Fatalf("%s: %s present=%t, want %t (%v)", when, filepath.Base(path), present, want, err)
		}
	}
}

// importCandidateFault imports the candidate pack under faultCtx and requires the corrupt-store error
// joined with fault.
func (sc importFaultScenario) importCandidateFault(t *testing.T, store *RetainedObjectStore, faultCtx context.Context, fault error) {
	t.Helper()
	_, _, err := store.ImportRepositoryObjects(faultCtx, []string{sc.objPack}, []string{sc.oidPack, missingTip})
	if !errors.Is(err, ErrCorruptObjectStore) || !errors.Is(err, fault) {
		t.Fatalf("expected joined ErrCorruptObjectStore and %v, got %v", fault, err)
	}
}

// reopenKeepsOnlyKept reopens the store and requires the kept object without the candidate's.
func (sc importFaultScenario) reopenKeepsOnlyKept(t *testing.T, after string) *RetainedObjectStore {
	t.Helper()
	store, err := NewRetainedObjectStoreContext(sc.ctx, sc.storeRoot)
	testutil.FailErr(t, "reopen after "+after, err)
	if !store.HasObject(sc.oidKeep) || store.HasObject(sc.oidPack) {
		t.Fatalf("reopen after %s: keep must survive and the candidate must be retired", after)
	}
	return store
}

// rollbackSyncFault fails the nth sync of the pack directory once the rollback marker is written.
func (sc importFaultScenario) rollbackSyncFault(n int32, fault error) context.Context {
	var markerWritten atomic.Bool
	var syncs atomic.Int32
	return withGCTestHooks(sc.ctx, gcTestHooks{
		beforeRollbackMarkerWrite: func(_, _ string) error {
			markerWritten.Store(true)
			return nil
		},
		syncDir: func(dirPath string) error {
			if markerWritten.Load() && dirPath == sc.packDstDir && syncs.Add(1) == n {
				return fault
			}
			return nil
		},
	})
}

// A marker that cannot be written leaves the candidate's files alone and drops it from the live stock,
// so continued use never advertises it and GC retires it.
func (sc importFaultScenario) rollbackMarkerCreateFault(t *testing.T) {
	fault := errors.New("injected marker creation failure")
	sc.importCandidateFault(t, sc.store, withGCTestHooks(sc.ctx, gcTestHooks{
		beforeRollbackMarkerWrite: func(_, _ string) error { return fault },
	}), fault)
	sc.candidateOnDisk(t, "marker creation failure", true, true, false)
	if sc.store.HasObject(sc.oidPack) || sc.store.HasObject("pack:"+sc.candidateBase) {
		t.Fatal("the live store advertises a candidate pack it rolled back")
	}
	_, err := sc.store.CollectGarbage(sc.ctx)
	testutil.FailErr(t, "collect the unleased candidate", err)
	if !sc.store.HasObject(sc.oidKeep) || sc.store.HasObject(sc.oidPack) {
		t.Fatal("GC must keep the leased object and collect the candidate")
	}
}

// When the intent sync fails, nothing is unlinked and the marker stays. The same store reconciles it at
// its next import, and a reopen reconciles a fresh one.
func (sc importFaultScenario) rollbackIntentSyncFault(t *testing.T) *RetainedObjectStore {
	fault := errors.New("injected intent directory sync failure")
	sc.importCandidateFault(t, sc.store, sc.rollbackSyncFault(1, fault), fault)
	sc.candidateOnDisk(t, "intent sync failure", true, true, true)
	if sc.store.HasObject(sc.oidPack) || sc.store.HasObject("pack:"+sc.candidateBase) {
		t.Fatal("the live store advertises a candidate pack after the intent sync failed")
	}
	_, _, err := sc.store.ImportRepositoryObjects(sc.ctx, []string{sc.objLoose}, []string{sc.oidLoose})
	testutil.FailErr(t, "continue importing on the same store", err)
	sc.candidateOnDisk(t, "continued import", false, false, false)
	if !sc.store.HasObject(sc.oidKeep) || !sc.store.HasObject(sc.oidLoose) || sc.store.HasObject(sc.oidPack) {
		t.Fatal("continued import must keep the kept and loose objects and drop the candidate")
	}
	sc.importCandidateFault(t, sc.store, sc.rollbackSyncFault(1, fault), fault)
	return sc.reopenKeepsOnlyKept(t, "intent sync failure")
}

// A failed second companion unlink leaves the index and the marker for reopen to finish.
func (sc importFaultScenario) rollbackCompanionUnlinkFault(t *testing.T) *RetainedObjectStore {
	fault := errors.New("injected second companion unlink failure")
	sc.importCandidateFault(t, sc.store, withGCTestHooks(sc.ctx, gcTestHooks{
		removeFile: func(p string) error {
			if p == sc.candidateIdxPath {
				return fault
			}
			return os.Remove(p)
		},
	}), fault)
	sc.candidateOnDisk(t, "companion unlink failure", false, true, true)
	return sc.reopenKeepsOnlyKept(t, "companion unlink failure")
}

// The marker outlives a failed sync after both companions are gone.
func (sc importFaultScenario) rollbackSecondBarrierFault(t *testing.T) *RetainedObjectStore {
	fault := errors.New("injected second barrier sync failure")
	sc.importCandidateFault(t, sc.store, sc.rollbackSyncFault(2, fault), fault)
	sc.candidateOnDisk(t, "second barrier failure", false, false, true)
	return sc.reopenKeepsOnlyKept(t, "second barrier failure")
}

// A marker that cannot be removed stays for reopen.
func (sc importFaultScenario) rollbackMarkerRemovalFault(t *testing.T) *RetainedObjectStore {
	fault := errors.New("injected marker removal failure")
	sc.importCandidateFault(t, sc.store, withGCTestHooks(sc.ctx, gcTestHooks{
		removeFile: func(p string) error {
			if p == sc.candidateMarkerPath {
				return fault
			}
			return os.Remove(p)
		},
	}), fault)
	if _, err := os.Stat(sc.candidateMarkerPath); err != nil {
		t.Fatalf("the marker must survive a failed removal: %v", err)
	}
	store, err := NewRetainedObjectStoreContext(sc.ctx, sc.storeRoot)
	testutil.FailErr(t, "reopen after marker removal failure", err)
	return store
}

// After a failed final sync, a reopened store retires the candidate and a retry imports it.
func (sc importFaultScenario) rollbackThirdBarrierFaultThenRetry(t *testing.T) {
	fault := errors.New("injected third barrier sync failure")
	sc.importCandidateFault(t, sc.store, sc.rollbackSyncFault(3, fault), fault)
	store := sc.reopenKeepsOnlyKept(t, "third barrier failure")
	_, _, err := store.ImportRepositoryObjects(sc.ctx, []string{sc.objPack}, []string{sc.oidPack})
	testutil.FailErr(t, "retry after the fault clears", err)
	if !store.HasObject(sc.oidKeep) || !store.HasObject(sc.oidPack) {
		t.Fatal("a retry must hold both the kept and the imported objects")
	}
}
