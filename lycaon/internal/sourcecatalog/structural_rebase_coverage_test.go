package sourcecatalog

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralColdRebaseKeepsNewParentAndSeparatelyObservedChild(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "existing/old.txt", "old")
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get cold rebase store", err)
	pin, err := store.retainGeneration(headGeneration, true)
	testutil.FailErr(t, "pin empty cold basis", err)
	defer pin.Release()
	builder, err := newStructuralBuilder(store, nil)
	testutil.FailErr(t, "create completed cold builder", err)
	defer builder.close()
	builder.comparison = pin.value
	observePublicationFixture(t, builder, store, ".", []string{"existing"}, true)
	observePublicationFixture(t, builder, store, "existing", []string{"old.txt"}, false)
	writeIndexFile(t, root.Path, "new/known.txt", "new")
	_, err = catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "publish newer root membership", err)
	child, err := catalog.ObserveDirectory(t.Context(), "p", root, "new", DirectoryRead{})
	testutil.FailErr(t, "publish separate foreground child", err)
	testutil.FailErr(t, "publish rebased cold inventory", store.publishStructure(t.Context(), builder, pin.Generation))
	complete, err := store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "measure merged cold coverage", err)
	if !complete {
		t.Fatal("known foreground child became unresolved during cold rebase")
	}
	after, err := store.readObservation(t.Context(), "new")
	testutil.FailErr(t, "read preserved foreground child", err)
	if after != child {
		t.Fatalf("foreground observation changed: got %+v, want %+v", after, child)
	}
	navigation, err := catalog.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open merged navigation", err)
	defer func() { _ = navigation.Close() }()
	if extent := navigationExtent(t, navigation); extent != 4 {
		t.Fatalf("merged root extent=%d, want4", extent)
	}
	for _, file := range []string{"existing/old.txt", "new/known.txt"} {
		_, err := navigation.Entry(t.Context(), file)
		testutil.FailErr(t, "read merged known descendant", err)
	}
}

// Newly observed memberships close in the same publication, even after repeated races.
func TestStructuralForegroundMembershipClosesCoverageAcrossManyRebases(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get advancing coverage store", err)
	filesystem, err := os.OpenRoot(root.Path)
	testutil.FailErr(t, "open coverage filesystem", err)
	defer func() { _ = filesystem.Close() }()
	for round := 0; round < 9; round++ {
		pin, err := store.retainGeneration(headGeneration, true)
		testutil.FailErr(t, "pin current coverage", err)
		builder, err := newStructuralBuilder(store, pin.value)
		testutil.FailErr(t, "create collected coverage builder", err)
		names := make([]string, round)
		for i := range names {
			names[i] = fmt.Sprintf("dir-%02d", i)
		}
		observePublicationFixture(t, builder, store, ".", names, true)
		for _, dir := range names {
			observePublicationFixture(t, builder, store, dir, []string{"file.txt"}, false)
		}
		introduced := fmt.Sprintf("dir-%02d", round)
		writeIndexFile(t, root.Path, introduced+"/file.txt", "new")
		store.fenceObservationSubtree(".")
		selected, err := catalog.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
		testutil.FailErr(t, "advance foreground membership", err)
		builder.coverage = func(ctx context.Context, target *structuralBuilder) error {
			return target.completeCoverage(ctx, filesystem, structuralScanOptions{store: store})
		}
		testutil.FailErr(t, "publish collected coverage beside newer parent", store.publishStructure(t.Context(), builder, pin.Generation))
		retained, err := store.readObservation(t.Context(), ".")
		testutil.FailErr(t, "read fixed foreground membership", err)
		if retained != selected {
			t.Fatalf("round%d rescanned selected parent membership", round)
		}
		builder.close()
		pin.Release()
		complete, err := store.subtreeComplete(t.Context(), ".")
		testutil.FailErr(t, "inspect newly introduced frontier", err)
		if !complete {
			t.Fatalf("round%d left newly visible directory unresolved", round)
		}
		navigation, err := catalog.OpenNavigation(t.Context(), "p", root)
		testutil.FailErr(t, "open advancing membership", err)
		_, err = navigation.Entry(t.Context(), introduced+"/file.txt")
		testutil.FailErr(t, "preserve newly visible directory", err)
		_ = navigation.Close()
	}
}
