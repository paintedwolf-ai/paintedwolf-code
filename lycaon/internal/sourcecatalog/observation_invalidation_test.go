package sourcecatalog

import (
	"fmt"
	"strings"
	"testing"
)

// Shared ancestors cover a localized burst within the pending-directory limit.
func TestCoarsenDirtyDirectoriesLiftsInsteadOfNamingTheRoot(t *testing.T) {
	dirty := make(map[string]struct{})
	var original []string
	for module := range 40 {
		for leaf := range 40 {
			dir := fmt.Sprintf("pkg/mod-%02d/sub-%02d", module, leaf)
			dirty[dir] = struct{}{}
			original = append(original, dir)
		}
	}
	coarsened, full := coarsenDirtyDirectories(dirty, 64)
	if full {
		t.Fatal("a burst inside one subtree escalated to the whole root")
	}
	if _, root := coarsened["."]; root {
		t.Fatal("coarsened set named the root")
	}
	if len(coarsened) > 64 {
		t.Fatalf("coarsened set = %d entries, want at most 64", len(coarsened))
	}
	for _, dir := range original {
		covered := false
		for scan := dir; ; {
			if _, found := coarsened[scan]; found {
				covered = true
				break
			}
			cut := strings.LastIndex(scan, "/")
			if cut < 0 {
				break
			}
			scan = scan[:cut]
		}
		if !covered {
			t.Fatalf("coarsened set lost coverage of %s", dir)
		}
	}
}

// A set that is broad at every depth has no narrower answer than the root.
func TestCoarsenDirtyDirectoriesNamesTheRootWhenBroadAtEveryDepth(t *testing.T) {
	dirty := make(map[string]struct{})
	for top := range 64 {
		dirty[fmt.Sprintf("top-%02d", top)] = struct{}{}
	}
	coarsened, full := coarsenDirtyDirectories(dirty, 8)
	if !full {
		t.Fatal("a set that cannot be lifted did not become a whole-tree pass")
	}
	if _, root := coarsened["."]; !root {
		t.Fatalf("exhausted coarsening = %v, want the root", coarsened)
	}
}

// Coarsening preserves invalidation coverage and freshness of unrelated subtrees.
func TestInvalidateObservationsCoarsensRatherThanMarkingTheRoot(t *testing.T) {
	store := checkpointTestStore(t)
	var paths []string
	for module := range 80 {
		for leaf := range 80 {
			paths = append(paths, fmt.Sprintf("pkg/mod-%02d/sub-%02d/file.go", module, leaf))
		}
	}
	if len(paths) <= maxPendingTreePaths {
		t.Fatalf("fixture paths = %d, want more than the pending bound", len(paths))
	}
	store.mu.Lock()
	store.invalidateObservationsLocked(paths)
	full := store.invalidation.full
	changed := store.observationMarkLocked("pkg/mod-07/sub-11")
	unrelated := store.observationMarkLocked("vendor/untouched")
	store.mu.Unlock()

	if full != 0 {
		t.Fatal("an overflowing burst marked the whole root stale")
	}
	if changed == 0 {
		t.Fatal("a changed directory lost its invalidation mark")
	}
	if unrelated != 0 {
		t.Fatal("an untouched subtree was marked stale by coarsening")
	}
}

// Tests can stale a listing without scheduling a background inventory pass.
func (s *indexStore) fenceObservationSubtree(rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	marks := &s.invalidation
	marks.next = observationSerial.Add(1)
	if len(marks.paths)+len(marks.parents) >= maxPendingTreePaths {
		marks.full = marks.next
		marks.paths, marks.parents = nil, nil
	}
	if marks.paths == nil {
		marks.paths = make(map[string]uint64)
		marks.parents = make(map[string]uint64)
	}
	marks.paths[rel] = marks.next
}

// The root listing retains its own invalidation mark during coarsening.
func TestCoarsenInvalidationKeepsTheRootListingMark(t *testing.T) {
	store := checkpointTestStore(t)
	paths := []string{"top.go"}
	for module := range 80 {
		for leaf := range 80 {
			paths = append(paths, fmt.Sprintf("pkg/mod-%02d/sub-%02d/file.go", module, leaf))
		}
	}
	store.mu.Lock()
	store.invalidateObservationsLocked(paths)
	full := store.invalidation.full
	root := store.observationMarkLocked(".")
	store.mu.Unlock()

	if full != 0 {
		t.Fatal("an overflowing burst marked the whole root stale")
	}
	if root == 0 {
		t.Fatal("coarsening dropped the root listing mark")
	}
}
