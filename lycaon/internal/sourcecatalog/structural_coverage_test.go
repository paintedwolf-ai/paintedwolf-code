package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

// subtreeComplete reads current coverage without waiting for a pass.
func (s *indexStore) subtreeComplete(ctx context.Context, dir string) (bool, error) {
	navigation, err := openNavigation(ctx, s, headGeneration)
	if err != nil {
		return false, err
	}
	defer func() { _ = navigation.Close() }()
	return SubtreeCovered(ctx, navigation, dir)
}

// observeSubtree waits for complete coverage of a subtree.
func (c *Catalog) observeSubtree(ctx context.Context, project string, root Root, dir string) error {
	return c.Directories.AwaitSubtree(ctx, project, root, dir, func(ctx context.Context, navigation *Navigation) (bool, error) {
		return SubtreeCovered(ctx, navigation, normalizeDir(dir))
	})
}

func TestStructuralCoveragePreservesSelectedMembershipAndSettlesMissingBranches(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "known/unobserved.txt", "later file")
	writeIndexFile(t, root.Path, "partial/nested/fresh.txt", "fresh")
	filesystem, err := os.OpenRoot(root.Path)
	testutil.FailErr(t, "open coverage root", err)
	defer func() { _ = filesystem.Close() }()
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get coverage store", err)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create coverage builder", err)
	defer builder.close()
	builder.directories.memoryLimit = 1
	observePublicationFixture(t, builder, store, ".", []string{"known", "partial", "gone", "link", "failed"}, true)
	rootChildren, rootObservation, err := builder.children(t.Context(), ".")
	testutil.FailErr(t, "read selected root", err)
	link, err := builder.item(t.Context(), indexNode{path: "link", name: "link", isDir: true, isSymlink: true})
	testutil.FailErr(t, "build link row", err)
	testutil.FailErr(t, "replace link row", rootChildren.SetBatch(t.Context(), []pagedview.RangeItem[TreeItem]{link}))
	testutil.FailErr(t, "save selected link", builder.save(t.Context(), rootChildren, rootObservation))
	observePublicationFixture(t, builder, store, "known", []string{"selected.txt"}, false)
	known, _, err := builder.directories.Get(t.Context(), "known")
	testutil.FailErr(t, "read selected known directory", err)
	observePublicationFixture(t, builder, store, "partial", []string{"obsolete"}, true)
	observePublicationFixture(t, builder, store, "partial/obsolete", nil, false)
	partial, found, err := builder.directories.Get(t.Context(), "partial")
	testutil.FailErr(t, "read partial fixture", err)
	if !found {
		t.Fatal("partial fixture missing")
	}
	partial.observation.Complete = false
	testutil.FailErr(t, "mark unfinished listing", builder.directories.Set(t.Context(), "partial", partial))
	observePublicationFixture(t, builder, store, "failed", []string{"unresolved"}, true)
	testutil.FailErr(t, "retain terminal failure", builder.retainFailedDirectory(t.Context(), DirectoryObservation{Path: "failed", Failure: "unavailable"}))
	var marksMu sync.Mutex
	marked := make(map[string]bool)
	options := structuralScanOptions{store: store, descend: func(string) (bool, error) { return false, nil }, mark: func(dir string) uint64 {
		marksMu.Lock()
		marked[dir] = true
		marksMu.Unlock()
		return store.observationMark(dir)
	}}
	builder.coverage = func(ctx context.Context, target *structuralBuilder) error {
		return target.completeCoverage(ctx, filesystem, options)
	}
	testutil.FailErr(t, "publish completed coverage", store.publishStructure(t.Context(), builder, 0))
	complete, err := store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "measure coverage closure", err)
	if !complete {
		t.Fatal("coverage closure left unresolved branches")
	}
	for _, dir := range []string{".", "known", "link", "partial/obsolete", "failed", "failed/unresolved"} {
		if marked[dir] {
			t.Errorf("coverage reread selected or retired directory %q", dir)
		}
	}
	if !marked["partial"] || !marked["partial/nested"] || !marked["gone"] {
		t.Fatalf("missing coverage reads: %v", marked)
	}
	after, err := store.readObservation(t.Context(), "known")
	testutil.FailErr(t, "read preserved observation", err)
	if after != known.observation {
		t.Fatalf("complete directory changed: got %+v, want %+v", after, known.observation)
	}
	gone, err := store.readObservation(t.Context(), "gone")
	testutil.FailErr(t, "read missing directory outcome", err)
	if gone.Failure == "" {
		t.Fatal("missing selected directory did not receive terminal failure")
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open coverage navigation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "partial/nested/fresh.txt")
	testutil.FailErr(t, "read newly covered nested file", err)
	_, err = navigation.Entry(t.Context(), "partial/obsolete")
	if !errors.Is(err, pagedview.ErrMissing) {
		t.Fatalf("obsolete partial member survived: %v", err)
	}
}

func TestStructuralCoverageCancellation(t *testing.T) {
	store := checkpointTestStore(t)
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create canceled coverage builder", err)
	defer builder.close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := builder.completeCoverage(ctx, nil, structuralScanOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled coverage returned %v", err)
	}
}

func TestStructuralFinalizationOrdersVirtualRootAfterPunctuationDirectories(t *testing.T) {
	for _, spill := range []bool{false, true} {
		name := "memory"
		if spill {
			name = "spilled"
		}
		t.Run(name, func(t *testing.T) {
			catalog, root := indexFixture(t)
			store, err := catalog.Trees.indexStore(t.Context(), "p", root)
			testutil.FailErr(t, "get punctuation store", err)
			builder, err := newStructuralBuilder(store, nil)
			testutil.FailErr(t, "create punctuation builder", err)
			defer builder.close()
			if spill {
				builder.directories.memoryLimit = 1
			}
			observePublicationFixture(t, builder, store, ".", []string{"!dir"}, true)
			observePublicationFixture(t, builder, store, "!dir", []string{"nested"}, true)
			observePublicationFixture(t, builder, store, "!dir/nested", []string{"file.txt"}, false)
			testutil.FailErr(t, "publish punctuation structure", store.publishStructure(t.Context(), builder, 0))
			navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
			testutil.FailErr(t, "open punctuation navigation", err)
			defer func() { _ = navigation.Close() }()
			if extent := navigationExtent(t, navigation); extent != 3 {
				t.Fatalf("punctuation extent=%d, want3", extent)
			}
			complete, err := store.subtreeComplete(t.Context(), ".")
			testutil.FailErr(t, "inspect punctuation coverage", err)
			if !complete {
				t.Fatal("complete punctuation subtree left root unresolved")
			}
			entry, offset, err := navigation.Child(t.Context(), ".", 2, true)
			testutil.FailErr(t, "seek final punctuation row", err)
			if entry.Path != "!dir" || offset != 2 {
				t.Fatalf("rank2=%s+%d", entry.Path, offset)
			}
			entry, offset, err = navigation.Child(t.Context(), "!dir", 1, true)
			testutil.FailErr(t, "seek nested punctuation row", err)
			if entry.Path != "!dir/nested" || offset != 1 {
				t.Fatalf("nested rank1=%s+%d", entry.Path, offset)
			}
		})
	}
}
