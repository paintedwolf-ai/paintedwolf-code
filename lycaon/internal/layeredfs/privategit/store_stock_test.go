package privategit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func TestRetainedObjectStoreIncrementalStockRestartDuplicatesGCAndInspect(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	ctx := t.Context()

	// Build a source repository with packed objects plus a new loose commit.
	srcRepo := filepath.Join(tmp, "src-repo")
	testutil.FailErr(t, "mkdir src-repo", os.MkdirAll(srcRepo, 0o755))
	gittest.Init(t, srcRepo)
	gittest.Run(t, srcRepo, "checkout", "-b", "main")
	testutil.FailErr(t, "write a.txt", os.WriteFile(filepath.Join(srcRepo, "a.txt"), []byte("packed-alpha\n"), 0o644))
	gittest.CommitAll(t, srcRepo, "commit packed")
	packedCommitOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))
	packedBlobOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD:a.txt"))

	// Pack the initial commit and prune loose copies in srcRepo.
	gittest.Run(t, srcRepo, "repack", "-a", "-d")

	// Add a second commit so its objects remain loose in srcRepo.
	testutil.FailErr(t, "write b.txt", os.WriteFile(filepath.Join(srcRepo, "b.txt"), []byte("loose-beta\n"), 0o644))
	gittest.CommitAll(t, srcRepo, "commit loose")
	looseCommitOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))
	looseBlobOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD:b.txt"))

	storeRoot := filepath.Join(tmp, "retained-store")
	store, err := NewRetainedObjectStore(storeRoot)
	testutil.FailErr(t, "NewRetainedObjectStore", err)

	srcObjDir := filepath.Join(srcRepo, ".git", "objects")
	_, reachable, err := store.ImportRepositoryObjects(ctx, []string{srcObjDir}, []string{looseCommitOID})
	testutil.FailErr(t, "ImportRepositoryObjects", err)

	m1 := store.Metrics()
	inv1, err := store.InspectStock(ctx)
	testutil.FailErr(t, "InspectStock initial", err)
	if !inv1.Complete {
		t.Fatalf("expected Complete=true, got %+v", inv1)
	}
	if inv1.LooseObjects <= 0 || inv1.PackedObjects <= 0 {
		t.Fatalf("expected both loose and packed objects, got %+v", inv1)
	}
	if m1.RetainedObjects != inv1.LogicalObjects || m1.RetainedBytes != inv1.StoredBytes {
		t.Fatalf("O(1) Metrics (%+v) mismatched InspectStock (%+v)", m1, inv1)
	}
	if inv1.StoredBytes != inv1.LooseBytes+inv1.PackBytes {
		t.Fatalf("expected StoredBytes == LooseBytes + PackBytes, got %+v", inv1)
	}

	// (a) Duplicate OID across loose and pack: write packedBlobOID as a loose object in a second source dir
	// and import it; LogicalObjects/RetainedObjects must stay unchanged while LooseBytes/StoredBytes increase.
	dupRepo := filepath.Join(tmp, "dup-repo")
	testutil.FailErr(t, "mkdir dup-repo", os.MkdirAll(dupRepo, 0o755))
	gittest.Init(t, dupRepo)
	dupAdded := strings.TrimSpace(gittest.Run(t, dupRepo, "hash-object", "-w", filepath.Join(srcRepo, "a.txt")))
	if dupAdded != packedBlobOID {
		t.Fatalf("expected duplicate blob OID %s, got %s", packedBlobOID, dupAdded)
	}
	_, _, err = store.ImportRepositoryObjects(ctx, []string{filepath.Join(dupRepo, ".git", "objects")}, []string{packedBlobOID})
	testutil.FailErr(t, "ImportRepositoryObjects duplicate loose blob", err)

	mDup := store.Metrics()
	invDup, err := store.InspectStock(ctx)
	testutil.FailErr(t, "InspectStock after duplicate loose blob", err)
	if mDup.RetainedObjects != inv1.LogicalObjects || invDup.LogicalObjects != inv1.LogicalObjects {
		t.Fatalf("duplicate OID across pack and loose must not inflate logical object count: before=%d metrics=%d inspect=%d",
			inv1.LogicalObjects, mDup.RetainedObjects, invDup.LogicalObjects)
	}
	if invDup.LooseObjects != inv1.LooseObjects+1 || invDup.LooseBytes <= inv1.LooseBytes {
		t.Fatalf("expected loose object count and bytes to reflect duplicate loose file: before=%+v after=%+v", inv1, invDup)
	}
	if mDup.RetainedBytes != invDup.StoredBytes || invDup.StoredBytes != invDup.LooseBytes+invDup.PackBytes {
		t.Fatalf("expected RetainedBytes (%d) == StoredBytes (%d) == LooseBytes+PackBytes (%d)",
			mDup.RetainedBytes, invDup.StoredBytes, invDup.LooseBytes+invDup.PackBytes)
	}

	// (b) Restart on preexisting store with packs and loose objects:
	testutil.FailErr(t, "store.Close", store.Close())
	reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "NewRetainedObjectStoreContext restart", err)

	mReopen := reopened.Metrics()
	if mReopen.StartupInitDuration <= 0 {
		t.Fatalf("expected StartupInitDuration > 0 on store reopen, got %v", mReopen.StartupInitDuration)
	}
	if mReopen.RetainedObjects != mDup.RetainedObjects || mReopen.RetainedBytes != mDup.RetainedBytes {
		t.Fatalf("reopened store Metrics (%+v) did not preserve preexisting stock (%+v)", mReopen, mDup)
	}
	if !reopened.HasObject(packedCommitOID) || !reopened.HasObject(packedBlobOID) || !reopened.HasObject(looseCommitOID) || !reopened.HasObject(looseBlobOID) {
		t.Fatalf("expected reopened store HasObject=true for both packed and loose OIDs")
	}

	// (c) Lease only packedCommitOID on the reopened store and run CollectGarbage:
	// the preexisting pack containing packedCommitOID must be preserved while unleased loose objects are collected.
	leasePacked, err := reopened.RetainForCheckpoint("cp-packed", []string{packedCommitOID})
	testutil.FailErr(t, "RetainForCheckpoint on reopened store", err)
	removed, err := reopened.CollectGarbage(ctx)
	testutil.FailErr(t, "CollectGarbage with packed lease", err)
	if removed <= 0 {
		t.Fatalf("expected unleased loose objects to be removed by GC, removed=%d", removed)
	}
	mAfterPartialGC := reopened.Metrics()
	invAfterPartialGC, err := reopened.InspectStock(ctx)
	testutil.FailErr(t, "InspectStock after partial GC", err)
	if mAfterPartialGC.RetainedObjects != invAfterPartialGC.LogicalObjects || mAfterPartialGC.RetainedBytes != invAfterPartialGC.StoredBytes {
		t.Fatalf("Metrics (%+v) diverged from InspectStock (%+v) after partial GC", mAfterPartialGC, invAfterPartialGC)
	}
	if !reopened.HasObject(packedCommitOID) || reopened.HasObject(looseBlobOID) {
		t.Fatalf("expected packedCommitOID retained and looseBlobOID collected after partial GC")
	}

	// Release remaining lease and run GC to empty the store completely.
	_ = reachable
	testutil.FailErr(t, "ReleaseLease", reopened.ReleaseLease(leasePacked))
	_, err = reopened.CollectGarbage(ctx)
	testutil.FailErr(t, "CollectGarbage empty", err)
	mEmpty := reopened.Metrics()
	invEmpty, err := reopened.InspectStock(ctx)
	testutil.FailErr(t, "InspectStock empty", err)
	if mEmpty.RetainedObjects != 0 || mEmpty.RetainedBytes != 0 || invEmpty.LogicalObjects != 0 || invEmpty.StoredBytes != 0 {
		t.Fatalf("expected zero stock after full GC, got metrics=%+v inspect=%+v", mEmpty, invEmpty)
	}
}

func TestRetainedObjectStoreInspectStockCancellationAndCorruptionErrors(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	ctx := t.Context()

	srcRepo := filepath.Join(tmp, "src-repo")
	testutil.FailErr(t, "mkdir src-repo", os.MkdirAll(srcRepo, 0o755))
	gittest.Init(t, srcRepo)
	gittest.Run(t, srcRepo, "checkout", "-b", "main")
	testutil.FailErr(t, "write file.txt", os.WriteFile(filepath.Join(srcRepo, "file.txt"), []byte("hello\n"), 0o644))
	gittest.CommitAll(t, srcRepo, "initial")
	headOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))
	gittest.Run(t, srcRepo, "repack", "-a", "-d")

	storeRoot := filepath.Join(tmp, "store-corrupt")
	store, err := NewRetainedObjectStore(storeRoot)
	testutil.FailErr(t, "NewRetainedObjectStore", err)
	_, _, err = store.ImportRepositoryObjects(ctx, []string{filepath.Join(srcRepo, ".git", "objects")}, []string{headOID})
	testutil.FailErr(t, "ImportRepositoryObjects", err)

	// 1. Canceled context returns Complete=false and context.Canceled for both InspectStock and NewRetainedObjectStoreContext.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if inv, cErr := store.InspectStock(cancelCtx); !errors.Is(cErr, context.Canceled) || inv.Complete {
		t.Fatalf("expected context.Canceled and Complete=false, got inv=%+v err=%v", inv, cErr)
	}
	if _, cErr := NewRetainedObjectStoreContext(cancelCtx, storeRoot); !errors.Is(cErr, context.Canceled) {
		t.Fatalf("expected NewRetainedObjectStoreContext to return context.Canceled, got %v", cErr)
	}

	// 2. Unreadable loose shard fails InspectStock and NewRetainedObjectStore with Complete=false.
	testutil.FailErr(t, "store.Close before shard chmod", store.Close())
	badShard := filepath.Join(store.ObjectsDir(), "aa")
	testutil.FailErr(t, "mkdir shard aa", os.MkdirAll(badShard, 0o755))
	badLoose := filepath.Join(badShard, strings.Repeat("b", 38))
	testutil.FailErr(t, "write badLoose", os.WriteFile(badLoose, []byte("x"), 0o000))
	if os.Geteuid() != 0 {
		if inv, uErr := store.InspectStock(ctx); uErr == nil || inv.Complete {
			t.Fatalf("expected unreadable loose object to fail InspectStock with Complete=false, got inv=%+v err=%v", inv, uErr)
		}
		if _, rErr := NewRetainedObjectStore(storeRoot); rErr == nil {
			t.Fatalf("expected NewRetainedObjectStore to fail on unreadable loose object")
		}
	}
	_ = os.Chmod(badLoose, 0o644)
	_ = os.RemoveAll(badShard)

	// 3. Corrupt pack .idx checksum fails InspectStock and NewRetainedObjectStore with ErrCorruptObjectStore.
	packDir := filepath.Join(store.ObjectsDir(), "pack")
	entries, err := os.ReadDir(packDir)
	testutil.FailErr(t, "readdir pack", err)
	var idxPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".idx") {
			idxPath = filepath.Join(packDir, e.Name())
			break
		}
	}
	if idxPath == "" {
		t.Fatalf("expected .idx file in %s", packDir)
	}
	_ = os.Chmod(packDir, 0o755)
	_ = os.Chmod(idxPath, 0o644)
	rawIdx, err := os.ReadFile(idxPath)
	testutil.FailErr(t, "read .idx", err)
	rawIdx[len(rawIdx)-1] ^= 0xff
	testutil.FailErr(t, "corrupt .idx", os.WriteFile(idxPath, rawIdx, 0o644))

	if inv, idxErr := store.InspectStock(ctx); !errors.Is(idxErr, ErrCorruptObjectStore) || inv.Complete {
		t.Fatalf("expected ErrCorruptObjectStore and Complete=false on corrupt .idx, got inv=%+v err=%v", inv, idxErr)
	}
	if _, rErr := NewRetainedObjectStore(storeRoot); !errors.Is(rErr, ErrCorruptObjectStore) {
		t.Fatalf("expected ErrCorruptObjectStore from NewRetainedObjectStore on corrupt .idx, got %v", rErr)
	}
}

func TestCheckpointCreatedObjectsUpdateRetainedStockAndReopen(t *testing.T) {
	t.Parallel()
	fix := buildRichRepoFixture(t)
	ctx := t.Context()

	mgr := newTestManager(t, fix.tmpDir)
	const viewID = "view-checkpoint-stock"
	core := newMountedViewWithManager(t, mgr, viewID, fix.trunkDir)
	exec := NewGitExecutor(mgr, viewID, RemotePolicy{})

	// Lazy-init private repository on mounted view and check baseline retained stock.
	_, err := exec.Status(ctx, "--porcelain")
	testutil.FailErr(t, "exec.Status init", err)

	mInit := mgr.Store().Metrics()
	invInit, err := mgr.Store().InspectStock(ctx)
	testutil.FailErr(t, "InspectStock after lazy init", err)
	if mInit.RetainedObjects <= 0 || mInit.RetainedObjects != invInit.LogicalObjects || mInit.RetainedBytes != invInit.StoredBytes {
		t.Fatalf("baseline stock mismatch after lazy init: metrics=%+v inspect=%+v", mInit, invInit)
	}

	// Create a new worker file and commit inside the view, then capture checkpoint Git state.
	_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "checkpoint-stock.txt"}, []byte("new-checkpoint-payload\n"), 0o644)
	testutil.FailErr(t, "core.WriteFile checkpoint-stock.txt", err)
	_, err = exec.Run(ctx, ".", "add", "checkpoint-stock.txt")
	testutil.FailErr(t, "exec.Run add", err)
	_, err = exec.Commit(ctx, "worker checkpoint commit")
	testutil.FailErr(t, "exec.Commit", err)

	cpState, err := mgr.CaptureCheckpointGitState(ctx, viewID, "cp-stock-1")
	testutil.FailErr(t, "CaptureCheckpointGitState", err)
	if cpState.Artifact.ImportedObjects <= 0 {
		t.Fatalf("expected checkpoint to import newly created worker objects, got %+v", cpState.Artifact)
	}

	mAfterCP := mgr.Store().Metrics()
	invAfterCP, err := mgr.Store().InspectStock(ctx)
	testutil.FailErr(t, "InspectStock after CaptureCheckpointGitState", err)
	if mAfterCP.RetainedObjects <= mInit.RetainedObjects || mAfterCP.RetainedBytes <= mInit.RetainedBytes {
		t.Fatalf("expected RetainedObjects/RetainedBytes to grow after checkpoint capture: before=%+v after=%+v", mInit, mAfterCP)
	}
	if mAfterCP.RetainedObjects != invAfterCP.LogicalObjects || mAfterCP.RetainedBytes != invAfterCP.StoredBytes {
		t.Fatalf("checkpoint-updated Metrics (%+v) mismatched InspectStock (%+v)", mAfterCP, invAfterCP)
	}

	// Reopen the store at the same root and verify startup duration and reconstructed stock match.
	storeRoot := mgr.Store().RootDir()
	testutil.FailErr(t, "store.Close", mgr.Store().Close())
	reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
	testutil.FailErr(t, "NewRetainedObjectStoreContext reopen", err)
	t.Cleanup(func() { _ = reopened.Close() })

	mReopen := reopened.Metrics()
	if mReopen.StartupInitDuration <= 0 || mReopen.RetainedObjects != mAfterCP.RetainedObjects || mReopen.RetainedBytes != mAfterCP.RetainedBytes {
		t.Fatalf("reopened store Metrics (%+v) did not match checkpoint stock (%+v)", mReopen, mAfterCP)
	}
}

func TestRetainedObjectStoreInterruptedPackRetirementReopenAtEachBoundary(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	ctx := t.Context()

	// Create two distinct packed repositories so we can lease pack 1 and retire pack 2 across
	// each interruption boundary while verifying pack 1 remains intact.
	buildPackedRepo := func(name, content string) (string, string) {
		dir := filepath.Join(tmp, name)
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(dir, 0o755))
		gittest.Init(t, dir)
		gittest.Run(t, dir, "checkout", "-b", "main")
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(content), 0o644))
		gittest.CommitAll(t, dir, "commit "+name)
		oid := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
		gittest.Run(t, dir, "repack", "-a", "-d")
		return filepath.Join(dir, ".git", "objects"), oid
	}

	objDirKeep, oidKeep := buildPackedRepo("keep", "retained-pack-payload\n")
	objDirRetire, oidRetire := buildPackedRepo("retire", "retiring-pack-payload\n")

	findPackBase := func(objDir string) string {
		entries, err := os.ReadDir(filepath.Join(objDir, "pack"))
		testutil.FailErr(t, "ReadDir pack", err)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".pack") {
				return strings.TrimSuffix(e.Name(), ".pack")
			}
		}
		t.Fatalf("no .pack found in %s", objDir)
		return ""
	}
	baseKeep := findPackBase(objDirKeep)
	baseRetire := findPackBase(objDirRetire)

	for _, boundary := range []string{
		"after_pack_renamed_to_retiring",
		"after_idx_renamed_to_retiring",
		"after_retiring_pack_unlinked",
	} {
		t.Run(boundary, func(t *testing.T) {
			storeRoot := filepath.Join(tmp, "store-"+boundary)
			store, err := NewRetainedObjectStore(storeRoot)
			testutil.FailErr(t, "NewRetainedObjectStore", err)
			_, _, err = store.ImportRepositoryObjects(ctx, []string{objDirKeep}, []string{oidKeep})
			testutil.FailErr(t, "import keep", err)
			_, _, err = store.ImportRepositoryObjects(ctx, []string{objDirRetire}, []string{oidRetire})
			testutil.FailErr(t, "import retire", err)
			testutil.FailErr(t, "store.Close", store.Close())

			packDir := filepath.Join(store.ObjectsDir(), "pack")
			srcPack := filepath.Join(packDir, baseRetire+".pack")
			srcIdx := filepath.Join(packDir, baseRetire+".idx")
			retPack := filepath.Join(packDir, retiringPackPrefix+baseRetire+".pack")
			retIdx := filepath.Join(packDir, retiringPackPrefix+baseRetire+".idx")

			switch boundary {
			case "after_pack_renamed_to_retiring":
				// Simulate crash right after renaming .pack -> .retiring-pack-*, while .idx is still at baseRetire+".idx".
				testutil.FailErr(t, "rename .pack", os.Rename(srcPack, retPack))
			case "after_idx_renamed_to_retiring":
				// Simulate crash after both .pack and .idx are renamed to .retiring-pack-*.
				testutil.FailErr(t, "rename .pack", os.Rename(srcPack, retPack))
				testutil.FailErr(t, "rename .idx", os.Rename(srcIdx, retIdx))
			case "after_retiring_pack_unlinked":
				// Simulate crash after .retiring-pack-*.pack was unlinked but .retiring-pack-*.idx remains.
				testutil.FailErr(t, "rename .pack", os.Rename(srcPack, retPack))
				testutil.FailErr(t, "rename .idx", os.Rename(srcIdx, retIdx))
				testutil.FailErr(t, "remove retiring .pack", os.Remove(retPack))
			}

			reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
			testutil.FailErr(t, "reopen after "+boundary, err)
			defer func() { _ = reopened.Close() }()

			if !reopened.HasObject(oidKeep) || !reopened.HasObject("pack:"+baseKeep) {
				t.Fatalf("expected retained pack %s (%s) preserved across %s", baseKeep, oidKeep, boundary)
			}
			if reopened.HasObject(oidRetire) || reopened.HasObject("pack:"+baseRetire) {
				t.Fatalf("expected retired pack %s (%s) finalized and absent after %s", baseRetire, oidRetire, boundary)
			}
			inv, err := reopened.InspectStock(ctx)
			testutil.FailErr(t, "InspectStock after "+boundary, err)
			m := reopened.Metrics()
			if !inv.Complete || m.RetainedObjects != inv.LogicalObjects || m.RetainedBytes != inv.StoredBytes {
				t.Fatalf("reopened stock mismatch after %s: metrics=%+v inspect=%+v", boundary, m, inv)
			}
		})
	}
}

func TestRetainedObjectStoreGCCancellationAndUnlinkFailureReopen(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	ctx := t.Context()

	buildPackedRepo := func(name, content string) (string, string) {
		dir := filepath.Join(tmp, name)
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(dir, 0o755))
		gittest.Init(t, dir)
		gittest.Run(t, dir, "checkout", "-b", "main")
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(content), 0o644))
		gittest.CommitAll(t, dir, "commit "+name)
		oid := strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
		gittest.Run(t, dir, "repack", "-a", "-d")
		return filepath.Join(dir, ".git", "objects"), oid
	}

	objDirKeep, oidKeep := buildPackedRepo("keep-gc", "keep-gc-pack-payload\n")
	objDirRetire, oidRetire := buildPackedRepo("retire-gc", "retire-gc-pack-payload\n")

	findPackBase := func(objDir string) string {
		entries, err := os.ReadDir(filepath.Join(objDir, "pack"))
		testutil.FailErr(t, "ReadDir pack", err)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".pack") {
				return strings.TrimSuffix(e.Name(), ".pack")
			}
		}
		t.Fatalf("no .pack found in %s", objDir)
		return ""
	}
	baseKeep := findPackBase(objDirKeep)
	baseRetire := findPackBase(objDirRetire)

	setupTwoPackStore := func(t *testing.T, name string) (string, *RetainedObjectStore) {
		t.Helper()
		storeRoot := filepath.Join(tmp, "store-gc-"+name)
		store, err := NewRetainedObjectStore(storeRoot)
		testutil.FailErr(t, "NewRetainedObjectStore", err)
		_, _, err = store.ImportRepositoryObjects(ctx, []string{objDirKeep}, []string{oidKeep})
		testutil.FailErr(t, "import keep", err)
		_, _, err = store.ImportRepositoryObjects(ctx, []string{objDirRetire}, []string{oidRetire})
		testutil.FailErr(t, "import retire", err)
		_, err = store.RetainForCheckpoint("cp-keep", []string{oidKeep})
		testutil.FailErr(t, "RetainForCheckpoint keep", err)
		return storeRoot, store
	}

	// 1. Unlink failure on .pack during CollectGarbage propagates error, rolls back staging so .idx is not
	// deleted, and allows clean store reopen with both packs intact.
	t.Run("pack_unlink_failure_rolls_back_and_reopens_intact", func(t *testing.T) {
		storeRoot, store := setupTwoPackStore(t, "pack-unlink-fail")
		injectedErr := errors.New("simulated .pack unlink I/O failure")
		hookCtx := withGCTestHooks(ctx, gcTestHooks{
			removeFile: func(p string) error {
				if strings.HasSuffix(p, baseRetire+".pack") {
					return injectedErr
				}
				return os.Remove(p)
			},
		})
		_, err := store.CollectGarbage(hookCtx)
		if !errors.Is(err, injectedErr) {
			t.Fatalf("expected CollectGarbage to propagate %v, got %v", injectedErr, err)
		}
		testutil.FailErr(t, "store.Close", store.Close())

		reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
		testutil.FailErr(t, "reopen after .pack unlink failure", err)
		defer func() { _ = reopened.Close() }()

		if !reopened.HasObject(oidKeep) || !reopened.HasObject("pack:"+baseKeep) {
			t.Fatalf("expected leased pack %s preserved after .pack unlink failure", baseKeep)
		}
		if !reopened.HasObject(oidRetire) || !reopened.HasObject("pack:"+baseRetire) {
			t.Fatalf("expected unleased pack %s rolled back intact when .pack unlink failed", baseRetire)
		}
		inv, err := reopened.InspectStock(ctx)
		testutil.FailErr(t, "InspectStock", err)
		m := reopened.Metrics()
		if !inv.Complete || m.RetainedObjects != inv.LogicalObjects || m.RetainedBytes != inv.StoredBytes {
			t.Fatalf("stock mismatch after .pack unlink rollback: metrics=%+v inspect=%+v", m, inv)
		}
	})

	// 2. Unlink failure on .idx (after .pack is already unlinked) propagates error from CollectGarbage and
	// from reopen while the failure persists, then reconciles cleanly on reopen once the failure clears.
	t.Run("idx_unlink_failure_propagates_and_reconciles_on_reopen", func(t *testing.T) {
		storeRoot, store := setupTwoPackStore(t, "idx-unlink-fail")
		injectedErr := errors.New("simulated .idx unlink I/O failure")
		hookCtx := withGCTestHooks(ctx, gcTestHooks{
			removeFile: func(p string) error {
				if strings.HasSuffix(p, baseRetire+".idx") {
					return injectedErr
				}
				return os.Remove(p)
			},
		})
		_, err := store.CollectGarbage(hookCtx)
		if !errors.Is(err, injectedErr) {
			t.Fatalf("expected CollectGarbage to propagate %v, got %v", injectedErr, err)
		}
		testutil.FailErr(t, "store.Close", store.Close())

		// Reopen while unlink failure is still active must propagate the I/O failure.
		if _, err := NewRetainedObjectStoreContext(hookCtx, storeRoot); !errors.Is(err, injectedErr) {
			t.Fatalf("expected reopen under active unlink failure to propagate %v, got %v", injectedErr, err)
		}

		// Once the transient unlink failure clears, reopen reconciles the retired pack and preserves keep.
		reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
		testutil.FailErr(t, "reopen after .idx unlink failure cleared", err)
		defer func() { _ = reopened.Close() }()

		if !reopened.HasObject(oidKeep) || !reopened.HasObject("pack:"+baseKeep) {
			t.Fatalf("expected leased pack %s preserved", baseKeep)
		}
		if reopened.HasObject(oidRetire) || reopened.HasObject("pack:"+baseRetire) {
			t.Fatalf("expected partially unlinked pack %s reconciled on reopen", baseRetire)
		}
		inv, err := reopened.InspectStock(ctx)
		testutil.FailErr(t, "InspectStock", err)
		m := reopened.Metrics()
		if !inv.Complete || m.RetainedObjects != inv.LogicalObjects || m.RetainedBytes != inv.StoredBytes {
			t.Fatalf("stock mismatch after .idx unlink reconciliation: metrics=%+v inspect=%+v", m, inv)
		}
	})

	// 3. Context cancellation mid-GC at each stage/unlink boundary returns context.Canceled and reopens cleanly.
	for _, cancelPoint := range []string{
		"before_idx_stage_rename",
		"before_pack_retired_unlink",
		"before_idx_retired_unlink",
	} {
		t.Run("cancel_"+cancelPoint, func(t *testing.T) {
			storeRoot, store := setupTwoPackStore(t, "cancel-"+cancelPoint)
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			hooks := gcTestHooks{}
			switch cancelPoint {
			case "before_idx_stage_rename":
				hooks.beforeStageRename = func(base, ext string) error {
					if base == baseRetire && ext == ".idx" {
						cancel()
						return cancelCtx.Err()
					}
					return nil
				}
			case "before_pack_retired_unlink":
				hooks.beforeUnlinkRetired = func(base, ext string) error {
					if base == baseRetire && ext == ".pack" {
						cancel()
						return cancelCtx.Err()
					}
					return nil
				}
			case "before_idx_retired_unlink":
				hooks.beforeUnlinkRetired = func(base, ext string) error {
					if base == baseRetire && ext == ".idx" {
						cancel()
						return cancelCtx.Err()
					}
					return nil
				}
			}

			_, err := store.CollectGarbage(withGCTestHooks(cancelCtx, hooks))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled at %s, got %v", cancelPoint, err)
			}
			testutil.FailErr(t, "store.Close", store.Close())

			reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
			testutil.FailErr(t, "reopen after "+cancelPoint, err)
			defer func() { _ = reopened.Close() }()

			if !reopened.HasObject(oidKeep) || !reopened.HasObject("pack:"+baseKeep) {
				t.Fatalf("expected leased pack %s preserved across %s", baseKeep, cancelPoint)
			}
			if reopened.HasObject(oidRetire) || reopened.HasObject("pack:"+baseRetire) {
				t.Fatalf("expected retiring pack %s finalized on reopen after %s", baseRetire, cancelPoint)
			}
			inv, err := reopened.InspectStock(ctx)
			testutil.FailErr(t, "InspectStock", err)
			m := reopened.Metrics()
			if !inv.Complete || m.RetainedObjects != inv.LogicalObjects || m.RetainedBytes != inv.StoredBytes {
				t.Fatalf("stock mismatch after %s: metrics=%+v inspect=%+v", cancelPoint, m, inv)
			}
		})
	}

	// 4. Re-importing a content-addressed pack after a cancelled GC clears stale tombstones and
	// preserves the freshly leased pack across InspectStock, CollectGarbage, and store reopen.
	t.Run("reimport_after_cancelled_gc_preserves_leased_pack", func(t *testing.T) {
		storeRoot, store := setupTwoPackStore(t, "reimport-after-cancel")
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		hooks := gcTestHooks{
			beforeStageRename: func(base, ext string) error {
				if base == baseRetire && ext == ".idx" {
					cancel()
					return cancelCtx.Err()
				}
				return nil
			},
		}
		_, err := store.CollectGarbage(withGCTestHooks(cancelCtx, hooks))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}

		// Re-import the retired pack on the same open store and lease it.
		_, _, err = store.ImportRepositoryObjects(ctx, []string{objDirRetire}, []string{oidRetire})
		testutil.FailErr(t, "re-import retire pack after cancelled GC", err)
		_, err = store.RetainForCheckpoint("cp-retire-reimported", []string{oidRetire, "pack:" + baseRetire})
		testutil.FailErr(t, "lease re-imported pack", err)

		inv, err := store.InspectStock(ctx)
		testutil.FailErr(t, "InspectStock after re-import", err)
		m := store.Metrics()
		if !inv.Complete || m.RetainedObjects != inv.LogicalObjects || m.RetainedBytes != inv.StoredBytes {
			t.Fatalf("stock mismatch after re-import: metrics=%+v inspect=%+v", m, inv)
		}
		if !store.HasObject(oidRetire) || !store.HasObject("pack:"+baseRetire) {
			t.Fatalf("expected re-imported pack %s present after InspectStock", baseRetire)
		}

		if removed, err := store.CollectGarbage(ctx); err != nil || removed != 0 {
			t.Fatalf("expected 0 removed when both packs are leased, got removed=%d err=%v", removed, err)
		}
		testutil.FailErr(t, "store.Close", store.Close())

		reopened, err := NewRetainedObjectStoreContext(ctx, storeRoot)
		testutil.FailErr(t, "reopen after re-import", err)
		defer func() { _ = reopened.Close() }()
		if !reopened.HasObject(oidKeep) || !reopened.HasObject("pack:"+baseKeep) || !reopened.HasObject(oidRetire) || !reopened.HasObject("pack:"+baseRetire) {
			t.Fatalf("expected both packs preserved after reopen")
		}
	})
}
