package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTreeEvictionRechecksPinAdmittedAfterSelection(t *testing.T) {
	store := checkpointTestStore(t)
	store.catalog.trees = map[string]projectionStore{"eviction": store}
	t.Cleanup(func() { testutil.FailErr(t, "drain eviction fixture", store.catalog.Drain(t.Context())) })
	generation, segment, page := lifecycleGeneration(t, store, 1)
	store.mu.Lock()
	store.initializeStructureLocked()
	store.installStructureLocked(generation)
	selected := treeStoreEvictableLocked(store)
	store.mu.Unlock()
	if !selected {
		t.Fatal("idle store was not an eviction candidate")
	}
	// Another caller enters after selection and before retirement takes the lock.
	pin, err := store.retainGeneration(1, false)
	testutil.FailErr(t, "retain selected generation", err)
	defer pin.Release()
	if retireTreeStore(store) {
		t.Fatal("eviction retired a newly pinned generation")
	}
	_, err = segment.bytes.Read(t.Context(), page&structuralOffsetMask)
	testutil.FailErr(t, "read generation protected from eviction", err)
	// The head and latest-complete snapshot own independent references. The pin
	// prevents retirement from releasing either while coordinates are active.
	if segment.refs.Load() != 2 {
		t.Fatalf("protected segment references = %d", segment.refs.Load())
	}
	pin.Release()
	if !retireTreeStore(store) {
		t.Fatal("released store remained ineligible for retirement")
	}
	if _, err := segment.bytes.Read(t.Context(), page&structuralOffsetMask); !errors.Is(err, errStructuralSegmentsClosed) {
		t.Fatalf("retired segment remains readable: %v", err)
	}
	if segment.refs.Load() != 0 {
		t.Fatalf("retired segment released more than once: %d", segment.refs.Load())
	}
}

// Review overlays write into their store between frames, when no navigation
// or pin holds it. Retiring it under an open projection would fail every later
// write with ErrExpired.
func TestTreeEvictionSkipsStoreWithOpenProjection(t *testing.T) {
	store := checkpointTestStore(t)
	rows, err := newProjectionRows(t.Context(), store)
	testutil.FailErr(t, "open projection rows", err)
	store.mu.Lock()
	selected := treeStoreEvictableLocked(store)
	store.mu.Unlock()
	if selected {
		t.Fatal("eviction selected a store whose projection rows are open")
	}
	if retireTreeStore(store) {
		t.Fatal("eviction retired a store whose projection rows are open")
	}
	testutil.FailErr(t, "write through the open projection", rows.Release(t.Context()))
	rows.Close()
	if !retireTreeStore(store) {
		t.Fatal("store stayed ineligible after its projection closed")
	}
}

// An open view reads its root between frames without pinning a generation.
// Its hold keeps the store through store-count eviction until released.
func TestTreeEvictionSkipsHeldRoot(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	catalog := New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	held := Root{ID: "held", Path: t.TempDir()}
	release, err := catalog.HoldRoot(t.Context(), "project", held)
	testutil.FailErr(t, "hold root", err)
	store, err := catalog.indexStore(t.Context(), "project", held)
	testutil.FailErr(t, "resolve held store", err)
	churn := func(prefix string) {
		for i := range catalog.rootLimit + catalog.scopedLimit + 4 {
			_, err := catalog.indexStore(t.Context(), "project", Root{ID: fmt.Sprintf("%s-%d", prefix, i), Path: t.TempDir()})
			testutil.FailErr(t, "open another root", err)
		}
	}
	churn("while-held")
	store.mu.Lock()
	retired := store.retired
	store.mu.Unlock()
	if retired {
		t.Fatal("eviction retired a held root")
	}
	release()
	release()
	churn("after-release")
	store.mu.Lock()
	retired = store.retired
	store.mu.Unlock()
	if !retired {
		t.Fatal("released root stayed resident past the store bound")
	}
}
