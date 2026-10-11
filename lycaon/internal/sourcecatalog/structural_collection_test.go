package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralPublicationKeepsQueuedChangesForNextPass(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "changed/removed/old.txt", "old")
	writeIndexFile(t, root.Path, "stable/nested/keep.txt", "keep")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get changing store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain cold basis", err)
	defer pin.Release()
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create cold builder", err)
	defer builder.close()
	builder.comparison = pin.value
	err = scanStructure(t.Context(), openRootForScan(t, root), ".", structuralScanOptions{store: store}, func(listing directoryDiscovery) error {
		return builder.observe(t.Context(), listing)
	})
	testutil.FailErr(t, "collect initial membership", err)
	stable, _, err := builder.directories.Get(t.Context(), "stable/nested")
	testutil.FailErr(t, "capture unaffected observation", err)
	testutil.FailErr(t, "remove collected subtree", os.RemoveAll(filepath.Join(root.Path, "changed/removed")))
	writeIndexFile(t, root.Path, "changed/newdir/leaf.txt", "new")
	store.mu.Lock()
	store.inventory.wake = make(chan struct{}, 1)
	store.invalidateObservationsLocked([]string{"changed/removed", "changed/newdir"})
	store.mu.Unlock()
	testutil.FailErr(t, "publish observed snapshot despite later changes", store.publishStructure(t.Context(), builder, pin.Generation))
	observed, err := store.readObservation(t.Context(), "changed")
	testutil.FailErr(t, "read collected observation", err)
	if observed.Invalidation >= store.observationMark("changed") {
		t.Fatal("publication claimed to include changes that followed collection")
	}
	store.mu.Lock()
	_, pending := store.inventory.dirty["changed"]
	store.mu.Unlock()
	if !pending {
		t.Fatal("publication discarded pending reconciliation")
	}
	testutil.FailErr(t, "reconcile queued changes", store.inventoryPass(t.Context()))
	after, err := store.readObservation(t.Context(), "stable/nested")
	testutil.FailErr(t, "read unaffected directory", err)
	if after != stable.observation {
		t.Fatal("reconciliation rescanned unaffected directory")
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open reconciled tree", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "changed/newdir/leaf.txt")
	testutil.FailErr(t, "find new child", err)
	if _, err := navigation.Entry(t.Context(), "changed/removed"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("removed child remains: %v", err)
	}
}

func TestStructuralPublicationDoesNotRequireQuietCollection(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range 20 {
		writeIndexFile(t, root.Path, fmt.Sprintf("d%d/file.txt", i), "source")
	}
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get busy store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain cold basis", err)
	defer pin.Release()
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create busy builder", err)
	defer builder.close()
	options := structuralScanOptions{store: store, mark: func(dir string) uint64 {
		mark := store.observationMark(dir)
		store.fenceObservationSubtree(".")
		return mark
	}}
	err = scanStructure(t.Context(), openRootForScan(t, root), ".", options, func(listing directoryDiscovery) error {
		return builder.observe(t.Context(), listing)
	})
	testutil.FailErr(t, "scan during continuous invalidation", err)
	testutil.FailErr(t, "publish complete observed membership", store.publishStructure(t.Context(), builder, pin.Generation))
	complete, err := store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "read snapshot coverage", err)
	if !complete {
		t.Fatal("complete observed snapshot was left unresolved")
	}
}

func TestStructuralPublicationGateCancellationKeepsReadersAvailable(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get publication store", err)
	release, err := store.acquireStructurePublication(t.Context())
	testutil.FailErr(t, "hold publication gate", err)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.acquireStructurePublication(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publisher returned %v", err)
	}
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "reader proceeds beside held publication gate", err)
	pin.Release()
}

func TestStructuralFailedCollectionRetainsPreOpenInvalidation(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get failed repair store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "retain failed repair basis", err)
	defer pin.Release()
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create failed repair builder", err)
	defer builder.close()
	observePublicationFixture(t, builder, store, ".", []string{"missing"}, true)
	observePublicationFixture(t, builder, store, "missing", []string{"known.txt"}, false)
	store.fenceObservationSubtree("missing")
	beforeOpen := store.observationMark("missing")
	options := structuralScanOptions{store: store, workers: 1, mark: func(dir string) uint64 {
		mark := store.observationMark(dir)
		store.fenceObservationSubtree(dir)
		return mark
	}}
	handle := openRootForScan(t, root)
	err = scanStructureRoot(t.Context(), handle, "missing", options, func(listing directoryDiscovery) error {
		return builder.retainFailedDirectory(t.Context(), listing.observation)
	})
	testutil.FailErr(t, "record missing directory failure", err)
	record, found, err := builder.directories.Get(t.Context(), "missing")
	testutil.FailErr(t, "read failed collection", err)
	if !found || record.observation.Failure == "" || record.observation.Invalidation != beforeOpen {
		t.Fatalf("failed observation lost its pre-open mark: %+v", record.observation)
	}
	testutil.FailErr(t, "publish failure without claiming freshness", store.publishStructure(t.Context(), builder, pin.Generation))
	writeIndexFile(t, root.Path, "missing/recovered.txt", "source")
	testutil.FailErr(t, "refresh directory that became available", store.buildStructure(t.Context(), map[string]struct{}{"missing": {}}, false))
	recovered, err := store.readObservation(t.Context(), "missing")
	testutil.FailErr(t, "read recovered observation", err)
	if recovered.Failure != "" || !recovered.Complete {
		t.Fatalf("recovered directory kept terminal failure: %+v", recovered)
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open recovered tree", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "missing/recovered.txt")
	testutil.FailErr(t, "retain recovered membership", err)
	if _, err := navigation.Entry(t.Context(), "missing/known.txt"); !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("recovery appended to obsolete membership: %v", err)
	}
}

func TestRepeatedInvalidationsAtCapacityStayLocal(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get invalidation store", err)
	store.mu.Lock()
	defer store.mu.Unlock()
	store.inventory.wake = make(chan struct{}, 1)
	store.inventory.dirty = make(map[string]struct{})
	store.invalidation.paths = make(map[string]uint64)
	store.invalidation.parents = make(map[string]uint64)
	for i := range maxPendingTreePaths / 2 {
		dir := fmt.Sprintf("d%d", i)
		store.invalidation.paths[dir+"/file.txt"] = 1
		store.invalidation.parents[dir] = 1
	}
	for i := range maxPendingTreePaths {
		store.inventory.dirty[fmt.Sprintf("d%d", i)] = struct{}{}
	}
	full := store.invalidation.full
	store.invalidateObservationsLocked([]string{"d0/file.txt", "d0/file.txt"})
	if store.invalidation.full != full || store.inventory.full {
		t.Fatal("repeated known change escalated to a full-root scan")
	}
	if store.invalidation.parents["d0"] <= 1 {
		t.Fatal("local invalidation did not advance")
	}
}

func TestStructuralScanRootsPrioritizesVirtualRoot(t *testing.T) {
	roots := structuralScanRoots(map[string]struct{}{"!first": {}, "-build/nested": {}, ".": {}, "z": {}})
	if len(roots) != 1 || roots[0] != "." {
		t.Fatalf("redundant scan roots: %v", roots)
	}
}

func TestStructuralScanRootsHandlesInterleavedSiblingNames(t *testing.T) {
	roots := structuralScanRoots(map[string]struct{}{"a": {}, "a-b": {}, "a/nested": {}, "a-b/child": {}})
	if len(roots) != 2 || roots[0] != "a" || roots[1] != "a-b" {
		t.Fatalf("redundant nested scan roots: %v", roots)
	}
}
