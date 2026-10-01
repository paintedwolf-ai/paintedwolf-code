package sourcecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralDrainClosesUnpinnedHeadWhileKeepingPinnedPredecessor(t *testing.T) {
	store := checkpointTestStore(t)
	store.catalog.trees = map[string]projectionStore{"lifecycle": store}
	t.Cleanup(func() { testutil.FailErr(t, "drain lifecycle catalog", store.catalog.Drain(t.Context())) })
	first, firstSegment, firstPage := lifecycleGeneration(t, store, 1)
	store.mu.Lock()
	store.initializeStructureLocked()
	store.installStructureLocked(first)
	store.mu.Unlock()
	pin, err := store.retainGeneration(1, false)
	testutil.FailErr(t, "pin predecessor", err)
	defer pin.Release()
	second, secondSegment, secondPage := lifecycleGeneration(t, store, 2)
	store.mu.Lock()
	store.installStructureLocked(second)
	store.mu.Unlock()
	testutil.FailErr(t, "drain with predecessor pinned", store.catalog.Drain(t.Context()))
	if _, err := secondSegment.bytes.Read(t.Context(), secondPage&structuralOffsetMask); !errors.Is(err, errStructuralSegmentsClosed) {
		t.Fatalf("unpinned head segment survived drain: %v", err)
	}
	_, err = firstSegment.bytes.Read(t.Context(), firstPage&structuralOffsetMask)
	testutil.FailErr(t, "read pinned predecessor after drain", err)
	pin.Release()
	if _, err := firstSegment.bytes.Read(t.Context(), firstPage&structuralOffsetMask); !errors.Is(err, errStructuralSegmentsClosed) {
		t.Fatalf("predecessor segment survived final release: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.structure != nil || len(store.pins.held) != 0 {
		t.Fatal("drained store retained an unowned generation")
	}
}

func TestClearTreeStoresLeavesPinnedSpilledGenerationReadable(t *testing.T) {
	catalog := New()
	catalog.treeDir = t.TempDir()
	store := checkpointTestStore(t)
	store.catalog = catalog
	store.structureFile = filepath.Join(catalog.treeDir, "snapshot.structure")
	generation := checkpointTestGenerationWithMemory(t, store, nil, 400, 16<<10)
	spilled := false
	for _, segment := range generation.segments {
		if segment.bytes.spill != nil {
			spilled = true
			break
		}
	}
	if !spilled {
		t.Fatal("generation did not spill")
	}
	store.mu.Lock()
	store.initializeStructureLocked()
	store.installStructureLocked(generation)
	store.mu.Unlock()
	catalog.trees = map[string]projectionStore{"spill": store}
	pin, err := store.retainGeneration(generation.id, false)
	testutil.FailErr(t, "pin spilled generation", err)
	defer pin.Release()
	root, _, err := pin.value.directories.Get(t.Context(), ".")
	testutil.FailErr(t, "read pinned directory metadata", err)

	clearCalled := false
	testutil.FailErr(t, "clear tree stores", catalog.ClearTreeStores(t.Context(), func() error {
		clearCalled = true
		return os.RemoveAll(catalog.treeDir)
	}))
	if !clearCalled {
		t.Fatal("clear callback was not called")
	}
	// A spill has no directory entry, so emptying the cache tree cannot reach
	// it and a reader that pinned the generation keeps reading it.
	index := pagedview.RangeIndex[TreeItem]{Store: pin.value, Root: root.page}
	item, err := index.SelectItem(t.Context(), 0)
	testutil.FailErr(t, "read pinned spill after clear", err)
	if item.Value.Path == "" {
		t.Fatal("pinned spill returned an empty item")
	}
}

func lifecycleGeneration(t *testing.T, store *indexStore, id int64) (*structuralGeneration, *structuralSegment, uint64) {
	t.Helper()
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create lifecycle generation", err)
	defer builder.close()
	page, err := builder.Write(t.Context(), 0, pagedview.RangePage[TreeItem]{Items: []pagedview.RangeItem[TreeItem]{
		{Key: DirectoryOrder("file", false), Value: TreeItem{Path: "file"}, Weight: 1},
	}})
	testutil.FailErr(t, "write lifecycle page", err)
	testutil.FailErr(t, "save lifecycle directory", builder.save(t.Context(), &pagedview.RangeIndex[TreeItem]{Store: builder, Root: page}, DirectoryObservation{Path: ".", Sequence: id, FirstListed: id, Entries: 1, Complete: true}))
	generation, err := builder.seal(t.Context(), id)
	testutil.FailErr(t, "seal lifecycle generation", err)
	return generation, generation.segments[uint32(page>>structuralOffsetBits)], page
}
