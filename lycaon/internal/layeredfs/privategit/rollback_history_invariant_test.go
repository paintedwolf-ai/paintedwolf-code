package privategit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// Existence and inventory counts cannot prove that a retained commit's trees and blobs survived.
func TestInterruptedImportRollbackPreservesReadableLeasedHistory(t *testing.T) {
	source := t.TempDir()
	gittest.Init(t, source)
	file := filepath.Join(source, "payload.txt")
	testutil.FailErr(t, "first content", os.WriteFile(file, []byte("first version\n"), 0o644))
	gittest.CommitAll(t, source, "first")
	first := strings.TrimSpace(gittest.Run(t, source, "rev-parse", "HEAD"))
	testutil.FailErr(t, "second content", os.WriteFile(file, []byte("second version\n"), 0o644))
	gittest.CommitAll(t, source, "second")
	second := strings.TrimSpace(gittest.Run(t, source, "rev-parse", "HEAD"))
	gittest.Run(t, source, "repack", "-a", "-d")
	root := t.TempDir()
	store, err := NewRetainedObjectStoreContext(t.Context(), root)
	testutil.FailErr(t, "open retained store", err)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	_, reachable, err := store.ImportRepositoryObjects(t.Context(), []string{filepath.Join(source, ".git", "objects")}, []string{second})
	testutil.FailErr(t, "retain history", err)
	_, err = store.RetainForCheckpoint("leased-history", reachable)
	testutil.FailErr(t, "lease history", err)
	// The oracle has only the retained store as an object source, never the original repository.
	testutil.FailErr(t, "remove original fixture", os.RemoveAll(source))
	reader := t.TempDir()
	gittest.Init(t, reader)
	testutil.FailErr(t, "reader alternates", os.WriteFile(filepath.Join(reader, ".git", "objects", "info", "alternates"), []byte(store.ObjectsDir()+"\n"), 0o600))
	assertReadable := func() {
		t.Helper()
		for oid, want := range map[string]string{first: "first version", second: "second version"} {
			if got := strings.TrimSpace(gittest.Run(t, reader, "show", oid+":payload.txt")); got != want {
				t.Fatalf("retained commit %s lost its payload: %q", oid, got)
			}
		}
		if got := strings.TrimSpace(gittest.Run(t, reader, "rev-parse", second+"^")); got != first {
			t.Fatalf("retained parent edge changed: %q", got)
		}
	}
	candidate := t.TempDir()
	gittest.Init(t, candidate)
	testutil.FailErr(t, "candidate content", os.WriteFile(filepath.Join(candidate, "other.txt"), []byte("unrelated\n"), 0o644))
	gittest.CommitAll(t, candidate, "candidate")
	gittest.Run(t, candidate, "repack", "-a", "-d")
	interrupted := errors.New("rollback intent unavailable")
	fault := withGCTestHooks(t.Context(), gcTestHooks{
		beforeRollbackMarkerWrite: func(string, string) error { return interrupted },
	})
	_, _, err = store.ImportRepositoryObjects(fault, []string{filepath.Join(candidate, ".git", "objects")}, []string{strings.Repeat("f", 40)})
	if !errors.Is(err, interrupted) {
		t.Fatalf("rollback interruption not exercised: %v", err)
	}
	assertReadable()
	_, err = store.CollectGarbage(t.Context())
	testutil.FailErr(t, "continued-use GC", err)
	assertReadable()
	testutil.FailErr(t, "close before recovery", store.Close())
	store, err = NewRetainedObjectStoreContext(t.Context(), root)
	testutil.FailErr(t, "reopen store", err)
	assertReadable()
}

// Not parallel: mutates package-level compactPackThreshold.
func TestCompactPackOverlappingAndMixedInventoryAccountingAndGC(t *testing.T) {
	origThreshold := compactPackThreshold
	compactPackThreshold = 1
	defer func() { compactPackThreshold = origThreshold }()

	// Build Repo A with 2 commits -> pack A (compact pack since object count > 1).
	repoA := t.TempDir()
	gittest.Init(t, repoA)
	testutil.FailErr(t, "write a1", os.WriteFile(filepath.Join(repoA, "a.txt"), []byte("a1\n"), 0o644))
	gittest.CommitAll(t, repoA, "commit a1")
	c1 := strings.TrimSpace(gittest.Run(t, repoA, "rev-parse", "HEAD"))
	testutil.FailErr(t, "write a2", os.WriteFile(filepath.Join(repoA, "a.txt"), []byte("a2\n"), 0o644))
	gittest.CommitAll(t, repoA, "commit a2")
	c2 := strings.TrimSpace(gittest.Run(t, repoA, "rev-parse", "HEAD"))
	gittest.Run(t, repoA, "repack", "-a", "-d")

	// Clone Repo A into Repo B and add commit c3 -> repack -a -d creates an overlapping compact pack B containing c1, c2, and c3!
	repoB := filepath.Join(t.TempDir(), "repoB")
	gittest.Run(t, t.TempDir(), "clone", repoA, repoB)
	testutil.FailErr(t, "write b3", os.WriteFile(filepath.Join(repoB, "b.txt"), []byte("b3\n"), 0o644))
	gittest.CommitAll(t, repoB, "commit b3")
	c3 := strings.TrimSpace(gittest.Run(t, repoB, "rev-parse", "HEAD"))
	gittest.Run(t, repoB, "repack", "-a", "-d")

	// Also create a loose object in Repo B that duplicates a1 blob AND a brand new loose commit c4.
	testutil.FailErr(t, "write a1 again", os.WriteFile(filepath.Join(repoB, "dup.txt"), []byte("a1\n"), 0o644))
	testutil.FailErr(t, "write c4", os.WriteFile(filepath.Join(repoB, "c4.txt"), []byte("unique-loose-c4\n"), 0o644))
	gittest.CommitAll(t, repoB, "commit c4")
	c4 := strings.TrimSpace(gittest.Run(t, repoB, "rev-parse", "HEAD"))

	allOIDsOut := gittest.Run(t, repoB, "rev-list", "--objects", c4)
	wantUnique := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(allOIDsOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 40 {
			wantUnique[fields[0]] = struct{}{}
		}
	}

	storeDir := t.TempDir()
	store, err := NewRetainedObjectStoreContext(t.Context(), storeDir)
	testutil.FailErr(t, "NewRetainedObjectStoreContext", err)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})

	_, reachA, err := store.ImportRepositoryObjects(t.Context(), []string{filepath.Join(repoA, ".git", "objects")}, []string{c2})
	testutil.FailErr(t, "import repoA", err)
	_, reachB, err := store.ImportRepositoryObjects(t.Context(), []string{filepath.Join(repoB, ".git", "objects")}, []string{c4})
	testutil.FailErr(t, "import repoB", err)

	if len(store.stock.compactPacks) < 2 {
		t.Fatalf("expected at least 2 compact packs in store, got %d", len(store.stock.compactPacks))
	}

	wantCount := int64(len(wantUnique))
	met := store.Metrics()
	inv, err := store.InspectStock(t.Context())
	testutil.FailErr(t, "InspectStock", err)
	if !inv.Complete {
		t.Fatalf("expected Complete=true, got %+v", inv)
	}
	if met.RetainedObjects != wantCount || inv.LogicalObjects != wantCount {
		t.Fatalf("logical unique object count mismatch: Metrics.RetainedObjects=%d InspectStock.LogicalObjects=%d want=%d (PackedObjects=%d LooseObjects=%d)",
			met.RetainedObjects, inv.LogicalObjects, wantCount, inv.PackedObjects, inv.LooseObjects)
	}

	// Reopen store from disk and verify reconstructed stock preserves deduplicated unique count.
	testutil.FailErr(t, "close store", store.Close())
	store, err = NewRetainedObjectStoreContext(t.Context(), storeDir)
	testutil.FailErr(t, "reopen store", err)
	if got := store.Metrics().RetainedObjects; got != wantCount {
		t.Fatalf("reopened Metrics().RetainedObjects = %d, want %d", got, wantCount)
	}

	// Lease only repoB's reachable set (which includes pack B and c4's loose objects, plus shared OIDs in pack A).
	leaseA, err := store.RetainForView("view-a", reachA)
	testutil.FailErr(t, "RetainForView a", err)
	leaseB, err := store.RetainForView("view-b", reachB)
	testutil.FailErr(t, "RetainForView b", err)
	testutil.FailErr(t, "ReleaseLease a", store.ReleaseLease(leaseA))
	testutil.FailErr(t, "ReleaseLease b", store.ReleaseLease(leaseB))

	// Now lease only pack B and c3 (excluding loose c4 objects), run GC, and verify unique accounting stays exact.
	c3OIDsOut := gittest.Run(t, repoB, "rev-list", "--objects", c3)
	wantC3Unique := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(c3OIDsOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 40 {
			wantC3Unique[fields[0]] = struct{}{}
		}
	}
	var packBOnly []string
	for _, item := range reachB {
		if strings.HasPrefix(item, "pack:") {
			packBOnly = append(packBOnly, item)
		}
	}
	_, err = store.RetainForCheckpoint("cp-b-pack-only", packBOnly)
	testutil.FailErr(t, "RetainForCheckpoint packBOnly", err)
	_, err = store.CollectGarbage(t.Context())
	testutil.FailErr(t, "CollectGarbage", err)

	metAfterGC := store.Metrics()
	invAfterGC, err := store.InspectStock(t.Context())
	testutil.FailErr(t, "InspectStock after GC", err)
	wantC3Count := int64(len(wantC3Unique))
	if metAfterGC.RetainedObjects != wantC3Count || invAfterGC.LogicalObjects != wantC3Count {
		t.Fatalf("after GC unique object count mismatch: Metrics=%d InspectStock=%d want=%d",
			metAfterGC.RetainedObjects, invAfterGC.LogicalObjects, wantC3Count)
	}
}

func TestSourceModificationAfterImportDoesNotMutateRetainedStore(t *testing.T) {
	t.Parallel()
	srcRepo := t.TempDir()
	gittest.Init(t, srcRepo)
	testutil.FailErr(t, "write file1", os.WriteFile(filepath.Join(srcRepo, "f1.txt"), []byte("immutable-packed-payload\n"), 0o644))
	gittest.CommitAll(t, srcRepo, "packed commit")
	c1 := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))
	gittest.Run(t, srcRepo, "repack", "-a", "-d")

	testutil.FailErr(t, "write file2", os.WriteFile(filepath.Join(srcRepo, "f2.txt"), []byte("immutable-loose-payload\n"), 0o644))
	gittest.CommitAll(t, srcRepo, "loose commit")
	c2 := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))

	// Make all source object files 0444 prior to import.
	var srcFiles []string
	srcObjDir := filepath.Join(srcRepo, ".git", "objects")
	_ = filepath.WalkDir(srcObjDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chmod(p, 0o444)
			srcFiles = append(srcFiles, p)
		}
		return nil
	})

	store, err := NewRetainedObjectStoreContext(t.Context(), t.TempDir())
	testutil.FailErr(t, "NewRetainedObjectStoreContext", err)
	t.Cleanup(func() { _ = store.Close() })

	_, reachable, err := store.ImportRepositoryObjects(t.Context(), []string{srcObjDir}, []string{c2})
	testutil.FailErr(t, "ImportRepositoryObjects", err)
	_, err = store.RetainForCheckpoint("cp-immutable", reachable)
	testutil.FailErr(t, "RetainForCheckpoint", err)

	// Verify no retained file shares an inode with any source file, then overwrite all source files in-place.
	for _, sp := range srcFiles {
		sInfo, sErr := os.Stat(sp)
		testutil.FailErr(t, "stat src", sErr)
		rel, rErr := filepath.Rel(srcObjDir, sp)
		testutil.FailErr(t, "rel src", rErr)
		dp := filepath.Join(store.ObjectsDir(), rel)
		if dInfo, dErr := os.Stat(dp); dErr == nil {
			if os.SameFile(sInfo, dInfo) {
				t.Fatalf("retained store file %s shares inode with source file %s", dp, sp)
			}
		}
		testutil.FailErr(t, "chmod src writable", os.Chmod(sp, 0o644))
		f, oErr := os.OpenFile(sp, os.O_WRONLY|os.O_TRUNC, 0o644)
		testutil.FailErr(t, "open src for overwrite", oErr)
		_, _ = f.Write([]byte("CORRUPTED-SOURCE-INODE-BYTES"))
		_ = f.Close()
	}

	// Verify retained store inventory and object contents remain completely uncorrupted.
	inv, err := store.InspectStock(t.Context())
	testutil.FailErr(t, "InspectStock after source corruption", err)
	if !inv.Complete {
		t.Fatalf("expected Complete=true after source corruption, got %+v", inv)
	}
	_, err = verifyStoreConnectivity(t.Context(), store.ObjectsDir(), []string{c1, c2})
	testutil.FailErr(t, "verifyStoreConnectivity after source corruption", err)
}

// Not parallel: mutates package-level compactPackThreshold.
func TestCompactPackRejectsMissingDescendantObject(t *testing.T) {
	origThreshold := compactPackThreshold
	compactPackThreshold = 1
	defer func() { compactPackThreshold = origThreshold }()

	srcRepo := t.TempDir()
	gittest.Init(t, srcRepo)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(srcRepo, "hello.txt"), []byte("hello missing blob\n"), 0o644))
	gittest.CommitAll(t, srcRepo, "commit with blob")
	commitOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD"))
	treeOID := strings.TrimSpace(gittest.Run(t, srcRepo, "rev-parse", "HEAD^{tree}"))

	// Pack ONLY the commit and tree (2 objects > compactPackThreshold=1), omitting the descendant blob!
	incompleteObjDir := filepath.Join(t.TempDir(), "incomplete-objects")
	packDir := filepath.Join(incompleteObjDir, "pack")
	testutil.FailErr(t, "mkdir packDir", os.MkdirAll(packDir, 0o755))
	out, code, pErr := gitexec.Run(t.Context(), srcRepo, []string{"pack-objects", "--quiet", filepath.Join(packDir, "pack")}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Input:   []byte(commitOID + "\n" + treeOID + "\n"),
	})
	if pErr != nil || code != 0 {
		t.Fatalf("git pack-objects failed (exit %d): %v: %s", code, pErr, string(out))
	}

	store, err := NewRetainedObjectStoreContext(t.Context(), t.TempDir())
	testutil.FailErr(t, "NewRetainedObjectStoreContext", err)
	t.Cleanup(func() { _ = store.Close() })

	_, _, err = store.ImportRepositoryObjects(t.Context(), []string{incompleteObjDir}, []string{commitOID})
	if !errors.Is(err, ErrCorruptObjectStore) {
		t.Fatalf("expected ErrCorruptObjectStore when compact pack is missing descendant blob, got %v", err)
	}
	if got := store.Metrics().RetainedObjects; got != 0 {
		t.Fatalf("expected rollback to leave 0 retained objects, got %d", got)
	}
}
