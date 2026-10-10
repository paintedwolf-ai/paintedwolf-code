package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestForegroundDirectoryCompletesWhileBulkDirectoryWorkerIsHeld(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "file.txt", "source")
	catalog.Trees.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
		backgroundwork.ResourceDirectory: {Total: 2, PerLane: 2},
	})
	hold, err := catalog.Trees.broker.Acquire(t.Context(), backgroundwork.Request{
		Key: "bulk", Lane: root.Path, Priority: backgroundwork.PriorityProactive,
		Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory},
	})
	testutil.FailErr(t, "hold bulk directory worker", err)
	defer hold()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, observeErr := catalog.Directories.ObserveDirectory(ctx, "p", root, ".", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		done <- observeErr
	}()
	testutil.FailErr(t, "foreground discovery with bulk worker held", <-done)
}

func TestIndexContinuesPastAnEntireVanishedMetadataPage(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range 300 {
		writeIndexFile(t, root.Path, fmt.Sprintf("file-%04d.txt", i), "source")
	}
	observation, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "observe files", err)
	walk := discoveryWalk(t, catalog, root)
	for i := range indexBatchSize {
		testutil.FailErr(t, "remove first metadata page", os.Remove(filepath.Join(root.Path, fmt.Sprintf("file-%04d.txt", i))))
	}
	nodes, next, err := walk.readObservedIndexNodes(t.Context(), ".", observation.Sequence, "")
	testutil.FailErr(t, "read vanished page", err)
	if len(nodes) != 0 || next == "" {
		t.Fatalf("vanished page: nodes=%d cursor=%q", len(nodes), next)
	}
	nodes, _, err = walk.readObservedIndexNodes(t.Context(), ".", observation.Sequence, next)
	testutil.FailErr(t, "read surviving page", err)
	if len(nodes) != 300-indexBatchSize {
		t.Fatalf("survivors=%d", len(nodes))
	}
}

func TestDiscoveryChargesWideDirectoryMembershipOnce(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.SetScopes(testScopes{plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{WalkEntries: 610}}})
	for index := range 600 {
		writeIndexFile(t, root.Path, fmt.Sprintf("file-%03d.txt", index), "source")
	}
	reader := waitIndex(t, catalog, root)
	count, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "count completed wide directory", err)
	if count != 600 {
		t.Fatalf("admitted files=%d", count)
	}
}

func TestColdDiscoveryCompletesWideDirectoryWithoutRestart(t *testing.T) {
	catalog, root := indexFixture(t)
	const entries = 1200
	for index := range entries {
		writeIndexFile(t, root.Path, fmt.Sprintf("file-%04d.txt", index), "source")
	}
	observation, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "discover wide directory", err)
	if !observation.Complete || observation.Entries != entries {
		t.Fatalf("wide discovery: observation=%+v", observation)
	}
	nav, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open completed wide directory", err)
	defer func() { _ = nav.Close() }()
	if extent := navigationExtent(t, nav); extent != entries {
		t.Fatalf("published extent=%d, want %d", extent, entries)
	}
}

func TestIndexAdmissionUsesIndependentStructuralStorage(t *testing.T) {
	catalog, root := indexFixture(t)
	for _, dir := range []string{"a", "b"} {
		writeIndexFile(t, root.Path, dir+"/entry.txt", "source")
	}
	for _, dir := range []string{".", "a", "b"} {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{Priority: backgroundwork.PriorityInteractive})
		testutil.FailErr(t, "prepare membership", err)
	}
	reader := waitIndex(t, catalog, root)
	count, err := reader.FileCount(t.Context(), FileScope{Audience: HumanAudience, IncludeHidden: true})
	testutil.FailErr(t, "count admitted files", err)
	if count != 2 {
		t.Fatalf("files=%d", count)
	}
	nav, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "read structural generation", err)
	defer func() { _ = nav.Close() }()
	if nav.pages == nil || nav.pages.directories.Len() != 3 {
		t.Fatalf("structural directory generation=%+v", nav.pages)
	}
	if reader.store == nil || reader.store.file == "" {
		t.Fatal("search index did not retain independent storage")
	}
}

func discoveryWalk(t *testing.T, catalog *Catalog, root Root) *indexWalk {
	t.Helper()
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open discovery store", err)
	db, physical, release, err := store.acquireIndex(t.Context())
	testutil.FailErr(t, "acquire discovery resources", err)
	t.Cleanup(release)
	walk := &indexWalk{store: store, db: db, root: physical, budget: indexBudget{limits: store.policy.budgets}}
	t.Cleanup(walk.discard)
	testutil.FailErr(t, "seed discovery frontier", walk.prepare(t.Context(), true, nil))
	return walk
}

func navigationExtent(t *testing.T, navigation *Navigation) int64 {
	t.Helper()
	children, err := navigation.Children(t.Context(), ".")
	testutil.FailErr(t, "open root ranks", err)
	extent, err := children.Extent(t.Context())
	testutil.FailErr(t, "read root extent", err)
	return extent
}

func TestDiscoveryRetainsPreviouslyObservedDescendants(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "dir/nested/file.txt", "source")
	_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, "dir/nested", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "observe descendant first", err)
	testutil.FailErr(t, "publish containing directories", catalog.Directories.ObserveDirectories(t.Context(), "p", root,
		[]string{"dir", "."}, backgroundwork.PriorityProactive))
	nav, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open completed ranks", err)
	defer func() { _ = nav.Close() }()
	if extent := navigationExtent(t, nav); extent != 3 {
		t.Fatalf("expanded extent=%d, want 3", extent)
	}
	entry, within, err := nav.Child(t.Context(), ".", 2, true)
	testutil.FailErr(t, "seek observed descendant", err)
	if entry.Path != "dir" || within != 2 {
		t.Fatalf("last rank=%s+%d", entry.Path, within)
	}
}

func TestDiscoveryPublicationFailureLeavesNoPartialFacts(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "file.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open discovery store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin empty generation", err)
	defer pin.Release()
	builder, err := newStructuralBuilder(store, pin.value)
	testutil.FailErr(t, "create structural builder", err)
	defer builder.close()
	listing := directoryDiscovery{nodes: []indexNode{{path: "file.txt", parent: ".", name: "file.txt", regular: true}},
		observation: DirectoryObservation{Path: ".", Entries: 1, Complete: true, Invalidation: store.observationMark(".")}}
	testutil.FailErr(t, "build valid listing", builder.observe(t.Context(), listing))
	testutil.FailErr(t, "finalize valid listing", builder.finalize(t.Context()))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.publishStructure(canceled, builder, pin.Generation); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publication error=%v", err)
	}
	if observation, err := store.readObservation(t.Context(), "."); !errors.Is(err, pagedview.ErrMissing) || observation.Path != "" {
		t.Fatalf("failed publication retained observation=%+v error=%v", observation, err)
	}
	testutil.FailErr(t, "retry valid publication", catalog.Directories.ObserveDirectories(t.Context(), "p", root, []string{"."}, backgroundwork.PriorityProactive))
	nav, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open recovered inventory", err)
	defer func() { _ = nav.Close() }()
	if extent := navigationExtent(t, nav); extent != 1 {
		t.Fatalf("recovered extent=%d, want 1", extent)
	}
}

func TestDiscoveryPublishesCountsAndRanksAcrossConcurrentAdditions(t *testing.T) {
	catalog, root := indexFixture(t)
	const directories = 4
	for index := range directories {
		writeIndexFile(t, root.Path, fmt.Sprintf("dir-%02d/a.txt", index), "initial")
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "open structural store", err)
	for _, dir := range []string{".", "dir-00", "dir-01", "dir-02", "dir-03"} {
		_, err := catalog.Directories.ObserveDirectory(t.Context(), "p", root, dir, DirectoryRead{})
		testutil.FailErr(t, "discover initial structure", err)
	}
	previous, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "pin original ranks", err)
	defer func() { _ = previous.Close() }()
	if extent := navigationExtent(t, previous); extent != directories*2 {
		t.Fatalf("initial extent=%d", extent)
	}
	stalePin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin stale build base", err)
	defer stalePin.Release()
	staleBuilder, err := newStructuralBuilder(store, stalePin.value)
	testutil.FailErr(t, "create stale builder", err)
	defer staleBuilder.close()
	staleObservation, err := store.readObservation(t.Context(), "dir-00")
	testutil.FailErr(t, "read stale observation", err)
	testutil.FailErr(t, "prepare stale listing", staleBuilder.observe(t.Context(), directoryDiscovery{
		nodes: []indexNode{{path: "dir-00/a.txt", parent: "dir-00", name: "a.txt", regular: true}}, observation: staleObservation,
	}))
	testutil.FailErr(t, "finalize stale listing", staleBuilder.finalize(t.Context()))
	writeIndexFile(t, root.Path, "dir-01/late.txt", "foreground")
	store.mu.Lock()
	store.invalidateObservationsLocked([]string{"dir-01/late.txt"})
	store.mu.Unlock()
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, "dir-01", DirectoryRead{Priority: backgroundwork.PriorityInteractive})
	testutil.FailErr(t, "publish foreground addition", err)
	testutil.FailErr(t, "rebase independent bulk publication", store.publishStructure(t.Context(), staleBuilder, stalePin.Generation))
	current, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "pin updated ranks", err)
	defer func() { _ = current.Close() }()
	if old, updated := navigationExtent(t, previous), navigationExtent(t, current); old != 8 || updated != 9 {
		t.Fatalf("snapshot extents old=%d updated=%d", old, updated)
	}
}
