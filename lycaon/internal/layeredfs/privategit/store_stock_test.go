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
	_, err = exec.Add(ctx, "checkpoint-stock.txt")
	testutil.FailErr(t, "exec.Add", err)
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
