package sourcecatalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStructuralRecursiveDemandDiscoversChildAfterInitialInventory(t *testing.T) {
	catalog, root := indexFixture(t)
	writeIndexFile(t, root.Path, "known.txt", "known")
	testutil.FailErr(t, "finish initial inventory", catalog.Directories.AwaitNavigation(t.Context(), "p", root))
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get initialized store", err)
	writeIndexFile(t, root.Path, "new/nested/file.txt", "new")
	// Refresh the foreground listing without scheduling an inventory invalidation.
	store.fenceObservationSubtree(".")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "publish newly discovered directory", err)
	complete, err := store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "inspect new coverage demand", err)
	if complete {
		t.Fatal("unknown child directory already has recursive coverage")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	testutil.FailErr(t, "prepare root after initial readiness", catalog.observeSubtree(ctx, "p", root, "."))
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open prepared generation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "new/nested/file.txt")
	testutil.FailErr(t, "resolve newly discovered descendant", err)
	complete, err = store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "read completed recursive coverage", err)
	if !complete {
		t.Fatal("recursive demand returned before covering new descendants")
	}
}

// Observing Done identifies admission to the request's wait select without a scanner hook.
type structuralWaitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *structuralWaitingContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestStructuralRecursiveViewsShareWorkAfterOneCancels(t *testing.T) {
	catalog, root := indexFixture(t)
	catalog.Trees.broker = backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceDirectory: {Total: 1}})
	testutil.FailErr(t, "finish empty initial inventory", catalog.Directories.AwaitNavigation(t.Context(), "p", root))
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get shared inventory store", err)
	writeIndexFile(t, root.Path, "target/nested/file.txt", "source")
	store.fenceObservationSubtree(".")
	_, err = catalog.Directories.ObserveDirectory(t.Context(), "p", root, ".", DirectoryRead{})
	testutil.FailErr(t, "discover unobserved target", err)
	store.mu.Lock()
	before := store.structure.id
	store.mu.Unlock()
	release, err := catalog.Trees.broker.Acquire(t.Context(), backgroundwork.Request{Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory}})
	testutil.FailErr(t, "hold recursive discovery admission", err)
	defer release()
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	firstLifetime, cancelFirst := context.WithCancel(deadline)
	defer cancelFirst()
	first := &structuralWaitingContext{Context: firstLifetime, waiting: make(chan struct{})}
	second := &structuralWaitingContext{Context: deadline, waiting: make(chan struct{})}
	firstResult, secondResult := make(chan error, 1), make(chan error, 1)
	go func() { firstResult <- catalog.observeSubtree(first, "p", root, "target") }()
	go func() { secondResult <- catalog.observeSubtree(second, "p", root, "target") }()
	for _, waiting := range []<-chan struct{}{first.waiting, second.waiting} {
		select {
		case <-waiting:
		case <-deadline.Done():
			t.Fatal("recursive views did not join preparation")
		}
	}
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recursive view returned %v", err)
	}
	select {
	case err := <-secondResult:
		t.Fatalf("remaining view settled before discovery admission: %v", err)
	default:
	}
	release()
	testutil.FailErr(t, "finish shared recursive preparation", <-secondResult)
	store.mu.Lock()
	after := store.structure.id
	store.mu.Unlock()
	if after != before+1 {
		t.Fatalf("two views published %d generations, want one shared publication", after-before)
	}
}

func TestStructuralParentRefreshPreservesUnrelatedSubtreeObservations(t *testing.T) {
	catalog, root := indexFixture(t)
	for i := range 48 {
		writeIndexFile(t, root.Path, fmt.Sprintf("stable/group-%02d/nested/file.txt", i), "source")
	}
	writeIndexFile(t, root.Path, "original.txt", "source")
	store, err := catalog.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get incremental inventory store", err)
	testutil.FailErr(t, "build initial recursive structure", store.buildStructure(t.Context(), map[string]struct{}{".": {}}, true))
	before, err := store.readObservation(t.Context(), "stable/group-24/nested")
	testutil.FailErr(t, "capture unrelated observation", err)
	store.mu.Lock()
	store.inventory.wake = make(chan struct{}, 1)
	store.mu.Unlock()
	writeIndexFile(t, root.Path, "new-root-file.txt", "new")
	catalog.Trees.invalidateTrees(root.Path, []string{"new-root-file.txt"})
	testutil.FailErr(t, "refresh root membership", store.inventoryPass(t.Context()))
	after, err := store.readObservation(t.Context(), "stable/group-24/nested")
	testutil.FailErr(t, "read preserved unrelated observation", err)
	if after.Sequence != before.Sequence || !after.Observed.Equal(before.Observed) || after.Invalidation != before.Invalidation {
		t.Fatalf("unrelated directory was scanned again: before %+v, after %+v", before, after)
	}
	navigation, err := catalog.Directories.OpenNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "open incremental generation", err)
	defer func() { _ = navigation.Close() }()
	_, err = navigation.Entry(t.Context(), "new-root-file.txt")
	testutil.FailErr(t, "resolve changed root membership", err)
	complete, err := store.subtreeComplete(t.Context(), ".")
	testutil.FailErr(t, "inspect retained complete coverage", err)
	if !complete {
		t.Fatal("incremental parent refresh lost existing subtree coverage")
	}
}
