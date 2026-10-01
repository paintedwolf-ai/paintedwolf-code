package sourcecatalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInventoryInvalidationsKeepHumanScope(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get catalog", err)
	store.inventory.wake = make(chan struct{}, 1)
	initialMark := store.invalidation.next
	writeIndexFile(t, root.Path, "staging/data", "in flight")
	t.Cleanup(repochange.HoldPrivateTree(filepath.Join(root.Path, "staging")))
	catalog.invalidateTrees(root.Path, []string{"staging/data"})
	if len(store.inventory.dirty) != 0 || store.invalidation.next != initialMark || len(store.inventory.wake) != 0 {
		t.Fatal("private staging scheduled structural work")
	}
	paths := []string{".git/objects/new", "nested/.git/config", ".paintedwolf/settings.json", ".task/output.log", "ignored/file.txt"}
	for _, rel := range paths {
		writeIndexFile(t, root.Path, rel, "source")
	}
	catalog.invalidateTrees(root.Path, paths)
	if len(store.inventory.dirty) != len(paths) {
		t.Fatalf("pending human-visible directories: %v", store.inventory.dirty)
	}
	store.inventory.invalidate(nil)
	testutil.FailErr(t, "publish mixed invalidations", store.inventoryPass(t.Context()))
	waitInventoryExtent(t, catalog, root, 12)
	writeIndexFile(t, root.Path, ".git/objects/later", "updated")
	catalog.invalidateTrees(root.Path, []string{".git/objects/later"})
	testutil.FailErr(t, "refresh Git metadata", store.inventoryPass(t.Context()))
	waitInventoryExtent(t, catalog, root, 13)
	reader := waitIndex(t, catalog, root)
	indexed, err := reader.FilePathsPage(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true}, "", TreeFilePageLimit)
	testutil.FailErr(t, "read search projection", err)
	for _, rel := range indexed {
		if skippedIndexPath(rel) {
			t.Fatalf("search admitted VCS metadata: %q", rel)
		}
	}
}

func TestIndexPublishesBeforeYieldingToInitialInventory(t *testing.T) {
	catalog, root := indexFixture(t)
	store, walk := indexWalkFixture(t, catalog, root)
	initial := make(chan struct{})
	store.mu.Lock()
	store.inventory.initial = initial
	store.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- walk.publish(ctx) }()
	testutil.WaitFor(t, time.Second, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.status.State == StateReady && !store.status.Complete
	})
	select {
	case err := <-finished:
		t.Fatalf("index continued before structural coverage: %v", err)
	default:
	}
	writerContext, stopWriter := context.WithTimeout(ctx, time.Second)
	defer stopWriter()
	unwrite, err := store.write(writerContext)
	testutil.FailErr(t, "acquire writer during inventory wait", err)
	unwrite()
	var revision int64
	testutil.FailErr(t, "read published index", walk.db.QueryRowContext(ctx, "SELECT revision FROM meta WHERE id=1").Scan(&revision))
	if revision == 0 {
		t.Fatal("index waited before publishing its first response")
	}
	close(initial)
	testutil.FailErr(t, "resume enrichment", <-finished)
}

func TestInventoryCompletesWhileSearchWriterIsOccupied(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range 400 {
		writeIndexFile(t, root.Path, fmt.Sprintf("dir-%03d/file.txt", i), "source")
	}
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get shared catalog", err)
	unwrite, err := store.write(t.Context())
	testutil.FailErr(t, "occupy search writer", err)
	defer unwrite()
	testutil.FailErr(t, "start inventory", catalog.WarmNavigation(t.Context(), "p", root))
	testutil.FailErr(t, "await initial structure", catalog.AwaitNavigation(t.Context(), "p", root))
	waitInventoryExtent(t, catalog, root, 800)
	if _, err := os.Stat(store.file); !os.IsNotExist(err) {
		t.Fatalf("inventory created search storage: %v", err)
	}
	before, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "retain original structure", err)
	defer func() { _ = before.Close() }()
	writeIndexFile(t, root.Path, "new/nested/file.txt", "new")
	catalog.invalidateTrees(root.Path, []string{"new"})
	waitInventoryExtent(t, catalog, root, 803)
	if total := navigationExtent(t, before); total != 800 {
		t.Fatalf("retained extent moved to %d", total)
	}
}

func waitInventoryExtent(t *testing.T, catalog *Catalog, root Root, want int64) {
	t.Helper()
	testutil.WaitFor(t, 15*time.Second, func() bool {
		nav, err := catalog.OpenNavigation(t.Context(), "p", root)
		if err != nil {
			return false
		}
		defer func() { _ = nav.Close() }()
		children, err := nav.Children(t.Context(), ".")
		if err != nil {
			return false
		}
		pending, err := children.Unresolved(t.Context())
		if err != nil || pending != 0 {
			return false
		}
		total, err := children.Extent(t.Context())
		return err == nil && total == want
	})
}

func TestInventoryRetriesFailedFrontierPublication(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "nested/file.txt", "source")
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get catalog", err)
	moved := root.Path + "-unavailable"
	testutil.FailErr(t, "make root unavailable", os.Rename(root.Path, moved))
	store.mu.Lock()
	store.inventory.dirty = map[string]struct{}{".": {}}
	store.mu.Unlock()
	if err := store.inventoryPass(t.Context()); err == nil {
		t.Fatal("inventory accepted an unavailable root")
	}
	store.mu.Lock()
	_, retained := store.inventory.dirty["."]
	store.mu.Unlock()
	if !retained {
		t.Fatal("failed publication dropped the pending root")
	}
	testutil.FailErr(t, "restore root", os.Rename(moved, root.Path))
	testutil.FailErr(t, "retry inventory", store.inventoryPass(t.Context()))
	waitInventoryExtent(t, catalog, root, 2)
}

func TestInitialInventoryFailureSettlesWaitersAndRecovers(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "nested/file.txt", "source")
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get catalog", err)
	moved := root.Path + "-unavailable"
	testutil.FailErr(t, "make root unavailable", os.Rename(root.Path, moved))
	testutil.FailErr(t, "start inventory", store.startInventory(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = store.awaitInitialInventory(ctx)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("initial inventory must settle with its failure, got %v (context %v)", err, ctx.Err())
	}
	testutil.FailErr(t, "restore root", os.Rename(moved, root.Path))
	testutil.WaitFor(t, 5*time.Second, func() bool { return store.awaitInitialInventory(ctx) == nil })
	waitInventoryExtent(t, catalog, root, 2)
}
